package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Many threads, not one.
//
// One thread was a party line. Two agents working on unrelated projects both
// wrote into it, and each one then had to read the other's traffic to find its
// own — at real token cost, for information that could not act on anything it
// was doing. A thread is a separate conversation, so an agent reads only what
// was said to it.
//
// Claims deliberately do not divide this way. A browser is one browser whatever
// conversation is talking about it, so the claim fold spans every thread and the
// HOLDS block says the same thing in all of them. Per-thread leases would be a
// lock that does not lock: two agents in two threads would both be told the
// browser was free, which is the exact failure claims exist to prevent. See
// currentClaims, which has no thread in it at all.

// GlobalThread is the thread that always exists.
//
// It is the default for every command and the landing place for every message
// written before threads existed, so it cannot be deleted: removing it would
// leave the unqualified case — an agent that never names a thread — writing into
// something that is not there.
const GlobalThread = "global"

// Thread is one conversation, with enough about it to choose between them.
type Thread struct {
	ID string
	// About is why this thread exists, shown in the picker and in `thread list`.
	About     string
	CreatedAt time.Time
	// Messages is how many messages it holds, and LastAt when the last one was
	// said. LastAt is zero for a thread nobody has written in yet — which reads
	// as "never", not as 1970.
	Messages int
	LastAt   time.Time
	// WorkspaceID is the herdr workspace this thread belongs to, empty for one
	// somebody made by hand. It is what makes membership answerable without
	// anyone having written anything: an agent is in this conversation because
	// herdr says its pane is in that workspace, not because it once posted.
	WorkspaceID string
	// ClosedAt is when the workspace went away. Nil means open.
	//
	// A closed thread is not a deleted one. It leaves `thread list`, refuses new
	// messages and reaches nobody, but `thread log --thread <id>` still reads it:
	// what changed on this machine stays true after the window is shut, and
	// losing a space's record of it to somebody closing a tab would be the
	// deletion nobody notices until they go looking.
	ClosedAt *time.Time
}

// Closed reports whether the thread has been retired.
func (t Thread) Closed() bool { return t.ClosedAt != nil }

// threadListSchema is the registry of threads and the index messages are read
// through. Both are applied on every Open, after the thread column exists: the
// index names a column, so it cannot be created before the column is.
const threadListSchema = `
CREATE TABLE IF NOT EXISTS threads (
  id          TEXT PRIMARY KEY,
  about       TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS thread_messages_thread ON thread_messages(thread, seq);
`

// ParseThreadID validates a thread id, which is shaped like a job id:
// lowercase, digits, and single hyphens between them.
//
// The shape is enforced rather than slugged into place, because a thread id is
// typed by one agent and read by another. `--thread Browser Work` silently
// becoming `browser-work` means the two agents are one keystroke away from
// writing into two different conversations and each seeing half of it.
func ParseThreadID(s string) (string, error) {
	id := strings.TrimSpace(s)
	if id == "" {
		return "", errors.New("a thread needs an id")
	}
	bad := func() error {
		return fmt.Errorf("%q is not a thread id: lowercase letters, digits and single "+
			"hyphens, like a job id (e.g. webapp, tiktok-deals)", s)
	}
	if strings.HasPrefix(id, "-") || strings.HasSuffix(id, "-") {
		return "", bad()
	}
	prevDash := false
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			prevDash = false
		case r == '-':
			if prevDash {
				return "", bad()
			}
			prevDash = true
		default:
			return "", bad()
		}
	}
	return id, nil
}

// resolveThreadID is the selection rule everything writes through: whatever was
// asked for, or global. It is deliberately not validating — a caller that means
// a specific thread has already been through ParseThreadID, and the store's job
// here is only to make "unset" mean one definite thing.
func resolveThreadID(id string) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	return GlobalThread
}

