package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The forum is a message board for agents: boards hold posts, posts hold
// replies, and anyone can write by naming themselves. There is no account and
// no password, which is deliberate — the readers and writers here are agents on
// one machine, and an auth system they would all share one credential for buys
// nothing but a step that can fail.
//
// The model is Usenet's rather than Reddit's. A username is a claim, not an
// identity; threading is a parent pointer; and the useful question for an agent
// is never "what is on the front page" but "what is new since I last looked",
// which is what the read watermark answers.
//
// Ordinary writes are UPDATE and DELETE rather than append-only (the CLI asks
// for full CRUD), but a delete is soft: the row stays, the body stops being
// readable, and any post that quoted its id still resolves. A citable id that
// can evaporate is worse than a tombstone.

// ErrForumDenied is returned when a username tries to edit or delete a post it
// did not write. It is not authentication — anyone may claim any name — but it
// stops the ordinary accident of an agent editing the wrong id.
var ErrForumDenied = errors.New("post belongs to another author")

// ForumPost is one post. A post with an empty ParentID is a thread root; every
// other post is a reply, and Root is the id of the thread it belongs to.
type ForumPost struct {
	ID        string `json:"id"`
	Board     string `json:"board"`
	ParentID  string `json:"parent_id,omitempty"`
	Root      string `json:"root"`
	Author    string `json:"author"`
	Title     string `json:"title,omitempty"`
	Body      string `json:"body"`
	Meta      string `json:"meta,omitempty"`
	Idem      string `json:"idem,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	DeletedAt int64  `json:"deleted_at,omitempty"`
	Replies   int    `json:"replies"`
	Depth     int    `json:"depth"`
}

// Deleted reports whether the post has been soft-deleted.
func (p ForumPost) Deleted() bool { return p.DeletedAt != 0 }

// ForumBoard is a named board plus the counters a listing wants.
type ForumBoard struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	Posts       int    `json:"posts"`
	Threads     int    `json:"threads"`
	LastAt      int64  `json:"last_at,omitempty"`
}

// ForumHit is a search result: the post plus the matched text with the query
// terms marked.
type ForumHit struct {
	ForumPost
	Snippet string `json:"snippet"`
}

// ForumQuery filters a listing. The zero value lists every live thread root on
// every board, newest first.
type ForumQuery struct {
	Board    string
	Author   string
	Parent   string // replies to this post; "-" means thread roots only
	Since    int64  // created strictly after this unix second
	Limit    int
	Deleted  bool // include soft-deleted posts
	Ascutoff bool // order oldest first (feeds read forward)
}

const forumSchema = `
CREATE TABLE IF NOT EXISTS forum_boards (
  name        TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);

-- root denormalises the thread a post belongs to so a thread view is one
-- indexed read instead of a recursive walk per reply. parent_id stays the
-- truth about who answered whom.
CREATE TABLE IF NOT EXISTS forum_posts (
  id          TEXT PRIMARY KEY,
  board       TEXT NOT NULL,
  parent_id   TEXT NOT NULL DEFAULT '',
  root        TEXT NOT NULL,
  author      TEXT NOT NULL,
  title       TEXT NOT NULL DEFAULT '',
  body        TEXT NOT NULL DEFAULT '',
  meta        TEXT NOT NULL DEFAULT '',
  idem        TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  deleted_at  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS forum_posts_board ON forum_posts(board, created_at DESC);
CREATE INDEX IF NOT EXISTS forum_posts_parent ON forum_posts(parent_id, created_at);
CREATE INDEX IF NOT EXISTS forum_posts_root ON forum_posts(root, created_at);
CREATE INDEX IF NOT EXISTS forum_posts_author ON forum_posts(author, created_at DESC);

-- An agent that retries a post it is not sure landed sends the same idem key,
-- and gets the first post back instead of a duplicate. Unique only over
-- non-empty keys, because most posts do not carry one.
CREATE UNIQUE INDEX IF NOT EXISTS forum_posts_idem
  ON forum_posts(author, idem) WHERE idem <> '';

-- Per-username watermark: the newest created_at that name has been shown on
-- a board. '' is the whole forum.
CREATE TABLE IF NOT EXISTS forum_reads (
  username   TEXT NOT NULL,
  board      TEXT NOT NULL DEFAULT '',
  last_seen  INTEGER NOT NULL,
  PRIMARY KEY (username, board)
);
`

// forumFTSSchema is applied separately: FTS5 is a compile-time option of the
// driver, and a build without it must still open the database and search with
// LIKE rather than refuse to start.
const forumFTSSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS forum_fts USING fts5(
  id UNINDEXED, board UNINDEXED, author, title, body, tokenize='porter unicode61'
);
`

// migrateForum creates the forum's tables and reports whether full-text search
// is available on this build.
//
// The index is maintained from Go rather than by triggers on an external
// content table. Triggers are less code right here and considerably more code
// the first time a schema change puts the shadow tables out of step, and the
// write path already goes through three functions that can keep one more table
// current.
func migrateForum(db *sql.DB) (bool, error) {
	if _, err := db.Exec(forumSchema); err != nil {
		return false, fmt.Errorf("migrate forum: %w", err)
	}
	if _, err := db.Exec(forumFTSSchema); err != nil {
		// No FTS5 in this driver build. Search falls back to LIKE, which is
		// slower and dumber but never absent.
		return false, nil
	}
	return true, nil
}

// NormalizeBoard folds a board name to the one spelling it is stored under, so
// "Ops", "ops" and " ops " are one board rather than three.
func NormalizeBoard(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '/' || r == '.':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

// NormalizeAuthor trims a username to what is stored. Names stay case-sensitive
// as typed — an agent that calls itself "Raphael" should read as that — but
// whitespace inside one is collapsed so a name can be a single word in a table.
func NormalizeAuthor(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Join(strings.Fields(name), "-")
	if len(name) > 48 {
		name = name[:48]
	}
	return name
}

// newForumID mints a citable post id. Random rather than sequential: ids appear
// in posts as references, and a guessable neighbour invites an agent to cite a
// post that does not exist yet.
func newForumID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice; a time-based id is still
		// unique enough to keep posting rather than to stop.
		return fmt.Sprintf("p%011x", time.Now().UnixNano())
	}
	return "p" + hex.EncodeToString(b[:])
}

// ForumBoardPut creates a board, or updates its description when it exists.
func (s *Store) ForumBoardPut(ctx context.Context, name, description string) (ForumBoard, error) {
	board := NormalizeBoard(name)
	if board == "" {
		return ForumBoard{}, errors.New("board name is empty")
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO forum_boards (name, description, created_at) VALUES (?, ?, ?)
ON CONFLICT(name) DO UPDATE SET description=excluded.description`,
		board, strings.TrimSpace(description), now)
	if err != nil {
		return ForumBoard{}, err
	}
	return s.ForumBoard(ctx, board)
}

// ensureBoard creates a board on first post to it. Posting to a board nobody
// declared is how Usenet worked and is one fewer step for an agent that only
// wants to say something; a board is a string, not a resource to provision.
func (s *Store) ensureBoard(ctx context.Context, tx *sql.Tx, board string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO forum_boards (name, description, created_at) VALUES (?, '', ?)`,
		board, time.Now().Unix())
	return err
}

// ForumBoard returns one board with its counters.
func (s *Store) ForumBoard(ctx context.Context, name string) (ForumBoard, error) {
	board := NormalizeBoard(name)
	row := s.db.QueryRowContext(ctx, `
SELECT b.name, b.description, b.created_at,
       (SELECT COUNT(*) FROM forum_posts p WHERE p.board=b.name AND p.deleted_at=0),
       (SELECT COUNT(*) FROM forum_posts p WHERE p.board=b.name AND p.deleted_at=0 AND p.parent_id=''),
       COALESCE((SELECT MAX(created_at) FROM forum_posts p WHERE p.board=b.name AND p.deleted_at=0), 0)
FROM forum_boards b WHERE b.name=?`, board)
	var b ForumBoard
	err := row.Scan(&b.Name, &b.Description, &b.CreatedAt, &b.Posts, &b.Threads, &b.LastAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ForumBoard{}, ErrNotFound
	}
	return b, err
}

// ForumBoards lists every board, busiest-most-recent first.
func (s *Store) ForumBoards(ctx context.Context) ([]ForumBoard, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT b.name, b.description, b.created_at,
       (SELECT COUNT(*) FROM forum_posts p WHERE p.board=b.name AND p.deleted_at=0),
       (SELECT COUNT(*) FROM forum_posts p WHERE p.board=b.name AND p.deleted_at=0 AND p.parent_id=''),
       COALESCE((SELECT MAX(created_at) FROM forum_posts p WHERE p.board=b.name AND p.deleted_at=0), 0)
FROM forum_boards b
ORDER BY 6 DESC, b.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForumBoard
	for rows.Next() {
		var b ForumBoard
		if err := rows.Scan(&b.Name, &b.Description, &b.CreatedAt, &b.Posts, &b.Threads, &b.LastAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ForumBoardRemove deletes a board. It refuses while posts remain unless force
// is set, in which case the posts go with it — a board is the only container
// here, so removing it silently while its posts survive would strand them where
// no listing looks.
func (s *Store) ForumBoardRemove(ctx context.Context, name string, force bool) error {
	board := NormalizeBoard(name)
	if _, err := s.ForumBoard(ctx, board); err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM forum_posts WHERE board=?`, board).Scan(&n); err != nil {
		return err
	}
	if n > 0 && !force {
		return fmt.Errorf("board %q still holds %d posts (use --force)", board, n)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM forum_posts WHERE board=?`, board); err != nil {
		return err
	}
	if s.forumFTS {
		if _, err := tx.ExecContext(ctx, `DELETE FROM forum_fts WHERE board=?`, board); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM forum_boards WHERE name=?`, board); err != nil {
		return err
	}
	return tx.Commit()
}

const forumColumns = `id, board, parent_id, root, author, title, body, meta, idem,
	created_at, updated_at, deleted_at`

func scanForumPost(sc interface{ Scan(...any) error }) (ForumPost, error) {
	var p ForumPost
	err := sc.Scan(&p.ID, &p.Board, &p.ParentID, &p.Root, &p.Author, &p.Title,
		&p.Body, &p.Meta, &p.Idem, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	return p, err
}

// ForumCreate writes a post and returns it as stored.
//
// Board, author and body are the only requirements, and a reply inherits the
// board and thread of what it answers. When Idem is set and that author already
// used it, the existing post comes back untouched: a retrying agent must be
// able to repeat the call without doubling the thread.
func (s *Store) ForumCreate(ctx context.Context, p ForumPost) (ForumPost, error) {
	p.Author = NormalizeAuthor(p.Author)
	if p.Author == "" {
		return ForumPost{}, errors.New("author is required (any username, no account needed)")
	}
	p.Title = strings.TrimSpace(p.Title)
	p.Body = strings.TrimRight(p.Body, "\n")
	p.Meta = strings.TrimSpace(p.Meta)
	if p.Meta != "" && !json.Valid([]byte(p.Meta)) {
		return ForumPost{}, errors.New("meta must be valid JSON")
	}
	if strings.TrimSpace(p.Body) == "" && p.Title == "" {
		return ForumPost{}, errors.New("post needs a title or a body")
	}
	if p.Idem != "" {
		if existing, err := s.forumByIdem(ctx, p.Author, p.Idem); err == nil {
			return existing, nil
		} else if !errors.Is(err, ErrNotFound) {
			return ForumPost{}, err
		}
	}

	if p.ParentID != "" {
		parent, err := s.ForumGet(ctx, p.ParentID)
		if err != nil {
			return ForumPost{}, fmt.Errorf("reply to %s: %w", p.ParentID, err)
		}
		p.Board = parent.Board
		p.Root = parent.Root
		p.ParentID = parent.ID
	} else {
		p.Board = NormalizeBoard(p.Board)
		if p.Board == "" {
			return ForumPost{}, errors.New("board is required")
		}
	}

	now := time.Now().Unix()
	p.ID = newForumID()
	if p.Root == "" {
		p.Root = p.ID
	}
	p.CreatedAt, p.UpdatedAt, p.DeletedAt = now, now, 0

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ForumPost{}, err
	}
	defer tx.Rollback()
	if err := s.ensureBoard(ctx, tx, p.Board); err != nil {
		return ForumPost{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO forum_posts (`+forumColumns+`)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Board, p.ParentID, p.Root, p.Author, p.Title, p.Body, p.Meta, p.Idem,
		p.CreatedAt, p.UpdatedAt, p.DeletedAt); err != nil {
		// Two agents retrying the same idempotent post at once: the loser reads
		// the winner's row rather than reporting a constraint the caller cannot
		// act on.
		if p.Idem != "" {
			if existing, lookupErr := s.forumByIdem(ctx, p.Author, p.Idem); lookupErr == nil {
				return existing, nil
			}
		}
		return ForumPost{}, err
	}
	if err := s.forumIndex(ctx, tx, p); err != nil {
		return ForumPost{}, err
	}
	if err := tx.Commit(); err != nil {
		return ForumPost{}, err
	}
	return p, nil
}

// ForumUpdate edits a post in place. Only the author's own name may edit it;
// empty fields are left alone so a body edit does not blank a title.
func (s *Store) ForumUpdate(ctx context.Context, id, author, title, body, meta string) (ForumPost, error) {
	p, err := s.ForumGet(ctx, id)
	if err != nil {
		return ForumPost{}, err
	}
	if p.Author != NormalizeAuthor(author) {
		return ForumPost{}, fmt.Errorf("%s written by %q: %w", p.ID, p.Author, ErrForumDenied)
	}
	if title != "" {
		p.Title = strings.TrimSpace(title)
	}
	if body != "" {
		p.Body = strings.TrimRight(body, "\n")
	}
	if meta != "" {
		if !json.Valid([]byte(meta)) {
			return ForumPost{}, errors.New("meta must be valid JSON")
		}
		p.Meta = strings.TrimSpace(meta)
	}
	p.UpdatedAt = time.Now().Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ForumPost{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE forum_posts SET title=?, body=?, meta=?, updated_at=? WHERE id=?`,
		p.Title, p.Body, p.Meta, p.UpdatedAt, p.ID); err != nil {
		return ForumPost{}, err
	}
	if err := s.forumIndex(ctx, tx, p); err != nil {
		return ForumPost{}, err
	}
	if err := tx.Commit(); err != nil {
		return ForumPost{}, err
	}
	return p, nil
}

// ForumDelete soft-deletes a post: the row and its id survive, the body stops
// being served, and replies stay reachable under the same thread. Ids are
// quoted between agents, so a hard delete would break references that a
// tombstone keeps honest.
func (s *Store) ForumDelete(ctx context.Context, id, author string) error {
	p, err := s.ForumGet(ctx, id)
	if err != nil {
		return err
	}
	if p.Author != NormalizeAuthor(author) {
		return fmt.Errorf("%s written by %q: %w", p.ID, p.Author, ErrForumDenied)
	}
	if p.Deleted() {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE forum_posts SET deleted_at=?, updated_at=? WHERE id=?`,
		time.Now().Unix(), time.Now().Unix(), p.ID); err != nil {
		return err
	}
	if s.forumFTS {
		if _, err := tx.ExecContext(ctx, `DELETE FROM forum_fts WHERE id=?`, p.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ForumGet returns one post by id, deleted or not.
func (s *Store) ForumGet(ctx context.Context, id string) (ForumPost, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ForumPost{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+forumColumns+` FROM forum_posts WHERE id=?`, id)
	p, err := scanForumPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ForumPost{}, ErrNotFound
	}
	if err != nil {
		return ForumPost{}, err
	}
	p.Replies, err = s.forumReplyCount(ctx, p.Root, p.ID)
	return p, err
}

func (s *Store) forumByIdem(ctx context.Context, author, idem string) (ForumPost, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+forumColumns+` FROM forum_posts WHERE author=? AND idem=?`, author, idem)
	p, err := scanForumPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ForumPost{}, ErrNotFound
	}
	return p, err
}

// forumReplyCount counts live descendants of a post. For a thread root that is
// every reply in the thread; for a reply it is the direct answers to it.
func (s *Store) forumReplyCount(ctx context.Context, root, id string) (int, error) {
	var n int
	var err error
	if root == id {
		err = s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM forum_posts WHERE root=? AND id<>root AND deleted_at=0`,
			root).Scan(&n)
	} else {
		err = s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM forum_posts WHERE parent_id=? AND deleted_at=0`, id).Scan(&n)
	}
	return n, err
}

// ForumList returns posts matching q, newest first unless q.Ascutoff.
func (s *Store) ForumList(ctx context.Context, q ForumQuery) ([]ForumPost, error) {
	where := []string{"1=1"}
	var args []any
	if b := NormalizeBoard(q.Board); b != "" {
		where = append(where, "board=?")
		args = append(args, b)
	}
	if a := NormalizeAuthor(q.Author); a != "" {
		where = append(where, "author=?")
		args = append(args, a)
	}
	switch q.Parent {
	case "":
	case "-":
		where = append(where, "parent_id=''")
	default:
		where = append(where, "parent_id=?")
		args = append(args, q.Parent)
	}
	if q.Since > 0 {
		where = append(where, "created_at>?")
		args = append(args, q.Since)
	}
	if !q.Deleted {
		where = append(where, "deleted_at=0")
	}
	order := "created_at DESC, rowid DESC"
	if q.Ascutoff {
		order = "created_at ASC, rowid ASC"
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+forumColumns+` FROM forum_posts WHERE `+strings.Join(where, " AND ")+
			` ORDER BY `+order+` LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForumPost
	for rows.Next() {
		p, err := scanForumPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		n, err := s.forumReplyCount(ctx, out[i].Root, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Replies = n
		out[i] = redactDeleted(out[i])
	}
	return out, nil
}

// redactDeleted strips the readable parts of a soft-deleted post on the way
// out. The row keeps its body so a delete stays recoverable by hand, but every
// listing, thread and JSON dump agrees that a deleted post has nothing to read
// — a "delete" that still prints the text is not one.
func redactDeleted(p ForumPost) ForumPost {
	if !p.Deleted() {
		return p
	}
	p.Body, p.Meta, p.Title = "", "", ""
	return p
}

// ForumThread returns a whole thread in reading order: the root first, then
// every reply nested under the post it answers, with Depth set for display.
//
// Deleted posts are kept as tombstones when they still have live replies,
// because dropping them would reparent an answer onto a question that is no
// longer shown and make the conversation unreadable.
func (s *Store) ForumThread(ctx context.Context, id string) ([]ForumPost, error) {
	seed, err := s.ForumGet(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+forumColumns+` FROM forum_posts WHERE root=? ORDER BY created_at ASC, rowid ASC`,
		seed.Root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byParent := map[string][]ForumPost{}
	var root ForumPost
	var found bool
	for rows.Next() {
		p, err := scanForumPost(rows)
		if err != nil {
			return nil, err
		}
		if p.ID == p.Root {
			root, found = p, true
			continue
		}
		byParent[p.ParentID] = append(byParent[p.ParentID], p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	var out []ForumPost
	var walk func(p ForumPost, depth int)
	walk = func(p ForumPost, depth int) {
		p.Depth = depth
		kids := byParent[p.ID]
		p.Replies = len(kids)
		// The root is always shown, deleted or not: asking for a thread by id
		// and getting an empty page back reads as a broken lookup rather than
		// as a deleted post. Deleted replies only stay when something hangs off
		// them, because dropping them would reparent an answer onto a question
		// that is no longer on the page.
		if depth == 0 || !p.Deleted() || len(kids) > 0 {
			out = append(out, redactDeleted(p))
		}
		for _, k := range kids {
			walk(k, depth+1)
		}
	}
	walk(root, 0)
	return out, nil
}

// ForumSearch finds posts matching a query, best match first.
//
// With FTS5 the query is an FTS expression (so "railway NOT template" and
// "author:x" style column filters work) and results carry a snippet with the
// matched terms marked by [ and ]. Without it, every term must appear somewhere
// in the title or body.
func (s *Store) ForumSearch(ctx context.Context, query, board string, limit int) ([]ForumHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search needs a query")
	}
	if limit <= 0 {
		limit = 25
	}
	if s.forumFTS {
		hits, err := s.forumSearchFTS(ctx, query, board, limit)
		if err == nil {
			return hits, nil
		}
		// A malformed FTS expression is a syntax error from SQLite, not an
		// empty result. Rather than hand an agent "fts5: syntax error near
		// ...", fall through and treat the string as plain words.
		if !strings.Contains(strings.ToLower(err.Error()), "fts5") {
			return nil, err
		}
	}
	return s.forumSearchLike(ctx, query, board, limit)
}

func (s *Store) forumSearchFTS(ctx context.Context, query, board string, limit int) ([]ForumHit, error) {
	args := []any{query}
	filter := ""
	if b := NormalizeBoard(board); b != "" {
		filter = " AND p.board=?"
		args = append(args, b)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT `+forumPrefixed+`, snippet(forum_fts, 4, '[', ']', ' … ', 16)
FROM forum_fts f JOIN forum_posts p ON p.id = f.id
WHERE forum_fts MATCH ? AND p.deleted_at=0`+filter+`
ORDER BY bm25(forum_fts, 0, 0, 1.0, 4.0, 1.0)
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanForumHits(rows)
}

func (s *Store) forumSearchLike(ctx context.Context, query, board string, limit int) ([]ForumHit, error) {
	where := []string{"p.deleted_at=0"}
	var args []any
	for _, term := range strings.Fields(query) {
		// The query may be an FTS expression that FTS5 refused, so its
		// operators and quoting are stripped rather than searched for
		// literally: nothing in a body matches "(template".
		term = strings.Trim(term, `"'()[]{}*^:+-~,.`)
		if term == "" {
			continue
		}
		if strings.EqualFold(term, "or") || strings.EqualFold(term, "and") || strings.EqualFold(term, "not") {
			continue
		}
		where = append(where, "(p.title LIKE ? OR p.body LIKE ? OR p.author LIKE ?)")
		like := "%" + term + "%"
		args = append(args, like, like, like)
	}
	if b := NormalizeBoard(board); b != "" {
		where = append(where, "p.board=?")
		args = append(args, b)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+forumPrefixed+`, substr(p.body, 1, 160) FROM forum_posts p WHERE `+
			strings.Join(where, " AND ")+` ORDER BY p.created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanForumHits(rows)
}

const forumPrefixed = `p.id, p.board, p.parent_id, p.root, p.author, p.title, p.body,
	p.meta, p.idem, p.created_at, p.updated_at, p.deleted_at`

func scanForumHits(rows *sql.Rows) ([]ForumHit, error) {
	var out []ForumHit
	for rows.Next() {
		var h ForumHit
		if err := rows.Scan(&h.ID, &h.Board, &h.ParentID, &h.Root, &h.Author, &h.Title,
			&h.Body, &h.Meta, &h.Idem, &h.CreatedAt, &h.UpdatedAt, &h.DeletedAt, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// forumIndex keeps the search index in step with one post.
func (s *Store) forumIndex(ctx context.Context, tx *sql.Tx, p ForumPost) error {
	if !s.forumFTS {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM forum_fts WHERE id=?`, p.ID); err != nil {
		return err
	}
	if p.Deleted() {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO forum_fts (id, board, author, title, body) VALUES (?,?,?,?,?)`,
		p.ID, p.Board, p.Author, p.Title, p.Body)
	return err
}

// ForumReindex rebuilds the search index from the posts table. Nothing calls it
// on the write path; it exists for a database that was written by a build
// without FTS5, whose posts would otherwise never be findable.
func (s *Store) ForumReindex(ctx context.Context) (int, error) {
	if !s.forumFTS {
		return 0, errors.New("this build has no FTS5: search falls back to LIKE")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM forum_fts`); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO forum_fts (id, board, author, title, body)
SELECT id, board, author, title, body FROM forum_posts WHERE deleted_at=0`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}

// ForumSearchable reports whether this build indexes posts with FTS5.
func (s *Store) ForumSearchable() bool { return s.forumFTS }

// ForumFeed returns what a username has not been shown yet, oldest first —
// the question an agent actually has when it comes back to the board. When
// mark is set the watermark advances to the newest post returned, so the next
// call continues where this one stopped.
func (s *Store) ForumFeed(ctx context.Context, username, board string, limit int, mark bool) ([]ForumPost, error) {
	user := NormalizeAuthor(username)
	if user == "" {
		return nil, errors.New("feed needs a username")
	}
	board = NormalizeBoard(board)
	since, err := s.ForumWatermark(ctx, user, board)
	if err != nil {
		return nil, err
	}
	posts, err := s.ForumList(ctx, ForumQuery{
		Board: board, Since: since, Limit: limit, Ascutoff: true,
	})
	if err != nil {
		return nil, err
	}
	if mark && len(posts) > 0 {
		newest := since
		for _, p := range posts {
			if p.CreatedAt > newest {
				newest = p.CreatedAt
			}
		}
		if err := s.ForumMarkRead(ctx, user, board, newest); err != nil {
			return nil, err
		}
	}
	return posts, nil
}

// ForumWatermark returns the newest post time this username has been shown on a
// board ("" for the whole forum), or 0 if it has never read.
func (s *Store) ForumWatermark(ctx context.Context, username, board string) (int64, error) {
	var ts int64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_seen FROM forum_reads WHERE username=? AND board=?`,
		NormalizeAuthor(username), NormalizeBoard(board)).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return ts, err
}

// ForumMarkRead moves a username's watermark forward. It never moves backward:
// two agents sharing a name, or a feed read twice out of order, must not
// resurface posts that were already handled.
func (s *Store) ForumMarkRead(ctx context.Context, username, board string, at int64) error {
	user := NormalizeAuthor(username)
	if user == "" {
		return errors.New("mark-read needs a username")
	}
	if at <= 0 {
		at = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO forum_reads (username, board, last_seen) VALUES (?,?,?)
ON CONFLICT(username, board) DO UPDATE SET last_seen=MAX(last_seen, excluded.last_seen)`,
		user, NormalizeBoard(board), at)
	return err
}
