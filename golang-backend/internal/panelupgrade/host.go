package panelupgrade

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const InstallDir = "/opt/network-panel"
const ServerPath = "/usr/local/bin/network-panel-server"
const Service = "network-panel.service"

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var tagPattern = regexp.MustCompile(`^v[0-9]+(\.[0-9]+){2,3}$`)
var ErrBusy = errors.New("UPGRADE_BUSY: 已有未完成事务，请先核对其状态")

type Request struct {
	ProxyPrefix string `json:"proxyPrefix"`
	Tag         string `json:"tag"`
}

type Status struct {
	ID        string   `json:"id"`
	State     string   `json:"state"`
	Tag       string   `json:"tag,omitempty"`
	Commit    string   `json:"commit,omitempty"`
	Error     string   `json:"error,omitempty"`
	Errors    []string `json:"errors,omitempty"`
	UpdatedAt string   `json:"updatedAt,omitempty"`
}

func validState(state string) bool {
	switch state {
	case "QUEUED", "PREPARING", "PREPARED", "ACTIVATING", "VERIFYING", "COMMITTED", "ROLLED_BACK", "FAILED", "ROLLBACK_FAILED", "RECOVERY_REQUIRED":
		return true
	}
	return false
}

func Terminal(state string) bool {
	switch state {
	case "COMMITTED", "FAILED", "ROLLED_BACK":
		return true
	}
	return false
}

func Container() bool {
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, e := os.Stat(p); e == nil {
			return true
		}
	}
	if os.Getenv("container") != "" {
		return true
	}
	b, _ := os.ReadFile("/proc/1/cgroup")
	s := strings.ToLower(string(b))
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "kubepods")
}

func ValidateRequest(r *Request) error {
	if !tagPattern.MatchString(r.Tag) {
		return errors.New("必须提供固定的发布标签，如 v2.0.0.31")
	}
	if r.ProxyPrefix == "" {
		return nil
	}
	u, err := url.Parse(r.ProxyPrefix)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(r.ProxyPrefix, "\r\n\t ") {
		return errors.New("proxyPrefix 必须是不含凭据、空白、查询或片段的 HTTP(S) URL 前缀")
	}
	r.ProxyPrefix = strings.TrimRight(r.ProxyPrefix, "/") + "/"
	return nil
}

func SafePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("需要规范化绝对路径")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current = filepath.Join(current, part)
		fi, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝符号链接路径: %s", current)
		}
		if stat, ok := fi.Sys().(*syscall.Stat_t); !ok || (os.Geteuid() == 0 && stat.Uid != 0) {
			return fmt.Errorf("拒绝非 root 所有者路径: %s", current)
		}
		if fi.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("拒绝非所有者可写路径: %s", current)
		}
	}
	return nil
}

// Supported checks the actual service owner, not merely systemctl availability.
func Supported(ctx context.Context) error {
	if Container() {
		return errors.New("IMAGE_UPGRADE_REQUIRED: Docker 部署必须通过镜像升级，禁止在线覆盖容器内程序")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("在线事务升级仅支持 root 管理的 Linux systemd 安装")
	}
	if os.Getenv("DB_DIALECT") != "sqlite" {
		return errors.New("DATABASE_ROLLBACK_REQUIRED: MySQL 尚无自动数据库回滚链，请使用外部备份升级流程")
	}
	cwd, err := os.Getwd()
	if err != nil || cwd != InstallDir {
		return errors.New("在线事务升级仅支持 /opt/network-panel 标准安装目录")
	}
	exe, err := os.Executable()
	if err != nil || exe != ServerPath {
		return errors.New("在线事务升级仅支持 /usr/local/bin/network-panel-server 标准程序")
	}
	for _, p := range []string{InstallDir, ServerPath} {
		if err := SafePath(p); err != nil {
			return err
		}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(checkCtx, "systemctl", "show", "--property=MainPID", "--value", Service).Output()
	if err != nil || strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		return errors.New("当前进程不是 network-panel.service 的 MainPID，拒绝在线升级")
	}
	for property, expected := range map[string]string{"WorkingDirectory": InstallDir, "KillMode": "control-group"} {
		value, err := serviceProperty(ctx, property)
		if err != nil || value != expected {
			return fmt.Errorf("service %s 不符合事务升级要求", property)
		}
	}
	return nil
}

