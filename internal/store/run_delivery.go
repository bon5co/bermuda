package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MaxHookAttempts bounds the work one event can consume before an operator
// explicitly asks for redelivery.
const MaxHookAttempts = 5

var retryWaits = [...]time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour}

// Status describes the delivery state without changing the run outcome.
func (e RunEvent) Status() string {
	if e.DeliveredAt != "" {
		if e.LastError == "no hook" {
			return "skipped"
		}
		return "delivered"
	}
	if e.Attempts >= MaxHookAttempts {
		return "dead"
	}
	if e.Attempts > 0 {
		return "retrying"
	}
	return "pending"
}

// RunEvent fetches one settlement by its durable event ID.
func (s *Store) RunEvent(ctx context.Context, id int64) (RunEvent, error) {
	e, err := scanRunEvent(s.db.QueryRowContext(ctx, `SELECT `+eventColumns+` FROM run_events WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// DueRunEvents returns events ready for delivery. Any undelivered event,
// including a dead one, holds only its own run's later settlements; unrelated
// runs keep moving.
func (s *Store) DueRunEvents(ctx context.Context, now time.Time, limit int) ([]RunEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventColumns+` FROM run_events e
		WHERE e.delivered_at='' AND e.attempts<? AND e.next_at<=?
		AND NOT EXISTS (SELECT 1 FROM run_events prior WHERE prior.run_id=e.run_id AND prior.id<e.id AND prior.delivered_at='')
		ORDER BY e.id LIMIT ?`, MaxHookAttempts, now.UTC().Format(eventTimeLayout), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunEvent
	for rows.Next() {
		e, err := scanRunEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RecordRunEventAttempt acknowledges one observed hook call. Generation and
// attempt count fence an acknowledgement racing with explicit redelivery.
func (s *Store) RecordRunEventAttempt(ctx context.Context, e RunEvent, now time.Time, hookErr error, skipped bool) error {
	stamp := now.UTC().Format(eventTimeLayout)
	var attempts = e.Attempts
	var delivered, lastError string
	next := e.NextAt
	if skipped {
		delivered, lastError = stamp, "no hook"
	} else {
		attempts++
		if hookErr == nil {
			delivered = stamp
		} else {
			lastError = hookErr.Error()
			if len(lastError) > 1024 {
				lastError = lastError[:1024]
			}
			if attempts < MaxHookAttempts {
				next = now.Add(retryWaits[attempts-1]).UTC().Format(eventTimeLayout)
			}
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE run_events SET attempts=?,next_at=?,delivered_at=?,last_error=? WHERE id=? AND generation=? AND attempts=? AND delivered_at=''`, attempts, next, delivered, lastError, e.ID, e.Generation, e.Attempts)
	return err
}

// RedeliverRunEvent requeues a known event without changing its identity or
// frozen payload. The generation invalidates a hook still running elsewhere.
func (s *Store) RedeliverRunEvent(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE run_events SET attempts=0,next_at=?,delivered_at='',last_error='',generation=generation+1 WHERE id=?`, time.Now().UTC().Format(eventTimeLayout), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RedeliverDeadRunEvents requeues every exhausted event, leaving pending,
// skipped and delivered rows untouched.
func (s *Store) RedeliverDeadRunEvents(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE run_events SET attempts=0,next_at=?,delivered_at='',last_error='',generation=generation+1 WHERE attempts>=? AND delivered_at=''`, time.Now().UTC().Format(eventTimeLayout), MaxHookAttempts)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// HookEvents lists outstanding and skipped events for operator inspection.
func (s *Store) HookEvents(ctx context.Context) ([]RunEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventColumns+` FROM run_events WHERE delivered_at='' OR last_error='no hook' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunEvent
	for rows.Next() {
		e, err := scanRunEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeadRunEventCount is the number of events requiring operator attention.
func (s *Store) DeadRunEventCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE delivered_at='' AND attempts>=?`, MaxHookAttempts).Scan(&n)
	return n, err
}
