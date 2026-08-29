package store

import (
	"context"
	"time"
)

// JobUsage is one job's token spend over a window.
type JobUsage struct {
	JobID               string
	Runs                int
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	Model               string
	LastRun             time.Time
}

// UsageByJob totals token usage per job for runs started at or after since,
// most recently active job first.
//
// It groups rather than listing runs: the question this answers is which job is
// expensive, and a per-run list buries that under the noise of every run.
func (s *Store) UsageByJob(ctx context.Context, since time.Time) ([]JobUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT job_id, COUNT(1),
		       SUM(input_tokens), SUM(output_tokens),
		       SUM(cache_read_tokens), SUM(cache_creation_tokens),
		       MAX(started_at)
		FROM runs WHERE started_at >= ?
		GROUP BY job_id ORDER BY MAX(started_at) DESC`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobUsage
	for rows.Next() {
		var u JobUsage
		var last int64
		if err := rows.Scan(&u.JobID, &u.Runs, &u.InputTokens, &u.OutputTokens,
			&u.CacheReadTokens, &u.CacheCreationTokens, &last); err != nil {
			return nil, err
		}
		u.LastRun = time.Unix(last, 0)
		// The model a job last ran on, since cost per token depends on it. A job
		// whose model changed mid-window shows the newest, which is what the
		// next run will cost.
		_ = s.db.QueryRowContext(ctx,
			`SELECT model FROM runs WHERE job_id=? AND started_at >= ? AND model != ''
			 ORDER BY started_at DESC LIMIT 1`, u.JobID, since.Unix()).Scan(&u.Model)
		out = append(out, u)
	}
	return out, rows.Err()
}
