package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedNextTamperRollsBack(t *testing.T) {
	o, _, _ := runFixture(t)
	calls := 0
	o.Stop = func(context.Context) error {
		calls++
		if calls == 1 {
			entries, e := os.ReadDir(filepath.Join(o.InstallDir, ".upgrades"))
			if e != nil {
				t.Fatal(e)
			}
			id := entries[0].Name()
			p := sibling(filepath.Join(o.InstallDir, "public"), id, "next")
			if e = os.WriteFile(filepath.Join(p, "index.html"), []byte("coherent-tamper"), 0644); e != nil {
				t.Fatal(e)
			}
		}
		return nil
	}
	s, e := Run(context.Background(), o)
	if e == nil || s.State != RolledBack {
		t.Fatalf("staged tamper committed: %+v %v", s, e)
	}
	b, _ := os.ReadFile(filepath.Join(o.InstallDir, "public", "index.html"))
	if string(b) != "old-ui" {
		t.Fatal("not restored")
	}
}
func TestActivatedTargetsTamperRollsBack(t *testing.T) {
	for _, name := range []string{"public/index.html", "install.sh", "easytier/run.sh"} {
		t.Run(name, func(t *testing.T) {
			o, _, _ := runFixture(t)
			o.Verify = func(context.Context, Status) error {
				return os.WriteFile(filepath.Join(o.InstallDir, name), []byte("tampered"), 0644)
			}
			s, e := Run(context.Background(), o)
			if e == nil || s.State != RolledBack {
				t.Fatalf("activation tamper committed %+v %v", s, e)
			}
		})
	}
}
func TestCorruptBeforeImageBlocksAllRestoration(t *testing.T) {
	o, _, _ := runFixture(t)
	o.Verify = func(_ context.Context, s Status) error {
		p := sibling(filepath.Join(o.InstallDir, "easytier"), s.ID, "before")
		os.WriteFile(filepath.Join(p, "run.sh"), []byte("corrupt"), 0600)
		os.WriteFile(o.DatabasePath, []byte("migrated"), 0600)
		return errors.New("unhealthy")
	}
	s, e := Run(context.Background(), o)
	if e == nil || s.State != RollbackFailed {
		t.Fatalf("corrupt backup accepted %+v %v", s, e)
	}
	b, _ := os.ReadFile(o.DatabasePath)
	if string(b) != "migrated" {
		t.Fatal("partial restore occurred before backup preflight")
	}
}
