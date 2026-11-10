package store

import (
	"context"
	"database/sql"
	"time"
)

// JobSession is the last conversation observed for a keep-context job.
type JobSession struct {
	JobID      string
	Harness    string
	Kind       string
	Value      string
	RunID      string
	CapturedAt time.Time
}

// PutJobSession replaces the last observed session, including a user /new.
func (s *Store) PutJobSession(ctx context.Context, session JobSession) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_sessions
 (job_id, harness, kind, value, run_id, captured_at) VALUES (?,?,?,?,?,?)
 ON CONFLICT(job_id) DO UPDATE SET harness=excluded.harness, kind=excluded.kind,
 value=excluded.value, run_id=excluded.run_id, captured_at=excluded.captured_at`,
		session.JobID, session.Harness, session.Kind, session.Value, session.RunID, session.CapturedAt.Unix())
	return err
}

// JobSession returns the last known session, or ErrNotFound.
func (s *Store) JobSession(ctx context.Context, jobID string) (*JobSession, error) {
	var session JobSession
	var captured int64
	err := s.db.QueryRowContext(ctx, `SELECT job_id,harness,kind,value,run_id,captured_at
 FROM job_sessions WHERE job_id=?`, jobID).Scan(&session.JobID, &session.Harness,
		&session.Kind, &session.Value, &session.RunID, &captured)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	session.CapturedAt = time.Unix(captured, 0)
	return &session, nil
}

// DeleteJobSession forgets an obsolete conversation after choosing fresh.
// Parked losses keep their session so a human can recover it.
func (s *Store) DeleteJobSession(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM job_sessions WHERE job_id=?", jobID)
	return err
}

// HadPriorConversation reports whether an earlier context-enabled run reached
// a harness. The current running row and launches that never prompted an agent
// cannot establish that a conversation was lost.
func (s *Store) HadPriorConversation(ctx context.Context, jobID, currentRunID string) (bool, error) {
	var had int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs
 WHERE job_id=? AND id<>? AND context<>'' AND tab_id<>'' AND status<>'')`,
		jobID, currentRunID).Scan(&had)
	return had != 0, err
}
