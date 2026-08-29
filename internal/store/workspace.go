package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Threads that belong to a workspace, rather than to whoever remembered to make
// one.
//
// A thread only helps if the agents that should be in it are in it, and asking
// agents to create and join threads themselves does not survive contact: the one
// that forgets is invisible to everybody else, and nothing anywhere reports that
// it forgot. Herdr already knows the answer — every agent sits in a pane, and
// every pane belongs to a workspace — so the workspace *is* the membership list
// and no agent has to do anything to be in it.
//
// This is also what makes `@all` affordable. Scoped to a workspace it reaches
// the handful of agents working on one thing; unscoped it reaches every agent on
// the machine, which is a broadcast into contexts that cannot act on it and pay
// for it anyway.

// WorkspaceThreadPrefix names a thread for a workspace nobody labelled.
//
// An unlabelled workspace still needs somewhere to talk, and `ws-a1b2c3` is at
// least honestly opaque — unlike a guess at what the space is for, which would
// read like something a person chose.
const WorkspaceThreadPrefix = "ws-"

// suffixAlphabet is what a disambiguating suffix is drawn from: exactly the
// characters a thread id may contain, so a generated id can never fail
// ParseThreadID.
const suffixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// suffixLen is how long that suffix is. Six characters of 36 is ~2 billion, on a
// machine that has a few dozen workspaces — the suffix exists to break a tie
// between two spaces a person gave the same name, not to be globally unique.
const suffixLen = 6

