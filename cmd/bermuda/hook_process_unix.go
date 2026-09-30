//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func settledHookCommand(dir string) (string, []string, bool, error) {
	path := filepath.Join(dir, "hooks", "run-settled")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", nil, false, nil
	}
	return path, nil, true, nil
}

func configureHookProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2_000_000_000
}
