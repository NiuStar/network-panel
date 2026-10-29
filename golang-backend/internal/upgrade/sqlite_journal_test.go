package upgrade

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The writer must die without closing SQLite: a graceful Close rolls back and
// deletes the very hot journal that the transaction must preserve.
func TestSQLiteDeleteWriterChild(t *testing.T) {
	path := os.Getenv("UPGRADE_DELETE_WRITER_DB")
	if path == "" {
		return
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL; PRAGMA cache_size=10; BEGIN IMMEDIATE; UPDATE retained SET value='UNCOMMITTED', payload=zeroblob(3000);`); err != nil {
		t.Fatal(err)
	}
	fmt.Println("DIRTY-UNCOMMITTED")
	var b [1]byte
	os.Stdin.Read(b[:])
	t.Fatal("writer was not killed")
}

func prepareHotJournalWriter(t *testing.T, path string) func(context.Context) error {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA journal_mode=DELETE; CREATE TABLE retained(id INTEGER PRIMARY KEY, value TEXT NOT NULL, payload BLOB NOT NULL); WITH RECURSIVE n(id) AS (VALUES(1) UNION ALL SELECT id+1 FROM n WHERE id<500) INSERT INTO retained SELECT id,'original',CAST(printf('%03000d',id) AS BLOB) FROM n;`)
	err = errors.Join(err, db.Close())
	if err != nil {
		t.Fatal(err)
	}
	original, err := fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestSQLiteDeleteWriterChild$")
	cmd.Env = append(os.Environ(), "UPGRADE_DELETE_WRITER_DB="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	stopped := false
	stop := func(context.Context) error {
		if stopped {
			return nil
		}
		stopped = true
		err := cmd.Process.Kill()
		cmd.Wait() // Reap our writer before any backup or cleanup.
		stdin.Close()
		cancel()
		if err != nil {
			return err
		}
		dirty, err := fingerprint(path)
		if err != nil {
			return err
		}
		journal, err := os.ReadFile(path + "-journal")
		if err != nil {
			return err
		}
		if sameImage(original, dirty) || len(journal) < 512 || string(journal[:8]) != "\xd9\xd5\x05\xf9\x20\xa1\x63\xd7" {
			return fmt.Errorf("fixture did not spill dirty pages with a hot journal: dirty=%v journal=%d", !sameImage(original, dirty), len(journal))
		}
		return nil
	}
	t.Cleanup(func() {
		if !stopped {
			stop(context.Background())
		}
		cancel()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "DIRTY-UNCOMMITTED\n" {
		t.Fatalf("writer readiness %q: %v", line, err)
	}
	return stop
}

func assertOriginalSQLiteBusiness(t *testing.T, path string) {
	t.Helper()
	// A writable old-process open must be able to perform hot recovery.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var integrity string
	if err = db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
	var total, original, uncommitted, migrated int
	if err = db.QueryRow(`SELECT count(*), sum(value='original'), sum(value='UNCOMMITTED'), sum(value='migrated') FROM retained`).Scan(&total, &original, &uncommitted, &migrated); err != nil {
		t.Fatal(err)
	}
	t.Logf("business rows: total=%d original=%d UNCOMMITTED=%d migrated=%d integrity=%s", total, original, uncommitted, migrated, integrity)
	if total != 500 || original != 500 || uncommitted != 0 || migrated != 0 {
		t.Fatalf("committed business data changed: total=%d original=%d UNCOMMITTED=%d migrated=%d", total, original, uncommitted, migrated)
	}
	var changedPayloads, minID, maxID int
	if err = db.QueryRow(`SELECT sum(payload != CAST(printf('%03000d',id) AS BLOB)), min(id), max(id) FROM retained`).Scan(&changedPayloads, &minID, &maxID); err != nil || changedPayloads != 0 || minID != 1 || maxID != 500 {
		t.Fatalf("original IDs/payload bytes changed: payloads=%d ids=%d..%d err=%v", changedPayloads, minID, maxID, err)
	}
	var schema string
	if err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='retained'`).Scan(&schema); err != nil || schema != "CREATE TABLE retained(id INTEGER PRIMARY KEY, value TEXT NOT NULL, payload BLOB NOT NULL)" {
		t.Fatalf("original schema not restored: %q %v", schema, err)
	}
	var extra int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='migration_side_effect'`).Scan(&extra); err != nil || extra != 0 {
		t.Fatalf("migration DDL survived: %d %v", extra, err)
	}
}

func TestRealSQLiteDeleteHotJournalRollbackPreservesCommittedData(t *testing.T) {
	o, _, _ := runFixture(t)
	o.Stop = prepareHotJournalWriter(t, o.DatabasePath)
	starts := 0
	o.Start = func(context.Context) error {
		starts++
		if starts != 1 {
			return nil
		}
		db, err := sql.Open("sqlite", o.DatabasePath)
		if err != nil {
			return err
		}
		// Opening/DDL recovers the original hot journal, then DELETE mode
		// removes it. Restoring only the dirty main file loses committed data.
		_, err = db.Exec(`ALTER TABLE retained ADD COLUMN dangerous TEXT; UPDATE retained SET value='migrated'; INSERT INTO retained(id,value,payload) VALUES(501,'new',x''); CREATE TABLE migration_side_effect(id INTEGER);`)
		err = errors.Join(err, db.Close())
		if err != nil {
			return err
		}
		if _, err = os.Stat(o.DatabasePath + "-journal"); !os.IsNotExist(err) {
			return fmt.Errorf("migration did not consume journal: %v", err)
		}
		return nil
	}
	o.Verify = func(context.Context, Status) error { return errors.New("unhealthy after migration") }
	s, err := Run(context.Background(), o)
	if err == nil || s.State != RolledBack || starts != 2 {
		t.Fatalf("rollback state=%s starts=%d err=%v", s.State, starts, err)
	}
	assertOriginalSQLiteBusiness(t, o.DatabasePath)
}

func TestRealSQLiteDeleteHotJournalOfflineRecoveryPreservesCommittedData(t *testing.T) {
	o, _, _ := runFixture(t)
	stopWriter := prepareHotJournalWriter(t, o.DatabasePath)
	stops, starts := 0, 0
	o.Stop = func(ctx context.Context) error {
		stops++
		if stops == 2 {
			return errors.New("rollback stop fault; leave durable recovery evidence")
		}
		return stopWriter(ctx)
	}
	o.Start = func(context.Context) error {
		starts++
		if starts != 1 {
			return nil
		}
		db, err := sql.Open("sqlite", o.DatabasePath)
		if err != nil {
			return err
		}
		_, err = db.Exec(`ALTER TABLE retained ADD COLUMN dangerous TEXT; UPDATE retained SET value='migrated'; CREATE TABLE migration_side_effect(id INTEGER);`)
		return errors.Join(err, db.Close())
	}
	o.Verify = func(context.Context, Status) error { return errors.New("migration unhealthy") }
	s, err := Run(context.Background(), o)
	if err == nil || s.State != RollbackFailed || starts != 1 {
		t.Fatalf("fault state=%s starts=%d err=%v", s.State, starts, err)
	}
	o.ID = s.ID
	s, err = Recover(context.Background(), o)
	if !errors.Is(err, ErrRecoveryRequired) || s.State != RolledBack || starts != 2 {
		t.Fatalf("offline recovery state=%s starts=%d err=%v", s.State, starts, err)
	}
	assertOriginalSQLiteBusiness(t, o.DatabasePath)
}
