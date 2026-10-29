package upgrade

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestRecoveryRejectsAbsentServerBeforeImage(t *testing.T) {
	o, _, _ := runFixture(t)
	o.Verify = func(context.Context, Status) error { return errors.New("failure") }
	o.VerifyRollback = func(context.Context, Status) error { return errors.New("failure") }
	s, _ := Run(context.Background(), o)
	for i := range s.Before {
		if s.Before[i].Name == "server" {
			s.Before[i].Absent = true
			s.Before[i].SHA256 = ""
			s.Before[i].Mode = 0
		}
	}
	persist(o.InstallDir, s)
	o.ID = s.ID
	s, e := Recover(context.Background(), o)
	if e == nil || s.State != RecoveryRequired {
		t.Fatalf("invalid journal accepted: %+v %v", s, e)
	}
	if _, e = os.Stat(o.ServerPath); e != nil {
		t.Fatal("deleted server")
	}
}
func TestJournalPermissionsFailClosed(t *testing.T) {
	root := t.TempDir()
	s, e := Reserve(root)
	if e != nil {
		t.Fatal(e)
	}
	dir, _ := TransactionDir(root, s.ID)
	os.Chmod(dir, 0755)
	if _, e = LoadStatus(root, s.ID); e == nil {
		t.Fatal("insecure transaction accepted")
	}
}
