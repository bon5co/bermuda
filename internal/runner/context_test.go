package runner

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/bon5co/bermuda/v3/internal/herdrcli"
	"github.com/bon5co/bermuda/v3/internal/store"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResumeArgs(t *testing.T) {
	for _, tc := range []struct {
		kind, sessionKind string
		want              []string
	}{
		{"codex", "id", []string{"resume", "--model", "chosen", "session"}},
		{"claude", "id", []string{"--model", "chosen", "--resume", "session"}},
		{"pi", "id", []string{"--model", "chosen", "--session", "session"}},
		{"omp", "id", []string{"--model", "chosen", "--session", "session"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			flags := []string{"--model", "chosen"}
			got, err := resumeArgs(tc.kind, store.JobSession{Harness: tc.kind, Kind: tc.sessionKind, Value: "session"}, flags)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, %v, want %v", got, err, tc.want)
			}
			if !reflect.DeepEqual(flags, []string{"--model", "chosen"}) {
				t.Fatal("mutated caller args")
			}
		})
	}
	for _, session := range []store.JobSession{
		{Harness: "codex", Kind: "path", Value: "file"},
		{Harness: "claude", Kind: "id", Value: "id"},
		{Harness: "codex", Kind: "id", Value: ""},
	} {
		if _, err := resumeArgs("codex", session, nil); err == nil {
			t.Fatalf("invalid session accepted: %+v", session)
		}
	}
	if _, err := resumeArgs("aider", store.JobSession{Harness: "aider", Kind: "id", Value: "id"}, nil); err == nil {
		t.Fatal("unsupported harness accepted")
	}
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	if _, err := resumeArgs("pi", store.JobSession{Harness: "pi", Kind: "path", Value: missing}, nil); err == nil {
		t.Fatal("missing Pi session file accepted")
	}
	existing := filepath.Join(t.TempDir(), "present.jsonl")
	if err := os.WriteFile(existing, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := resumeArgs("omp", store.JobSession{Harness: "omp", Kind: "path", Value: existing}, nil); err != nil || !reflect.DeepEqual(got, []string{"--session", existing}) {
		t.Fatalf("existing OMP session: %v %v", got, err)
	}
}

func TestWorkingResumeTimeoutKeepsItsTab(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	s, err := store.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutJobSession(ctx, store.JobSession{JobID: "mail", Harness: "codex", Kind: "id", Value: "session"}); err != nil {
		t.Fatal(err)
	}
	recordWorkspace(t, state, "w1", "Bermuda")
	h, calls := fakeContextCLI(t, state, t.TempDir(), "codex", "", "session", "wait-timeout")
	run, _ := (&Runner{Herdr: h, Store: s, StateDir: state, StartTimeout: time.Second}).Execute(ctx,
		Job{ID: "mail", Kind: "codex", Persistent: true, KeepContext: true, Timeout: time.Second}, "run-2")
	if run.Outcome != OutcomeParked || run.ParkReason != ParkTimeout || run.TabID != "w1:t9" {
		t.Fatalf("valid working conversation discarded: %+v", run)
	}
	if strings.Contains(calls(), "tab close") || strings.Count(calls(), "agent start") != 1 || strings.Contains(calls(), "agent prompt") {
		t.Fatalf("working conversation replaced:\n%s", calls())
	}
}

type refuseSessionWrite struct{ *store.Store }

func (s refuseSessionWrite) PutJobSession(context.Context, store.JobSession) error {
	return errors.New("session disk full")
}

func TestCaptureFailureVisibleAlongsideResult(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	s, err := store.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h, _ := fakeContextCLI(t, state, t.TempDir(), "codex", "kept", "session", "")
	run := &Run{RunID: "run", JobID: "mail", AgentName: "bmp-mail", PaneID: "w1:p9", Context: "kept", Result: &Result{Status: "ok", Note: "PELICAN"}, Outcome: OutcomeDone}
	(&Runner{Herdr: h, Store: refuseSessionWrite{s}}).captureSession(ctx, run, Job{ID: "mail", Kind: "codex"})
	if run.Outcome != OutcomeDone || run.Err != nil || !strings.Contains(run.ContextNote, "session disk full") || !strings.Contains(run.Note(), "session disk full") || !strings.Contains(run.Note(), "PELICAN") {
		t.Fatalf("durable capture failure hidden: %+v note %q", run, run.Note())
	}
}

