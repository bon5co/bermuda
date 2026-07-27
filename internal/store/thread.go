package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The thread: durable shared state for ephemeral agents.
//
// Agents are ephemeral; the machine they act on is not. Nothing survives a run
// except memory files, which are write-time snapshots of a world other agents
// keep changing. The thread is the opposite: an append-only record of what is
// currently true, written by whoever changed it and read by whoever comes next.
//
// It is deliberately not a chat channel. An agent given a conversation will
// fill it, at real token cost for no information, so the thread carries five
// message kinds and nothing else.
//
// Claims are the load-bearing part, and three properties decide whether they
// work at all:
//
//   - Expiry is evaluated at read time, never by a sweeper. A sweeper that is
//     not running grants nothing and blocks everything, and says nothing about
//     either.
//   - Releasing what you do not hold is an error. A silent no-op that reports
//     success is the exact bug class this repo keeps finding.
//   - Acquiring is atomic. The check and the write share one IMMEDIATE
//     transaction, so two agents racing for the browser cannot both win.

// ThreadKind is what a thread message says. There are five, and free-form
// discussion is not among them: two agents that need to negotiate want a chain
// with a human gate, not a conversation.
type ThreadKind string

const (
	// KindClaim takes an exclusive resource, optionally with a lease.
	KindClaim ThreadKind = "claim"
	// KindRelease gives a claimed resource back.
	KindRelease ThreadKind = "release"
	// KindEvent records that the world changed. It is cache invalidation, not
	// a status update: an agent whose memory says camoufox exists learns here
	// that it does not.
	KindEvent ThreadKind = "event"
	// KindAsk puts a question to a human. Resumption is not implemented yet;
	// the message is recorded so the schema does not have to change when it is.
	KindAsk ThreadKind = "ask"
	// KindNote leaves something for the next agent on this thread.
	KindNote ThreadKind = "note"
)

// ThreadKinds is every legal kind, in the order they are documented.
var ThreadKinds = []ThreadKind{KindClaim, KindRelease, KindEvent, KindAsk, KindNote}

// ParseThreadKind validates a kind supplied as text.
func ParseThreadKind(s string) (ThreadKind, error) {
	k := ThreadKind(strings.ToLower(strings.TrimSpace(s)))
	for _, valid := range ThreadKinds {
		if k == valid {
			return k, nil
		}
	}
	return "", fmt.Errorf("unknown thread kind %q: one of %s", s, joinKinds(ThreadKinds))
}

func joinKinds(kinds []ThreadKind) string {
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}

// Identity is who is speaking in the thread.
//
// A bermuda run knows its job and run ids, so it is identified by both. Anything
// else — an interactive session, a one-off script — supplies a name. The name is
// always required: an anonymous claim is a claim nobody can be asked about, and
// the whole point of a lease is knowing who to go and find.
//
// A name on its own is not enough for the interactive case, and the day this
// field was added is the day two agents both called `ada` proved it: one
// released the other's live lease, was told it had succeeded, and neither was
// told anything was wrong. PID is what tells them apart.
type Identity struct {
	Name  string
	JobID string
	RunID string
	// PID discriminates two interactive agents sharing a name. It is a string
	// rather than a number because the thing that identifies a long-lived agent
	// is not always a process id — a herdr pane id does the same job — and
	// because a bermuda run and every message written before this field existed
	// have none at all.
	PID string
}

// Valid reports whether the identity can be attributed to anyone.
func (i Identity) Valid() bool { return strings.TrimSpace(i.Name) != "" }

// Equal reports whether two identities are the same holder.
//
// Name, job and run must match. Two runs of the same job are different holders:
// one of them releasing the other's lease is exactly the theft this prevents.
//
// The pid is compared only when both sides carry one, and that asymmetry is
// deliberate. Every claim and message written before the field existed has no
// pid, and a pid-less identity that matched nothing would leave those claims
// unreleasable forever — a resource held by a row nobody can give back. So a
// missing pid means "identified by name, as it always was", and two identities
// that both know their pid must agree on it.
func (i Identity) Equal(other Identity) bool {
	if i.Name != other.Name || i.JobID != other.JobID || i.RunID != other.RunID {
		return false
	}
	if i.PID == "" || other.PID == "" {
		return true
	}
	return i.PID == other.PID
}

