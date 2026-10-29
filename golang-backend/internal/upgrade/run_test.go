package upgrade

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var fixtureOnce sync.Once
var fixtureBinary []byte
var fixtureOldBinary []byte
var fixtureErr error

func binaryFixture(t *testing.T) []byte {
	t.Helper()
	fixtureOnce.Do(func() {
		dir, e := os.MkdirTemp("", "upgrade-binary-")
		if e != nil {
			fixtureErr = e
			return
		}
		defer os.RemoveAll(dir)
		src := `package main
import("fmt";"os")
func main(){if len(os.Args)==2 && os.Args[1]=="--version-only" {fmt.Println("2.0.0.31");return};os.Exit(2)}`
		if e = os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0600); e != nil {
			fixtureErr = e
			return
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "server"), filepath.Join(dir, "main.go"))
		if b, e := cmd.CombinedOutput(); e != nil {
			fixtureErr = errors.New(string(b) + e.Error())
			return
		}
		fixtureBinary, fixtureErr = os.ReadFile(filepath.Join(dir, "server"))
		if fixtureErr != nil {
			return
		}
		if fixtureErr = os.WriteFile(filepath.Join(dir, "main.go"), []byte(strings.ReplaceAll(src, "2.0.0.31", "2.0.0.30")), 0600); fixtureErr != nil {
			return
		}
		cmd = exec.Command("go", "build", "-o", filepath.Join(dir, "old"), filepath.Join(dir, "main.go"))
		if b, e := cmd.CombinedOutput(); e != nil {
			fixtureErr = errors.New(string(b) + e.Error())
			return
		}
		fixtureOldBinary, fixtureErr = os.ReadFile(filepath.Join(dir, "old"))
	})
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	return fixtureBinary
}
func runFixture(t *testing.T) (Options, map[string][]byte, *int) {
	t.Helper()
	root := t.TempDir()
	server := filepath.Join(t.TempDir(), "network-panel-server")
	b := binaryFixture(t)
	for p, v := range map[string]string{"public/index.html": "old-ui", "easytier/run.sh": "old-script", "install.sh": "old-install", "data/panel.db": "old-db", "data/panel.db-wal": "old-wal", ".env": "secret", "certs/key": "key"} {
		p = filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(p), 0755)
		if e := os.WriteFile(p, []byte(v), 0640); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(server, fixtureOldBinary, 0751); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(server, 0751); e != nil {
		t.Fatal(e)
	}
	client, data, _, _ := fixture(t)
	data["network-panel-server-linux-amd64"] = b
	stops := new(int)
	return Options{InstallDir: root, ServerPath: server, Dialect: "sqlite", DatabasePath: filepath.Join(root, "data", "panel.db"), CurrentVersion: "2.0.0.30", HTTPClient: client,
		Stop: func(context.Context) error { *stops++; return nil }, Start: func(context.Context) error { return nil }, VerifyRollback: func(context.Context, Status) error { return nil }, Verify: func(context.Context, Status) error { return nil }}, data, stops
}
func TestRunCommitsOnlyAfterVerified(t *testing.T) {
	o, _, stops := runFixture(t)
	o.Verify = func(_ context.Context, s Status) error {
		if s.State != Verifying || s.ServerSHA256 == "" {
			t.Fatalf("verify status %+v", s)
		}
		b, _ := os.ReadFile(filepath.Join(o.InstallDir, "public", "index.html"))
		if string(b) != "new-ui" {
			t.Fatal("not activated")
		}
		return nil
	}
	s, e := Run(context.Background(), o)
	if e != nil || s.State != Committed || *stops != 1 {
		t.Fatalf("%+v %v stops=%d", s, e, *stops)
	}
	for p, v := range map[string]string{".env": "secret", "certs/key": "key", "data/panel.db": "old-db", "install.sh": "new-install"} {
		b, _ := os.ReadFile(filepath.Join(o.InstallDir, p))
		if string(b) != v {
			t.Fatalf("%s changed: %q", p, b)
		}
	}
	got, e := LoadStatus(o.InstallDir, s.ID)
	if e != nil || got.State != Committed {
		t.Fatalf("durable commit %+v %v", got, e)
	}
}
func TestRunVerificationFailureRestoresDatabaseAndFiles(t *testing.T) {
	o, _, _ := runFixture(t)
	o.Verify = func(context.Context, Status) error {
		os.WriteFile(o.DatabasePath, []byte("migrated"), 0600)
		os.WriteFile(o.DatabasePath+"-shm", []byte("new-shm"), 0600)
		return errors.New("unhealthy")
	}
	s, e := Run(context.Background(), o)
	if e == nil || s.State != RolledBack {
		t.Fatalf("%+v %v", s, e)
	}
	for p, v := range map[string]string{"public/index.html": "old-ui", "easytier/run.sh": "old-script", "install.sh": "old-install", "data/panel.db": "old-db", "data/panel.db-wal": "old-wal"} {
		b, _ := os.ReadFile(filepath.Join(o.InstallDir, p))
		if string(b) != v {
			t.Fatalf("%s not restored: %q", p, b)
		}
	}
	if _, e = os.Stat(o.DatabasePath + "-shm"); !os.IsNotExist(e) {
		t.Fatal("new shm not removed")
	}
	st, _ := os.Stat(o.ServerPath)
	if st.Mode().Perm() != 0751 {
		t.Fatalf("mode not restored: %v", st.Mode())
	}
}
func TestRunFailsBeforeStopOnUntrustedDownload(t *testing.T) {
	o, data, stops := runFixture(t)
	data["BUILD-INFO.json"] = []byte(`{"tag":"v2.0.0.30","commit":"` + strings.Repeat("1", 40) + `"}`)
	s, e := Run(context.Background(), o)
	if e == nil || s.State != Failed || *stops != 0 {
		t.Fatalf("%+v %v stops=%d", s, e, *stops)
	}
	b, _ := os.ReadFile(filepath.Join(o.InstallDir, "public", "index.html"))
	if string(b) != "old-ui" {
		t.Fatal("formal modified")
	}
}
