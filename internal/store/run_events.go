package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bon5co/bermuda/v3/internal/version"
)

const eventTimeLayout = "2006-01-02T15:04:05.000000000Z"

// RunEvent is a frozen run settlement waiting for the daemon's hook worker.
type RunEvent struct {
	ID              int64
	RunID           string
	Kind            string
	Settlement      int
	PreviousOutcome string
	Payload         json.RawMessage
	CreatedAt       string
	Attempts        int
	NextAt          string
	DeliveredAt     string
	LastError       string
	Generation      int64
}

func isSettled(outcome string) bool {
	return outcome == "done" || outcome == "failed" || outcome == "parked"
}

func enqueueRunSettlement(ctx context.Context, conn *sql.Conn, r Run, result json.RawMessage) error {
	var lastSettlement int
	var lastPayload string
	err := conn.QueryRowContext(ctx, `SELECT settlement, payload FROM run_events WHERE run_id=? ORDER BY settlement DESC LIMIT 1`, r.ID).Scan(&lastSettlement, &lastPayload)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var previousOutcome string
	if err == nil {
		var p struct {
			Run struct {
				Outcome string `json:"outcome"`
			} `json:"run"`
		}
		if err := json.Unmarshal([]byte(lastPayload), &p); err != nil {
			return fmt.Errorf("previous run event: %w", err)
		}
		previousOutcome = p.Run.Outcome
	}
	now := time.Now().UTC().Format(eventTimeLayout)
	res, err := conn.ExecContext(ctx, `INSERT INTO run_events (run_id,kind,settlement,prev_outcome,payload,created_at,next_at) VALUES (?,?,?,?,?,?,?)`, r.ID, "run.settled", lastSettlement+1, previousOutcome, "", now, now)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(settlementPayload(r, id, lastSettlement+1, previousOutcome, result))
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `UPDATE run_events SET payload=? WHERE id=?`, string(payload), id)
	return err
}

func settlementPayload(r Run, id int64, settlement int, previous string, result json.RawMessage) any {
	type usage struct {
		Input         int64 `json:"input"`
		Output        int64 `json:"output"`
		CacheRead     int64 `json:"cache_read"`
		CacheCreation int64 `json:"cache_creation"`
	}
	type run struct {
		ID         string  `json:"id"`
		JobID      string  `json:"job_id"`
		Flow       string  `json:"flow"`
		Trigger    string  `json:"trigger"`
		Outcome    string  `json:"outcome"`
		ParkReason string  `json:"park_reason"`
		Note       string  `json:"note"`
		Ref        string  `json:"ref"`
		RunDir     string  `json:"run_dir"`
		Space      string  `json:"space"`
		Thread     string  `json:"thread"`
		StartedAt  string  `json:"started_at"`
		EndedAt    *string `json:"ended_at"`
		Model      string  `json:"model"`
		Tokens     usage   `json:"tokens"`
	}
	var ended *string
	if r.EndedAt != nil {
		value := r.EndedAt.UTC().Format(time.RFC3339)
		ended = &value
	}
	return struct {
		Version         int             `json:"version"`
		Event           string          `json:"event"`
		EventID         int64           `json:"event_id"`
		Settlement      int             `json:"settlement"`
		PreviousOutcome string          `json:"previous_outcome"`
		BermudaVersion  string          `json:"bermuda_version"`
		Run             run             `json:"run"`
		Result          json.RawMessage `json:"result"`
	}{
		Version: 1, Event: "run.settled", EventID: id, Settlement: settlement,
		PreviousOutcome: previous, BermudaVersion: version.String(),
		Run: run{ID: r.ID, JobID: r.JobID, Flow: r.Flow, Trigger: r.Trigger,
			Outcome: r.Outcome, ParkReason: r.ParkReason, Note: r.Note, Ref: r.Ref,
			RunDir: r.RunDir, Space: r.Space, Thread: r.Thread,
			StartedAt: r.StartedAt.UTC().Format(time.RFC3339), EndedAt: ended,
			Model: r.Model, Tokens: usage{r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheCreationTokens}},
		Result: result,
	}
}

const eventColumns = `id,run_id,kind,settlement,prev_outcome,payload,created_at,attempts,next_at,delivered_at,last_error,generation`

func scanRunEvent(rows interface{ Scan(...any) error }) (RunEvent, error) {
	var e RunEvent
	var payload string
	err := rows.Scan(&e.ID, &e.RunID, &e.Kind, &e.Settlement, &e.PreviousOutcome, &payload, &e.CreatedAt, &e.Attempts, &e.NextAt, &e.DeliveredAt, &e.LastError, &e.Generation)
	e.Payload = json.RawMessage(payload)
	return e, err
}

// RunEvents lists the frozen settlements for one run in delivery order.
func (s *Store) RunEvents(ctx context.Context, runID string) ([]RunEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventColumns+` FROM run_events WHERE run_id=? ORDER BY id`, runID)
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
