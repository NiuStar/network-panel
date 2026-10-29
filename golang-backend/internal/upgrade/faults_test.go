package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestQueuedValidationFailureIsDurable(t *testing.T) {
	o, _, stops := runFixture(t)
	s, e := Reserve(o.InstallDir)
	if e != nil {
		t.Fatal(e)
	}
	s.State = State("QUEUED")
	if e = persist(o.InstallDir, s); e != nil {
		t.Fatal(e)
	}
	dir, _ := TransactionDir(o.InstallDir, s.ID)
	o.TransactionDir = dir
	o.Dialect = "mysql"
	s, e = Run(context.Background(), o)
	if e == nil || s.State != Failed || *stops != 0 {
		t.Fatalf("queued remained: %+v %v", s, e)
	}
	got, e := LoadStatus(o.InstallDir, s.ID)
	if e != nil || got.State != Failed {
		t.Fatalf("not persisted: %+v %v", got, e)
	}
}
func TestQueuedRun(t *testing.T) {
	o, _, _ := runFixture(t)
	s, e := Reserve(o.InstallDir)
	if e != nil {
		t.Fatal(e)
	}
	s.State = State("QUEUED")
	s.Tag = "v2.0.0.31"
	if e = persist(o.InstallDir, s); e != nil {
		t.Fatal(e)
	}
	o.TransactionDir, _ = TransactionDir(o.InstallDir, s.ID)
	o.Tag = s.Tag
	s, e = Run(context.Background(), o)
	if e != nil || s.State != Committed {
		t.Fatalf("%+v %v", s, e)
	}
}
func TestRunLockBusyAndCrashRequiresRecovery(t *testing.T) {
	o, _, _ := runFixture(t)
	lock, e := lockRoot(o.InstallDir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Run(context.Background(), o); !errors.Is(e, ErrBusy) {
		t.Fatalf("not busy: %v", e)
	}
	unlock(lock)
	s, e := Reserve(o.InstallDir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Run(context.Background(), o); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatalf("pending overwritten: %v", e)
	}
	o.ID = s.ID
	s.State = Activating
	if e = persist(o.InstallDir, s); e != nil {
		t.Fatal(e)
	}
	s, e = Recover(context.Background(), o)
	if !errors.Is(e, ErrRecoveryRequired) || s.State != RecoveryRequired {
		t.Fatalf("unsafe recovery %+v %v", s, e)
	}
}
func TestCallbacksErrorsAreNeverSuccess(t *testing.T) {
	for _, failure := range []string{"stop", "start", "verify", "rollback-stop", "rollback-start"} {
		t.Run(failure, func(t *testing.T) {
			o, _, _ := runFixture(t)
			stops, starts := 0, 0
			o.Stop = func(context.Context) error {
				stops++
				if failure == "stop" && stops == 1 || failure == "rollback-stop" && stops == 2 {
					return errors.New("stop-fault")
				}
				return nil
			}
			o.Start = func(context.Context) error {
				starts++
				if failure == "start" && starts == 1 || failure == "rollback-start" && starts == 2 {
					return errors.New("start-fault")
				}
				return nil
			}
			o.Verify = func(context.Context, Status) error {
				if failure == "verify" || failure == "rollback-stop" || failure == "rollback-start" {
					return errors.New("verify-fault")
				}
				return nil
			}
			s, e := Run(context.Background(), o)
			if e == nil || s.State == Committed {
				t.Fatalf("false success %+v %v", s, e)
			}
			if failure == "rollback-stop" || failure == "rollback-start" {
				if s.State != RollbackFailed {
					t.Fatalf("not fail closed %+v", s)
				}
				if _, e = Reserve(o.InstallDir); !errors.Is(e, ErrRecoveryRequired) {
					t.Fatalf("failed rollback not blocking %v", e)
				}
			} else if s.State != RolledBack {
				t.Fatalf("not restored %+v", s)
			}
		})
	}
}
func TestCurrentTargetSymlinkFailsBeforeStop(t *testing.T) {
	o, _, stops := runFixture(t)
	p := filepath.Join(o.InstallDir, "easytier", "run.sh")
	os.Remove(p)
	os.Symlink(filepath.Join(o.InstallDir, ".env"), p)
	s, e := Run(context.Background(), o)
	if e == nil || *stops != 0 || s.State != Failed {
		t.Fatalf("symlink accepted %+v %v", s, e)
	}
}
func TestRecoverDoesNotTrustJournalPaths(t *testing.T) {
	o, _, stops := runFixture(t)
	s, e := Reserve(o.InstallDir)
	if e != nil {
		t.Fatal(e)
	}
	s.State = Activating
	s.Before = []BeforeImage{{Name: "../../.env", Absent: true}}
	persist(o.InstallDir, s)
	o.ID = s.ID
	s, e = Recover(context.Background(), o)
	if e == nil || s.State != RecoveryRequired || *stops != 0 {
		t.Fatalf("journal trusted %+v %v", s, e)
	}
}
