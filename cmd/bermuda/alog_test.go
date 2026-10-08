package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bon5co/bermuda/v3/internal/alog"
)

func TestALogCLIWriteReadListEdit(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	if commands()["alog"] == nil {
		t.Fatal("alog not dispatchable")
	}
	id, err := captureStdout(t, func() error {
		return alogCmd([]string{"write", "--repo", "acme/widget", "--branch", "feat/log", "--topic", "Started", "--body", "Working on cards."})
	})
	if err != nil {
		t.Fatal(err)
	}
	id = strings.TrimSpace(id)
	if _, err := os.Stat(filepath.Join(alog.Dir(stateDir()), id+".md")); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return alogCmd([]string{"edit", id, "--body", "Cards verified."}) }); err != nil {
		t.Fatal(err)
	}
	text, err := captureStdout(t, func() error { return alogCmd([]string{"read", id, "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var entry alog.Entry
	if err := json.Unmarshal([]byte(text), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Body != "Cards verified." || entry.Topic != "Started" || entry.Repo != "acme/widget" || entry.Branch != "feat/log" {
		t.Fatalf("partial edit lost fields: %#v", entry)
	}
	text, err = captureStdout(t, func() error { return alogCmd([]string{"list", "--json", "--limit", "1"}) })
	if err != nil {
		t.Fatal(err)
	}
	var entries []alog.Entry
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != id {
		t.Fatalf("list=%#v", entries)
	}
}

func TestALogCLIRefusesInvalidArguments(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	for _, argv := range [][]string{
		{}, {"unknown"}, {"read"}, {"edit"}, {"edit", "missing"}, {"path", "extra"}, {"list", "--limit", "-1"}, {"list", "extra"},
		{"write", "--repo", "acme/widget", "--branch", "main", "--topic", "Ready", "--body", strings.Repeat("word ", 51)},
	} {
		if err := alogCmd(argv); err == nil {
			t.Errorf("accepted %v", argv)
		}
	}
}

func TestALogCLIStdinBody(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("Stdin summary.\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	before := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = before }()
	id, err := captureStdout(t, func() error {
		return alogCmd([]string{"write", "--repo", "acme/widget", "--branch", "main", "--topic", "Ready", "--body", "-"})
	})
	if err != nil {
		t.Fatal(err)
	}
	e, err := alog.Read(alog.Dir(stateDir()), strings.TrimSpace(id))
	if err != nil || e.Body != "Stdin summary." {
		t.Fatalf("stdin body=%#v %v", e, err)
	}
}