// migrateThreadList adds the thread column, the registry, and global itself.
//
// Every step is additive. The column carries a default of 'global', so every
// message written before threads existed lands in the thread that every
// unqualified command reads — the migration moves no rows and cannot lose one.
func migrateThreadList(db *sql.DB) error {
	if err := addThreadColumn(db); err != nil {
		return err
	}
	if _, err := db.Exec(threadListSchema); err != nil {
		return fmt.Errorf("migrate threads: %w", err)
	}
	// Both are additive and both default to "a thread nobody automated": an
	// existing thread has no workspace and is open, which is exactly what every
	// thread created by hand before this existed actually is.
	if err := addColumn(db, "threads", "workspace_id", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := addColumn(db, "threads", "closed_at", `INTEGER`); err != nil {
		return err
	}
	// Created on demand, on every open, so a database that has never seen a
	// thread command still has somewhere for the first one to land.
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO threads (id, about, created_at) VALUES (?,?,?)`,
		GlobalThread, "everything that is not about one project", time.Now().Unix()); err != nil {
		return fmt.Errorf("migrate threads: %w", err)
	}
	return nil
}

// addThreadColumn puts every existing message in global.
//
// SQLite backfills every existing row with the default as part of the ALTER, so
// the one statement is also the data migration: the migration moves no rows and
// cannot lose one.
func addThreadColumn(db *sql.DB) error {
	return addMessageColumn(db, "thread", `TEXT NOT NULL DEFAULT '`+GlobalThread+`'`)
}

// addMessageColumn adds one column to thread_messages, if it is not already
// there.
//
// The check and the ALTER share one IMMEDIATE transaction for the same reason
// the room rename does: bermuda opens this database from the daemon, the board,
// and whichever agent is running a thread command, and two of them starting
// together would otherwise both see the column missing and the loser would fail
// on "duplicate column name" — a store that refuses to open, on a schema change
// that had already succeeded.
func addMessageColumn(db *sql.DB, column, ddl string) error {
	return addColumn(db, "thread_messages", column, ddl)
}

// addColumn is that migration for any table. The jobs and runs schema used to
// do the same check and ALTER on a bare connection with no transaction at all —
// the exact bug the comment above describes, in the code it was written next
// to.
func addColumn(db *sql.DB, table, column, ddl string) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("migrate %s: %w", table, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	has, err := connHasColumn(ctx, conn, table, column)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", table, err)
	}
	if has {
		return nil
	}
	if _, err := conn.ExecContext(ctx,
		"ALTER TABLE "+table+" ADD COLUMN "+column+" "+ddl); err != nil {
		return fmt.Errorf("migrate %s: %w", table, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("migrate %s: %w", table, err)
	}
	committed = true
	return nil
}

// connHasColumn is hasColumn asked inside a transaction, so the answer cannot
// change between the question and the ALTER that depends on it.
func connHasColumn(ctx context.Context, conn *sql.Conn, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Threads lists every conversation, global first and the rest alphabetically.
//
// Global leads because it is where an unqualified command writes. The rest are
// by name because this is the order `bermuda thread list` prints in, and a
// report is read by looking a name up in it. Each row carries its message count
// and its last activity, so a caller that wants another order — the board sorts
// its row and its picker by activity — has what it needs to sort by.
func (s *Store) Threads(ctx context.Context) ([]Thread, error) {
	return s.threads(ctx, "t.closed_at IS NULL")
}

// ClosedThreads is the same list for conversations whose workspace has gone.
//
// They are kept out of Threads rather than merged into it because the list is
// read to choose where to write, and a closed thread is not somewhere anything
// can be written. They stay readable so the record survives the window.
func (s *Store) ClosedThreads(ctx context.Context) ([]Thread, error) {
	return s.threads(ctx, "t.closed_at IS NOT NULL")
}

func (s *Store) threads(ctx context.Context, where string) ([]Thread, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.about, t.created_at, t.workspace_id, t.closed_at,
		       COUNT(m.seq), COALESCE(MAX(m.created_at), 0)
		FROM threads t
		LEFT JOIN thread_messages m ON m.thread = t.id
		WHERE `+where+`
		GROUP BY t.id, t.about, t.created_at, t.workspace_id, t.closed_at
		ORDER BY t.id <> ?, t.id`, GlobalThread)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// An empty result is impossible in practice — global is created on open —
	// but a caller rendering JSON wants a list rather than null either way.
	out := []Thread{}
	for rows.Next() {
		var t Thread
		var created, last int64
		var closed sql.NullInt64
		if err := rows.Scan(&t.ID, &t.About, &created, &t.WorkspaceID, &closed,
			&t.Messages, &last); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(created, 0)
		if last > 0 {
			t.LastAt = time.Unix(last, 0)
		}
		if closed.Valid {
			at := time.Unix(closed.Int64, 0)
			t.ClosedAt = &at
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ThreadExists reports whether a thread has been created.
func (s *Store) ThreadExists(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM threads WHERE id = ?`, resolveThreadID(id)).Scan(&n)
	return n > 0, err
}

// NewThread creates a conversation, and refuses to create one twice.
//
// Re-creating is an error rather than a silent no-op because the second caller
// usually meant to start something fresh; being handed somebody else's ongoing
// conversation instead, with no indication, is how two projects end up sharing
// one thread again.
func (s *Store) NewThread(ctx context.Context, id, about string) (Thread, error) {
	parsed, err := ParseThreadID(id)
	if err != nil {
		return Thread{}, err
	}
	t := Thread{ID: parsed, About: strings.TrimSpace(about), CreatedAt: time.Now()}
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO threads (id, about, created_at) VALUES (?,?,?)`,
		t.ID, t.About, t.CreatedAt.Unix())
	if err != nil {
		return Thread{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Thread{}, fmt.Errorf("thread %s already exists", t.ID)
	}
	return t, nil
}

// RemoveThread deletes a conversation and everything said in it, returning how
// many messages went with it.
//
// Global is refused outright, and a thread that still holds messages is refused
// without force: the thread is the record of what is currently true, and losing
// it silently to a mistyped id is the one deletion nobody would notice until
// they went looking for something that used to be written down.
func (s *Store) RemoveThread(ctx context.Context, id string, force bool) (int, error) {
	target := resolveThreadID(id)
	if target == GlobalThread {
		return 0, errors.New("global cannot be deleted: it is where every message " +
			"lands that does not name a thread, including every message written " +
			"before threads existed")
	}

	// The count and the delete share one IMMEDIATE transaction, so a message
	// posted between them is either refused with the rest or removed with the
	// rest — never left behind in a thread that no longer exists, where nothing
	// would ever list it again.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	var exists, held int
	if err := conn.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(1) FROM threads WHERE id = ?),
		        (SELECT COUNT(1) FROM thread_messages WHERE thread = ?)`,
		target, target).Scan(&exists, &held); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, fmt.Errorf("no thread %s: `bermuda thread list` says which there are", target)
	}
	if held > 0 && !force {
		return 0, fmt.Errorf("%s holds %d messages: pass --force to delete it and them",
			target, held)
	}
	// Refused even under --force, because --force is a statement about messages
	// and this is a lock.
	//
	// Claims live in the same append-only table as everything else, so deleting
	// a thread deletes the claim rows written in it — and the claims fold reads
	// what is left. A live lease taken from this thread would simply stop
	// existing: the browser would read as free while an agent was still driving
	// it, with nothing raised anywhere. That is the two-agents-on-one-browser
	// failure claims exist to prevent, caused by the tool meant to prevent it.
	if err := refuseWhileAnythingIsHeld(ctx, conn, target, time.Now()); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx,
		`DELETE FROM thread_messages WHERE thread = ?`, target); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM threads WHERE id = ?`, target); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, err
	}
	committed = true
	return held, nil
}

