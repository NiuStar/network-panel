package upgrade

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func increasingVersion(tag, current string) error {
	parse := func(v string) ([4]uint64, error) {
		var out [4]uint64
		v = "v" + strings.TrimPrefix(v, "v")
		if !tagPattern.MatchString(v) {
			return out, errors.New("invalid version")
		}
		parts := strings.Split(v[1:], ".")
		for i, p := range parts {
			n, e := strconv.ParseUint(p, 10, 64)
			if e != nil {
				return out, e
			}
			out[i] = n
		}
		return out, nil
	}
	next, e := parse(tag)
	if e != nil {
		return e
	}
	old, e := parse(current)
	if e != nil {
		return e
	}
	for i := 0; i < 4; i++ {
		if next[i] > old[i] {
			return nil
		}
		if next[i] < old[i] {
			return errors.New("downgrade forbidden")
		}
	}
	return errors.New("same-version upgrade forbidden")
}
func normalize(o Options) (Options, error) {
	if o.InstallDir != "" {
		if o.InstallRoot != "" && o.InstallDir != o.InstallRoot {
			return o, errors.New("conflicting install roots")
		}
		o.InstallRoot = o.InstallDir
	}
	o.InstallRoot = rootPath(o.InstallRoot)
	o.InstallDir = o.InstallRoot
	if o.DatabasePath != "" {
		if o.SQLitePath != "" && o.SQLitePath != o.DatabasePath {
			return o, errors.New("conflicting database paths")
		}
		o.SQLitePath = o.DatabasePath
	}
	if o.ServerPath == "" {
		o.ServerPath = "/usr/local/bin/network-panel-server"
	}
	if o.TransactionDir != "" {
		id := filepath.Base(o.TransactionDir)
		dir, e := TransactionDir(o.InstallRoot, id)
		if e != nil || dir != o.TransactionDir || (o.ID != "" && o.ID != id) {
			return o, errors.New("invalid transaction directory")
		}
		o.ID = id
	}
	if o.Dialect != "sqlite" {
		return o, errors.New("online upgrade requires sqlite; MySQL has no rollback strategy")
	}
	if o.Stop == nil || o.Start == nil || o.Verify == nil {
		return o, errors.New("Stop, Start and fresh-process Verify callbacks are required")
	}
	if o.CurrentVersion == "" {
		return o, errors.New("current version is required")
	}
	if _, _, e := proxyPrefix(o.ProxyPrefix); e != nil {
		return o, e
	}
	if o.Tag != "" && !tagPattern.MatchString(o.Tag) {
		return o, errors.New("invalid expected tag")
	}
	if !inside(o.InstallRoot, o.SQLitePath) {
		return o, errors.New("database must be a fixed file inside installation root")
	}
	for _, p := range []string{o.InstallRoot, o.ServerPath, o.SQLitePath, o.SQLitePath + "-wal", o.SQLitePath + "-shm", o.SQLitePath + "-journal", filepath.Join(o.InstallRoot, ".upgrades")} {
		if e := noLinks(p); e != nil {
			return o, e
		}
	}
	dbrel, _ := filepath.Rel(o.InstallRoot, o.SQLitePath)
	first := strings.SplitN(dbrel, string(filepath.Separator), 2)[0]
	if first == "public" || first == "easytier" || first == "install.sh" || first == ".upgrades" || first == ".env" || first == "certs" || strings.HasPrefix(first, ".upgrade") {
		return o, errors.New("database overlaps protected/managed paths")
	}
	if inside(o.InstallRoot, o.ServerPath) || o.ServerPath == o.InstallRoot {
		return o, errors.New("server must be outside installation root")
	}
	return o, nil
}
func validateDBFiles(o Options) error {
	for _, p := range []string{o.SQLitePath, o.SQLitePath + "-wal", o.SQLitePath + "-shm", o.SQLitePath + "-journal"} {
		if e := noLinks(p); e != nil {
			return e
		}
		st, e := os.Lstat(p)
		if os.IsNotExist(e) && p != o.SQLitePath {
			continue
		}
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			return errors.New("sqlite database and sidecars must be regular files")
		}
	}
	return nil
}
func archName() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH, nil
	case "arm":
		return "armv7", nil
	}
	return "", errors.New("unsupported native architecture")
}
func binaryVersion(ctx context.Context, p, arch, version string) error {
	f, e := elf.Open(p)
	if e != nil {
		return fmt.Errorf("server is not ELF: %w", e)
	}
	machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64, "armv7": elf.EM_ARM}[arch]
	valid := f.Machine == machine && f.ByteOrder.String() == "LittleEndian" && (f.Type == elf.ET_EXEC || f.Type == elf.ET_DYN)
	f.Close()
	if !valid {
		return errors.New("server ELF architecture mismatch")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "--version-only")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	cmd.Dir = filepath.Dir(p)
	out := &boundedOutput{}
	cmd.Stdout = out
	cmd.Stderr = out
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("server version probe: %w", e)
	}
	if out.exceeded || strings.TrimSpace(string(out.b)) != strings.TrimPrefix(version, "v") {
		return errors.New("server version mismatch")
	}
	return nil
}

