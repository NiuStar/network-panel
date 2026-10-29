package panelupgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type runtimeReceipt struct {
	PID           int    `json:"pid"`
	Version       string `json:"version"`
	ExecutableSHA string `json:"executableSHA"`
	DatabasePath  string `json:"databasePath"`
}

// RecordReadiness is called only after DB initialization, with the actual SQLite
// connection's PRAGMA database_list pathname, not a configured guess.
func RecordReadiness(databasePath, version string) error {
	if Container() {
		return nil
	}
	root := filepath.Join(InstallDir, ".upgrades")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "admission.lock" {
			continue
		}
		if !idPattern.MatchString(entry.Name()) {
			return errors.New("unrecognized upgrade transaction")
		}
		status, err := readStatus(root, entry.Name())
		if err != nil {
			return err
		}
		if status.State != "VERIFYING" && status.State != "ACTIVATING" {
			continue
		}
		exeSHA, err := fileSHA("/proc/self/exe")
		if err != nil {
			return err
		}
		if !filepath.IsAbs(databasePath) {
			return errors.New("actual sqlite database path is not absolute")
		}
		receipt := runtimeReceipt{PID: os.Getpid(), Version: version, ExecutableSHA: exeSHA, DatabasePath: filepath.Clean(databasePath)}
		if err := writeJSON(filepath.Join(root, entry.Name(), "runtime-ready.json"), receipt); err != nil {
			return err
		}
	}
	return nil
}

func configurationFingerprint() (string, error) {
	h := sha256.New()
	for _, p := range []string{"/etc/systemd/system/network-panel.service", "/etc/default/network-panel", filepath.Join(InstallDir, ".env")} {
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			h.Write([]byte(p + "=ABSENT\n"))
			continue
		}
		if err != nil {
			return "", err
		}
		if err := SafePath(p); err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("service config must be regular file")
		}
		digest, err := fileSHA(p)
		if err != nil {
			return "", err
		}
		h.Write([]byte(p + "=" + digest + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func actualDatabasePath(configured string) string {
	if configured == "" {
		configured = "./flux.db"
	}
	if !filepath.IsAbs(configured) {
		configured = filepath.Join(InstallDir, configured)
	}
	return filepath.Clean(strings.TrimSpace(configured))
}