// String renders the identity for a human reading a status line.
func (i Identity) String() string {
	name := i.Short()
	switch {
	case i.RunID != "":
		return name + " (" + i.RunID + ")"
	case i.JobID != "" && i.JobID != i.Name:
		return name + " (" + i.JobID + ")"
	default:
		return name
	}
}

// Short is the identity as a narrow column cell: the name, and the pid when
// there is one.
//
// The pid is not decoration. A blocked agent reading HOLDS has to know *which*
// ada to go and ask, and `ada` alone does not answer that on a machine
// running three of them.
func (i Identity) Short() string {
	if i.PID == "" {
		return i.Name
	}
	return i.Name + "#" + i.PID
}

// ThreadMessage is one line in the thread.
type ThreadMessage struct {
	// Seq is the append order and the thread's only authoritative ordering.
	// Timestamps are stored to the second, so two messages can share one, and
	// claim/release folding cannot be ambiguous about which came last.
	Seq int64
	// Thread is the conversation this message belongs to. Every message belongs
	// to exactly one; unqualified writes land in GlobalThread.
	Thread string
	Kind   ThreadKind
	By     Identity
	// Resource is what a claim or release is about; empty for anything else.
	Resource string
	Body     string
	// ExpiresAt is a claim's lease end. Nil means the claim never expires,
	// which is legitimate for a long deliberate hold and dangerous for
	// everything else.
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// TTL is how long a claim message asked to hold for, or 0 when it asked
// forever.
func (m ThreadMessage) TTL() time.Duration {
	if m.ExpiresAt == nil {
		return 0
	}
	return m.ExpiresAt.Sub(m.CreatedAt)
}

// Claim is a resource currently held by someone.
type Claim struct {
	Resource string
	Holder   Identity
	// Why is the reason given when the resource was taken. It is what makes a
	// blocked agent's wait explicable instead of mysterious.
	Why string
	// Since is when this unbroken hold started, not when it was last extended:
	// re-claiming to extend a lease must not make a two-hour hold look new.
	Since     time.Time
	ExpiresAt *time.Time
	Seq       int64
}

// Expired reports whether the lease has run out as of now.
func (c Claim) Expired(now time.Time) bool {
	return c.ExpiresAt != nil && !c.ExpiresAt.After(now)
}

// Remaining is how much lease is left, or 0 when the claim never expires.
func (c Claim) Remaining(now time.Time) time.Duration {
	if c.ExpiresAt == nil {
		return 0
	}
	if d := c.ExpiresAt.Sub(now); d > 0 {
		return d
	}
	return 0
}

// ExpiryLabel renders the lease for display.
func (c Claim) ExpiryLabel(now time.Time) string {
	if c.ExpiresAt == nil {
		return "no expiry"
	}
	if c.Expired(now) {
		return "expired"
	}
	return "in " + ShortDuration(c.Remaining(now))
}

// ShortDuration renders a lease the way a person would say it, since a lease is
// read as "how long have I got" and 12m4.113s does not answer that.
//
// Minutes are rounded rather than truncated: a 20-minute lease read a moment
// after it was taken has 19m59s left, and reporting "19m" for the lease you
// just asked for reads as a bug in the thing you are trying to trust.
func ShortDuration(d time.Duration) string {
	if d < time.Minute {
		// Rounded up, because expiries are stored to the second: a lease taken
		// for one second has a shade under one left the instant it is read, and
		// "0s" for a lease that is still good reads as broken.
		return fmt.Sprintf("%ds", int((d+time.Second-1)/time.Second))
	}
	// Rounding happens once, before the branch, so 59m45s reads as 1h rather
	// than as 60m.
	d = d.Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	if mins := int(d.Minutes()) - h*60; mins != 0 {
		return fmt.Sprintf("%dh%dm", h, mins)
	}
	return fmt.Sprintf("%dh", h)
}

// HeldError is returned when a resource is already claimed by someone else. It
// carries the claim rather than a formatted string so a caller can decide to
// wait, and so the holder can be named without re-querying.
type HeldError struct {
	Claim Claim
	At    time.Time
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("%s is held by %s since %s (expires %s)%s",
		e.Claim.Resource, e.Claim.Holder, e.Claim.Since.Format("15:04:05"),
		e.Claim.ExpiryLabel(e.At), whySuffix(e.Claim.Why))
}

func whySuffix(why string) string {
	if strings.TrimSpace(why) == "" {
		return ""
	}
	return ": " + why
}

// NotHeldError is returned when releasing something you do not hold.
//
// The two cases are told apart deliberately. Nobody holding it usually means a
// lease quietly expired under a long job; somebody else holding it means the
// resource has moved on and releasing would have stolen it.
type NotHeldError struct {
	Resource string
	By       Identity
	// Current is who holds it instead, or nil when nobody does.
	Current *Claim
	At      time.Time
}

func (e *NotHeldError) Error() string {
	if e.Current == nil {
		return fmt.Sprintf("%s is not held by %s: nobody holds it (an expired lease looks like this)",
			e.Resource, e.By)
	}
	return fmt.Sprintf("%s is not held by %s: it is held by %s (expires %s)",
		e.Resource, e.By, e.Current.Holder, e.Current.ExpiryLabel(e.At))
}

// threadSchema is applied on every Open.
//
// The messages table is append-only: nothing updates or deletes a row, so the
// log is the whole history and the current state is derived from it. seq is the
// ordering, because created_at is second-resolution and a claim followed
// immediately by a release would otherwise be a coin toss. seq is also global
// rather than per-thread, so a claim in one conversation and a release in
// another still order against each other.
const threadSchema = `
CREATE TABLE IF NOT EXISTS thread_messages (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  thread      TEXT NOT NULL DEFAULT 'global',
  kind        TEXT NOT NULL,
  agent       TEXT NOT NULL,
  job_id      TEXT NOT NULL DEFAULT '',
  run_id      TEXT NOT NULL DEFAULT '',
  pid         TEXT NOT NULL DEFAULT '',
  resource    TEXT NOT NULL DEFAULT '',
  body        TEXT NOT NULL DEFAULT '',
  expires_at  INTEGER,
  created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS thread_messages_created ON thread_messages(created_at);
CREATE INDEX IF NOT EXISTS thread_messages_resource ON thread_messages(resource, seq);
`

// currentClaims derives who holds what: for every resource, the last thing that
// happened to it, kept only when that was a claim. Releasing appends rather than
// deleting, so this is a fold over an append-only log rather than a table anyone
// writes.
//
// It deliberately says nothing about threads either, and that is the point.
// There is one browser on this machine whichever conversation is talking about
// it, so the fold spans every thread and the same holds are pinned above all of
// them. A per-thread fold would tell two agents in two threads that the same
// resource was free.
//
// It deliberately says nothing about expiry. Every caller wraps it and applies
// its own instant, so the answer is "held as of when you asked" rather than as
// of whenever something last swept. That is what makes a dead agent's lease
// lapse with nothing running to reap it.
//
// This is a query fragment rather than a SQL VIEW on purpose. A view is a
// schema object, so keeping its definition current means dropping and
// recreating it — and every other process with the database open can hit the
// window where it does not exist. Bermuda routinely has three: the daemon, the
// board, and whichever agent is running a thread command. A fragment compiled
// into the binary is always the definition this build means, and creates no
// window at all.
const currentClaims = `
SELECT m.seq, m.resource, m.agent, m.job_id, m.run_id, m.pid, m.body, m.expires_at, m.created_at
FROM thread_messages m
JOIN (
  SELECT resource, MAX(seq) AS seq
  FROM thread_messages
  WHERE kind IN ('claim','release') AND resource <> ''
  GROUP BY resource
) latest ON latest.seq = m.seq
WHERE m.kind = 'claim'
`

// migrateThreads brings the thread's table and indexes up to date. Everything
// here is create-if-absent or rename-if-needed: a migration that drops rows
// would be a migration that another live process can be caught inside.
func migrateThreads(db *sql.DB) error {
	// Before the schema, not after. See renameRoomMessages.
	if err := renameRoomMessages(db); err != nil {
		return err
	}
	if _, err := db.Exec(threadSchema); err != nil {
		return fmt.Errorf("migrate thread: %w", err)
	}
	// After the messages table exists and before anything reads it: the thread
	// column and the registry are what every query below assumes.
	if err := migrateThreadList(db); err != nil {
		return err
	}
	// The pid column is the same shape of migration and defaults to empty, which
	// is exactly what Identity.Equal reads as "identified by name". Every claim
	// taken before identities carried a pid stays releasable by the name that
	// took it; a claim that nothing could release would hold a resource forever.
	if err := addMessageColumn(db, "pid", `TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	// An earlier build of this feature shipped the claims fold as a view. It is
	// no longer read from, and leaving it behind would leave a stale definition
	// in the schema of every database that already has one. The name is the one
	// that was actually created, so it is not renamed with the rest.
	if _, err := db.Exec(`DROP VIEW IF EXISTS room_current_claims`); err != nil {
		return fmt.Errorf("migrate thread: %w", err)
	}
	return nil
}

// renameRoomMessages carries a database written before the feature was called
// threads over to the new table name.
//
// It has to run before threadSchema, not after. CREATE TABLE IF NOT EXISTS
// would otherwise make an empty thread_messages first, the guard below would
// then see both tables and decline, and every real message would be stranded
// in a table nothing reads any more — data loss with nothing raised to notice
// it by.
//
// The guard is one exact shape: the old table present and the new one absent.
// Every other state — already migrated, neither present, both present because
// somebody restored half a backup — is left alone, which is what makes opening
// the store a second time, or from the daemon and the board at once, a no-op.
func renameRoomMessages(db *sql.DB) error {
	// The check and the rename share one IMMEDIATE transaction. Bermuda opens
	// this database from three processes, and two of them starting together
	// would otherwise both read an un-renamed table and the loser would fail on
	// a table that no longer exists.
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("migrate thread: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	var old, current int
	if err := conn.QueryRowContext(ctx, `
		SELECT
		  COUNT(*) FILTER (WHERE name = 'room_messages'),
		  COUNT(*) FILTER (WHERE name = 'thread_messages')
		FROM sqlite_master WHERE type = 'table'`).Scan(&old, &current); err != nil {
		return fmt.Errorf("migrate thread: %w", err)
	}
	if old == 0 || current > 0 {
		return nil
	}

	// The indexes go first. ALTER TABLE RENAME carries them across under their
	// old names, and threadSchema would then create the new ones alongside —
	// two indexes per column, both maintained on every insert, on a table that
	// is only ever appended to.
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS room_messages_created`,
		`DROP INDEX IF EXISTS room_messages_resource`,
		`ALTER TABLE room_messages RENAME TO thread_messages`,
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate thread: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("migrate thread: %w", err)
	}
	committed = true
	return nil
}

// resolveNow lets a caller state the instant it is asking about. Tests set it to
// step a lease past its expiry without sleeping; everything else leaves it zero
// and means now.
func resolveNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// ThreadPost appends a message that is not a claim or a release.
//
// Claims go through ThreadClaim and ThreadRelease, which enforce the semantics.
// Letting them in here would give an agent a way to write a claim row without
// ever checking whether the resource was free.
func (s *Store) ThreadPost(ctx context.Context, m ThreadMessage) (ThreadMessage, error) {
	switch m.Kind {
	case KindClaim, KindRelease:
		return ThreadMessage{}, fmt.Errorf("%s messages must go through ThreadClaim/ThreadRelease", m.Kind)
	case KindEvent, KindAsk, KindNote:
	default:
		return ThreadMessage{}, fmt.Errorf("unknown thread kind %q: one of %s", m.Kind, joinKinds(ThreadKinds))
	}
	if !m.By.Valid() {
		return ThreadMessage{}, errors.New("a thread message needs an author")
	}
	if strings.TrimSpace(m.Body) == "" {
		return ThreadMessage{}, fmt.Errorf("a %s needs something to say", m.Kind)
	}
	m.CreatedAt = resolveNow(m.CreatedAt)
	m.Thread = resolveThreadID(m.Thread)
	// The existence check is the INSERT's own WHERE rather than a query before
	// it, so a thread removed a moment ago cannot be written into by a caller
	// that checked first: the row either lands in a thread that exists, or no
	// row lands at all.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO thread_messages (thread, kind, agent, job_id, run_id, pid, resource, body, expires_at, created_at)
		SELECT ?,?,?,?,?,?,?,?,NULL,? WHERE EXISTS (SELECT 1 FROM threads WHERE id = ?)`,
		m.Thread, string(m.Kind), m.By.Name, m.By.JobID, m.By.RunID, m.By.PID, m.Resource,
		strings.TrimSpace(m.Body), m.CreatedAt.Unix(), m.Thread)
	if err != nil {
		return ThreadMessage{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ThreadMessage{}, unknownThread(m.Thread)
	}
	m.Seq, _ = res.LastInsertId()
	return m, nil
}

// ClaimRequest is an attempt to take an exclusive resource.
type ClaimRequest struct {
	Resource string
	By       Identity
	Why      string
	// Thread is which conversation the claim is *recorded* in. It does not
	// scope the claim: the resource is taken from everybody, in every thread.
	// Empty means global.
	Thread string
	// TTL bounds the lease. Zero means no expiry, which should be rare: a lease
	// that never lapses is how a killed agent holds the browser forever.
	TTL time.Duration
	// Now is the instant the request is made against. Zero means time.Now().
	Now time.Time
}

// ThreadClaim takes a resource, or fails naming whoever has it.
//
// Re-claiming something you already hold extends the lease instead of
// deadlocking: a job that legitimately needs longer must have a way to say so
// that is not "release and hope nobody was waiting".
//
// The claim spans every thread even though the message recording it sits in
// one. Which conversation an agent was having when it took the browser is
// commentary; that it took the browser is true for everybody.
func (s *Store) ThreadClaim(ctx context.Context, req ClaimRequest) (Claim, error) {
	resource := strings.TrimSpace(req.Resource)
	if resource == "" {
		return Claim{}, errors.New("a claim needs a resource")
	}
	if !req.By.Valid() {
		return Claim{}, errors.New("a claim needs a holder")
	}
	if req.TTL < 0 {
		return Claim{}, errors.New("a claim's ttl cannot be negative")
	}
	now := resolveNow(req.Now)

	// One IMMEDIATE transaction around the check and the write. A deferred
	// transaction takes its write lock only at the INSERT, by which time both
	// racing agents have already read an unheld resource and both believe they
	// won — which is the two-browsers-at-once failure this exists to stop.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Claim{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Claim{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	thread := resolveThreadID(req.Thread)
	var exists int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM threads WHERE id = ?`, thread).Scan(&exists); err != nil {
		return Claim{}, err
	}
	if exists == 0 {
		// Checked before the resource, so a typo in --thread is reported as the
		// typo it is rather than as a lease that was taken and then vanished
		// from every log anyone reads.
		return Claim{}, unknownThread(thread)
	}

	current, err := claimOf(ctx, conn, resource, now)
	if err != nil {
		return Claim{}, err
	}
	if current != nil && !current.Holder.Equal(req.By) {
		return Claim{}, &HeldError{Claim: *current, At: now}
	}

	why := strings.TrimSpace(req.Why)
	if current != nil && why == "" {
		// An extension with no new reason keeps the original one, so a renewed
		// lease does not lose the explanation of why it exists.
		why = current.Why
	}
	var expires any
	var expiresAt *time.Time
	if req.TTL > 0 {
		t := now.Add(req.TTL)
		expires, expiresAt = t.Unix(), &t
	}
	res, err := conn.ExecContext(ctx, `
		INSERT INTO thread_messages (thread, kind, agent, job_id, run_id, pid, resource, body, expires_at, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		thread, string(KindClaim), req.By.Name, req.By.JobID, req.By.RunID, req.By.PID,
		resource, why, expires, now.Unix())
	if err != nil {
		return Claim{}, err
	}
	seq, _ := res.LastInsertId()
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Claim{}, err
	}
	committed = true

	since := now
	if current != nil {
		since = current.Since
	}
	return Claim{
		Resource: resource, Holder: req.By, Why: why,
		Since: since, ExpiresAt: expiresAt, Seq: seq,
	}, nil
}

// ThreadClaimWait takes a resource, waiting up to wait for it to come free.
//
// It polls rather than waiting on a signal: the holder is another process, the
// lease may lapse with nobody running to announce it, and a resource held for
// minutes is not measurably worse served by a quarter-second poll. A poll also
// cannot miss a wake-up, which a signal between processes can.
func (s *Store) ThreadClaimWait(ctx context.Context, req ClaimRequest, wait time.Duration) (Claim, error) {
	c, err := s.ThreadClaim(ctx, req)
	var held *HeldError
	if err == nil || wait <= 0 || !errors.As(err, &held) {
		return c, err
	}
	// Waiting is against real time: the point is to outlast a lease held by
	// somebody else, which no injected clock can advance.
	deadline := time.Now().Add(wait)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return Claim{}, err
		}
		delay := threadPollInterval
		if remaining < delay {
			delay = remaining
		}
		select {
		case <-ctx.Done():
			return Claim{}, ctx.Err()
		case <-time.After(delay):
		}
		attempt := req
		attempt.Now = time.Time{} // each attempt asks about the present
		c, err = s.ThreadClaim(ctx, attempt)
		if err == nil || !errors.As(err, &held) {
			return c, err
		}
	}
}

// threadPollInterval is how often a waiting claim retries.
const threadPollInterval = 250 * time.Millisecond

// ThreadRelease gives a resource back, and fails if the caller does not hold it.
//
// It is deliberately not idempotent. A release that reports success for a lease
// somebody else now holds tells an agent it tidied up when it did not, and
// releasing a lease that already lapsed is worth knowing about: it means the
// work outlived its own claim and may have overlapped somebody else's.
//
// It takes no thread: the release is written into whichever thread the claim
// was made in, so the pair always reads as one exchange. An agent that claimed
// the browser from one conversation and released it from another would
// otherwise leave a claim that is never given back in the first thread and a
// release for nothing in the second.
func (s *Store) ThreadRelease(ctx context.Context, resource string, by Identity, now time.Time) (Claim, error) {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return Claim{}, errors.New("a release needs a resource")
	}
	if !by.Valid() {
		return Claim{}, errors.New("a release needs a holder")
	}
	now = resolveNow(now)

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Claim{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Claim{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	current, err := claimOf(ctx, conn, resource, now)
	if err != nil {
		return Claim{}, err
	}
	if current == nil || !current.Holder.Equal(by) {
		return Claim{}, &NotHeldError{Resource: resource, By: by, Current: current, At: now}
	}
	var thread string
	if err := conn.QueryRowContext(ctx,
		`SELECT thread FROM thread_messages WHERE seq = ?`, current.Seq).Scan(&thread); err != nil {
		return Claim{}, err
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO thread_messages (thread, kind, agent, job_id, run_id, pid, resource, body, expires_at, created_at)
		VALUES (?,?,?,?,?,?,?,'',NULL,?)`,
		thread, string(KindRelease), by.Name, by.JobID, by.RunID, by.PID,
		resource, now.Unix()); err != nil {
		return Claim{}, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Claim{}, err
	}
	committed = true
	return *current, nil
}

