// Package upgrade implements fail-closed native Linux/systemd upgrade transactions.
// Options are trusted operator configuration, never decoded from an HTTP request.
package upgrade

import (
	"context"
	"errors"
	"net/http"
)

type State string

const (
	Queued           State = "QUEUED"
	Preparing        State = "PREPARING"
	Prepared         State = "PREPARED"
	Activating       State = "ACTIVATING"
	Verifying        State = "VERIFYING"
	Committed        State = "COMMITTED"
	RolledBack       State = "ROLLED_BACK"
	Failed           State = "FAILED"
	RollbackFailed   State = "ROLLBACK_FAILED"
	RecoveryRequired State = "RECOVERY_REQUIRED"
)

var (
	ErrBusy             = errors.New("upgrade busy")
	ErrRecoveryRequired = errors.New("upgrade recovery required")
)

type Status struct {
	ID                  string        `json:"id"`
	State               State         `json:"state"`
	Tag                 string        `json:"tag,omitempty"`
	Commit              string        `json:"commit,omitempty"`
	ServerSHA256        string        `json:"server_sha256,omitempty"`
	OldServerSHA256     string        `json:"old_server_sha256,omitempty"`
	OldServerPath       string        `json:"old_server_path,omitempty"`
	Errors              []string      `json:"errors,omitempty"`
	Before              []BeforeImage `json:"before,omitempty"`
	After               []BeforeImage `json:"after,omitempty"`
	DatabaseReady       bool          `json:"database_ready,omitempty"`
	ConfigurationSHA256 string        `json:"configuration_sha256,omitempty"`
}

// BeforeImage contains a fixed logical name, never an arbitrary restore path.
type BeforeImage struct {
	Name   string `json:"name"`
	Absent bool   `json:"absent"`
	SHA256 string `json:"sha256,omitempty"`
	Mode   uint32 `json:"mode"`
}

type Options struct {
	// Compatibility aliases for the worker/controller. Both aliases, if set, must agree.
	InstallDir     string
	DatabasePath   string
	TransactionDir string
	ProxyPrefix    string // Asset bytes only; trusted GitHub release metadata stays direct.
	Tag            string // Optional expected tag; latest is still resolved exactly once.
	// ID is returned by Reserve. Empty ID lets Run reserve while holding the lock.
	ID string
	// Defaults: /opt/network-panel and /usr/local/bin/network-panel-server.
	InstallRoot string
	ServerPath  string
	// Only sqlite is supported. SQLitePath must be a fixed absolute regular file
	// beneath InstallRoot, outside public/easytier and the managed script paths.
	Dialect        string
	SQLitePath     string
	CurrentVersion string
	// HTTPClient may inject a Transport; URL validation cannot be disabled.
	HTTPClient *http.Client
	// Stop must return only after the service and all DB writers have exited.
	// Start must return only after systemd accepted the start; Verify must attest
	// a fresh process, its running executable SHA/version and fresh healthy HTTP.
	Stop   func(context.Context) error
	Start  func(context.Context) error
	Verify func(context.Context, Status) error
	// VerifyRollback verifies the restarted OLD service. If nil, Verify is used
	// with State=ROLLED_BACK, Tag=v+CurrentVersion and ServerSHA256=old SHA.
	VerifyRollback func(context.Context, Status) error
	// Optional stricter resource limits; zero means conservative defaults.
	Limits Limits
}
type Limits struct {
	AssetBytes int64
	TotalBytes int64
	ZipBytes   int64
	ZipEntries int
}

// Public entry points (implemented in this package):
// Reserve(root string) (Status, error)
// Run(ctx context.Context, options Options) (Status, error)
// Recover(ctx context.Context, options Options) (Status, error)
// LoadStatus(root, id string) (Status, error)
// TransactionDir(root, id string) (string, error)
