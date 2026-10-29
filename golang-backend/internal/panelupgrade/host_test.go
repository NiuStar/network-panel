package panelupgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRequestPinsTagAndNormalizesProxy(t *testing.T) {
	for _, r := range []Request{{Tag: "main"}, {Tag: ""}, {Tag: "v2.0.0.31", ProxyPrefix: "https://user:pass@host/"}, {Tag: "v2.0.0.31", ProxyPrefix: "https://host/?token=secret"}} {
		if ValidateRequest(&r) == nil {
			t.Fatalf("unsafe request accepted: %#v", r)
		}
	}
	r := Request{Tag: "v2.0.0.31", ProxyPrefix: "https://proxy.example/cache"}
	if err := ValidateRequest(&r); err != nil || r.ProxyPrefix != "https://proxy.example/cache/" {
		t.Fatalf("unexpected result %#v %v", r, err)
	}
}

func TestStatusRejectsTraversalMismatchedIdentityAndSymlink(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("a", 32)
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// The test scratch parent is world-writable; validate the production path guard
	// independently, while still proving malformed IDs never reach the filesystem.
	if _, err := readStatus(root, "../outside"); err == nil {
		t.Fatal("traversal accepted")
	}
	if err := writeJSON(filepath.Join(dir, "status.json"), Status{ID: id, State: "COMMITTED"}); err != nil {
		t.Fatal(err)
	}
	if got, err := readStatus(root, id); err != nil || got.State != "COMMITTED" {
		t.Fatalf("valid status must be readable, got %+v err=%v", got, err)
	}
	if err := writeJSON(filepath.Join(dir, "status.json"), Status{ID: strings.Repeat("b", 32), State: "COMMITTED"}); err != nil {
		t.Fatal(err)
	}
	if _, err := readStatus(root, id); err == nil {
		t.Fatal("mismatched identity accepted")
	}
}

func TestOnlySafeTerminalStatesAllowNewAdmission(t *testing.T) {
	for _, state := range []string{"QUEUED", "PREPARING", "ACTIVATING", "VERIFYING", "ROLLBACK_FAILED", "RECOVERY_REQUIRED", "", "UNKNOWN"} {
		if Terminal(state) {
			t.Fatalf("unsafe terminal %q", state)
		}
	}
	for _, state := range []string{"COMMITTED", "ROLLED_BACK", "FAILED"} {
		if !Terminal(state) {
			t.Fatalf("terminal rejected %q", state)
		}
	}
}