type boundedOutput struct {
	b        []byte
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.b)+n > 4096 {
		b.exceeded = true
		return 0, errors.New("version output limit")
	}
	b.b = append(b.b, p...)
	return n, nil
}
func fingerprint(p string) (BeforeImage, error) {
	out := BeforeImage{}
	if e := noLinks(p); e != nil {
		return out, e
	}
	st, e := os.Lstat(p)
	if os.IsNotExist(e) {
		out.Absent = true
		return out, nil
	}
	if e != nil {
		return out, e
	}
	out.Mode = uint32(st.Mode().Perm())
	h := sha256.New()
	if st.Mode().IsRegular() {
		f, e := os.Open(p)
		if e != nil {
			return out, e
		}
		_, e = io.Copy(h, f)
		e = errors.Join(e, f.Close())
		if e != nil {
			return out, e
		}
	} else if st.IsDir() {
		e = filepath.WalkDir(p, func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			info, e := d.Info()
			if e != nil {
				return e
			}
			if !info.Mode().IsRegular() && !info.IsDir() {
				return errors.New("managed tree contains symlink or special file")
			}
			rel, _ := filepath.Rel(p, path)
			size := info.Size()
			if info.IsDir() {
				size = 0
			}
			fmt.Fprintf(h, "%d:%s:%o:%d\n", len(rel), rel, info.Mode().Perm(), size)
			if info.Mode().IsRegular() {
				f, e := os.Open(path)
				if e != nil {
					return e
				}
				_, e = io.Copy(h, f)
				e = errors.Join(e, f.Close())
				return e
			}
			return nil
		})
		if e != nil {
			return out, e
		}
	} else {
		return out, errors.New("managed target is special file")
	}
	out.SHA256 = hex.EncodeToString(h.Sum(nil))
	return out, nil
}
func sameImage(a, b BeforeImage) bool {
	return a.Absent == b.Absent && a.SHA256 == b.SHA256 && a.Mode == b.Mode
}
func configurationSHA(o Options) string {
	h := sha256.Sum256([]byte(o.InstallRoot + "\x00" + o.ServerPath + "\x00" + o.SQLitePath + "\x00" + o.CurrentVersion))
	return hex.EncodeToString(h[:])
}
func targets(o Options) map[string]string {
	return map[string]string{"server": o.ServerPath, "public": filepath.Join(o.InstallRoot, "public"), "install.sh": filepath.Join(o.InstallRoot, "install.sh"), "easytier": filepath.Join(o.InstallRoot, "easytier"), "db": o.SQLitePath, "db-wal": o.SQLitePath + "-wal", "db-shm": o.SQLitePath + "-shm", "db-journal": o.SQLitePath + "-journal"}
}
func sibling(p, id, suffix string) string {
	return filepath.Join(filepath.Dir(p), "."+filepath.Base(p)+".upgrade-"+id+"."+suffix)
}
func copyTree(src, dst string) error {
	st, e := os.Lstat(src)
	if e != nil {
		return e
	}
	if st.Mode().IsRegular() {
		in, e := os.Open(src)
		if e != nil {
			return e
		}
		defer in.Close()
		out, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
		if e != nil {
			return e
		}
		_, e = io.Copy(out, in)
		if e == nil {
			e = out.Chmod(st.Mode().Perm())
		}
		if e == nil {
			e = out.Sync()
		}
		return errors.Join(e, out.Close())
	}
	if !st.IsDir() {
		return errors.New("cannot copy special file")
	}
	if e = os.Mkdir(dst, st.Mode().Perm()); e != nil {
		return e
	}
	entries, e := os.ReadDir(src)
	if e != nil {
		return e
	}
	for _, ent := range entries {
		if e = copyTree(filepath.Join(src, ent.Name()), filepath.Join(dst, ent.Name())); e != nil {
			return e
		}
	}
	if e = os.Chmod(dst, st.Mode().Perm()); e != nil {
		return e
	}
	return syncDir(dst)
}
func setState(o Options, s *Status, state State) error {
	s.State = state
	return persist(o.InstallRoot, *s)
}
func failBefore(o Options, s Status, cause error) (Status, error) {
	s.Errors = append(s.Errors, cause.Error())
	if e := setState(o, &s, Failed); e != nil {
		return s, errors.Join(cause, e)
	}
	return s, cause
}

