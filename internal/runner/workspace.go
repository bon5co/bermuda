package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bon5co/bermuda/v3/internal/herdrcli"
	"github.com/bon5co/bermuda/v3/internal/lockfile"
	"github.com/bon5co/bermuda/v3/internal/statefs"
)

// Owning a space rather than moving into one.
//
// Bermuda used to find its workspace by label: the first space called Bermuda
// was bermuda's, and one was created when there was none. That is fine until
// somebody already has a space by that name — and somebody working on bermuda
// will — because then every run opens a tab in the space they are working in,
// which is the one thing the design promised never to do. Nothing announces it,
// either: the tabs simply appear among theirs.
//
// A label cannot answer "is this mine", so it is the wrong question. Herdr has
// no readable owner for a workspace — metadata tokens are display-only and
// `workspace get` does not return them — so bermuda records the id of the space
// it created and asks herdr about that id afterwards. What it gets back is one
// of three answers: the space is there and is still called Bermuda, so it is
// the one; the id is gone, so a new one is created and recorded; or the id
// exists under some other name, which means herdr reused it after a restart and
// it belongs to somebody else now, so it is left alone.

// workspaceRecord is what bermuda remembers about the space it owns. The label
// is stored alongside the id so a reused id can be told apart from its own.
type workspaceRecord struct {
	ID    string `json:"workspace_id"`
	Label string `json:"label"`
}

// workspaceFile is where that record lives, beside the store.
func workspaceFile(stateDir string) string {
	return filepath.Join(stateDir, "workspace.json")
}

// workspaceLock serialises creation. The daemon and the board can both want the
// space at the same moment on a fresh machine, and two creates would make two
// spaces, each believing it was the one.
func workspaceLock(stateDir string) string {
	return filepath.Join(stateDir, "workspace.lock")
}

// lockWait bounds how long one process waits for another's create. It is a
// couple of herdr round trips, not a queue.
const lockWait = 3 * time.Second

// EnsureWorkspace returns the workspace bermuda owns, creating it if it has
// none, and never adopting one it did not create.
func EnsureWorkspace(ctx context.Context, h *herdrcli.Client, stateDir, cwd string) (*herdrcli.Workspace, error) {
	if ws := ownedWorkspace(ctx, h, stateDir); ws != nil {
		return ws, nil
	}

	lock, err := acquireWithin(workspaceLock(stateDir), lockWait)
	if err != nil {
		// Somebody else is creating it. Their record is the one to use, and if
		// it is not there yet then this is a machine where the lock is held by
		// something that is not going to finish — worth saying so rather than
		// making a second space to work around it.
		if ws := ownedWorkspace(ctx, h, stateDir); ws != nil {
			return ws, nil
		}
		return nil, fmt.Errorf("workspace is being created elsewhere: %w", err)
	}
	defer lock.Release()

	// The winner of the lock may have created it while this call was waiting.
	if ws := ownedWorkspace(ctx, h, stateDir); ws != nil {
		return ws, nil
	}

	ws, _, err := h.WorkspaceCreate(ctx, WorkspaceLabel, cwd, nil)
	if err != nil {
		return nil, err
	}
	if err := writeWorkspaceRecord(stateDir, workspaceRecord{ID: ws.WorkspaceID, Label: ws.Label}); err != nil {
		// The space exists and the run can use it. Losing the record costs the
		// next session a second space, which is worth saying out loud and not
		// worth failing a run over.
		return ws, fmt.Errorf("workspace %s created but not recorded: %w", ws.WorkspaceID, err)
	}
	return ws, nil
}

// ownedWorkspace returns the recorded space if herdr still has it under
// bermuda's name, and nil for every other answer.
func ownedWorkspace(ctx context.Context, h *herdrcli.Client, stateDir string) *herdrcli.Workspace {
	rec, err := readWorkspaceRecord(stateDir)
	if err != nil || rec.ID == "" {
		return nil
	}
	ws, err := h.WorkspaceGet(ctx, rec.ID)
	if err != nil || ws == nil {
		return nil // gone: herdr was restarted, or somebody closed it
	}
	if !IsWorkspaceLabel(ws.Label) {
		// Herdr hands ids out again after a restart, so this one is now
		// somebody else's space. Adopting it is precisely what this is here to
		// prevent.
		return nil
	}
	return ws
}

func readWorkspaceRecord(stateDir string) (workspaceRecord, error) {
	var rec workspaceRecord
	b, err := os.ReadFile(workspaceFile(stateDir))
	if err != nil {
		return rec, err
	}
	return rec, json.Unmarshal(b, &rec)
}

func writeWorkspaceRecord(stateDir string, rec workspaceRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, statefs.Dir); err != nil {
		return err
	}
	// Written whole and renamed into place: a half-written record read by the
	// next process would look like no record at all, and make a second space.
	tmp := workspaceFile(stateDir) + ".tmp"
	if err := os.WriteFile(tmp, b, statefs.File); err != nil {
		return err
	}
	return os.Rename(tmp, workspaceFile(stateDir))
}

// acquireWithin waits for the lock rather than failing on the first attempt.
func acquireWithin(path string, wait time.Duration) (*lockfile.Lock, error) {
	deadline := time.Now().Add(wait)
	for {
		lock, err := lockfile.Acquire(path)
		if err == nil {
			return lock, nil
		}
		var held *lockfile.ErrHeld
		if !errors.As(err, &held) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}
