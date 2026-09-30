//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWindowsBatchHookUsesTheUnescapedPathInACmdCommandLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state dir")
	t.Setenv("BERMUDA_STATE_DIR", dir)
	if err := os.MkdirAll(filepath.Join(dir, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hooks", "run-settled.cmd")
	if err := os.WriteFile(path, []byte("@echo off\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	program, args, found, err := settledHookCommand(dir)
	if err != nil || !found || program != "cmd.exe" {
		t.Fatalf("batch hook lookup = %q, %v, %t, %v", program, args, found, err)
	}
	cmd := exec.CommandContext(context.Background(), program, args...)
	configureHookProcess(cmd)
	want := `cmd.exe /D /S /C ""` + path + `""`
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine != want {
		t.Fatalf("batch hook command line = %#v, want %q", cmd.SysProcAttr, want)
	}
}
