package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/improve"
)

// A file avoids blocking on the platform's pipe capacity when an unrestricted
// learning body is printed. These tests intentionally exercise long output.
func captureFeedOutput(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = before }()
	runErr := fn()
	os.Stdout = before
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), runErr
}

func TestImproveCLIUnlimitedWriteReadEditList(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	if commands()["improve"] == nil {
		t.Fatal("improve not dispatchable")
	}
	body := strings.TrimSpace(strings.Repeat("measured evidence ", 3000))
	id, err := captureFeedOutput(t, func() error {
		return improveCmd([]string{"write", "--kind", "recovery", "--repo", "acme/widget", "--branch", "feat/learning", "--topic", "Cache recovery", "--body", body})
	})
	if err != nil {
		t.Fatal(err)
	}
	id = strings.TrimSpace(id)
	if _, err := os.Stat(filepath.Join(improve.Dir(stateDir()), id+".md")); err != nil {
		t.Fatal(err)
	}
	text, err := captureFeedOutput(t, func() error { return improveCmd([]string{"read", id, "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var first improve.Entry
	if err := json.Unmarshal([]byte(text), &first); err != nil {
		t.Fatal(err)
	}
	if first.Kind != "recovery" || first.Body != body {
		t.Fatal("write/read changed body or kind")
	}
	if _, err := captureFeedOutput(t, func() error { return improveCmd([]string{"edit", id, "--kind", "mistake"}) }); err != nil {
		t.Fatal(err)
	}
	changed, err := improve.Read(improve.Dir(stateDir()), id)
	if err != nil || changed.Kind != "mistake" || changed.Body != body || changed.Repo != first.Repo || changed.Topic != first.Topic || changed.Branch != first.Branch || !changed.Created.Equal(first.Created) {
		t.Fatalf("partial kind edit lost data: %#v %v", changed, err)
	}
	text, err = captureFeedOutput(t, func() error { return improveCmd([]string{"list", "--json", "--limit", "1"}) })
	if err != nil {
		t.Fatal(err)
	}
	var entries []improve.Entry
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != id || entries[0].Body != body {
		t.Fatal("list changed entry")
	}
	text, err = captureFeedOutput(t, func() error { return improveCmd([]string{"read", id}) })
	if err != nil || !strings.Contains(text, "kind: mistake") || !strings.Contains(text, body) {
		t.Fatalf("plain read missing metadata/body: %v", err)
	}
	text, err = captureFeedOutput(t, func() error { return improveCmd([]string{"path"}) })
	if err != nil || strings.TrimSpace(text) != improve.Dir(stateDir()) {
		t.Fatalf("path=%q %v", text, err)
	}
}

func TestImproveCLIRequiredKindsAndActivityCapRemainSeparate(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	base := []string{"write", "--repo", "acme/widget", "--branch", "main", "--topic", "Learning", "--body", "Evidence."}
	for _, argv := range [][]string{
		{}, {"unknown"}, {"read"}, {"edit"}, {"edit", "missing"}, {"list", "--limit", "-1"}, {"path", "extra"},
		base, append(append([]string{}, base...), "--kind", "unknown"),
	} {
		if err := improveCmd(argv); err == nil {
			t.Errorf("accepted invalid args %v", argv)
		}
	}
	for _, kind := range []string{"discovery", "mistake", "recovery"} {
		args := append(append([]string{}, base...), "--kind", kind)
		id, err := captureFeedOutput(t, func() error { return improveCmd(args) })
		if err != nil {
			t.Fatalf("kind %s rejected: %v", kind, err)
		}
		id = strings.TrimSpace(id)
		if _, err := captureFeedOutput(t, func() error { return improveCmd([]string{"edit", id, "--kind", ""}) }); err == nil {
			t.Fatal("empty kind edit accepted")
		}
		e, err := improve.Read(improve.Dir(stateDir()), id)
		if err != nil || e.Kind != kind {
			t.Fatal("rejected kind edit changed entry")
		}
	}
	long := append([]string{}, base...)
	long[len(long)-1] = strings.Repeat("word ", 51)
	if err := alogCmd(long); err == nil {
		t.Fatal("A.LOG CLI accepted 51 words")
	}
	if _, err := captureFeedOutput(t, func() error { return improveCmd(append(long, "--kind", "discovery")) }); err != nil {
		t.Fatalf("IMPROVE CLI rejected 51 words: %v", err)
	}
	if err := alogCmd(append(append([]string{}, base...), "--kind", "discovery")); err == nil {
		t.Fatal("A.LOG accepted IMPROVE kind flag")
	}
}

func TestImproveCLIUnlimitedStdin(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	body := strings.TrimSpace(strings.Repeat("long multiline learning\n", 10000))
	f, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	before := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = before }()
	id, err := captureFeedOutput(t, func() error {
		return improveCmd([]string{"write", "--kind", "discovery", "--repo", "acme/widget", "--branch", "main", "--topic", "Detailed learning", "--body", "-"})
	})
	if err != nil {
		t.Fatal(err)
	}
	e, err := improve.Read(improve.Dir(stateDir()), strings.TrimSpace(id))
	if err != nil || e.Body != body {
		t.Fatalf("stdin truncated or rejected: %v", err)
	}
}