// refuseWhileAnythingIsHeld refuses to delete a thread a live lease was taken
// from, naming the resource and its holder.
//
// It runs inside the caller's transaction, so a claim taken between the check
// and the delete is either refused with the rest or was never live at all.
//
// Expiry is applied here, at read time, like every other claim query: a lease
// that lapsed holds nothing, and one killed agent must not make its thread
// permanently undeletable.
//
// The way out is deliberately the ordinary one — release the resource, or wait
// for the lease to lapse — rather than a stronger flag. A hold nothing can
// release is a hold whose agent died without a TTL, and that is worth being
// made to look at.
func refuseWhileAnythingIsHeld(ctx context.Context, conn *sql.Conn, thread string, now time.Time) error {
	var resource string
	var holder Identity
	err := conn.QueryRowContext(ctx, `
		SELECT c.resource, c.agent, c.pid
		FROM (`+currentClaims+`) c
		JOIN thread_messages m ON m.seq = c.seq
		WHERE m.thread = ? AND (c.expires_at IS NULL OR c.expires_at > ?)
		ORDER BY c.seq
		LIMIT 1`, thread, now.Unix()).Scan(&resource, &holder.Name, &holder.PID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// The holder is named with its pid, because that is which agent to go and
	// find; the suggested command uses the bare name, because the pid is
	// resolved from the environment of whoever runs it and typing somebody
	// else's would be refused anyway.
	return fmt.Errorf("%s cannot be deleted while %s is held from it by %s: "+
		"the claim is a message in this thread, so deleting it would hand %s "+
		"to the next agent that asks and tell nobody — "+
		"`bermuda thread release %s --as %s` first, or wait for the lease to lapse",
		thread, resource, holder.Short(), resource, resource, holder.Name)
}

// unknownThread is the refusal a write into a thread that was never created
// gets.
//
// Auto-creating instead would be the quiet failure: `--thread betterlingu` would
// succeed, and the agent reading `webapp` would never see the message or
// have any reason to suspect one existed.
func unknownThread(id string) error {
	return fmt.Errorf("no thread %s: create it with `bermuda thread new %s`", id, id)
}

// whyNotWritable explains a write that landed nowhere.
//
// A closed thread and a thread that never existed both refuse the insert, and
// they need different answers: one is a typo to fix, the other is a space that
// has been shut and a suggestion to create it would be actively wrong.
func (s *Store) whyNotWritable(ctx context.Context, id string) error {
	var closed sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT closed_at FROM threads WHERE id = ?`, id).Scan(&closed)
	if errors.Is(err, sql.ErrNoRows) {
		return unknownThread(id)
	}
	if err != nil {
		return err
	}
	if closed.Valid {
		return fmt.Errorf("thread %s is closed: its workspace is gone, so nothing new "+
			"can be said in it — `bermuda thread log --thread %s` still reads what was",
			id, id)
	}
	// The row exists and is open, so the insert lost a race with a delete or a
	// close between the statement and this question. Either way it is gone now.
	return unknownThread(id)
}
