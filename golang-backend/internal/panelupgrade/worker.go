package panelupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"network-panel/golang-backend/internal/upgrade"
)

func boundedJSON(path string, v any) error {
	if err := SafePath(path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("worker input exceeds size limit")
	}
	return json.Unmarshal(b, v)
}

// Worker consumes only a private, controller-reserved directory and fixed service paths.
func Worker(ctx context.Context, dir, currentVersion string) error {
	id := filepath.Base(dir)
	if !idPattern.MatchString(id) || dir != filepath.Join(InstallDir, ".upgrades", id) {
		return errors.New("invalid worker transaction path")
	}
	if Container() || os.Geteuid() != 0 {
		return errors.New("worker requires native root systemd installation")
	}
	if err := SafePath(dir); err != nil {
		return err
	}
	var request Request
	if err := boundedJSON(filepath.Join(dir, "request.json"), &request); err != nil {
		return err
	}
	if err := ValidateRequest(&request); err != nil {
		return err
	}
	// Secrets stay in a private file, never systemd command arguments or logs.
	var environment []string
	if err := boundedJSON(filepath.Join(dir, "environment.json"), &environment); err != nil {
		return err
	}
	os.Clearenv() // Restore the captured environment without evaluating shell syntax.
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" || strings.ContainsAny(key, "\x00=") {
			return errors.New("invalid saved environment")
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	if os.Getenv("DB_DIALECT") != "sqlite" {
		return errors.New("DATABASE_ROLLBACK_REQUIRED")
	}
	if err := os.Chdir(InstallDir); err != nil {
		return err
	}
	cwd, err := serviceProperty(ctx, "WorkingDirectory")
	if err != nil || cwd != InstallDir {
		return errors.New("service working directory changed")
	}
	cg, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || !strings.Contains(string(cg), "network-panel-upgrade-"+id+".service") {
		return errors.New("worker must run in its independent systemd unit")
	}
	dbPath := os.Getenv("DB_SQLITE_PATH")
	if dbPath == "" {
		dbPath = "./flux.db"
	}
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(InstallDir, dbPath)
	}
	dbPath = filepath.Clean(dbPath)
	port := os.Getenv("PORT")
	if port == "" {
		port = "6365"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("invalid configured panel port")
	}
	oldPID, err := servicePID(ctx)
	if err != nil || oldPID <= 0 {
		return errors.New("current panel MainPID is unavailable")
	}
	configuration, err := configurationFingerprint()
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "expected-runtime.json"), expectedRuntime{DatabasePath: dbPath, Dialect: "sqlite", Port: port, Configuration: configuration}); err != nil {
		return err
	}
	service := nativeService{port: port, lastPID: oldPID, transactionDir: dir, databasePath: dbPath, configuration: configuration}
	options := upgrade.Options{InstallDir: InstallDir, ServerPath: ServerPath, DatabasePath: dbPath, TransactionDir: dir, Dialect: "sqlite", CurrentVersion: currentVersion, Tag: request.Tag, ProxyPrefix: request.ProxyPrefix,
		Stop: service.stop, Start: service.start, Verify: service.verify}
	_, err = upgrade.Run(ctx, options)
	return err
}

func MarkWorkerFailure(dir string, cause error) error {
	id := filepath.Base(dir)
	if !idPattern.MatchString(id) || dir != filepath.Join(InstallDir, ".upgrades", id) {
		return errors.New("invalid failure transaction")
	}
	var status Status
	if err := boundedJSON(filepath.Join(dir, "status.json"), &status); err != nil {
		return err
	}
	// Once the engine has touched service state, never replace its rollback journal.
	if status.State != "QUEUED" {
		return cause
	}
	status.State = "FAILED"
	status.Error = cause.Error()
	return writeJSON(filepath.Join(dir, "status.json"), status)
}

type nativeService struct {
	port           string
	lastPID        int
	transactionDir string
	databasePath   string
	configuration  string
}

func serviceProperty(ctx context.Context, name string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b, err := exec.CommandContext(c, "systemctl", "show", "--property="+name, "--value", Service).Output()
	return strings.TrimSpace(string(b)), err
}
func servicePID(ctx context.Context) (int, error) {
	s, err := serviceProperty(ctx, "MainPID")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}
