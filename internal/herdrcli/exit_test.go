package herdrcli

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A herdr that exits nonzero has failed, whatever its stdout looks like.
//
// run used to consult the exit status only when the JSON failed to parse, so a
// failure that still printed a well-formed envelope was reported as success and
// the caller carried on with a decoded value that meant nothing.
func TestRunFailsOnNonZeroExitWithValidEnvelope(t *testing.T) {
	f := newFake(t, `{"id":"1","result":{}}`, "herdr: server went away\n", 1)
	err := f.client().run(context.Background(), nil, "pane", "list")
	if err == nil {
		t.Fatal("nonzero exit reported as success")
	}
	if !strings.Contains(err.Error(), "server went away") {
		t.Errorf("error %q does not carry what herdr said on stderr", err)
	}
}

// An envelope with no result is an empty reply, and says so — rather than
// blaming the JSON decoder for input that parsed perfectly well.
func TestRunSaysWhenTheReplyHasNoResult(t *testing.T) {
	f := newFake(t, `{"id":"1"}`, "", 0)
	var out struct {
		Panes []Pane `json:"panes"`
	}
	err := f.client().run(context.Background(), &out, "pane", "list")
	if err == nil {
		t.Fatal("an empty reply decoded into a value")
	}
	if !strings.Contains(err.Error(), "no result in reply") {
		t.Errorf("error %q should name the empty reply", err)
	}
}

// Code answers about a herdr error however deeply it has been wrapped. It used
// to type-assert, so one %w — which this package and the runner both add —
// made every question about a herdr error code answer false.
func TestCodeSeesThroughWrapping(t *testing.T) {
	f := newFake(t, "", `{"id":"1","error":{"code":"timeout","message":"agent did not settle"}}`, 1)
	err := f.client().run(context.Background(), nil, "agent", "wait")
	if !Code(err, "timeout") {
		t.Fatalf("Code did not recognise the unwrapped error: %v", err)
	}
	wrapped := fmt.Errorf("start agent: %w", err)
	if !Code(wrapped, "timeout") {
		t.Errorf("Code lost the error through one wrap: %v", wrapped)
	}
}
