package upgrade

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCrashChild(t *testing.T) {
	if os.Getenv("UPGRADE_CRASH_CHILD") != "1" {
		return
	}
	var input struct{ Root, Server, DB, Phase string }
	if e := json.NewDecoder(os.Stdin).Decode(&input); e != nil {
		t.Fatal(e)
	}
	client, data, _, _ := fixture(t)
	b := binaryFixture(t)
	data["network-panel-server-linux-amd64"] = b
	o := Options{InstallDir: input.Root, ServerPath: input.Server, Dialect: "sqlite", DatabasePath: input.DB, CurrentVersion: "2.0.0.30", HTTPClient: client}
	o.Stop = func(context.Context) error { os.Stdout.WriteString("LOCKED-ACTIVATING\n"); select {} }
	o.Start = func(context.Context) error { return nil }
	o.Verify = func(context.Context, Status) error { return nil }
	if input.Phase == "VERIFYING" {
		o.Stop = func(context.Context) error { return nil }
		o.Verify = func(context.Context, Status) error {
			os.WriteFile(input.DB, []byte("migrated-before-crash"), 0600)
			os.Stdout.WriteString("LOCKED-VERIFYING\n")
			select {}
		}
	}
	Run(context.Background(), o)
	t.Fatal("child unexpectedly returned")
}
func TestCrossProcessLockAndKilledWorkerRecovery(t *testing.T) {
	for _, phase := range []string{"ACTIVATING", "VERIFYING"} {
		t.Run(phase, func(t *testing.T) {
			o, _, _ := runFixture(t)
			exe, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			cmd := exec.Command(exe, "-test.run=^TestCrashChild$")
			cmd.Env = append(os.Environ(), "UPGRADE_CRASH_CHILD=1")
			stdin, e := cmd.StdinPipe()
			if e != nil {
				t.Fatal(e)
			}
			stdout, e := cmd.StdoutPipe()
			if e != nil {
				t.Fatal(e)
			}
			cmd.Stderr = os.Stderr
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			defer func() { cmd.Process.Kill(); cmd.Wait() }()
			json.NewEncoder(stdin).Encode(struct{ Root, Server, DB, Phase string }{o.InstallDir, o.ServerPath, o.DatabasePath, phase})
			stdin.Close()
			reader := bufio.NewReader(stdout)
			line, e := reader.ReadString('\n')
			if e != nil || line != "LOCKED-"+phase+"\n" {
				t.Fatalf("child %q %v", line, e)
			}
			if _, e = Run(context.Background(), o); !errors.Is(e, ErrBusy) {
				t.Fatalf("cross-process lock not held: %v", e)
			}
			if e = cmd.Process.Kill(); e != nil {
				t.Fatal(e)
			}
			cmd.Wait()
			entries, e := os.ReadDir(filepath.Join(o.InstallDir, ".upgrades"))
			if e != nil || len(entries) != 1 {
				t.Fatalf("transactions %v %v", entries, e)
			}
			id := entries[0].Name()
			s, e := LoadStatus(o.InstallDir, id)
			if e != nil || s.State != State(phase) {
				t.Fatalf("lost durable crash witness %+v %v", s, e)
			}
			if _, e = Reserve(o.InstallDir); !errors.Is(e, ErrRecoveryRequired) {
				t.Fatalf("crash not blocking: %v", e)
			}
			o.ID = id
			s, e = Recover(context.Background(), o)
			if !errors.Is(e, ErrRecoveryRequired) || s.State != RolledBack {
				t.Fatalf("explicit recovery %+v %v", s, e)
			}
			b, _ := os.ReadFile(filepath.Join(o.InstallDir, "public", "index.html"))
			if string(b) != "old-ui" {
				t.Fatal("recovery changed old public")
			}
			b, _ = os.ReadFile(o.DatabasePath)
			if string(b) != "old-db" {
				t.Fatal("recovery did not restore old database")
			}
		})
	}
}