// Only the external process is replaced. The runner, SQLite and result
// classification stay real; the call log catches duplicate starts.
func fakeContextCLI(t *testing.T, state, dir, kind, live, captured, failure string) (*herdrcli.Client, func() string) {
	t.Helper()
	log := filepath.Join(dir, "calls")
	agent := herdrcli.Agent{Name: "bmp-mail", Agent: kind, AgentStatus: herdrcli.StatusIdle, TabID: "w1:t9", PaneID: "w1:p9", WorkspaceID: "w1", InteractiveReady: true,
		AgentSession: &herdrcli.AgentSession{Agent: kind, Kind: "id", Value: captured}}
	if strings.HasPrefix(live, "adopt") {
		agent.Name = ""
	}
	if live == "foreign" {
		agent.WorkspaceID = "w8"
	}
	if failure == "blocked" {
		agent.AgentStatus = herdrcli.StatusBlocked
	}
	if failure == "working" || failure == "working-mismatch" || failure == "wait-timeout" {
		agent.AgentStatus = herdrcli.StatusWorking
	}
	if failure == "wrong-harness" {
		agent.Agent = "claude"
		agent.AgentSession.Agent = "claude"
	}
	if failure == "unknown" {
		agent.AgentStatus = herdrcli.StatusUnknown
	}
	if failure == "unready" {
		agent.InteractiveReady = false
	}
	ag, _ := json.Marshal(map[string]any{"result": map[string]any{"agent": agent}})
	agents := []herdrcli.Agent{agent}
	if live == "adopted-twice" {
		second := agent
		second.PaneID, second.TabID = "w1:p10", "w1:t10"
		agents = append(agents, second)
	}
	list, _ := json.Marshal(map[string]any{"result": map[string]any{"agents": agents}})
	settled := agent
	settled.AgentStatus = herdrcli.StatusIdle
	settledJSON, _ := json.Marshal(map[string]any{"result": map[string]any{"agent": settled}})
	empty := agent
	empty.AgentSession = nil
	if strings.HasSuffix(failure, "no-session") {
		ag, _ = json.Marshal(map[string]any{"result": map[string]any{"agent": empty}})
	}
	emptyJSON, _ := json.Marshal(map[string]any{"result": map[string]any{"agent": empty}})
	for name, body := range map[string][]byte{"agent.json": ag, "list.json": list, "settled.json": settledJSON, "empty.json": emptyJSON} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.Join([]string{
		"#!/bin/sh",
		"printf '%s\\n' \"$*\" >> '" + log + "'",
		"case \"$1 $2\" in",
		"'agent get')",
		" if [ '" + failure + "' = verify-error ] && [ -f '" + dir + "/started' ]; then printf '%s\\n' '{\"error\":{\"code\":\"transport\",\"message\":\"offline\"}}'; exit 1; fi",
		" if [ '" + live + "' = kept ] || [ '" + live + "' = foreign ] || [ \"$3\" = w1:p9 ] || [ -f '" + dir + "/started' ]; then",
		" if [ -f '" + dir + "/waited' ]; then cat '" + dir + "/settled.json';",
		" elif [ '" + failure + "' = delayed ] && [ ! -f '" + dir + "/observed' ]; then touch '" + dir + "/observed'; cat '" + dir + "/empty.json';",
		" else cat '" + dir + "/agent.json'; fi; else",
		" printf '%s\\n' '{\"error\":{\"code\":\"agent_not_found\",\"message\":\"gone\"}}'; exit 1; fi ;;",
		"'agent list')",
		" if [ '" + failure + "' = list ]; then printf '%s\\n' '{\"error\":{\"code\":\"transport\",\"message\":\"offline\"}}'; exit 1; fi",
		" if [ '" + live + "' = adopted ] || [ '" + live + "' = adopted-twice ]; then cat '" + dir + "/list.json'; else printf '%s\\n' '{\"result\":{\"agents\":[]}}'; fi ;;",
		"'workspace get') printf '%s\\n' '{\"result\":{\"workspace\":{\"workspace_id\":\"w1\",\"label\":\"Bermuda\"}}}' ;;",
		"'tab create') printf '%s\\n' '{\"result\":{\"root_pane\":{\"pane_id\":\"w1:p9\",\"tab_id\":\"w1:t9\"}}}' ;;",
		"'pane process-info') printf '%s\\n' '{\"result\":{\"process_info\":{\"shell_pid\":123,\"foreground_process_group_id\":123}}}' ;;",
		"'agent start')",
		" if [ '" + failure + "' = resume ] || [ '" + failure + "' = resume-no-session ] && [ ! -f '" + dir + "/attempted' ]; then touch '" + dir + "/attempted'; printf '%s\\n' '{\"error\":{\"code\":\"agent_not_ready\",\"message\":\"session missing\"}}'; exit 1; fi",
		" touch '" + dir + "/started'; cat '" + dir + "/agent.json' ;;",
		"'agent wait')",
		" if [ '" + failure + "' = wait-timeout ]; then printf '%s\\n' '{\"error\":{\"code\":\"timeout\",\"message\":\"still working\"}}'; exit 1; fi",
		" touch '" + dir + "/waited'; printf '%s\\n' '{\"result\":{}}' ;;",
		"'agent prompt')",
		" printf '%s\\n' '{\"status\":\"ok\",\"note\":\"PELICAN\"}' > '" + state + "/runs/run-2/result.json'",
		" printf '%s\\n' '{\"result\":{}}' ;;",
		"*) printf '%s\\n' '{\"result\":{}}' ;;",
		"esac", ""}, "\n")
	bin := filepath.Join(dir, "herdr")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return &herdrcli.Client{Bin: bin}, func() string { b, _ := os.ReadFile(log); return string(b) }
}

func TestKeepContextRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, kind, live, failure, policy, captured, want string
		stored, keep                                      bool
		starts                                            int
	}{
		{"first", "codex", "", "", "", "session", "fresh", false, true, 1},
		{"resume", "codex", "", "", "", "session", "resumed", true, true, 1},
		{"optional metadata on resume", "codex", "", "no-session", "", "session", "resumed", true, true, 1},
		{"adopt unnamed", "codex", "adopted", "", "", "session", "adopted", true, true, 0},
		{"keep and new session", "codex", "kept", "", "", "changed", "kept", true, true, 0},
		{"unsupported", "aider", "", "", "", "session", "lost", true, true, 1},
		{"failed resume fresh", "codex", "", "resume", "", "new", "lost", true, true, 2},
		{"failed resume park", "codex", "", "resume", "park", "session", "lost", true, true, 1},
		{"unsupported park", "aider", "", "", "park", "session", "lost", true, true, 0},
		{"opt out", "codex", "", "", "", "new", "", true, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			state := t.TempDir()
			s, err := store.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if tc.stored {
				if err := s.PutJobSession(ctx, store.JobSession{JobID: "mail", Harness: tc.kind, Kind: "id", Value: "session", RunID: "run-1", CapturedAt: time.Unix(10, 0)}); err != nil {
					t.Fatal(err)
				}
			}
			recordWorkspace(t, state, "w1", "Bermuda")
			h, calls := fakeContextCLI(t, state, t.TempDir(), tc.kind, tc.live, tc.captured, tc.failure)
			r := &Runner{Herdr: h, Store: s, StateDir: state}
			run, err := r.Execute(ctx, Job{ID: "mail", Kind: tc.kind, Persistent: true, KeepContext: tc.keep, OnContextLoss: tc.policy, Prompt: "remember", Timeout: time.Second}, "run-2")
			if err != nil {
				t.Fatal(err)
			}
			if run.Context != tc.want {
				t.Fatalf("context %q, want %q; %+v", run.Context, tc.want, run)
			}
			if n := strings.Count(calls(), "agent start "); n != tc.starts {
				t.Fatalf("%d starts, want %d:\n%s", n, tc.starts, calls())
			}
			if tc.policy == "park" {
				if run.Outcome != OutcomeParked || run.ParkReason != ParkContextLost || strings.Contains(calls(), "agent prompt") {
					t.Fatalf("did not hand loss to human: %+v\n%s", run, calls())
				}
				saved, _ := s.JobSession(ctx, "mail")
				if saved.Value != "session" {
					t.Fatal("park overwrote lost session")
				}
			} else {
				if run.Outcome != OutcomeDone {
					t.Fatalf("outcome: %+v", run)
				}
				saved, err := s.JobSession(ctx, "mail")
				if tc.keep && tc.failure != "no-session" && (err != nil || saved.Value != tc.captured || saved.RunID != "run-2") {
					t.Fatalf("capture %+v %v", saved, err)
				}
				if tc.failure == "no-session" && (saved.Value != "session" || !strings.Contains(run.ContextNote, "has not confirmed")) {
					t.Fatal("unknown session discarded or hidden")
				}
				if !tc.keep && saved.Value != "session" {
					t.Fatal("opt-out captured session")
				}
			}
			if tc.want != "" && !strings.Contains(run.Note(), "context: "+tc.want) {
				t.Fatalf("no context note: %q", run.Note())
			}
			if tc.want == "resumed" && !strings.Contains(calls(), "-- resume session") {
				t.Fatalf("resume args missing:\n%s", calls())
			}
			if tc.want == "adopted" && !strings.Contains(calls(), "agent prompt w1:p9") {
				t.Fatalf("unnamed restored agent not targeted by pane:\n%s", calls())
			}
			if tc.want == "lost" && run.ContextNote == "" {
				t.Fatal("loss unexplained")
			}
		})
	}
}

func TestRecoveryReadinessAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, live, failure, captured, policy, reason string
		wantError                                     bool
	}{
		{"foreign named", "foreign", "", "session", "", "context_lost", false},
		{"wrong named harness", "kept", "wrong-harness", "session", "", "context_lost", false},
		{"ambiguous restored agents", "adopted-twice", "", "session", "", "context_lost", false},
		{"unknown named agent", "kept", "unknown", "session", "", "blocked", false},
		{"unready named agent", "kept", "unready", "session", "", "blocked", false},
		{"unready restored agent", "adopted", "unready", "session", "", "blocked", false},
		{"resumed blocked", "", "blocked", "session", "", "blocked", false},
		{"resumed working", "", "working", "session", "", "", false},
		{"delayed mismatch parks", "", "delayed", "changed", "park", "context_lost", false},
		{"list failure does not start", "", "list", "session", "", "", true},
		{"verification transport", "", "verify-error", "session", "", "agent_lost", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			state := t.TempDir()
			s, err := store.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.PutJobSession(ctx, store.JobSession{JobID: "mail", Harness: "codex", Kind: "id", Value: "session", RunID: "run-1"}); err != nil {
				t.Fatal(err)
			}
			recordWorkspace(t, state, "w1", "Bermuda")
			h, calls := fakeContextCLI(t, state, t.TempDir(), "codex", tc.live, tc.captured, tc.failure)
			run, err := (&Runner{Herdr: h, Store: s, StateDir: state, StartTimeout: time.Second}).Execute(ctx,
				Job{ID: "mail", Kind: "codex", Persistent: true, KeepContext: true, OnContextLoss: tc.policy, Timeout: time.Second}, "run-2")
			if (err != nil) != tc.wantError {
				t.Fatalf("run %+v err %v", run, err)
			}
			if tc.failure == "verify-error" && (strings.Contains(calls(), "tab close") || strings.Count(calls(), "agent start") != 1) {
				t.Fatal("observation failure destroyed resumed conversation")
			}
			if tc.reason != "" {
				if run.Outcome != OutcomeParked || string(run.ParkReason) != tc.reason {
					t.Fatalf("expected park %s: %+v", tc.reason, run)
				}
				if (tc.name == "foreign named" || tc.name == "wrong named harness") && run.Status != herdrcli.StatusIdle {
					t.Fatalf("park lost observed agent status: %+v", run)
				}
				if strings.Contains(calls(), "agent prompt") {
					t.Fatalf("prompted unsafe agent: %s", calls())
				}
				saved, _ := s.JobSession(ctx, "mail")
				if saved.Value != "session" {
					t.Fatal("overwrote session before human recovery")
				}
			} else if tc.wantError {
				if strings.Contains(calls(), "agent start") {
					t.Fatal("double-start risk after failed observation")
				}
			} else {
				if run.Outcome != OutcomeDone || !strings.Contains(calls(), "agent wait") {
					t.Fatalf("working resume not waited: %+v\n%s", run, calls())
				}
			}
		})
	}
}