// queuedFailure publishes validation errors only into an untouched admission record.
// It deliberately cannot overwrite an activation journal or bypass the engine lock.
func queuedFailure(o Options, cause error) (Status, error) {
	root := o.InstallRoot
	if root == "" {
		root = o.InstallDir
	}
	root = rootPath(root)
	id := o.ID
	if o.TransactionDir != "" {
		id = filepath.Base(o.TransactionDir)
		expected, e := TransactionDir(root, id)
		if e != nil || expected != o.TransactionDir {
			return Status{}, cause
		}
	}
	if !idPattern.MatchString(id) {
		return Status{}, cause
	}
	lock, e := lockRoot(root)
	if e != nil {
		return Status{}, errors.Join(cause, e)
	}
	defer unlock(lock)
	s, e := LoadStatus(root, id)
	if e != nil {
		return s, errors.Join(cause, e)
	}
	if (s.State != Queued && s.State != Preparing) || len(s.Before) > 0 || s.Commit != "" {
		return s, errors.Join(cause, ErrRecoveryRequired)
	}
	return failBefore(Options{InstallRoot: root}, s, cause)
}

// Run holds the filesystem lock from admission through commit or rollback.
// Callbacks must not panic; their returned errors are always transaction failures.
func Run(ctx context.Context, options Options) (Status, error) {
	o, e := normalize(options)
	if e != nil {
		return queuedFailure(options, e)
	}
	arch, e := archName()
	if e != nil {
		return queuedFailure(options, e)
	}
	lock, e := lockRoot(o.InstallRoot)
	if e != nil {
		return Status{}, e
	}
	defer unlock(lock)
	var s Status
	if o.ID == "" {
		s, e = reserveLocked(o.InstallRoot)
		o.ID = s.ID
	} else {
		if e = pending(o.InstallRoot, o.ID); e != nil {
			return s, e
		}
		s, e = LoadStatus(o.InstallRoot, o.ID)
		if os.IsNotExist(e) {
			dir, _ := TransactionDir(o.InstallRoot, o.ID)
			if e = noLinks(dir); e == nil {
				var st os.FileInfo
				st, e = os.Stat(dir)
				if e == nil && (!st.IsDir() || st.Mode().Perm() != 0700) {
					e = errors.New("transaction directory must be 0700")
				}
				if e == nil {
					s = Status{ID: o.ID, State: Preparing}
					e = persist(o.InstallRoot, s)
				}
			}
		}
		if e == nil && ((s.State != Preparing && s.State != Queued) || len(s.Before) != 0 || s.Commit != "") {
			return s, ErrRecoveryRequired
		}
		if e == nil && s.State == Queued {
			if s.Tag != o.Tag {
				return failBefore(o, s, errors.New("queued tag does not match requested tag"))
			}
			s.Tag = ""
			e = setState(o, &s, Preparing)
		}
	}
	if e != nil {
		return s, e
	}
	s.ConfigurationSHA256 = configurationSHA(o)
	if e = persist(o.InstallRoot, s); e != nil {
		return s, e
	}
	dir, _ := TransactionDir(o.InstallRoot, o.ID)
	stage := filepath.Join(dir, "stage")
	if e = os.Mkdir(stage, 0700); e != nil {
		return s, fmt.Errorf("%w: existing stage: %v", ErrRecoveryRequired, e)
	}
	if e = validateDBFiles(o); e != nil {
		return failBefore(o, s, e)
	}
	rel, e := stageRelease(ctx, o, stage, arch)
	if e != nil {
		return failBefore(o, s, e)
	}
	s.Tag = rel.Tag
	s.Commit = rel.Commit
	s.ServerSHA256 = rel.ServerSHA256
	s.OldServerPath = o.ServerPath
	if e = binaryVersion(ctx, filepath.Join(stage, "server"), arch, s.Tag); e != nil {
		return failBefore(o, s, e)
	}
	if e = binaryVersion(ctx, o.ServerPath, arch, o.CurrentVersion); e != nil {
		return failBefore(o, s, e)
	}
	paths := targets(o)
	for _, name := range []string{"server", "public", "install.sh", "easytier"} {
		image, e := fingerprint(paths[name])
		if e != nil {
			return failBefore(o, s, e)
		}
		if name == "server" && image.Absent {
			return failBefore(o, s, errors.New("missing current server"))
		}
		image.Name = name
		s.Before = append(s.Before, image)
		if name == "server" {
			s.OldServerSHA256 = image.SHA256
		}
		for _, suffix := range []string{"before", "next", "discard", "restore"} {
			p := sibling(paths[name], s.ID, suffix)
			if _, e = os.Lstat(p); !os.IsNotExist(e) {
				return failBefore(o, s, errors.New("backup/staging path already exists"))
			}
		}
		src := filepath.Join(stage, name)
		if name == "install.sh" || name == "easytier" {
			src = filepath.Join(stage, "scripts", name)
		}
		if e = copyTree(src, sibling(paths[name], s.ID, "next")); e != nil {
			return failBefore(o, s, e)
		}
		next, e := fingerprint(sibling(paths[name], s.ID, "next"))
		if e != nil {
			return failBefore(o, s, e)
		}
		next.Name = name
		s.After = append(s.After, next)
		if e = syncDir(filepath.Dir(paths[name])); e != nil {
			return failBefore(o, s, e)
		}
	}
	if e = setState(o, &s, Prepared); e != nil {
		return s, e
	}
	// Mark possible service effects before invoking Stop. A crash now is never success.
	if e = setState(o, &s, Activating); e != nil {
		return s, e
	}
	if e = o.Stop(ctx); e != nil {
		return rollback(o, s, fmt.Errorf("stop: %w", e))
	}
	// The old service is quiescent. Preserve every SQLite sidecar, including a
	// DELETE-mode hot journal needed to undo uncommitted pages in the main file.
	if e = validateDBFiles(o); e != nil {
		return rollback(o, s, e)
	}
	for _, name := range []string{"db", "db-wal", "db-shm", "db-journal"} {
		p := paths[name]
		image, e := fingerprint(p)
		if e != nil {
			return rollback(o, s, e)
		}
		image.Name = name
		if name == "db" && image.Absent {
			return rollback(o, s, errors.New("sqlite database missing"))
		}
		backup := filepath.Join(dir, name+".before")
		if !image.Absent {
			if e = copyTree(p, backup); e != nil {
				return rollback(o, s, e)
			}
		}
		s.Before = append(s.Before, image)
	}
	if e = syncDir(dir); e != nil {
		return rollback(o, s, e)
	}
	s.DatabaseReady = true
	if e = persist(o.InstallRoot, s); e != nil {
		return rollback(o, s, e)
	}
	for _, expected := range s.After {
		got, e := fingerprint(sibling(paths[expected.Name], s.ID, "next"))
		if e != nil || !sameImage(expected, got) {
			return rollback(o, s, errors.New("prepared target drift: "+expected.Name))
		}
	}
	for _, image := range s.Before {
		if strings.HasPrefix(image.Name, "db") {
			continue
		}
		p := paths[image.Name]
		now, e := fingerprint(p)
		if e != nil || !sameImage(image, now) {
			return rollback(o, s, errors.New("formal target changed after preparation"))
		}
		if !image.Absent {
			if e = os.Rename(p, sibling(p, s.ID, "before")); e != nil {
				return rollback(o, s, e)
			}
			if e = syncDir(filepath.Dir(p)); e != nil {
				return rollback(o, s, e)
			}
		}
		if e = os.Rename(sibling(p, s.ID, "next"), p); e != nil {
			return rollback(o, s, e)
		}
		if e = syncDir(filepath.Dir(p)); e != nil {
			return rollback(o, s, e)
		}
	}
	if e = setState(o, &s, Verifying); e != nil {
		return rollback(o, s, e)
	}
	if e = o.Start(ctx); e != nil {
		return rollback(o, s, fmt.Errorf("start: %w", e))
	}
	if e = o.Verify(ctx, s); e != nil {
		return rollback(o, s, fmt.Errorf("verify: %w", e))
	}
	// Verify the installed pathname too; the callback checks the running executable.
	for _, expected := range s.After {
		got, e := fingerprint(paths[expected.Name])
		if e != nil || !sameImage(expected, got) {
			return rollback(o, s, errors.New("activated target drift: "+expected.Name))
		}
	}
	image, e := fingerprint(o.ServerPath)
	if e != nil || image.SHA256 != s.ServerSHA256 {
		return rollback(o, s, errors.New("activated server SHA changed"))
	}
	if e = setState(o, &s, Committed); e != nil {
		return rollback(o, s, e)
	}
	return s, nil
}
func rollback(o Options, s Status, cause error) (Status, error) {
	s.Errors = append(s.Errors, cause.Error())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// A failed stop must never be followed by writing DB files beneath a live writer.
	if e := o.Stop(ctx); e != nil {
		s.Errors = append(s.Errors, "rollback stop: "+e.Error())
		s.State = RollbackFailed
		return s, errors.Join(cause, e, persist(o.InstallRoot, s))
	}
	if e := restore(o, s); e != nil {
		s.Errors = append(s.Errors, "restore: "+e.Error())
		s.State = RollbackFailed
		return s, errors.Join(cause, e, persist(o.InstallRoot, s))
	}
	if e := o.Start(ctx); e != nil {
		s.Errors = append(s.Errors, "rollback start: "+e.Error())
		s.State = RollbackFailed
		return s, errors.Join(cause, e, persist(o.InstallRoot, s))
	}
	old := s
	old.State = RolledBack
	old.Tag = "v" + strings.TrimPrefix(o.CurrentVersion, "v")
	old.ServerSHA256 = s.OldServerSHA256
	verify := o.VerifyRollback
	if verify == nil {
		verify = o.Verify
	}
	if e := verify(ctx, old); e != nil {
		s.Errors = append(s.Errors, "rollback verify: "+e.Error())
		s.State = RollbackFailed
		return s, errors.Join(cause, e, persist(o.InstallRoot, s))
	}
	s.State = RolledBack
	return s, errors.Join(cause, persist(o.InstallRoot, s))
}
func validateBefore(s Status) error {
	seen := map[string]bool{}
	for _, im := range s.Before {
		switch im.Name {
		case "server", "public", "install.sh", "easytier", "db", "db-wal", "db-shm", "db-journal":
		default:
			return errors.New("unknown before-image name")
		}
		if seen[im.Name] || (!im.Absent && !shaPattern.MatchString(im.SHA256)) || im.Mode > 0777 {
			return errors.New("invalid before image")
		}
		if (im.Name == "server" || im.Name == "db") && im.Absent {
			return errors.New("required before image cannot be absent")
		}
		if im.Absent && (im.Mode != 0 || im.SHA256 != "") {
			return errors.New("invalid absent image")
		}
		if im.Name == "server" && im.SHA256 != s.OldServerSHA256 {
			return errors.New("old server witness mismatch")
		}
		seen[im.Name] = true
	}
	for _, n := range []string{"server", "public", "install.sh", "easytier"} {
		if !seen[n] {
			return errors.New("incomplete formal before images")
		}
	}
	if s.DatabaseReady {
		// Older durable journals omitted db-journal entirely. Missing is NOT an
		// absent-file witness: recovery cannot safely infer its original contents.
		for _, n := range []string{"db", "db-wal", "db-shm", "db-journal"} {
			if !seen[n] {
				return errors.New("incomplete DB before images")
			}
		}
	}
	return nil
}
func restore(o Options, s Status) error {
	if e := validateBefore(s); e != nil {
		return e
	}
	paths := targets(o)
	dir, _ := TransactionDir(o.InstallRoot, s.ID)
	// Validate every source before writing any destination. Journal names never supply paths.
	for _, im := range s.Before {
		if strings.HasPrefix(im.Name, "db") && !s.DatabaseReady {
			continue
		}
		p := paths[im.Name]
		if e := noLinks(p); e != nil {
			return e
		}
		if im.Absent {
			continue
		}
		backup := sibling(p, s.ID, "before")
		if strings.HasPrefix(im.Name, "db") {
			backup = filepath.Join(dir, im.Name+".before")
		}
		got, e := fingerprint(backup)
		if e != nil {
			return e
		}
		if got.Absent && !strings.HasPrefix(im.Name, "db") {
			got, e = fingerprint(p)
			if e != nil {
				return e
			}
		}
		if !sameImage(im, got) {
			return fmt.Errorf("before image unavailable or corrupted: %s", im.Name)
		}
	}
	images := append([]BeforeImage(nil), s.Before...)
	sort.SliceStable(images, func(i, j int) bool { return images[i].Name < images[j].Name })
	for _, im := range images {
		if strings.HasPrefix(im.Name, "db") && !s.DatabaseReady {
			continue
		}
		p := paths[im.Name]
		backup := sibling(p, s.ID, "before")
		db := strings.HasPrefix(im.Name, "db")
		if db {
			backup = filepath.Join(dir, im.Name+".before")
		}
		if !db && !im.Absent {
			got, e := fingerprint(p)
			if e == nil && sameImage(im, got) {
				continue
			}
		}
		// Backups are retained. Restore via a fresh sibling copy, never rename them away.
		temp := sibling(p, s.ID, "restore")
		if _, e := os.Lstat(temp); !os.IsNotExist(e) {
			return errors.New("stale restore temporary requires manual recovery")
		}
		if !im.Absent {
			if e := copyTree(backup, temp); e != nil {
				return e
			}
		}
		if e := noLinks(p); e != nil {
			return e
		}
		if st, e := os.Lstat(p); e == nil && st.IsDir() {
			discard := sibling(p, s.ID, "discard")
			if _, e = os.Lstat(discard); !os.IsNotExist(e) {
				return errors.New("discard already exists")
			}
			if e = os.Rename(p, discard); e != nil {
				return e
			}
		} else if e != nil && !os.IsNotExist(e) {
			return e
		} else if e == nil {
			if e = os.Remove(p); e != nil {
				return e
			}
		}
		if !im.Absent {
			if e := os.Rename(temp, p); e != nil {
				return e
			}
		}
		if e := syncDir(filepath.Dir(p)); e != nil {
			return e
		}
	}
	for _, im := range s.Before {
		if strings.HasPrefix(im.Name, "db") && !s.DatabaseReady {
			continue
		}
		got, e := fingerprint(paths[im.Name])
		if e != nil || !sameImage(im, got) {
			return fmt.Errorf("restored image mismatch: %s", im.Name)
		}
	}
	return nil
}

