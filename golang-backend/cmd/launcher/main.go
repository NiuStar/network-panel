package main

import (
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const serverPath = "/app/server"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("[launcher] starting, will supervise", serverPath)

	// signal handling
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGUSR1)

	childExit := make(chan struct{})
	var childCmd *exec.Cmd

	startChild := func() *exec.Cmd {
		cmd := exec.Command(serverPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(), "NP_LAUNCHER=1")
		if err := cmd.Start(); err != nil {
			log.Printf("[launcher] start child failed: %v", err)
			return nil
		}
		log.Printf("[launcher] child started pid=%d", cmd.Process.Pid)
		go func() {
			err := cmd.Wait()
			if err != nil {
				log.Printf("[launcher] child exited with error: %v", err)
			} else {
				log.Printf("[launcher] child exited")
			}
			childExit <- struct{}{}
		}()
		return cmd
	}

	stopChild := func(cmd *exec.Cmd) {
		if cmd == nil || cmd.Process == nil {
			return
		}
		log.Printf("[launcher] stopping child pid=%d", cmd.Process.Pid)
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			log.Printf("[launcher] child did not exit in time, killing pid=%d", cmd.Process.Pid)
			_ = cmd.Process.Kill()
		}
	}

	childCmd = startChild()

	for {
		select {
		case sig := <-sigCh:
			switch sig {
			case syscall.SIGINT, syscall.SIGTERM:
				log.Printf("[launcher] received %s, shutting down", sig.String())
				stopChild(childCmd)
				return
			case syscall.SIGHUP, syscall.SIGUSR1:
				log.Printf("[launcher] received %s, restarting child", sig.String())
				stopChild(childCmd)
				time.Sleep(500 * time.Millisecond)
				childCmd = startChild()
			}
		case <-childExit:
			log.Printf("[launcher] child exit detected, restarting")
			time.Sleep(500 * time.Millisecond)
			childCmd = startChild()
		}
	}
}