func TestContextLossParkWritesRunDirectoryNote(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	s, err := store.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutJobSession(ctx, store.JobSession{JobID: "mail", Harness: "aider", Kind: "id", Value: "old", RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	recordWorkspace(t, state, "w1", "Bermuda")
	h, _ := fakeContextCLI(t, state, t.TempDir(), "aider", "", "old", "")
	run, err := (&Runner{Herdr: h, Store: s, StateDir: state}).Execute(ctx,
		Job{ID: "mail", Kind: "aider", Persistent: true, KeepContext: true, OnContextLoss: "park"}, "run-2")
	if err != nil || run.ParkReason != ParkContextLost {
		t.Fatalf("context loss did not park: %+v %v", run, err)
	}
	note, err := os.ReadFile(filepath.Join(run.RunDir, ErrFile))
	if err != nil || !strings.Contains(string(note), "conversation") {
		t.Fatalf("run directory lacks context-loss note: %q %v", note, err)
	}
}

func TestFirstKeepContextRunIgnoresOptedOutHistory(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	s, err := store.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutRun(ctx, store.Run{ID: "old", JobID: "mail", Outcome: "done", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	recordWorkspace(t, state, "w1", "Bermuda")
	h, _ := fakeContextCLI(t, state, t.TempDir(), "codex", "", "session", "")
	run, err := (&Runner{Herdr: h, Store: s, StateDir: state}).Execute(ctx,
		Job{ID: "mail", Kind: "codex", Persistent: true, KeepContext: true, OnContextLoss: "park"}, "run-2")
	if err != nil || run.Context != "fresh" || run.Outcome != OutcomeDone {
		t.Fatalf("first enablement parked: %+v %v", run, err)
	}
}

func TestFreshFallbackCannotResumeObsoleteSessionNextTime(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	s, err := store.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutJobSession(ctx, store.JobSession{JobID: "mail", Harness: "codex", Kind: "id", Value: "obsolete"}); err != nil {
		t.Fatal(err)
	}
	recordWorkspace(t, state, "w1", "Bermuda")
	h, _ := fakeContextCLI(t, state, t.TempDir(), "codex", "", "new", "resume-no-session")
	run, err := (&Runner{Herdr: h, Store: s, StateDir: state}).Execute(ctx,
		Job{ID: "mail", Kind: "codex", Persistent: true, KeepContext: true}, "run-2")
	if err != nil || run.Context != "lost" || run.Outcome != OutcomeDone {
		t.Fatalf("fallback: %+v %v", run, err)
	}
	if old, err := s.JobSession(ctx, "mail"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("fresh run retained obsolete conversation: %+v %v", old, err)
	}
}

func TestWorkingResumeMismatchHonorsLossPolicy(t *testing.T) {
	for _, policy := range []string{"fresh", "park"} {
		t.Run(policy, func(t *testing.T) {
			ctx := context.Background()
			state := t.TempDir()
			s, err := store.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.PutJobSession(ctx, store.JobSession{JobID: "mail", Harness: "codex", Kind: "id", Value: "session"}); err != nil {
				t.Fatal(err)
			}
			recordWorkspace(t, state, "w1", "Bermuda")
			h, calls := fakeContextCLI(t, state, t.TempDir(), "codex", "", "changed", "working-mismatch")
			run, err := (&Runner{Herdr: h, Store: s, StateDir: state}).Execute(ctx, Job{ID: "mail", Kind: "codex", Persistent: true, KeepContext: true, OnContextLoss: policy, Timeout: time.Second}, "run-2")
			if err != nil || run.Context != "lost" {
				t.Fatalf("mismatch: %+v %v", run, err)
			}
			if policy == "fresh" {
				if run.Outcome != OutcomeDone || strings.Count(calls(), "agent start") != 2 || !strings.Contains(calls(), "agent prompt") {
					t.Fatalf("no fresh fallback: %+v\n%s", run, calls())
				}
				saved, err := s.JobSession(ctx, "mail")
				if err != nil || saved.Value != "changed" {
					t.Fatalf("replacement session: %+v %v", saved, err)
				}
			} else {
				if run.Outcome != OutcomeParked || run.ParkReason != ParkContextLost || strings.Count(calls(), "agent start") != 1 || strings.Contains(calls(), "agent prompt") {
					t.Fatalf("park policy: %+v\n%s", run, calls())
				}
				saved, err := s.JobSession(ctx, "mail")
				if err != nil || saved.Value != "session" {
					t.Fatalf("original session lost: %+v %v", saved, err)
				}
			}
		})
	}
}

func TestFreshFallbackInvalidatesObsoleteSessionBeforeNextRun(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	s, err := store.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutJobSession(ctx, store.JobSession{JobID: "mail", Harness: "codex", Kind: "id", Value: "old", RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	recordWorkspace(t, state, "w1", "Bermuda")
	h, _ := fakeContextCLI(t, state, t.TempDir(), "codex", "", "", "resume")
	job := Job{ID: "mail", Kind: "codex", Persistent: true, KeepContext: true, Timeout: time.Second}
	run, err := (&Runner{Herdr: h, Store: s, StateDir: state}).Execute(ctx, job, "run-2")
	if err != nil || run.Context != "lost" || run.Outcome != OutcomeDone {
		t.Fatalf("fresh fallback: %+v %v", run, err)
	}
	if _, err := s.JobSession(ctx, job.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old session remained active after fresh fallback: %v", err)
	}
	if err := s.PutRun(ctx, store.Run{ID: run.RunID, JobID: job.ID, Context: run.Context, Outcome: string(run.Outcome),
		TabID: run.TabID, Status: string(run.Status), StartedAt: run.StartedAt}); err != nil {
		t.Fatal(err)
	}
	h, calls := fakeContextCLI(t, state, t.TempDir(), "codex", "", "new", "")
	third, err := (&Runner{Herdr: h, Store: s, StateDir: state}).Execute(ctx, job, "run-3")
	if err != nil || third.Context != "lost" || strings.Contains(calls(), "-- resume old") {
		t.Fatalf("third run reopened obsolete session: %+v %v\n%s", third, err, calls())
	}
}

func TestLiveReuseClearsMissingSavedSessionWarning(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	s, err := store.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutRun(ctx, store.Run{ID: "run-1", JobID: "mail", Context: "fresh", Outcome: "done",
		TabID: "w1:t9", Status: "idle", StartedAt: time.Unix(1, 0)}); err != nil {
		t.Fatal(err)
	}
	recordWorkspace(t, state, "w1", "Bermuda")
	h, _ := fakeContextCLI(t, state, t.TempDir(), "codex", "kept", "live-session", "")
	run, err := (&Runner{Herdr: h, Store: s, StateDir: state}).Execute(ctx,
		Job{ID: "mail", Kind: "codex", Persistent: true, KeepContext: true}, "run-2")
	if err != nil || run.Context != "kept" || strings.Contains(run.ContextNote, "no recorded harness session") {
		t.Fatalf("live conversation kept a stale loss warning: %+v %v", run, err)
	}
}
