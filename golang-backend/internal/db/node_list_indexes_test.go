package db

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func nodeListSchemaSnapshot(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var schema []string
	var version int
	if err := db.Raw("PRAGMA schema_version").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	schema = append(schema, fmt.Sprint(version))
	var definitions []string
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type, name").Scan(&definitions).Error; err != nil {
		t.Fatal(err)
	}
	return append(schema, definitions...)
}

func TestNodeListIndexesRejectSameNameMismatchUnchanged(t *testing.T) {
	for _, columns := range []string{"time_ms, node_id, id", "node_id, cmd, id"} {
		t.Run(columns, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			pool, _ := db.DB()
			defer pool.Close()
			for _, statement := range []string{
				"CREATE TABLE node_op_log (id INTEGER PRIMARY KEY, node_id BIGINT, time_ms BIGINT, cmd LONGTEXT, message LONGTEXT)",
				"CREATE TABLE node_anytls_cert_log (id INTEGER PRIMARY KEY, node_id BIGINT, time_ms BIGINT, step LONGTEXT, data LONGTEXT)",
				"INSERT INTO node_op_log VALUES (1, 20, 100, 'cmd', 'untouched')",
				// Put the mismatch last: startup must preflight before adding earlier indexes.
				"CREATE INDEX idx_cert_node_time ON node_anytls_cert_log (" + strings.ReplaceAll(columns, "cmd", "step") + ")",
			} {
				if err := db.Exec(statement).Error; err != nil {
					t.Fatal(err)
				}
			}
			before := nodeListSchemaSnapshot(t, db)
			err = ensureNodeListIndexes(db)
			if err == nil || !strings.Contains(err.Error(), "idx_cert_node_time") {
				t.Fatalf("expected named index mismatch, got %v", err)
			}
			if !reflect.DeepEqual(before, nodeListSchemaSnapshot(t, db)) {
				t.Fatal("schema changed despite mismatched existing index")
			}
			var message string
			if err := db.Raw("SELECT message FROM node_op_log WHERE id=1").Scan(&message).Error; err != nil || message != "untouched" {
				t.Fatalf("row changed: %q, %v", message, err)
			}
		})
	}
}

func TestNodeListIndexesAdditiveIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	for _, sql := range []string{
		"CREATE TABLE node_op_log (id INTEGER PRIMARY KEY, node_id BIGINT, time_ms BIGINT, cmd LONGTEXT, message LONGTEXT)",
		"CREATE TABLE node_anytls_cert_log (id INTEGER PRIMARY KEY, node_id BIGINT, time_ms BIGINT, step LONGTEXT, data LONGTEXT)",
		"INSERT INTO node_op_log VALUES (1, 20, 100, 'OpLog:gost_api', 'untouched')",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		before := nodeListSchemaSnapshot(t, db)
		if err := ensureNodeListIndexes(db); err != nil {
			t.Fatal(err)
		}
		if i > 0 && !reflect.DeepEqual(before, nodeListSchemaSnapshot(t, db)) {
			t.Fatal("valid existing indexes changed on repeat startup")
		}
	}
	for name, want := range map[string][]string{
		"idx_node_op_node_time":     {"node_id", "time_ms", "id"},
		"idx_node_op_cmd_node_time": {"cmd", "node_id", "time_ms", "id"},
		"idx_cert_node_time":        {"node_id", "time_ms", "id"},
		"idx_cert_node_step_time":   {"node_id", "step", "time_ms", "id"},
	} {
		var rows []struct{ Name string }
		if err := db.Raw("PRAGMA index_info(" + name + ")").Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, r := range rows {
			got = append(got, r.Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s columns=%v want=%v", name, got, want)
		}
	}
	var value string
	if err := db.Raw("SELECT message FROM node_op_log WHERE id=1").Scan(&value).Error; err != nil || value != "untouched" {
		t.Fatalf("data changed: %q %v", value, err)
	}
	var columns []struct {
		Name string
		Type string
	}
	if err := db.Raw("PRAGMA table_info(node_op_log)").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	for _, c := range columns {
		if (c.Name == "cmd" || c.Name == "message") && c.Type != "LONGTEXT" {
			t.Fatalf("column type changed: %#v", c)
		}
	}
}
