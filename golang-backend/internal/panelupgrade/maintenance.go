package panelupgrade

import (
	"os"
	"path/filepath"
)

// Maintenance keeps a restarted candidate from sending node commands before the
// independent worker has committed. PREPARING does not interrupt the old service.
func Maintenance() bool {
	if Container() {
		return false
	}
	return maintenanceAt(filepath.Join(InstallDir, ".upgrades"))
}
func maintenanceAt(root string) bool {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if entry.Name() == "admission.lock" {
			continue
		}
		if !idPattern.MatchString(entry.Name()) {
			return true
		}
		status, err := readStatus(root, entry.Name())
		if err != nil {
			return true
		}
		switch status.State {
		case "QUEUED", "PREPARING", "PREPARED", "COMMITTED", "ROLLED_BACK", "FAILED":
		default:
			return true
		}
	}
	return false
}
