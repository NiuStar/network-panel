//go:build !windows
// +build !windows

package main

import (
	"os"
	"syscall"
	"time"
)

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func execReplace(target string, args []string, env []string) error {
	return syscall.Exec(target, args, env)
}

func terminateProcess(p *os.Process) error {
	if p == nil {
		return nil
	}
	_ = p.Signal(syscall.SIGTERM)
	time.Sleep(300 * time.Millisecond)
	return p.Kill()
}
