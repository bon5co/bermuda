package main

import (
	"flag"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The plugin manifest is code that only ever runs on someone else's machine.
//
// It shipped `bermuda daemon --detach`, a flag that did not exist — so one
// startup hook printed "flag provided but not defined" and exited 2 on every
// Herdr start — and `bermuda job run --pick`, which `job run` reads as a job id
// and always failed with "not found". Nothing caught either, because nothing
// here had ever read the manifest.
func TestManifestOnlyInvokesRealCommands(t *testing.T) {
	raw, err := os.ReadFile("../../herdr-plugin.toml")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	known := commands()
	invocations := manifestCommands(t, string(raw))
	if len(invocations) == 0 {
		t.Fatal("no commands found in the manifest — the parser, not the manifest, is wrong")
	}

	for _, argv := range invocations {
		if len(argv) == 0 || !strings.HasSuffix(argv[0], "bermuda") {
			continue // herdr's own commands, not ours
		}
		args := argv[1:]
		if len(args) == 0 {
			t.Errorf("manifest invokes bermuda with no subcommand: %v", argv)
			continue
		}
		if _, ok := known[args[0]]; !ok {
			t.Errorf("manifest invokes %q, which is not a bermuda command", args[0])
			continue
		}
		for _, flagName := range flagsIn(args[1:]) {
			if !flagIsDefined(args, flagName) {
				t.Errorf("manifest passes --%s to `bermuda %s`, which does not define it",
					flagName, strings.Join(args, " "))
			}
		}
	}
}

// flagSetFor returns the flags a manifest invocation would be parsed with, for
// the commands the manifest actually uses.
func flagSetFor(args []string) *flag.FlagSet {
	switch {
	case args[0] == "daemon":
		fs, _ := daemonFlagSet()
		return fs
	case args[0] == "run" && len(args) > 1 && args[1] == "list":
		fs, _ := runListFlagSet()
		return fs
	case args[0] == "board":
		fs, _ := boardFlagSet()
		return fs
	}
	return nil
}

// flagIsDefined asks the command itself, so this cannot drift from the flags
// the command actually parses.
func flagIsDefined(args []string, name string) bool {
	fs := flagSetFor(args)
	if fs == nil {
		// A command whose flags this test cannot reach. Better to say so than
		// to guess: the subcommand check above still applies.
		return true
	}
	return fs.Lookup(name) != nil
}

var flagPattern = regexp.MustCompile(`^--?([a-zA-Z][\w-]*)`)

// flagsIn returns the flag names in an argument list, ignoring their values.
func flagsIn(args []string) []string {
	var out []string
	for _, a := range args {
		if m := flagPattern.FindStringSubmatch(a); m != nil {
			name, _, _ := strings.Cut(m[1], "=")
			out = append(out, name)
		}
	}
	return out
}

// manifestCommands pulls every `command = [...]` array out of the manifest.
// A hand-rolled reader, so the test adds no dependency for six lines of TOML.
var commandLine = regexp.MustCompile(`(?m)^\s*command\s*=\s*\[(.*)\]\s*$`)

func manifestCommands(t *testing.T, toml string) [][]string {
	t.Helper()
	var out [][]string
	for _, m := range commandLine.FindAllStringSubmatch(toml, -1) {
		var argv []string
		for _, part := range strings.Split(m[1], ",") {
			argv = append(argv, strings.Trim(strings.TrimSpace(part), `"`))
		}
		out = append(out, argv)
	}
	return out
}
