package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReservePersistsAndBlocksPending(t *testing.T) {
	root := t.TempDir()
	s, err := Reserve(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadStatus(root, s.ID)
	if err != nil || got.State != Preparing || got.ID != s.ID {
		t.Fatalf("journal: %+v %v", got, err)
	}
	if _, err = Reserve(root); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("pending accepted: %v", err)
	}
	dir, _ := TransactionDir(root, s.ID)
	st, _ := os.Stat(dir)
	if st.Mode().Perm() != 0700 {
		t.Fatalf("permissions: %v", st.Mode())
	}
	if _, err = LoadStatus(root, "../escape"); err == nil {
		t.Fatal("accepted traversal")
	}
}
func TestReserveRejectsSymlinkAncestor(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "link")
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := Reserve(root); err == nil {
		t.Fatal("symlink accepted")
	}
}
