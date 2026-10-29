package panelupgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaintenanceFailClosedStateMatrix(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("c", 32)
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		state   string
		blocked bool
	}{{"QUEUED", false}, {"PREPARING", false}, {"PREPARED", false}, {"ACTIVATING", true}, {"VERIFYING", true}, {"COMMITTED", false}, {"ROLLED_BACK", false}, {"FAILED", false}, {"ROLLBACK_FAILED", true}, {"RECOVERY_REQUIRED", true}, {"UNKNOWN", true}} {
		if err := writeJSON(filepath.Join(dir, "status.json"), Status{ID: id, State: tc.state}); err != nil {
			t.Fatal(err)
		}
		if got := maintenanceAt(root); got != tc.blocked {
			t.Fatalf("state %s blocked=%v want %v", tc.state, got, tc.blocked)
		}
	}
}