// ThreadClaims lists everything held as of now, oldest hold first.
func (s *Store) ThreadClaims(ctx context.Context, now time.Time) ([]Claim, error) {
	now = resolveNow(now)
	rows, err := s.db.QueryContext(ctx, `
		SELECT * FROM (`+currentClaims+`)
		WHERE expires_at IS NULL OR expires_at > ?
		ORDER BY created_at, seq`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// An empty result is "nothing is claimed", which JSON should render as an
	// empty list rather than as null.
	out := []Claim{}
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.fillSince(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ThreadClaimOf returns the live claim on one resource, or nil when it is free.
func (s *Store) ThreadClaimOf(ctx context.Context, resource string, now time.Time) (*Claim, error) {
	now = resolveNow(now)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	c, err := claimOf(ctx, conn, strings.TrimSpace(resource), now)
	if err != nil || c == nil {
		return nil, err
	}
	if err := s.fillSince(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// claimOf reads the live claim on a resource inside whatever transaction the
// caller has open. Expiry is applied here, at the moment of reading, so a lease
// whose owner died lapses without anything having to run.
//
// Since is left at the latest claim's timestamp; callers that display it use
// fillSince to walk back to the start of the unbroken hold.
func claimOf(ctx context.Context, conn *sql.Conn, resource string, now time.Time) (*Claim, error) {
	row := conn.QueryRowContext(ctx, `
		SELECT * FROM (`+currentClaims+`)
		WHERE resource = ? AND (expires_at IS NULL OR expires_at > ?)`,
		resource, now.Unix())
	c, err := scanClaim(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// fillSince walks back to the first claim of this unbroken hold.
//
// Without it, extending a lease would reset "held since" and a two-hour hold
// would read as brand new every time it was renewed — which is precisely when
// somebody is looking at the board wondering how long this has been going on.
//
// Three things break a hold, and all three have to be honoured or the answer
// overstates it: a release, a different holder, and a lapse. The last one is the
// subtle one — the same agent taking a resource back an hour after its lease ran
// out has held it since it took it back, not since the claim that died.
func (s *Store) fillSince(ctx context.Context, c *Claim) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind, agent, job_id, run_id, pid, expires_at, created_at
		FROM thread_messages
		WHERE resource = ? AND kind IN ('claim','release') AND seq < ?
		ORDER BY seq DESC`, c.Resource, c.Seq)
	if err != nil {
		return err
	}
	defer rows.Close()

	// The walk starts at the current claim and steps backwards while each
	// earlier claim covers the one after it. SQLite streams in seq order, so it
	// reads only as far as the hold actually reaches.
	start := c.Since
	for rows.Next() {
		var kind string
		var holder Identity
		var expires sql.NullInt64
		var created int64
		if err := rows.Scan(&kind, &holder.Name, &holder.JobID, &holder.RunID, &holder.PID,
			&expires, &created); err != nil {
			return err
		}
		if ThreadKind(kind) == KindRelease || !holder.Equal(c.Holder) {
			break
		}
		// A lease that had already run out when the next claim was taken did not
		// cover it: that is a gap, not one hold.
		if expires.Valid && time.Unix(expires.Int64, 0).Before(start) {
			break
		}
		start = time.Unix(created, 0)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	c.Since = start
	return nil
}

func scanClaim(row interface{ Scan(...any) error }) (Claim, error) {
	var c Claim
	var expires sql.NullInt64
	var created int64
	if err := row.Scan(&c.Seq, &c.Resource, &c.Holder.Name, &c.Holder.JobID,
		&c.Holder.RunID, &c.Holder.PID, &c.Why, &expires, &created); err != nil {
		return c, err
	}
	c.Since = time.Unix(created, 0)
	if expires.Valid {
		t := time.Unix(expires.Int64, 0)
		c.ExpiresAt = &t
	}
	return c, nil
}

// ThreadFilter narrows the log.
type ThreadFilter struct {
	// Thread keeps only one conversation. Empty means every thread, the same
	// way an empty Kinds means every kind — the callers that mean one thread
	// resolve it before they ask, so "unset" can honestly mean "unfiltered".
	Thread string
	// Since drops anything older. Zero means no lower bound.
	Since time.Time
	// Kinds keeps only these kinds. Empty means all of them.
	Kinds []ThreadKind
	// Limit caps how many messages come back. It selects the newest N, which
	// are then returned oldest-first. Zero, or anything above ThreadMaxLimit,
	// reads as the ceiling: there is no way to ask this for the whole table.
	Limit int
}

// How far back a read of the log is allowed to reach.
//
// The thread is a record of what is *currently* true, and an agent's context is
// the scarce thing being spent on it. Two hundred messages of mostly-settled
// history is a large fraction of that budget for a record whose interesting part
// is nearly always the last hour: claims are folded separately by thread status,
// so the log does not have to carry the locking story to be useful.
//
// So there are two pairs. The default is what a caller gets for asking for
// nothing, and it is deliberately small. The ceiling is what a caller gets no
// matter what it asks for, and exists because "give me everything" is a request
// the reader cannot afford to have granted.
const (
	// ThreadDefaultLimit is how many messages an unqualified read returns.
	ThreadDefaultLimit = 50
	// ThreadDefaultAge is how far back an unqualified read reaches. A note from
	// last week is usually stale; whichever of the two bounds bites first wins.
	ThreadDefaultAge = 24 * time.Hour
	// ThreadMaxLimit is the most any read can return, however large a --limit it
	// asks for. This was the old default, which is the honest place for it: it
	// was always the number past which nobody could usefully read.
	ThreadMaxLimit = 200
	// ThreadMaxAge is the furthest back any read can reach.
	ThreadMaxAge = 7 * 24 * time.Hour
)

// ThreadWindow is a resolved read window: the bounds a log read will actually
// be given, and whether the caller asked for more than it can have.
//
// Asking for more is not an error and must not be one. A script that passes a
// generous --limit today is not doing anything wrong, and failing it would break
// it for no gain — it simply cannot have more than the ceiling, and is told so.
type ThreadWindow struct {
	// Limit is how many messages the read may return.
	Limit int
	// Age is how far back it may reach.
	Age time.Duration
	// Since is Age applied to the instant the window was resolved.
	Since time.Time
	// LimitClamped is set when the caller asked for more than ThreadMaxLimit.
	LimitClamped bool
	// AgeClamped is set when the caller asked to reach past ThreadMaxAge.
	AgeClamped bool
}

// ThreadReadWindow resolves what a read of the log is allowed to see.
//
// A limit or age of zero or less means "unspecified", and gets the default.
// Anything past the ceiling is brought back to it rather than refused.
func ThreadReadWindow(limit int, age time.Duration, now time.Time) ThreadWindow {
	w := ThreadWindow{Limit: limit, Age: age}
	if w.Limit <= 0 {
		w.Limit = ThreadDefaultLimit
	}
	if w.Age <= 0 {
		w.Age = ThreadDefaultAge
	}
	if w.Limit > ThreadMaxLimit {
		w.Limit, w.LimitClamped = ThreadMaxLimit, true
	}
	if w.Age > ThreadMaxAge {
		w.Age, w.AgeClamped = ThreadMaxAge, true
	}
	w.Since = resolveNow(now).Add(-w.Age)
	return w
}

// Apply narrows a filter to the window.
func (w ThreadWindow) Apply(f ThreadFilter) ThreadFilter {
	f.Limit = w.Limit
	f.Since = w.Since
	return f
}

// where builds the shared predicate, so that counting what a filter matches and
// reading it cannot drift apart. A count that disagreed with the log it
// describes would be worse than no count at all: it is printed precisely to tell
// a reader how much it is not seeing.
func (f ThreadFilter) where() (string, []any) {
	var where []string
	var args []any
	if thread := strings.TrimSpace(f.Thread); thread != "" {
		where = append(where, "thread = ?")
		args = append(args, thread)
	}
	if !f.Since.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since.Unix())
	}
	if len(f.Kinds) > 0 {
		marks := make([]string, len(f.Kinds))
		for i, k := range f.Kinds {
			marks[i] = "?"
			args = append(args, string(k))
		}
		where = append(where, "kind IN ("+strings.Join(marks, ",")+")")
	}
	if len(where) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// ThreadCount is how many messages a filter matches, ignoring its Limit.
//
// It is what makes a truncated log say so. A log that was cut at fifty and looks
// complete is this feature's failure mode: an agent reads it, concludes it has
// the whole picture, and acts on a thread whose load-bearing message was number
// fifty-one.
func (s *Store) ThreadCount(ctx context.Context, f ThreadFilter) (int, error) {
	clause, args := f.where()
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM thread_messages`+clause, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ThreadLog returns messages oldest-first, limited to the most recent Limit.
//
// The two halves of that sentence are both deliberate. A log reads downwards, so
// the newest line belongs at the bottom where the eye finishes; but "the last N"
// is what anyone actually wants of an append-only table that grows forever, and
// taking the first N would pin every reader to the day the thread was created.
//
// The ceiling is enforced here rather than only at the flag, so no caller can
// ask the store for an unbounded read by going round the CLI.
func (s *Store) ThreadLog(ctx context.Context, f ThreadFilter) ([]ThreadMessage, error) {
	limit := f.Limit
	if limit <= 0 || limit > ThreadMaxLimit {
		limit = ThreadMaxLimit
	}
	q := `SELECT seq, thread, kind, agent, job_id, run_id, pid, resource, body, expires_at, created_at
	      FROM thread_messages`
	clause, args := f.where()
	q += clause
	q += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ThreadMessage
	for rows.Next() {
		var m ThreadMessage
		var kind string
		var expires sql.NullInt64
		var created int64
		if err := rows.Scan(&m.Seq, &m.Thread, &kind, &m.By.Name, &m.By.JobID, &m.By.RunID,
			&m.By.PID, &m.Resource, &m.Body, &expires, &created); err != nil {
			return nil, err
		}
		m.Kind = ThreadKind(kind)
		m.CreatedAt = time.Unix(created, 0)
		if expires.Valid {
			t := time.Unix(expires.Int64, 0)
			m.ExpiresAt = &t
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Read newest-first so the limit takes the newest; hand back oldest-first so
	// the caller can print it as a thread.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
