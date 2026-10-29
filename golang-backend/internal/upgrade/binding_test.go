package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverRefusesChangedConfiguredDatabase(t *testing.T) {
	o, _, _ := runFixture(t)
	o.Verify = func(context.Context, Status) error { return errors.New("fail") }
	o.VerifyRollback = func(context.Context, Status) error { return errors.New("old not ready") }
	s, e := Run(context.Background(), o)
	if e == nil || s.State != RollbackFailed {
		t.Fatalf("setup %+v %v", s, e)
	}
	other := filepath.Join(o.InstallDir, "other.db")
	os.WriteFile(other, []byte("do-not-touch"), 0600)
	o.ID = s.ID
	o.DatabasePath = other
	s, e = Recover(context.Background(), o)
	if e == nil || s.State != RecoveryRequired {
		t.Fatalf("rebound recovery %+v %v", s, e)
	}
	b, _ := os.ReadFile(other)
	if string(b) != "do-not-touch" {
		t.Fatal("recovery overwrote different database")
	}
}
