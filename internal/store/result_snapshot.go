package store

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	maxResultSnapshotBytes = 1 << 20
	resultSnapshotTimeout  = time.Second
)

// A stalled filesystem must not accumulate unbounded readers. Workers retain
// their slot until all I/O (including Close) finishes, even if a caller expires.
var resultSnapshotSlots = make(chan struct{}, 4)

// resultAtSettlement is best effort: invalid, nonregular, oversized, or slow
// files become JSON null. Call this before taking any database connection.
func resultAtSettlement(ctx context.Context, runDir string) json.RawMessage {
	if runDir == "" || ctx.Err() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, resultSnapshotTimeout)
	defer cancel()
	select {
	case resultSnapshotSlots <- struct{}{}:
	case <-ctx.Done():
		return nil
	}
	result := make(chan json.RawMessage, 1)
	go func() {
		defer func() { <-resultSnapshotSlots }()
		result <- readResultSnapshot(ctx, filepath.Join(runDir, "result.json"))
	}()
	select {
	case b := <-result:
		if ctx.Err() == nil {
			return b
		}
	case <-ctx.Done():
	}
	return nil
}

func readResultSnapshot(ctx context.Context, path string) json.RawMessage {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxResultSnapshotBytes || ctx.Err() != nil {
		return nil
	}
	f, err := openResultSnapshot(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	// Check the opened descriptor too: the path may have changed since Stat.
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxResultSnapshotBytes || ctx.Err() != nil {
		return nil
	}
	// Limit the read even if the file grows after Stat.
	b, err := io.ReadAll(io.LimitReader(f, maxResultSnapshotBytes+1))
	if err != nil || len(b) > maxResultSnapshotBytes || !json.Valid(b) {
		return nil
	}
	return json.RawMessage(b)
}
