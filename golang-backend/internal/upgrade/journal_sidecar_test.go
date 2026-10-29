package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteJournalRejectedBeforeStop(t *testing.T) {
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			o, _, stops := runFixture(t)
			p := o.DatabasePath + "-journal"
			var err error
			if kind == "symlink" {
				err = os.Symlink(filepath.Join(o.InstallDir, ".env"), p)
			} else {
				err = os.Mkdir(p, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			s, err := Run(context.Background(), o)
			if err == nil || *stops != 0 || (s.ID != "" && s.State != Failed) {
				t.Fatalf("unsafe journal touched service: state=%s stops=%d err=%v", s.State, *stops, err)
			}
		})
	}
}

func TestDeleteJournalRevalidatedAfterStop(t *testing.T) {
	o, _, _ := runFixture(t)
	stops, starts := 0, 0
	o.Stop = func(context.Context) error {
		stops++
		if stops == 1 {
			return os.Mkdir(o.DatabasePath+"-journal", 0700)
		}
		return nil
	}
	o.Start = func(context.Context) error { starts++; return nil }
	s, err := Run(context.Background(), o)
	if err == nil || s.State != RolledBack || s.DatabaseReady || starts != 1 {
		t.Fatalf("invalid post-stop sidecar allowed activation: state=%s ready=%v starts=%d err=%v", s.State, s.DatabaseReady, starts, err)
	}
}

func TestDeleteJournalPresentAndAbsentRestoration(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "present"}[present], func(t *testing.T) {
			o, _, _ := runFixture(t)
			p := o.DatabasePath + "-journal"
			if present {
				if err := os.WriteFile(p, []byte("original-journal"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := fingerprint(p)
			if err != nil {
				t.Fatal(err)
			}
			o.Verify = func(_ context.Context, s Status) error {
				durable, err := LoadStatus(o.InstallDir, s.ID)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, im := range durable.Before {
					if im.Name == "db-journal" {
						found = true
						if !sameImage(im, before) {
							t.Fatalf("wrong durable journal witness: %+v expected %+v", im, before)
						}
					}
				}
				if !found || !durable.DatabaseReady {
					t.Fatal("activation without durable journal witness")
				}
				if err = os.WriteFile(p, []byte("new-journal"), 0644); err != nil {
					t.Fatal(err)
				}
				return errors.New("fail migration")
			}
			o.VerifyRollback = func(context.Context, Status) error {
				got, err := fingerprint(p)
				if err != nil || !sameImage(before, got) {
					t.Fatalf("journal bytes/mode/absence not restored: %+v %v", got, err)
				}
				return nil
			}
			s, err := Run(context.Background(), o)
			if err == nil || s.State != RolledBack {
				t.Fatalf("state=%s err=%v", s.State, err)
			}
		})
	}
}

