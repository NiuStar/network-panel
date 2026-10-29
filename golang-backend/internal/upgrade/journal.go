package upgrade

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func rootPath(root string) string {
	if root == "" {
		return "/opt/network-panel"
	}
	return root
}
func TransactionDir(root, id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", errors.New("invalid transaction ID")
	}
	root = rootPath(root)
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("invalid installation root")
	}
	return filepath.Join(root, ".upgrades", id), nil
}
func noLinks(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("noncanonical path: %s", path)
	}
	for p := path; ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if e == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink: %s", p)
		}
		if p == "/" {
			break
		}
	}
	return nil
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func persist(root string, s Status) error {
	dir, e := TransactionDir(root, s.ID)
	if e != nil {
		return e
	}
	if e = noLinks(dir); e != nil {
		return e
	}
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(dir, "status.next"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(e) {
		return errors.New("stale status.next: recovery required")
	}
	if e != nil {
		return e
	}
	p := f.Name()
	defer os.Remove(p)
	_, e = f.Write(append(b, '\n'))
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(p, filepath.Join(dir, "status.json")); e != nil {
		return e
	}
	return syncDir(dir)
}
func LoadStatus(root, id string) (Status, error) {
	var s Status
	dir, e := TransactionDir(root, id)
	if e != nil {
		return s, e
	}
	p := filepath.Join(dir, "status.json")
	st, e := os.Lstat(dir)
	if e != nil {
		return s, e
	}
	if !st.IsDir() || st.Mode().Perm() != 0700 {
		return s, errors.New("transaction directory is not private")
	}
	if e = noLinks(p); e != nil {
		return s, e
	}
	f, e := os.Open(p)
	if e != nil {
		return s, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return s, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return s, errors.New("status must be a private regular file")
	}
	b, e := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if e != nil {
		return s, e
	}
	if len(b) > 1<<20 {
		return s, errors.New("journal too large")
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, e
	}
	if s.ID != id {
		return s, errors.New("journal identity mismatch")
	}
	switch s.State {
	case Queued, Preparing, Prepared, Activating, Verifying, Committed, RolledBack, Failed, RollbackFailed, RecoveryRequired:
	default:
		return s, errors.New("invalid journal state")
	}
	return s, nil
}
func Terminal(s State) bool { return s == Committed || s == RolledBack || s == Failed }
func lockRoot(root string) (*os.File, error) {
	root = rootPath(root)
	if e := noLinks(root); e != nil {
		return nil, e
	}
	st, e := os.Stat(root)
	if e != nil {
		return nil, e
	}
	if !st.IsDir() {
		return nil, errors.New("root is not directory")
	}
	p := filepath.Join(root, ".upgrade.lock")
	if e = noLinks(p); e != nil {
		return nil, e
	}
	fd, e := syscall.Open(p, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), p)
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		if errors.Is(e, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, e
	}
	return f, nil
}
func unlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
func pending(root, except string) error {
	base := filepath.Join(root, ".upgrades")
	if e := noLinks(base); e != nil {
		return e
	}
	entries, e := os.ReadDir(base)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, ent := range entries {
		if ent.Name() == "admission.lock" {
			continue
		}
		if ent.Name() == except {
			continue
		}
		if !idPattern.MatchString(ent.Name()) || !ent.IsDir() {
			return fmt.Errorf("%w: unrecognized transaction entry", ErrRecoveryRequired)
		}
		s, e := LoadStatus(root, ent.Name())
		if e != nil || !Terminal(s.State) {
			return fmt.Errorf("%w: %s", ErrRecoveryRequired, ent.Name())
		}
	}
	return nil
}
func reserveLocked(root string) (Status, error) {
	var s Status
	if e := pending(root, ""); e != nil {
		return s, e
	}
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return s, e
	}
	s = Status{ID: hex.EncodeToString(b), State: Preparing}
	base := filepath.Join(root, ".upgrades")
	if e := os.MkdirAll(base, 0700); e != nil {
		return s, e
	}
	if e := noLinks(base); e != nil {
		return s, e
	}
	dir, _ := TransactionDir(root, s.ID)
	if e := os.Mkdir(dir, 0700); e != nil {
		return s, e
	}
	if e := syncDir(base); e != nil {
		return s, e
	}
	return s, persist(root, s)
}
func Reserve(root string) (Status, error) {
	root = rootPath(root)
	f, e := lockRoot(root)
	if e != nil {
		return Status{}, e
	}
	defer unlock(f)
	return reserveLocked(root)
}
func inside(root, path string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
