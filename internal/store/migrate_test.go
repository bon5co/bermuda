package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

// Two processes opening the same old database at once must both succeed.
//
// The daemon, the board and every thread command open this store, and Herdr
// starts several of them together. The jobs and runs migration used to check
// for a column and then ALTER it on a bare connection: both openers saw the
// column missing, both altered, and the loser died on "duplicate column name" —
// a store refusing to open over a schema change that had already worked.
func TestOpenMigratesConcurrently(t *testing.T) {
	dir := t.TempDir()
	seedOldSchema(t, filepath.Join(dir, "bermuda.db"))

	const openers = 8
	errs := make(chan error, openers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // together, or the race never happens
			s, err := Open(dir)
			if err != nil {
				errs <- err
				return
			}
			errs <- s.Close()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent open: %v", err)
		}
	}

	// And the columns really are there afterwards.
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.PutJob(context.Background(), Job{
		ID: "j", Name: "j", Prompt: "p", Schedule: ScheduleManual, Tags: []string{"a"},
	}); err != nil {
		t.Fatalf("put job into the migrated schema: %v", err)
	}
}

// seedOldSchema writes a database with the original tables and none of the
// columns added since, which is what an upgrade actually finds on disk.
func seedOldSchema(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
}
