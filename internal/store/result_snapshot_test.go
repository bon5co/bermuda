package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResultSnapshotAcceptsOnlyRegularValidJSONWithinTheSizeLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want []byte
	}{
		{"valid", []byte(`{"note":"ok"}`), []byte(`{"note":"ok"}`)},
		{"invalid", []byte(`{"note":`), nil},
		{"at-limit", append(append([]byte{'"'}, bytes.Repeat([]byte{'a'}, maxResultSnapshotBytes-2)...), '"'), nil},
		{"oversized", append(append([]byte{'"'}, bytes.Repeat([]byte{'a'}, maxResultSnapshotBytes-1)...), '"'), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "result.json"), tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if tc.name == "at-limit" {
				want = tc.data
			}
			if got := resultAtSettlement(context.Background(), dir); !bytes.Equal(got, want) {
				t.Fatalf("snapshot length=%d, want %d", len(got), len(want))
			}
		})
	}
	dir := t.TempDir()
	if got := resultAtSettlement(context.Background(), dir); got != nil {
		t.Fatal("missing file returned a snapshot")
	}
	if err := os.Mkdir(filepath.Join(dir, "result.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := resultAtSettlement(context.Background(), dir); got != nil {
		t.Fatal("directory returned a snapshot")
	}
}

// Occupied slots model readers stuck in filesystem calls. No test goroutines
// remain blocked, and cleanup releases the slots even if an assertion fails.
func occupyResultReaders(t *testing.T) {
	t.Helper()
	for range cap(resultSnapshotSlots) {
		select {
		case resultSnapshotSlots <- struct{}{}:
		case <-time.After(time.Second):
			t.Fatal("previous snapshot reader did not finish")
		}
		t.Cleanup(func() { <-resultSnapshotSlots })
	}
}

func TestResultSnapshotContentionIsBoundedWithoutACallerDeadline(t *testing.T) {
	occupyResultReaders(t)
	done := make(chan json.RawMessage, 1)
	go func() { done <- resultAtSettlement(context.Background(), t.TempDir()) }()
	select {
	case result := <-done:
		if result != nil {
			t.Fatal("exhausted readers returned a snapshot")
		}
	case <-time.After(2 * resultSnapshotTimeout):
		t.Fatal("snapshot wait exceeded its internal deadline")
	}
}

func TestWaitingForASnapshotAllowsOtherWritesAndHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	occupyResultReaders(t)
	now := time.Now()
	r := Run{ID: "waiting-snapshot", Outcome: "done", RunDir: dir, StartedAt: now, EndedAt: &now}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settled := make(chan error, 1)
	go func() { settled <- s.PutRun(ctx, r) }()
	// Give the settlement a chance to enter its snapshot wait. With all slots
	// occupied, it cannot advance until cancellation or the internal timeout.
	time.Sleep(50 * time.Millisecond)
	written := make(chan error, 1)
	go func() {
		written <- s.PutJob(context.Background(), Job{ID: "independent", Prompt: "write while snapshot waits"})
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("snapshot wait held a database connection or write lock")
		cancel()
		if err := <-written; err != nil {
			t.Error(err)
		}
	}
	cancel()
	select {
	case err := <-settled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("settlement error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot wait ignored cancellation")
	}
	events, err := s.RunEvents(context.Background(), r.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("canceled settlement persisted events=%v err=%v", events, err)
	}
}