func readStatus(root, id string) (Status, error) {
	var s Status
	if !idPattern.MatchString(id) {
		return s, errors.New("无效事务 ID")
	}
	path := filepath.Join(root, id, "status.json")
	if err := SafePath(path); err != nil {
		return s, err
	}
	f, err := os.Open(path)
	if err != nil {
		return s, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return s, err
	}
	if len(data) > 64<<10 {
		return s, errors.New("事务记录过大")
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	if s.ID != id || !validState(s.State) {
		return s, errors.New("事务记录身份或状态不一致")
	}
	return s, nil
}
func LoadStatus(id string) (Status, error) {
	return readStatus(filepath.Join(InstallDir, ".upgrades"), id)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func createPrivateDir(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return SafePath(path)
}

// Start admits one task and launches a transient unit outside the panel cgroup.
func Start(ctx context.Context, r Request) (Status, error) {
	var zero Status
	if err := ValidateRequest(&r); err != nil {
		return zero, err
	}
	if err := Supported(ctx); err != nil {
		return zero, err
	}
	onDisk, err := fileSHA(ServerPath)
	if err != nil {
		return zero, err
	}
	running, err := fileSHA("/proc/self/exe")
	if err != nil || onDisk != running {
		return zero, errors.New("运行程序与安装路径字节不一致，拒绝升级")
	}

	root := filepath.Join(InstallDir, ".upgrades")
	if err := createPrivateDir(root); err != nil {
		return zero, err
	}
	lockPath := filepath.Join(root, "admission.lock")
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return zero, err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	defer lock.Close()
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return zero, ErrBusy
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	entries, err := os.ReadDir(root)
	if err != nil {
		return zero, err
	}
	for _, entry := range entries {
		if entry.Name() == "admission.lock" {
			continue
		}
		if !idPattern.MatchString(entry.Name()) || !entry.IsDir() {
			return zero, ErrBusy
		}
		s, err := readStatus(root, entry.Name())
		if err != nil || !Terminal(s.State) {
			return zero, ErrBusy
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return zero, err
	}
	id := hex.EncodeToString(random[:])
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		return zero, err
	}
	s := Status{ID: id, State: "QUEUED", Tag: r.Tag, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	fail := func(err error) (Status, error) {
		s.State = "FAILED"
		s.Error = err.Error()
		if e := writeJSON(filepath.Join(dir, "status.json"), s); e != nil {
			return s, fmt.Errorf("%v; persist failure: %w", err, e)
		}
		return s, err
	}
	if err := writeJSON(filepath.Join(dir, "status.json"), s); err != nil {
		return zero, err
	}
	if err := writeJSON(filepath.Join(dir, "request.json"), r); err != nil {
		return fail(err)
	}
	if err := writeJSON(filepath.Join(dir, "environment.json"), os.Environ()); err != nil {
		return fail(err)
	}
	// Pin the already-running bytes, not a potentially replaced on-disk pathname.
	in, err := os.Open("/proc/self/exe")
	if err != nil {
		return fail(err)
	}
	defer in.Close()
	worker := filepath.Join(dir, "worker")
	out, err := os.OpenFile(worker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return fail(err)
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fail(err)
	}
	launchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := []string{"--unit=network-panel-upgrade-" + id, "--property=Type=exec", "--property=WorkingDirectory=" + InstallDir, "--property=UMask=0077", worker, "--upgrade-worker", dir}
	if err := exec.CommandContext(launchCtx, "systemd-run", args...).Run(); err != nil {
		// Dispatch timeout/error is ambiguous: the unit may already exist. Never mark
		// this reservation FAILED or allow another task to race a possibly-live worker.
		// Do not overwrite status.json: the worker may already have advanced it.
		// A private dispatch receipt plus the QUEUED reservation prevents blind retry.
		s.State = "RECOVERY_REQUIRED"
		s.Error = "升级进程启动结果未确认，请核对事务与对应 systemd unit"
		if persistErr := writeJSON(filepath.Join(dir, "dispatch-uncertain.json"), s); persistErr != nil {
			return s, persistErr
		}
		return s, errors.New(s.Error)
	}
	return s, nil
}