func TestRecoveryRejectsOldIncompleteDatabaseJournalBeforeCallbacks(t *testing.T) {
	o, _, _ := runFixture(t)
	s, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	// A real durable journal in the old format, with all previous witnesses.
	s.State = Verifying
	images := s.Before[:0]
	for _, im := range s.Before {
		if im.Name != "db-journal" {
			images = append(images, im)
		}
	}
	s.Before = images
	if err = persist(o.InstallDir, s); err != nil {
		t.Fatal(err)
	}
	calls := 0
	o.Stop = func(context.Context) error { calls++; return nil }
	o.Start = o.Stop
	o.ID = s.ID
	// Check targets independently; persisting RECOVERY_REQUIRED changes status.
	paths := []string{o.ServerPath, o.DatabasePath, o.DatabasePath + "-journal", filepath.Join(o.InstallDir, "public")}
	witnesses := make([]BeforeImage, len(paths))
	for i, p := range paths {
		witnesses[i], err = fingerprint(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err = Recover(context.Background(), o)
	if !errors.Is(err, ErrRecoveryRequired) || s.State != RecoveryRequired || calls != 0 {
		t.Fatalf("incomplete legacy journal recovered: state=%s calls=%d err=%v", s.State, calls, err)
	}
	durable, err := LoadStatus(o.InstallDir, s.ID)
	if err != nil || durable.State != RecoveryRequired {
		t.Fatalf("not durably recovery-required: state=%s err=%v", durable.State, err)
	}
	for i, p := range paths {
		got, err := fingerprint(p)
		if err != nil || !sameImage(witnesses[i], got) {
			t.Fatalf("recovery mutated target %s: %v", p, err)
		}
	}
	if _, err = Reserve(o.InstallDir); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("incomplete old journal did not block new upgrade: %v", err)
	}
}

func TestDeleteJournalBackupFaultsFailClosed(t *testing.T) {
	for _, fault := range []string{"missing", "corrupt", "symlink", "destination-symlink", "restore-temp"} {
		t.Run(fault, func(t *testing.T) {
			o, _, _ := runFixture(t)
			p := o.DatabasePath + "-journal"
			if err := os.WriteFile(p, []byte("old-journal"), 0600); err != nil {
				t.Fatal(err)
			}
			starts := 0
			o.Start = func(context.Context) error { starts++; return nil }
			o.Verify = func(_ context.Context, s Status) error {
				dir, _ := TransactionDir(o.InstallDir, s.ID)
				backup := filepath.Join(dir, "db-journal.before")
				var err error
				switch fault {
				case "missing":
					err = os.Remove(backup)
				case "corrupt":
					err = os.WriteFile(backup, []byte("corrupted"), 0600)
				case "symlink":
					err = os.Remove(backup)
					if err == nil {
						err = os.Symlink(filepath.Join(o.InstallDir, ".env"), backup)
					}
				case "destination-symlink":
					err = os.Remove(p)
					if err == nil {
						err = os.Symlink(filepath.Join(o.InstallDir, ".env"), p)
					}
				case "restore-temp":
					err = os.WriteFile(sibling(p, s.ID, "restore"), []byte("stale"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(o.DatabasePath, []byte("migrated-db"), 0600); err != nil {
					t.Fatal(err)
				}
				return errors.New("failed health")
			}
			s, err := Run(context.Background(), o)
			if err == nil || s.State != RollbackFailed || starts != 1 {
				t.Fatalf("fault %s not closed: state=%s starts=%d err=%v", fault, s.State, starts, err)
			}
			if fault != "restore-temp" {
				b, err := os.ReadFile(o.DatabasePath)
				if err != nil || string(b) != "migrated-db" {
					t.Fatalf("preflight partially restored DB: %q %v", b, err)
				}
			}
			b, err := os.ReadFile(filepath.Join(o.InstallDir, ".env"))
			if err != nil || string(b) != "secret" {
				t.Fatalf("followed symlink: %q %v", b, err)
			}
			if _, err = Reserve(o.InstallDir); !errors.Is(err, ErrRecoveryRequired) {
				t.Fatalf("failed rollback did not block: %v", err)
			}
		})
	}
}

func TestDeleteJournalBackupCopyFailureDoesNotActivate(t *testing.T) {
	o, _, _ := runFixture(t)
	p := o.DatabasePath + "-journal"
	if err := os.WriteFile(p, []byte("original-journal"), 0600); err != nil {
		t.Fatal(err)
	}
	stops, starts := 0, 0
	o.Stop = func(context.Context) error {
		stops++
		if stops != 1 {
			return nil
		}
		entries, err := os.ReadDir(filepath.Join(o.InstallDir, ".upgrades"))
		if err != nil || len(entries) != 1 {
			t.Fatalf("transaction entries: %v %v", entries, err)
		}
		dir, _ := TransactionDir(o.InstallDir, entries[0].Name())
		return os.Mkdir(filepath.Join(dir, "db-journal.before"), 0700)
	}
	o.Start = func(context.Context) error { starts++; return nil }
	s, err := Run(context.Background(), o)
	if err == nil || s.State != RolledBack || s.DatabaseReady || starts != 1 {
		t.Fatalf("backup fault allowed activation: state=%s ready=%v starts=%d err=%v", s.State, s.DatabaseReady, starts, err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "original-journal" {
		t.Fatalf("incomplete snapshot changed original journal: %q %v", b, err)
	}
}

func TestDeleteJournalValidationRetainsFixedAllowlist(t *testing.T) {
	o, _, _ := runFixture(t)
	s, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"db-unknown", "db-journal/../../.env", "/arbitrary/path"} {
		bad := s
		bad.Before = append(append([]BeforeImage(nil), s.Before...), BeforeImage{Name: name, Absent: true})
		if err = validateBefore(bad); err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("unknown path/name accepted %q: %v", name, err)
		}
	}
}
