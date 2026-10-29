package upgrade

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealProcessUpgradeAndRollback(t *testing.T) {
	// No systemctl or production paths: two actual ELF children listen on an
	// ephemeral loopback socket and are stopped/reaped by test-owned callbacks.
	for _, failNew := range []bool{false, true} {
		t.Run(fmt.Sprintf("new-start-fails=%v", failNew), func(t *testing.T) {
			o, data, _ := runFixture(t)
			oldBin := buildHTTPFixture(t, "2.0.0.30", false)
			newBin := buildHTTPFixture(t, "2.0.0.31", failNew)
			if e := os.WriteFile(o.ServerPath, oldBin, 0751); e != nil {
				t.Fatal(e)
			}
			data["network-panel-server-linux-amd64"] = newBin
			o.CurrentVersion = "2.0.0.30"
			var child *exec.Cmd
			address := ""
			var pid int
			stop := func(context.Context) error {
				if child == nil {
					return nil
				}
				err := child.Process.Kill()
				wait := child.Wait()
				child = nil
				if err != nil && !errors.Is(err, os.ErrProcessDone) {
					return err
				}
				var ee *exec.ExitError
				if wait != nil && !errors.As(wait, &ee) {
					return wait
				}
				return nil
			}
			start := func(ctx context.Context) error {
				cmd := exec.Command(o.ServerPath)
				cmd.Env = []string{"PATH=/usr/bin:/bin"}
				stdout, e := cmd.StdoutPipe()
				if e != nil {
					return e
				}
				cmd.Stderr = io.Discard
				if e = cmd.Start(); e != nil {
					return e
				}
				child = cmd
				pid = cmd.Process.Pid
				line, e := bufio.NewReader(stdout).ReadString('\n')
				if e != nil {
					cmd.Wait()
					child = nil
					return fmt.Errorf("server did not listen: %w", e)
				}
				address = strings.TrimSpace(line)
				return nil
			}
			verify := func(ctx context.Context, s Status) error {
				if child == nil {
					return errors.New("no process")
				}
				p := fmt.Sprintf("/proc/%d/exe", pid)
				b, e := os.ReadFile(p)
				if e != nil {
					return e
				}
				h := sha256.Sum256(b)
				if hex.EncodeToString(h[:]) != s.ServerSHA256 {
					return errors.New("running executable SHA mismatch")
				}
				ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				req, _ := http.NewRequestWithContext(ctx, "GET", address+"/health", nil)
				client := &http.Client{Timeout: 2 * time.Second}
				resp, e := client.Do(req)
				if e != nil {
					return e
				}
				defer resp.Body.Close()
				var out struct {
					Version string `json:"version"`
					PID     int    `json:"pid"`
				}
				if e = json.NewDecoder(resp.Body).Decode(&out); e != nil {
					return e
				}
				if out.Version != strings.TrimPrefix(s.Tag, "v") || out.PID != pid {
					return fmt.Errorf("not fresh expected process/version: %+v", out)
				}
				return nil
			}
			o.Stop = stop
			o.Start = start
			o.Verify = verify
			o.VerifyRollback = verify
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			defer stop(context.Background())
			if e := start(ctx); e != nil {
				t.Fatal(e)
			}
			oldPID := pid
			s, e := Run(ctx, o)
			if failNew {
				if e == nil || s.State != RolledBack {
					t.Fatalf("rollback %+v %v", s, e)
				}
			} else {
				if e != nil || s.State != Committed {
					t.Fatalf("commit %+v %v", s, e)
				}
			}
			if oldPID == pid {
				t.Fatal("not an independently restarted process")
			}
			want := s
			if failNew {
				want.Tag = "v2.0.0.30"
				want.ServerSHA256 = s.OldServerSHA256
			}
			if e = verify(ctx, want); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func buildHTTPFixture(t *testing.T, version string, fail bool) []byte {
	t.Helper()
	dir := t.TempDir()
	src := fmt.Sprintf(`package main
import("fmt";"os";"net";"net/http";"encoding/json")
func main(){if len(os.Args)==2&&os.Args[1]=="--version-only"{fmt.Println(%q);return};if %v{os.Exit(33)};ln,e:=net.Listen("tcp","127.0.0.1:0");if e!=nil{panic(e)};fmt.Println("http://"+ln.Addr().String());http.HandleFunc("/health",func(w http.ResponseWriter,r *http.Request){json.NewEncoder(w).Encode(map[string]any{"version":%q,"pid":os.Getpid()})});http.Serve(ln,nil)}
`, version, fail, version)
	p := filepath.Join(dir, "main.go")
	if e := os.WriteFile(p, []byte(src), 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "server"), p)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, b)
	}
	b, e := os.ReadFile(filepath.Join(dir, "server"))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