// Recover is an explicit offline rollback, not an automatic continuation. Only fixed
// targets from trusted Options and validated existing before images may be restored.
func Recover(ctx context.Context, options Options) (Status, error) {
	o, e := normalize(options)
	if e != nil {
		return Status{}, e
	}
	if o.ID == "" {
		return Status{}, errors.New("recovery transaction ID required")
	}
	lock, e := lockRoot(o.InstallRoot)
	if e != nil {
		return Status{}, e
	}
	defer unlock(lock)
	s, e := LoadStatus(o.InstallRoot, o.ID)
	if e != nil {
		return s, e
	}
	if Terminal(s.State) {
		return s, errors.New("transaction already terminal")
	}
	if s.ConfigurationSHA256 != configurationSHA(o) {
		s.Errors = append(s.Errors, "recovery configuration changed")
		s.State = RecoveryRequired
		return s, errors.Join(ErrRecoveryRequired, persist(o.InstallRoot, s))
	}
	if e = validateBefore(s); e != nil {
		s.Errors = append(s.Errors, e.Error())
		s.State = RecoveryRequired
		return s, errors.Join(ErrRecoveryRequired, persist(o.InstallRoot, s))
	}
	// ctx is checked before recovery, but rollback thereafter uses its own cancellation
	// budget so an HTTP caller cannot interrupt restoration after service shutdown.
	if e = ctx.Err(); e != nil {
		return s, e
	}
	return rollback(o, s, ErrRecoveryRequired)
}
