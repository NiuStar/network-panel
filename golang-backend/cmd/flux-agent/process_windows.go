//go:build windows
// +build windows

package main

import (
	"errors"
	"os"
)

func processAlive(pid int) bool {
	// Best-effort on Windows: if PID is present, assume running.
	return pid > 0
}

func execReplace(target string, args []string, env []string) error {
	return errors.New("exec replace not supported on windows")
}

func terminateProcess(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
