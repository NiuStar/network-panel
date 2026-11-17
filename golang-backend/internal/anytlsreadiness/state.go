// Package anytlsreadiness retains bounded runtime evidence independently of
// rotating audit history. Only the authenticated WS receiver may call Persist.
package anytlsreadiness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"network-panel/golang-backend/internal/app/model"
)

// A failed invalidation cannot be represented by a successful DB transaction.
// Latch reads closed in this controller until an authenticated boot is stored;
// closing the WS on failure forces precisely that reconnect/recovery sequence.
// Keys are actual pools + authenticated node IDs, never payload identities.
var blocked sync.Map
var localMu sync.RWMutex

type blockedKey struct {
	pool   gorm.ConnPool
	nodeID int64
}

func newer(a, b model.AnyTLSRuntimeEvent) bool {
	return a.TimeMs > b.TimeMs || (a.TimeMs == b.TimeMs && a.ID > b.ID)
}

func apply(s *model.AnyTLSRuntimeState, rec model.NodeAnyTLSCertLog) {
	var data struct {
		ProcessID   string `json:"processId"`
		Context     string `json:"context"`
		MatchLatest bool   `json:"matchLatest"`
		Installed   struct {
			Fingerprint string `json:"fingerprintSha256"`
		} `json:"installed"`
	}
	valid := len(rec.Data) <= 65536 && json.Unmarshal([]byte(rec.Data), &data) == nil
	e := model.AnyTLSRuntimeEvent{TimeMs: rec.TimeMs, ID: rec.ID}
	if valid && len(data.ProcessID) > 0 && len(data.ProcessID) <= 256 {
		hash := sha256.Sum256([]byte(data.ProcessID))
		e.ProcessHash = hex.EncodeToString(hash[:])
	}
	switch rec.Step {
	case "agent_boot":
		if newer(e, s.Boot) {
			s.Boot = e
		}
	case "installed_check":
		if newer(e, s.Installed) {
			s.Installed = e
			s.Fingerprint = ""
			pin, err := hex.DecodeString(data.Installed.Fingerprint)
			if valid && err == nil && len(pin) == 32 {
				s.Fingerprint = strings.ToLower(data.Installed.Fingerprint)
			}
			s.Confirmed = valid && data.Context == "post_reload" && data.MatchLatest && s.Fingerprint != ""
		}
	case "reload_err":
		if newer(e, s.Failure) {
			s.Failure = e
		}
	}
}

// Ready applies the existing runtime gates; certificate/CA/domain validation
// remains the caller's responsibility. now is explicit for boundary tests.
func Ready(s model.AnyTLSRuntimeState, pin string, now time.Time) bool {
	return s.NodeID > 0 && s.Boot.ID > 0 && s.Installed.ID > 0 && s.Confirmed &&
		s.Boot.ProcessHash != "" && s.Boot.ProcessHash == s.Installed.ProcessHash &&
		newer(s.Installed, s.Boot) && !newer(s.Failure, s.Installed) &&
		s.Installed.TimeMs <= now.UnixMilli()+5000 &&
		s.Installed.TimeMs >= now.Add(-2*time.Minute).UnixMilli() &&
		len(pin) == 64 && strings.EqualFold(s.Fingerprint, pin)
}

// loadLocked creates/locks the single per-node row before reading evidence.
// The no-op UPDATE takes a real SQLite write lock and an InnoDB row lock;
// unlike SELECT FOR UPDATE it works on both supported database dialects.
func loadLocked(tx *gorm.DB, nodeID int64) (model.AnyTLSRuntimeState, error) {
	s := model.AnyTLSRuntimeState{NodeID: nodeID}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&s).Error; err != nil {
		return s, err
	}
	if err := tx.Model(&model.AnyTLSRuntimeState{}).Where("node_id = ?", nodeID).UpdateColumn("node_id", nodeID).Error; err != nil {
		return s, err
	}
	err := tx.Where("node_id = ?", nodeID).First(&s).Error
	return s, err
}

// retained merges at most the latest event of each kind. Missing/malformed boot
// stays missing/invalid. All reads propagate errors, including negative queries.
func retained(tx *gorm.DB, s *model.AnyTLSRuntimeState) error {
	for _, step := range []string{"agent_boot", "installed_check", "reload_err"} {
		var rec model.NodeAnyTLSCertLog
		err := tx.Where("node_id = ? AND step = ?", s.NodeID, step).Order("time_ms desc, id desc").First(&rec).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		apply(s, rec)
	}
	return nil
}

