package runner

import (
	"fmt"
	"strings"
	"testing"
)

// herdrNameProblem mirrors what herdr enforces on an agent name, verified
// against the live binary: "agent name must start with a lowercase letter and
// contain only lowercase letters, digits, '-' or '_' (1-32 characters)". Name
// validation happens before the target pane is even looked up, so an illegal
// name is not a degraded run — it is a run that cannot start at all. Returns an
// empty string when the name is acceptable.
func herdrNameProblem(name string) string {
	if len(name) < 1 || len(name) > 32 {
		return fmt.Sprintf("length %d is outside the 1-32 range", len(name))
	}
	if name[0] < 'a' || name[0] > 'z' {
		return "does not start with a lowercase letter"
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return fmt.Sprintf("contains the illegal rune %q", r)
		}
	}
	return ""
}

// A persistent job's agent is found again by name, so the name has to be both
// legal and the same every time. It was neither for a long job id: nothing
// bounded it to 32 characters, so "bmp-bermuda-nightly-coverage-sweep" (34) was
// rejected by herdr and every run of that job failed to start.
func TestPersistentAgentNameIsLegalForAnyJobID(t *testing.T) {
	cases := []struct {
		name  string
		jobID string
	}{
		{"short", "smoke"},
		{"typical", "rt-template-daily"},
		{"just over the limit", "bermuda-nightly-coverage-sweep"},
		{"far over the limit", strings.Repeat("long-job-name-", 8)},
		{"uppercase and spaces", "Nightly Coverage Sweep For Bermuda"},
		{"punctuation only", "!!!"},
		{"empty", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := persistentAgentName(c.jobID)
			if problem := herdrNameProblem(got); problem != "" {
				t.Fatalf("persistentAgentName(%q) = %q: %s", c.jobID, got, problem)
			}
		})
	}
}

// Stability is the whole point of the name: it is how the next run finds the
// agent that is still alive. A name that varied per call would start a fresh
// agent every time and quietly leak the previous one.
func TestPersistentAgentNameIsStablePerJob(t *testing.T) {
	for _, id := range []string{"smoke", "bermuda-nightly-coverage-sweep"} {
		if a, b := persistentAgentName(id), persistentAgentName(id); a != b {
			t.Errorf("persistentAgentName(%q) returned %q then %q", id, a, b)
		}
	}
}

// Two persistent jobs must never share an agent: reuse hands the incoming
// prompt to a live agent, so a collision would run one job inside another job's
// agent. Long ids that agree on their first 28 characters are the case that
// truncation alone gets wrong.
func TestPersistentAgentNamesDoNotCollide(t *testing.T) {
	ids := []string{
		"daily-coverage-report-for-project-alpha",
		"daily-coverage-report-for-project-beta",
		"daily-coverage-report-for-project-alpha-and-beta",
		"smoke",
		"smoke-2",
	}
	seen := map[string]string{}
	for _, id := range ids {
		name := persistentAgentName(id)
		if prev, dup := seen[name]; dup {
			t.Errorf("jobs %q and %q both map to agent %q", prev, id, name)
		}
		seen[name] = id
	}
}

// A persistent agent and a per-run agent are different things: one survives the
// run and one is torn down with it. If the two naming schemes ever produced the
// same name, closing a finished run's tab would take the persistent agent with
// it.
func TestPersistentAndPerRunNamesAreDistinct(t *testing.T) {
	const job = "smoke"
	p := persistentAgentName(job)
	r := agentName(job, "20260729T210001Z-smoke")
	if p == r {
		t.Fatalf("persistent and per-run agent names are both %q", p)
	}
}