// SlugThread turns a workspace label into a thread id.
//
// The rule against slugging user-typed ids does not apply here, and the
// difference is worth stating because the two look alike. `--thread "Better
// Lingo"` is refused because a human typed it and a silent repair puts two
// agents one keystroke apart in two conversations. This slug is derived, not
// typed: the same workspace always produces the same id, nobody is guessing at
// it, and the alternative is an opaque id in every `thread list` and every
// delivered `[thread <id>]` prefix.
//
// The label names the thread once, at creation, and never again. Everything
// afterwards — membership, auto-close, which thread this agent is in — resolves
// through the workspace_id column, so renaming a space keeps its conversation
// and leaves the id alone. That is deliberate: a thread id appears in scripts,
// in cron entries and in messages already delivered, and an id that follows a
// rename is an id that stops resolving the day somebody tidies their workspace
// names. `thread list` shows the space's current label next to the id, which is
// where the drift actually needs to be visible.
func SlugThread(label string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(label)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			// Any run of anything else collapses to one hyphen, and a leading one
			// never gets written at all.
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// randomSuffix is the tiebreaker appended to a slug that is already taken.
func randomSuffix() (string, error) {
	buf := make([]byte, suffixLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("name a thread: %w", err)
	}
	out := make([]byte, suffixLen)
	for i, b := range buf {
		out[i] = suffixAlphabet[int(b)%len(suffixAlphabet)]
	}
	return string(out), nil
}

// ThreadForWorkspace returns the open thread belonging to a workspace, or nil.
func (s *Store) ThreadForWorkspace(ctx context.Context, workspaceID string) (*Thread, error) {
	id := strings.TrimSpace(workspaceID)
	if id == "" {
		return nil, nil
	}
	var t Thread
	var created int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, about, created_at, workspace_id
		FROM threads WHERE workspace_id = ? AND closed_at IS NULL`,
		id).Scan(&t.ID, &t.About, &created, &t.WorkspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.CreatedAt = time.Unix(created, 0)
	return &t, nil
}

// EnsureWorkspaceThread returns the workspace's thread, creating it if this is
// the first time anything has been said in that space.
//
// Creating on demand is the whole point: an agent should not have to know that
// threads exist, let alone make one. It is idempotent, so every agent in the
// space racing to write its first message ends up in the same conversation
// rather than each making its own.
func (s *Store) EnsureWorkspaceThread(ctx context.Context, workspaceID, label string) (Thread, error) {
	wsID := strings.TrimSpace(workspaceID)
	if wsID == "" {
		return Thread{}, errors.New("a workspace thread needs a workspace id")
	}
	if existing, err := s.ThreadForWorkspace(ctx, wsID); err != nil {
		return Thread{}, err
	} else if existing != nil {
		return *existing, nil
	}

	about := "the " + strings.TrimSpace(label) + " workspace"
	if strings.TrimSpace(label) == "" {
		about = "a workspace nobody labelled"
	}
	// Up to a handful of attempts, because the only way one fails is a collision
	// with an id that is already taken, and the second draw of six random
	// characters colliding as well is not a case worth looping forever over.
	for attempt := 0; attempt < 5; attempt++ {
		id, err := s.workspaceThreadID(ctx, label, attempt)
		if err != nil {
			return Thread{}, err
		}
		t := Thread{ID: id, About: about, CreatedAt: time.Now(), WorkspaceID: wsID}
		res, err := s.db.ExecContext(ctx, `
			INSERT OR IGNORE INTO threads (id, about, created_at, workspace_id)
			VALUES (?,?,?,?)`, t.ID, t.About, t.CreatedAt.Unix(), t.WorkspaceID)
		if err != nil {
			return Thread{}, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return t, nil
		}
		// Lost the race, or the slug was taken between the check and the insert.
		// Whoever won may have been another agent in this same workspace, in
		// which case its thread is now ours too.
		if existing, err := s.ThreadForWorkspace(ctx, wsID); err != nil {
			return Thread{}, err
		} else if existing != nil {
			return *existing, nil
		}
	}
	return Thread{}, fmt.Errorf("could not name a thread for workspace %s", wsID)
}

// workspaceThreadID is the id to try on this attempt: the bare slug first, then
// the slug with a random suffix.
//
// A closed thread still holds the id, so a space labelled the same as one that
// was shut yesterday gets a suffix rather than writing into the old record.
func (s *Store) workspaceThreadID(ctx context.Context, label string, attempt int) (string, error) {
	slug := SlugThread(label)
	if slug == "" || slug == GlobalThread {
		// An unlabelled space, or one a person called "global", which would
		// otherwise collide with the thread that cannot be deleted.
		suffix, err := randomSuffix()
		if err != nil {
			return "", err
		}
		return WorkspaceThreadPrefix + suffix, nil
	}
	if attempt == 0 {
		taken, err := s.threadIDTaken(ctx, slug)
		if err != nil {
			return "", err
		}
		if !taken {
			return slug, nil
		}
	}
	suffix, err := randomSuffix()
	if err != nil {
		return "", err
	}
	return slug + "-" + suffix, nil
}

// threadIDTaken reports whether an id is in use, closed threads included.
func (s *Store) threadIDTaken(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM threads WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// CloseThread retires a conversation without deleting a word of it.
//
// Global is refused, for the reason it cannot be deleted: it is where every
// unqualified write lands, and closing it would leave those writes with nowhere
// to go.
//
// A live lease taken from the thread refuses the close, exactly as it refuses a
// delete. The reasoning is not quite the same — closing keeps the claim rows, so
// the fold still reads the resource as held — but the resulting state is a lock
// held from a conversation nobody can write to, so the agent that needs to
// release it cannot say so in the place the claim was made. Better to leave the
// thread open until the resource comes back.
func (s *Store) CloseThread(ctx context.Context, id string, now time.Time) error {
	target := resolveThreadID(id)
	if target == GlobalThread {
		return errors.New("global cannot be closed: it is where every message lands " +
			"that does not name a thread")
	}
	now = resolveNow(now)

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	var exists int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM threads WHERE id = ?`, target).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("no thread %s: `bermuda thread list` says which there are", target)
	}
	if err := refuseWhileAnythingIsHeld(ctx, conn, target, now); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx,
		`UPDATE threads SET closed_at = ? WHERE id = ? AND closed_at IS NULL`,
		now.Unix(), target); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// CloseVanishedWorkspaces closes every workspace thread whose workspace herdr no
// longer reports, and returns the ids it closed.
//
// A thread with no workspace column is left alone: somebody made it by hand and
// nothing about herdr's list says anything about whether they are done with it.
//
// A close that is refused because something is still held is not an error here.
// The daemon runs this every tick, so the next one will try again, and the
// resource coming back is the ordinary way that resolves. Failing the sweep over
// it would mean one stuck lease stopped every other thread from ever being
// tidied.
func (s *Store) CloseVanishedWorkspaces(ctx context.Context, live []string, now time.Time) ([]string, error) {
	alive := make(map[string]bool, len(live))
	for _, id := range live {
		if id = strings.TrimSpace(id); id != "" {
			alive[id] = true
		}
	}
	open, err := s.Threads(ctx)
	if err != nil {
		return nil, err
	}
	var closed []string
	for _, t := range open {
		if t.WorkspaceID == "" || alive[t.WorkspaceID] {
			continue
		}
		if err := s.CloseThread(ctx, t.ID, now); err != nil {
			continue
		}
		closed = append(closed, t.ID)
	}
	return closed, nil
}
