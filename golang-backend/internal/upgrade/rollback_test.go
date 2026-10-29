package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRollbackNeedsOldProcessVerification(t *testing.T) {
	o, _, _ := runFixture(t)
	calls := 0
	o.Verify = func(context.Context, Status) error { return errors.New("new unhealthy") }
	o.VerifyRollback = func(_ context.Context, s Status) error {
		calls++
		if s.Tag != "v"+o.CurrentVersion || s.ServerSHA256 != s.OldServerSHA256 || s.ServerSHA256 == "" {
			t.Fatalf("wrong rollback witness %+v", s)
		}
		return errors.New("old unhealthy")
	}
	s, e := Run(context.Background(), o)
	if e == nil || s.State != RollbackFailed || calls != 1 {
		t.Fatalf("unverified rollback: %+v %v calls %d", s, e, calls)
	}
}
func TestQueuedTagCannotBeSubstituted(t *testing.T) {
	o, _, stops := runFixture(t)
	s, e := Reserve(o.InstallDir)
	if e != nil {
		t.Fatal(e)
	}
	s.State = Queued
	s.Tag = "v2.0.0.30"
	persist(o.InstallDir, s)
	o.TransactionDir, _ = TransactionDir(o.InstallDir, s.ID)
	o.Tag = "v2.0.0.31"
	s, e = Run(context.Background(), o)
	if e == nil || *stops != 0 || s.State != Failed {
		t.Fatalf("substitution accepted: %+v %v", s, e)
	}
}
func TestDirectoryFingerprintStableAcrossPhysicalLayouts(t *testing.T) {
	source := t.TempDir()
	for i := 0; i < 1000; i++ {
		p := filepath.Join(source, stringsForFile(i))
		os.WriteFile(p, []byte("x"), 0600)
		os.Remove(p)
	}
	os.WriteFile(filepath.Join(source, "file"), []byte("data"), 0644)
	dest := filepath.Join(t.TempDir(), "copy")
	if e := copyTree(source, dest); e != nil {
		t.Fatal(e)
	}
	a, e := fingerprint(source)
	if e != nil {
		t.Fatal(e)
	}
	b, e := fingerprint(dest)
	if e != nil || !sameImage(a, b) {
		t.Fatalf("unstable directory images: %+v %+v %v", a, b, e)
	}
}
func stringsForFile(i int) string { return fmt.Sprintf("removed-%04d", i) }