// Persist atomically appends a sanitized audit record and advances durable
// state. nodeID must come from the server-authenticated WS, not its payload.
// TimeMs is server receive time. IDs are always assigned by the audit database.
func Persist(db *gorm.DB, nodeID int64, rec model.NodeAnyTLSCertLog) error {
	if db == nil || nodeID <= 0 {
		return fmt.Errorf("AnyTLS runtime database/identity unavailable")
	}
	localMu.Lock()
	defer localMu.Unlock()
	key := blockedKey{db.ConnPool, nodeID}
	rec.NodeID = nodeID
	rec.ID = 0
	rec.Step = strings.TrimSpace(rec.Step)
	// Retain no arbitrary agent data or message in this security path.
	rec = sanitize(rec)
	for attempt := 0; attempt < 5; attempt++ {
		recovered := false
		err := db.Transaction(func(tx *gorm.DB) error {
			s, err := loadLocked(tx, nodeID)
			if err != nil {
				return err
			}
			// Cold migration and live ingestion share the same row lock. Never select
			// old history after unlocking, or overwrite an already durable witness.
			if s.Boot.ID == 0 && s.Installed.ID == 0 && s.Failure.ID == 0 {
				if err := retained(tx, &s); err != nil {
					return err
				}
			}
			row := rec
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			apply(&s, row)
			recovered = row.Step == "agent_boot" && s.Boot.ID == row.ID && s.Boot.TimeMs == row.TimeMs
			return tx.Save(&s).Error
		})
		if err == nil {
			if recovered {
				blocked.Delete(key)
			}
			return nil
		}
		// SQLite lock contention and InnoDB deadlocks are safe to retry only as
		// whole transactions. All other errors fail closed without partial audit.
		text := strings.ToLower(err.Error())
		if attempt == 4 || !(strings.Contains(text, "database is locked") || strings.Contains(text, "database table is locked") || strings.Contains(text, "deadlock") || strings.Contains(text, "lock wait timeout")) {
			blocked.Store(key, true)
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return fmt.Errorf("AnyTLS runtime write unavailable")
}

// Load uses durable state once present. During a cold upgrade, a read-only,
// bounded projection of the real retained chain preserves compatibility; it
// never creates a boot or overwrites newer durable state. A missing state
// table is an error, not permission to fall back. First live Persist imports
// retained evidence under its write lock before atomically adding the event.
func Load(db *gorm.DB, nodeID int64) (model.AnyTLSRuntimeState, error) {
	s := model.AnyTLSRuntimeState{NodeID: nodeID}
	if db == nil || nodeID <= 0 {
		return s, fmt.Errorf("AnyTLS runtime database/identity unavailable")
	}
	localMu.RLock()
	defer localMu.RUnlock()
	if _, denied := blocked.Load(blockedKey{db.ConnPool, nodeID}); denied {
		return s, fmt.Errorf("AnyTLS runtime requires authenticated reconnect after write failure")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		err := tx.Where("node_id = ?", nodeID).First(&s).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := retained(tx, &s); err != nil {
				return err
			}
			// A concurrent first live write may have created durable state after
			// the initial miss under READ COMMITTED. Prefer that state rather
			// than publishing a stale legacy projection. Nothing is written here.
			var live model.AnyTLSRuntimeState
			err = tx.Where("node_id = ?", nodeID).First(&live).Error
			if err == nil {
				s = live
				return nil
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		return err
	})
	return s, err
}

// certReport is an allowlist, not a copy of arbitrary agent JSON. The bounded
// metadata remains useful to certificate status UI without retaining secrets.
type certReport struct {
	ProcessID   string `json:"processId,omitempty"`
	Context     string `json:"context,omitempty"`
	MatchLatest bool   `json:"matchLatest"`
	Domain      string `json:"domain,omitempty"`
	NotBeforeMs int64  `json:"notBeforeMs,omitempty"`
	NotAfterMs  int64  `json:"notAfterMs,omitempty"`
	Installed   struct {
		Fingerprint string   `json:"fingerprintSha256,omitempty"`
		DNSNames    []string `json:"dnsNames,omitempty"`
		NotBeforeMs int64    `json:"notBeforeMs,omitempty"`
		NotAfterMs  int64    `json:"notAfterMs,omitempty"`
	} `json:"installed"`
}

func sanitize(rec model.NodeAnyTLSCertLog) model.NodeAnyTLSCertLog {
	var data certReport
	if len(rec.Data) > 65536 || json.Unmarshal([]byte(rec.Data), &data) != nil {
		data = certReport{}
	}
	if len(data.ProcessID) > 256 {
		data.ProcessID = ""
	}
	if data.Context != "post_reload" {
		data.Context = ""
	}
	if pin, err := hex.DecodeString(data.Installed.Fingerprint); err != nil || len(pin) != 32 {
		data.Installed.Fingerprint = ""
	}
	if len(data.Domain) > 253 {
		data.Domain = ""
	}
	if len(data.Installed.DNSNames) > 8 {
		data.Installed.DNSNames = nil
	}
	for _, name := range data.Installed.DNSNames {
		if len(name) > 253 {
			data.Installed.DNSNames = nil
			break
		}
	}
	b, _ := json.Marshal(data)
	rec.Data = string(b)
	rec.Message = ""
	if len(rec.Step) > 64 {
		rec.Step = "invalid"
	}
	return rec
}
