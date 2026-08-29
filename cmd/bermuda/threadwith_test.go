package main

import (
	"strings"
	"testing"
)

// `thread with` is the enforceable form of a lease: everything it refuses to do
// is the point of the command. Each guard below rejects a form that would
// otherwise reach the store, take a real claim, and then leave the resource in
// a state nothing can recover:
//
//   - no `--` at all means no command, so the lease would be taken and released
//     with nothing run between — a claim that looks like work and is not.
//   - a trailing `--` is the same hole one keystroke away, and it is the one an
//     agent actually types.
//   - no `--ttl` means a wrapper killed outright holds the resource forever,
//     which is exactly the orphaned browser this command exists to prevent.
//
// These all have to fail before openStore, because a guard that only fires
// after the claim is taken has already done the damage it was meant to stop.
func TestThreadWithRefusesFormsThatWouldStrandTheResource(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "no command separator",
			argv: []string{"browser", "--ttl", "20m"},
			want: "usage:",
		},
		{
			name: "separator with nothing after it",
			argv: []string{"browser", "--ttl", "20m", "--"},
			want: "usage:",
		},
		{
			name: "no ttl",
			argv: []string{"browser", "--as", "ada", "--", "/bin/true"},
			want: "needs --ttl",
		},
		{
			name: "an explicitly zero ttl",
			argv: []string{"browser", "--ttl", "0", "--as", "ada", "--", "/bin/true"},
			want: "needs --ttl",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The identity and state directory are isolated for the same reason
			// the claim tests isolate them: a parse that reaches resolveIdentity
			// must not touch Handler's live agents or state.
			claimEnv(t)

			err := threadWith(c.argv)
			if err == nil {
				t.Fatalf("threadWith(%v) succeeded; the guard has to fire before any claim is taken", c.argv)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("threadWith(%v) = %q, want it to mention %q", c.argv, err, c.want)
			}
		})
	}
}

// The refusal for a missing TTL names the resource. A caller reading it in a
// log has to be able to tell which lease was asked for, since a wrapper script
// may take more than one.
func TestThreadWithNamesTheResourceItRefused(t *testing.T) {
	claimEnv(t)

	err := threadWith([]string{"browser", "--as", "ada", "--", "/bin/true"})
	if err == nil {
		t.Fatal("threadWith succeeded without a ttl")
	}
	if !strings.Contains(err.Error(), "browser") {
		t.Errorf("error = %q, want the resource named", err)
	}
}
