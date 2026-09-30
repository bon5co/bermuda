package main

import "testing"

func TestWindowsBatchHookCommandLineKeepsAPathWithSpacesAndMetacharactersQuoted(t *testing.T) {
	t.Setenv("BERMUDA_STATE_DIR", t.TempDir())
	path := `C:\State Dir\A&B\hooks\run-settled.cmd`
	want := `cmd.exe /D /S /C ""C:\State Dir\A&B\hooks\run-settled.cmd""`
	if got := batchHookCommandLine(path); got != want {
		t.Fatalf("batch command line = %q, want %q", got, want)
	}
}
