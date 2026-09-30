package main

import (
	"context"
	"time"

	"github.com/bon5co/bermuda/v3/internal/store"
)

type settledHookInvoker func(context.Context, string, store.RunEvent, time.Duration) (bool, error)

// deliverRunEvents drains due events without holding a database transaction
// during the executable. One daemon worker calls it, while all other Bermuda
// processes only enqueue.
func deliverRunEvents(ctx context.Context, s *store.Store, dir string, invoke settledHookInvoker) error {
	for ctx.Err() == nil {
		events, err := s.DueRunEvents(ctx, time.Now(), 20)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		for _, e := range events {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			skipped, hookErr := invoke(ctx, dir, e, hookTimeout)
			// Shutdown leaves the event pending for the next daemon. A hook
			// timeout is different: the daemon is still alive and retries it.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := s.RecordRunEventAttempt(ctx, e, time.Now(), hookErr, skipped); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
