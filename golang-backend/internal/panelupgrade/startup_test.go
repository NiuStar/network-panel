package panelupgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupRejectsOtherDatabaseBeforeMigration(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("d", 32)
	dir := filepath.Join(root, id)
	os.Mkdir(dir, 0700)
	status := Status{ID: id, State: "VERIFYING"}
	writeJSON(filepath.Join(dir, "status.json"), status)
	expected := expectedRuntime{DatabasePath: "/opt/network-panel/panel.db", Dialect: "sqlite", Port: "6365", Configuration: "frozen"}
	writeJSON(filepath.Join(dir, "expected-runtime.json"), expected)
	fingerprint := func() (string, error) { return "frozen", nil }
	if err := checkStartupAt(root, expected, fingerprint); err != nil {
		t.Fatal(err)
	}
	for _, actual := range []expectedRuntime{{DatabasePath: "/other.db", Dialect: "sqlite", Port: "6365"}, {DatabasePath: expected.DatabasePath, Dialect: "mysql", Port: "6365"}, {DatabasePath: expected.DatabasePath, Dialect: "sqlite", Port: "7373"}} {
		if err := checkStartupAt(root, actual, fingerprint); err == nil {
			t.Fatalf("accepted different effective runtime %+v", actual)
		}
	}
	if err := checkStartupAt(root, expected, func() (string, error) { return "changed", nil }); err == nil {
		t.Fatal("configuration drift accepted")
	}
	status.State = "RECOVERY_REQUIRED"
	writeJSON(filepath.Join(dir, "status.json"), status)
	if err := checkStartupAt(root, expected, fingerprint); err == nil {
		t.Fatal("recovery-required allowed to initialize DB")
	}
}
func TestStartupReadinessUsesActualSQLitePath(t *testing.T) {
	if actualDatabasePath("./data/panel.db") != "/opt/network-panel/data/panel.db" {
		t.Fatal("relative DB resolution mismatch")
	}
}
