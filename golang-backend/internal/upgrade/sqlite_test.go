package upgrade

import (
	"context"
	"database/sql"
	"errors"
	_ "modernc.org/sqlite"
	"os"
	"testing"
)

func TestRealSQLiteWALMigrationRollbackIntegrity(t *testing.T) {
	o, _, _ := runFixture(t)
	os.Remove(o.DatabasePath)
	os.Remove(o.DatabasePath + "-wal")
	db, e := sql.Open("sqlite", o.DatabasePath)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	if _, e = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE retained(id INTEGER PRIMARY KEY, value TEXT NOT NULL); INSERT INTO retained VALUES(1,'original'),(2,'preserved');`); e != nil {
		t.Fatal(e)
	}
	o.Stop = func(context.Context) error {
		if db == nil {
			return nil
		}
		e := db.Close()
		db = nil
		return e
	}
	starts := 0
	o.Start = func(context.Context) error {
		starts++
		if starts == 1 {
			var e error
			db, e = sql.Open("sqlite", o.DatabasePath)
			if e != nil {
				return e
			}
			_, e = db.Exec(`PRAGMA journal_mode=WAL; ALTER TABLE retained ADD COLUMN dangerous TEXT; UPDATE retained SET value='migrated'; INSERT INTO retained(id,value) VALUES(3,'new'); CREATE TABLE migration_side_effect(id INTEGER);`)
			return e
		}
		return nil
	}
	o.Verify = func(context.Context, Status) error { return errors.New("failed after actual DDL and writes") }
	o.VerifyRollback = func(context.Context, Status) error {
		recovered, e := sql.Open("sqlite", "file:"+o.DatabasePath+"?mode=ro")
		if e != nil {
			return e
		}
		defer recovered.Close()
		var check string
		if e = recovered.QueryRow(`PRAGMA integrity_check`).Scan(&check); e != nil || check != "ok" {
			t.Fatalf("integrity %q %v", check, e)
		}
		var schema string
		if e = recovered.QueryRow(`SELECT sql FROM sqlite_master WHERE name='retained'`).Scan(&schema); e != nil {
			t.Fatal(e)
		}
		if schema != "CREATE TABLE retained(id INTEGER PRIMARY KEY, value TEXT NOT NULL)" {
			t.Fatalf("schema drift %s", schema)
		}
		var rows string
		if e = recovered.QueryRow(`SELECT group_concat(id || ':' || value, ',') FROM (SELECT id,value FROM retained ORDER BY id)`).Scan(&rows); e != nil || rows != "1:original,2:preserved" {
			t.Fatalf("rows %q %v", rows, e)
		}
		var extra int
		if e = recovered.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='migration_side_effect'`).Scan(&extra); e != nil || extra != 0 {
			t.Fatalf("DDL not undone %d %v", extra, e)
		}
		return nil
	}
	s, e := Run(context.Background(), o)
	if e == nil || s.State != RolledBack {
		t.Fatalf("real DB rollback %+v %v", s, e)
	}
}
