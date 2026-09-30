//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

func settledHookCommand(dir string) (string, []string, bool, error) {
	base := filepath.Join(dir, "hooks", "run-settled")
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		path := base + ext
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", nil, false, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if ext == ".exe" {
			return path, nil, true, nil
		}
		return "cmd.exe", []string{"/D", "/S", "/C", path}, true, nil
	}
	return "", nil, false, nil
}

func configureHookProcess(cmd *exec.Cmd) {
	if len(cmd.Args) == 5 && cmd.Args[0] == "cmd.exe" {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: batchHookCommandLine(cmd.Args[4])}
	}
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := kill.Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 2_000_000_000
}