func serviceCommand(ctx context.Context, command string) error {
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := exec.CommandContext(c, "systemctl", command, Service).Run(); err != nil {
		return fmt.Errorf("systemctl %s failed: %w", command, err)
	}
	return nil
}
func (s *nativeService) stop(ctx context.Context) error {
	pid, err := servicePID(ctx)
	if err != nil {
		return err
	}
	if pid > 0 {
		s.lastPID = pid
	}
	if err := serviceCommand(ctx, "stop"); err != nil {
		return err
	}
	state, err := serviceProperty(ctx, "ActiveState")
	if err != nil {
		return err
	}
	pid, err = servicePID(ctx)
	if err != nil || pid != 0 || (state != "inactive" && state != "failed") {
		return errors.New("panel has not stopped completely")
	}
	// Default systemd KillMode=control-group is required, since SQLite may have WAL writers.
	mode, err := serviceProperty(ctx, "KillMode")
	if err != nil || mode != "control-group" {
		return errors.New("service KillMode must be control-group")
	}
	return nil
}
func (s *nativeService) start(ctx context.Context) error {
	current, err := configurationFingerprint()
	if err != nil || current != s.configuration {
		return errors.New("service/environment configuration changed during upgrade")
	}
	return serviceCommand(ctx, "start")
}
func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *nativeService) verify(ctx context.Context, status upgrade.Status) error {
	verifyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	expectedVersion := strings.TrimPrefix(status.Tag, "v")
	var last error
	for {
		if err := verifyCtx.Err(); err != nil {
			return fmt.Errorf("new process readiness not confirmed: %v: %w", last, err)
		}
		pid, err := servicePID(verifyCtx)
		if err == nil && pid > 0 && pid != s.lastPID && ownsPort(pid, s.port) {
			digest, e := fileSHA(fmt.Sprintf("/proc/%d/exe", pid))
			if e == nil && digest == status.ServerSHA256 && s.receiptMatches(pid, status) {
				address := "http://127.0.0.1:" + s.port
				health, e := readHTTP(verifyCtx, client, address+"/health", 4096)
				if e == nil && string(health) == "ok" {
					body, e := readHTTP(verifyCtx, client, address+"/api/v1/version", 16384)
					var version struct {
						Code int `json:"code"`
						Data struct {
							Server    string `json:"server"`
							ProcessID int    `json:"processId"`
						} `json:"data"`
					}
					if e == nil && json.Unmarshal(body, &version) == nil && version.Code == 0 && version.Data.Server == expectedVersion && version.Data.ProcessID == pid {
						after, e := servicePID(verifyCtx)
						if e == nil && after == pid {
							digest, e = fileSHA(fmt.Sprintf("/proc/%d/exe", pid))
							if e == nil && digest == status.ServerSHA256 {
								return nil
							}
						}
					}
					last = errors.New("version/process changed during readiness verification")
				} else {
					last = e
				}
			} else {
				last = errors.New("running executable SHA mismatch")
			}
		} else {
			last = errors.New("fresh MainPID not available")
		}
		select {
		case <-verifyCtx.Done():
			return fmt.Errorf("readiness timeout: %v", last)
		case <-time.After(500 * time.Millisecond):
		}
	}
}
func (s *nativeService) receiptMatches(pid int, status upgrade.Status) bool {
	var receipt runtimeReceipt
	if boundedJSON(filepath.Join(s.transactionDir, "runtime-ready.json"), &receipt) != nil {
		return false
	}
	current, err := configurationFingerprint()
	return err == nil && current == s.configuration && receipt.PID == pid && receipt.Version == strings.TrimPrefix(status.Tag, "v") && receipt.ExecutableSHA == status.ServerSHA256 && receipt.DatabasePath == s.databasePath
}

func readHTTP(ctx context.Context, client *http.Client, url string, max int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("health HTTP status not 200")
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, max+1))
	if int64(len(b)) > max {
		return nil, errors.New("health response exceeds limit")
	}
	return b, err
}
