package panelupgrade

import (
	"errors"
	"os"
	"path/filepath"
)

type expectedRuntime struct {
	DatabasePath  string `json:"databasePath"`
	Dialect       string `json:"dialect"`
	Port          string `json:"port"`
	Configuration string `json:"configuration"`
}

func configuredRuntime() expectedRuntime {
	port := os.Getenv("PORT")
	if port == "" {
		port = "6365"
	}
	return expectedRuntime{DatabasePath: actualDatabasePath(os.Getenv("DB_SQLITE_PATH")), Dialect: os.Getenv("DB_DIALECT"), Port: port}
}

// CheckStartup runs BEFORE database initialization. A new process may not migrate
// any database except the exact SQLite file protected by the worker's before image.
func CheckStartup() error {
	if Container() {
		return nil
	}
	return checkStartupAt(filepath.Join(InstallDir, ".upgrades"), configuredRuntime(), configurationFingerprint)
}
func checkStartupAt(root string, actual expectedRuntime, fingerprint func() (string, error)) error {
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
			return errors.New("unknown upgrade state blocks database startup")
		}
		status, err := readStatus(root, entry.Name())
		if err != nil {
			return err
		}
		switch status.State {
		case "QUEUED", "PREPARING", "PREPARED", "COMMITTED", "ROLLED_BACK", "FAILED":
			continue
		case "ACTIVATING", "VERIFYING":
		default:
			return errors.New("upgrade recovery must be reconciled before database startup")
		}
		var expected expectedRuntime
		if err := boundedJSON(filepath.Join(root, entry.Name(), "expected-runtime.json"), &expected); err != nil {
			return err
		}
		if expected.DatabasePath != actual.DatabasePath || expected.Dialect != "sqlite" || actual.Dialect != expected.Dialect || expected.Port != actual.Port {
			return errors.New("effective runtime/database configuration differs from transaction")
		}
		digest, err := fingerprint()
		if err != nil {
			return err
		}
		if digest != expected.Configuration {
			return errors.New("service configuration differs from transaction")
		}
	}
	return nil
}
