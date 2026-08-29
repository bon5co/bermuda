package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/statefs"
)

// The store holds every thread body and forum post there is, and SECURITY.md
// promises none of it is readable by a second login on the same machine. That
// promise is one umask away from quietly false — SQLite creates the database
// file itself — so it is asserted rather than commented.
func TestOpenLeavesStoreOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if fi, err := os.Stat(dir); err != nil {
		t.Fatalf("stat dir: %v", err)
	} else if got := fi.Mode().Perm(); got != statefs.Dir {
		t.Errorf("state dir mode = %04o, want %04o", got, statefs.Dir)
	}

	if fi, err := os.Stat(filepath.Join(dir, "bermuda.db")); err != nil {
		t.Fatalf("stat db: %v", err)
	} else if got := fi.Mode().Perm(); got != statefs.File {
		t.Errorf("bermuda.db mode = %04o, want %04o", got, statefs.File)
	}
}

// A store made by a bermuda old enough to have written it world-readable is
// tightened by the next open, not left as it was found.
func TestOpenTightensAnOlderStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s.Close()

	db := filepath.Join(dir, "bermuda.db")
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	if err := os.Chmod(db, 0o644); err != nil {
		t.Fatalf("chmod db: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	if fi, _ := os.Stat(dir); fi.Mode().Perm() != statefs.Dir {
		t.Errorf("state dir left at %04o", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(db); fi.Mode().Perm() != statefs.File {
		t.Errorf("bermuda.db left at %04o", fi.Mode().Perm())
	}
}
