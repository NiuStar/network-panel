package controller

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/app/util"
	dbpkg "network-panel/golang-backend/internal/db"
)

const (
	anyTLSCertDirEnv        = "ANYTLS_CERT_DIR"
	anyTLSCertDirDefault    = "./data/anytls"
	anyTLSCACertFileName    = "ca_cert.pem"
	anyTLSCAKeyFileName     = "ca_key.pem"
	anyTLSLeafValidity      = 90 * 24 * time.Hour
	anyTLSCAValidity        = 5 * 365 * 24 * time.Hour
	anyTLSCARefreshBefore   = 7 * 24 * time.Hour
	anyTLSCertCommonName    = "E8"
	anyTLSCertOrganization  = "Let's Encrypt"
	anyTLSDomainSuffix      = "docker.com"
	anyTLSRandomPrefixSize  = 8
	anyTLSDomainLabelMaxLen = 63
	anyTLSAgentRefreshHours = 24
	anyTLSCACertDBKey       = "anytls_ca_cert_pem"
	anyTLSCAKeyDBKey        = "anytls_ca_key_pem"
	anyTLSCertEnabledDBKey  = "anytls_cert_enabled"
)

type anyTLSNodeCertBundle struct {
	Domain      string
	CertPEM     string
	KeyPEM      string
	CAPEM       string
	NotBeforeMS int64
	NotAfterMS  int64
}

func anyTLSCertDomainKey(nodeID int64) string {
	return fmt.Sprintf("anytls_cert_domain_%d", nodeID)
}

func anyTLSCertRevisionKey(nodeID int64) string {
	return fmt.Sprintf("anytls_cert_revision_%d", nodeID)
}

func anyTLSCertDir() string {
	p := strings.TrimSpace(os.Getenv(anyTLSCertDirEnv))
	if p == "" {
		p = anyTLSCertDirDefault
	}
	return p
}

var anyTLSLeafCertMu sync.Mutex

func anyTLSNodeCertPath(nodeID int64) string {
	return filepath.Join(anyTLSCertDir(), "nodes", strconv.FormatInt(nodeID, 10), "cert.pem")
}

func anyTLSNodeKeyPath(nodeID int64) string {
	return filepath.Join(anyTLSCertDir(), "nodes", strconv.FormatInt(nodeID, 10), "key.pem")
}

func anyTLSLeafDBKey(nodeID int64, domain string, revision int64, ca *x509.Certificate) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%d|%x", nodeID, domain, revision, ca.Raw)))
	return hex.EncodeToString(sum[:])
}

func selectSharedAnyTLSLeaf(row model.AnyTLSNodeLeaf, nodeID int64, domain string, revision int64, caCert *x509.Certificate, caPEM string) (*anyTLSNodeCertBundle, bool) {
	pair, err := tls.X509KeyPair([]byte(row.CertPEM), []byte(row.KeyPEM))
	if err != nil || len(pair.Certificate) == 0 {
		return nil, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.CheckSignatureFrom(caCert) != nil || leaf.VerifyHostname(domain) != nil ||
		leaf.NotAfter.Before(time.Now().Add(7*24*time.Hour)) || leaf.NotBefore.After(time.Now()) ||
		leaf.Subject.SerialNumber != strconv.FormatInt(revision, 10) {
		return nil, false
	}
	return &anyTLSNodeCertBundle{Domain: domain, CertPEM: row.CertPEM, KeyPEM: row.KeyPEM, CAPEM: caPEM,
		NotBeforeMS: leaf.NotBefore.UnixMilli(), NotAfterMS: leaf.NotAfter.UnixMilli()}, true
}

func readAnyTLSNodeCertDB(key string, nodeID int64, domain string, revision int64, caCert *x509.Certificate, caPEM string) (*anyTLSNodeCertBundle, bool) {
	if dbpkg.DB == nil {
		return nil, false
	}
	var row model.AnyTLSNodeLeaf
	if err := dbpkg.DB.Where("leaf_key = ?", key).First(&row).Error; err != nil {
		return nil, false
	}
	return selectSharedAnyTLSLeaf(row, nodeID, domain, revision, caCert, caPEM)
}

func anyTLSCAForPin(nodeID int64, domain, pin string) string {
	if pin == "" {
		return ""
	}
	ca, _, pemText, err := ensureAnyTLSCA()
	if err != nil {
		return ""
	}
	revision := getAnyTLSCertRevision(nodeID)
	key := anyTLSLeafDBKey(nodeID, domain, revision, ca)
	bundle, ok := readAnyTLSNodeCertDB(key, nodeID, domain, revision, ca, pemText)
	if !ok || !strings.EqualFold(anyTLSCertPinFromPEM(bundle.CertPEM), pin) {
		return ""
	}
	return pemText
}

func anyTLSCertPinFromPEM(raw string) string {
	block, _ := pem.Decode([]byte(raw))
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

func anyTLSNodeCertPin(nodeID int64, domain string) string {
	if nodeID <= 0 || domain == "" || dbpkg.DB == nil {
		return ""
	}
	caCert, _, caPEM, err := ensureAnyTLSCA()
	if err != nil {
		return ""
	}
	key := anyTLSLeafDBKey(nodeID, domain, getAnyTLSCertRevision(nodeID), caCert)
	bundle, ok := readAnyTLSNodeCertDB(key, nodeID, domain, getAnyTLSCertRevision(nodeID), caCert, caPEM)
	if !ok {
		return ""
	}
	pin := anyTLSCertPinFromPEM(bundle.CertPEM)
	if pin == "" {
		return ""
	}
	// Only publish a pin once the agent has reported the same installed leaf.
	var log model.NodeAnyTLSCertLog
	if err := dbpkg.DB.Where("node_id = ? AND step = ?", nodeID, "installed_check").Order("time_ms desc, id desc").First(&log).Error; err != nil {
		return ""
	}
	if log.TimeMs > time.Now().UnixMilli()+5000 || time.Since(time.UnixMilli(log.TimeMs)) > 2*time.Minute {
		return ""
	}
	var boot model.NodeAnyTLSCertLog
	if err := dbpkg.DB.Where("node_id = ? AND step = ?", nodeID, "agent_boot").Order("time_ms desc, id desc").First(&boot).Error; err != nil || boot.TimeMs > log.TimeMs || (boot.TimeMs == log.TimeMs && boot.ID >= log.ID) {
		return ""
	}
	var bootData struct {
		ProcessID string `json:"processId"`
	}
	var confirmation struct {
		ProcessID string `json:"processId"`
	}
	if json.Unmarshal([]byte(boot.Data), &bootData) != nil || json.Unmarshal([]byte(log.Data), &confirmation) != nil || bootData.ProcessID == "" || bootData.ProcessID != confirmation.ProcessID {
		return ""
	}
	// A newer reload failure invalidates an older successful confirmation.
	var newerFailure model.NodeAnyTLSCertLog
	if err := dbpkg.DB.Where("node_id = ? AND step = ? AND (time_ms > ? OR (time_ms = ? AND id > ?))", nodeID, "reload_err", log.TimeMs, log.TimeMs, log.ID).Order("time_ms desc, id desc").First(&newerFailure).Error; err == nil {
		return ""
	}
	// Agent boot marks the old process's runtime confirmation obsolete, even
	// when the certificate itself has not changed.
	var newerBoot model.NodeAnyTLSCertLog
	if err := dbpkg.DB.Where("node_id = ? AND step = ? AND (time_ms > ? OR (time_ms = ? AND id > ?))", nodeID, "agent_boot", log.TimeMs, log.TimeMs, log.ID).First(&newerBoot).Error; err == nil {
		return ""
	}
	var report struct {
		Context   string `json:"context"`
		Installed struct {
			Fingerprint string `json:"fingerprintSha256"`
		} `json:"installed"`
		MatchLatest bool `json:"matchLatest"`
	}
	if json.Unmarshal([]byte(log.Data), &report) != nil || report.Context != "post_reload" || !report.MatchLatest || !strings.EqualFold(report.Installed.Fingerprint, pin) {
		return ""
	}
	if _, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM)); err != nil {
		return ""
	}
	block, _ := pem.Decode([]byte(bundle.CertPEM))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || leaf.VerifyHostname(domain) != nil || leaf.NotBefore.After(time.Now()) ||
		leaf.NotAfter.Before(time.Now().Add(7*24*time.Hour)) ||
		leaf.Subject.SerialNumber != strconv.FormatInt(getAnyTLSCertRevision(nodeID), 10) {
		return ""
	}
	if leaf.CheckSignatureFrom(caCert) != nil {
		return ""
	}
	return pin
}

func readAnyTLSNodeCert(nodeID int64, domain string, revision int64, caCert *x509.Certificate, caPEM string) (*anyTLSNodeCertBundle, bool) {
	certPEM, err := os.ReadFile(anyTLSNodeCertPath(nodeID))
	if err != nil {
		return nil, false
	}
	keyPEM, err := os.ReadFile(anyTLSNodeKeyPath(nodeID))
	if err != nil {
		return nil, false
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.CheckSignatureFrom(caCert) != nil || leaf.VerifyHostname(domain) != nil ||
		leaf.NotAfter.Before(time.Now().Add(7*24*time.Hour)) || leaf.NotBefore.After(time.Now()) ||
		leaf.Subject.SerialNumber != strconv.FormatInt(revision, 10) {
		return nil, false
	}
	return &anyTLSNodeCertBundle{Domain: domain, CertPEM: string(certPEM), KeyPEM: string(keyPEM), CAPEM: caPEM,
		NotBeforeMS: leaf.NotBefore.UnixMilli(), NotAfterMS: leaf.NotAfter.UnixMilli()}, true
}

func saveAnyTLSNodeCert(nodeID int64, certPEM, keyPEM string) error {
	folder := filepath.Dir(anyTLSNodeCertPath(nodeID))
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	write := func(path, value string) error {
		tmp, err := os.CreateTemp(folder, ".anytls-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if err := tmp.Chmod(0o600); err != nil {
			tmp.Close()
			return err
		}
		if _, err := tmp.WriteString(value); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), path)
	}
	if err := write(anyTLSNodeKeyPath(nodeID), keyPEM); err != nil {
		return err
	}
	return write(anyTLSNodeCertPath(nodeID), certPEM)
}

func anyTLSCACertPath() string {
	return filepath.Join(anyTLSCertDir(), anyTLSCACertFileName)
}

func anyTLSCAKeyPath() string {
	return filepath.Join(anyTLSCertDir(), anyTLSCAKeyFileName)
}

func getViteConfigValue(name string) string {
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", name).First(&cfg).Error; err != nil {
		return ""
	}
	return cfg.Value
}

func isAnyTLSCertFeatureEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(getViteConfigValue(anyTLSCertEnabledDBKey)))
	switch v {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

func normalizeAnyTLSCertDomain(nodeID int64, raw string) (string, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw != "" {
		if u, err := url.Parse(raw); err == nil && strings.TrimSpace(u.Hostname()) != "" {
			raw = strings.TrimSpace(strings.ToLower(u.Hostname()))
		}
	}
	raw = strings.Trim(raw, ".")
	if raw == "" {
		raw = randomDNSLabel(anyTLSRandomPrefixSize)
	}

	var domain string
	if strings.Contains(raw, ".") {
		domain = raw
	} else {
		label := sanitizeDNSLabel(raw)
		if label == "" {
			label = randomDNSLabel(anyTLSRandomPrefixSize)
		}
		domain = fmt.Sprintf("%djs.%s.%s", nodeID, label, anyTLSDomainSuffix)
	}
	domain = strings.Trim(domain, ".")
	if !isValidDNSName(domain) {
		return "", fmt.Errorf("invalid cert domain")
	}
	return domain, nil
}

func sanitizeDNSLabel(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	b := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b = append(b, c)
		}
	}
	out := strings.Trim(string(b), "-")
	if len(out) > anyTLSDomainLabelMaxLen {
		out = out[:anyTLSDomainLabelMaxLen]
		out = strings.Trim(out, "-")
	}
	return out
}

func randomDNSLabel(n int) string {
	if n <= 0 {
		n = anyTLSRandomPrefixSize
	}
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		buf := make([]byte, 1)
		if _, err := rand.Read(buf); err != nil {
			out[i] = letters[int(time.Now().UnixNano()%int64(len(letters)))]
			continue
		}
		out[i] = letters[int(buf[0])%len(letters)]
	}
	out[0] = letters[(int(out[0])+1)%len(letters)]
	return strings.Trim(sanitizeDNSLabel(string(out)), "-")
}

func isValidDNSName(domain string) bool {
	domain = strings.TrimSpace(strings.ToLower(strings.Trim(domain, ".")))
	if domain == "" || len(domain) > 253 {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, lb := range labels {
		if lb == "" || len(lb) > anyTLSDomainLabelMaxLen {
			return false
		}
		if lb[0] == '-' || lb[len(lb)-1] == '-' {
			return false
		}
		for i := 0; i < len(lb); i++ {
			c := lb[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func setAnyTLSCertDomain(nodeID int64, raw string) (string, error) {
	if nodeID <= 0 {
		return "", fmt.Errorf("invalid node id")
	}
	domain, err := normalizeAnyTLSCertDomain(nodeID, raw)
	if err != nil {
		return "", err
	}
	key := anyTLSCertDomainKey(nodeID)
	now := time.Now().UnixMilli()
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", key).Order("id desc").First(&cfg).Error; err == nil {
		cfg.Value = domain
		cfg.Time = now
		if err := dbpkg.DB.Save(&cfg).Error; err != nil {
			return "", err
		}
		_ = dbpkg.DB.Where("name = ? AND id <> ?", key, cfg.ID).Delete(&model.ViteConfig{}).Error
		return domain, nil
	}
	return domain, dbpkg.DB.Create(&model.ViteConfig{Name: key, Value: domain, Time: now}).Error
}

func getAnyTLSCertDomain(nodeID int64) string {
	if nodeID <= 0 {
		return ""
	}
	key := anyTLSCertDomainKey(nodeID)
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", key).Order("id desc").First(&cfg).Error; err != nil {
		return ""
	}
	domain := strings.TrimSpace(cfg.Value)
	if domain == "" {
		return ""
	}
	domain, err := normalizeAnyTLSCertDomain(nodeID, domain)
	if err != nil {
		return ""
	}
	return domain
}

func ensureAnyTLSCertDomain(nodeID int64, raw string) (string, error) {
	if strings.TrimSpace(raw) != "" {
		return setAnyTLSCertDomain(nodeID, raw)
	}
	if existing := getAnyTLSCertDomain(nodeID); existing != "" {
		return existing, nil
	}
	return setAnyTLSCertDomain(nodeID, "")
}

func getAnyTLSCertRevision(nodeID int64) int64 {
	if nodeID <= 0 {
		return 0
	}
	key := anyTLSCertRevisionKey(nodeID)
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", key).Order("id desc").First(&cfg).Error; err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(cfg.Value), 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

func bumpAnyTLSCertRevision(nodeID int64) (int64, error) {
	if nodeID <= 0 {
		return 0, fmt.Errorf("invalid node id")
	}
	key := anyTLSCertRevisionKey(nodeID)
	now := time.Now().UnixMilli()
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", key).Order("id desc").First(&cfg).Error; err == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(cfg.Value), 10, 64)
		if v < 0 {
			v = 0
		}
		// Use current epoch-ms as revision seed to guarantee each force reissue gets a new cert.
		v = now
		cfg.Value = strconv.FormatInt(v, 10)
		cfg.Time = now
		if err := dbpkg.DB.Save(&cfg).Error; err != nil {
			return 0, err
		}
		_ = dbpkg.DB.Where("name = ? AND id <> ?", key, cfg.ID).Delete(&model.ViteConfig{}).Error
		return v, nil
	}
	cfg = model.ViteConfig{
		Name:  key,
		Value: strconv.FormatInt(now, 10),
		Time:  now,
	}
	if err := dbpkg.DB.Create(&cfg).Error; err != nil {
		return 0, err
	}
	return now, nil
}

func currentAnyTLSLeafWindow(now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	// Use rolling validity window: each reissue starts from now.
	notBefore := now.Add(-1 * time.Hour)
	notAfter := now.Add(anyTLSLeafValidity)
	return notBefore, notAfter
}

func anyTLSNodeCertStatus(nodeID int64, ensureDomain bool) (map[string]any, error) {
	if nodeID <= 0 {
		return nil, fmt.Errorf("invalid node id")
	}
	if !isAnyTLSCertFeatureEnabled() {
		return nil, nil
	}
	var (
		domain string
		err    error
	)
	if ensureDomain {
		domain, err = ensureAnyTLSCertDomain(nodeID, "")
		if err != nil {
			return nil, err
		}
	} else {
		domain = getAnyTLSCertDomain(nodeID)
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return nil, nil
	}

	now := time.Now().UTC()
	if logDomain, logNotBefore, logNotAfter, logUpdatedAt, ok := latestAnyTLSCertMetaFromLogs(nodeID); ok && logNotAfter > 0 {
		if strings.TrimSpace(logDomain) != "" {
			domain = strings.TrimSpace(logDomain)
		}
		days := int64(time.Until(time.UnixMilli(logNotAfter)).Hours() / 24)
		state := "ok"
		if now.UnixMilli() > logNotAfter {
			state = "expired"
		} else if days <= 7 {
			state = "expiring"
		}
		return map[string]any{
			"domain":      domain,
			"notBeforeMs": logNotBefore,
			"notAfterMs":  logNotAfter,
			"daysLeft":    days,
			"state":       state,
			"revision":    getAnyTLSCertRevision(nodeID),
			"updatedAtMs": logUpdatedAt,
			"source":      "agent_log",
		}, nil
	}

	notBefore, notAfter := currentAnyTLSLeafWindow(now)
	days := int64(time.Until(notAfter).Hours() / 24)
	state := "ok"
	if now.After(notAfter) {
		state = "expired"
	} else if days <= 7 {
		state = "expiring"
	}
	return map[string]any{
		"domain":      domain,
		"notBeforeMs": notBefore.UnixMilli(),
		"notAfterMs":  notAfter.UnixMilli(),
		"daysLeft":    days,
		"state":       state,
		"revision":    getAnyTLSCertRevision(nodeID),
		"updatedAtMs": now.UnixMilli(),
		"source":      "controller_estimate",
	}, nil
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case float32:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	default:
		return 0
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return ""
	}
}

func latestAnyTLSCertMetaFromLogs(nodeID int64) (domain string, notBeforeMs, notAfterMs, updatedAtMs int64, ok bool) {
	if nodeID <= 0 {
		return "", 0, 0, 0, false
	}
	var logs []model.NodeAnyTLSCertLog
	if err := dbpkg.DB.Where("node_id = ? AND step IN ?", nodeID, []string{
		"installed_check",
		"material_updated",
		"material_unchanged",
	}).Order("time_ms desc, id desc").Limit(30).Find(&logs).Error; err != nil || len(logs) == 0 {
		return "", 0, 0, 0, false
	}
	for _, lg := range logs {
		if strings.TrimSpace(lg.Data) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(lg.Data), &m); err != nil {
			continue
		}
		d := strings.TrimSpace(asString(m["domain"]))
		nb := asInt64(m["notBeforeMs"])
		na := asInt64(m["notAfterMs"])
		if lg.Step == "installed_check" {
			if ins, okIns := m["installed"].(map[string]any); okIns {
				if nb == 0 {
					nb = asInt64(ins["notBeforeMs"])
				}
				if na == 0 {
					na = asInt64(ins["notAfterMs"])
				}
				if d == "" {
					if dns, okDNS := ins["dnsNames"].([]any); okDNS && len(dns) > 0 {
						d = asString(dns[0])
					}
				}
			}
			if d == "" {
				if exp, okExp := m["expected"].(map[string]any); okExp {
					d = asString(exp["domain"])
				}
			}
		}
		if na > 0 {
			return d, nb, na, lg.TimeMs, true
		}
	}
	return "", 0, 0, 0, false
}

func parsePrivateKeyPEM(pemBytes []byte) (any, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("invalid key pem")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("unsupported private key format")
}

func parseAnyTLSCA(certPEM, keyPEM string) (*x509.Certificate, crypto.Signer, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("invalid CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 || time.Until(cert.NotAfter) <= anyTLSCARefreshBefore {
		return nil, nil, fmt.Errorf("invalid or expiring CA certificate")
	}
	parsed, err := parsePrivateKeyPEM([]byte(keyPEM))
	if err != nil {
		return nil, nil, err
	}
	key, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("CA key is not a signer")
	}
	pub, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return nil, nil, err
	}
	certPub, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil || !bytes.Equal(pub, certPub) {
		return nil, nil, fmt.Errorf("CA key does not match certificate")
	}
	return cert, key, nil
}

func readSharedAnyTLSCA() (*x509.Certificate, crypto.Signer, string, error) {
	if dbpkg.DB == nil {
		return nil, nil, "", fmt.Errorf("shared CA database unavailable")
	}
	var row model.AnyTLSCAAuthority
	if err := dbpkg.DB.Where("id = ?", 1).First(&row).Error; err != nil {
		return nil, nil, "", err
	}
	cert, key, err := parseAnyTLSCA(row.CertPEM, row.KeyPEM)
	if err != nil {
		return nil, nil, "", err
	}
	return cert, key, row.CertPEM, nil
}

func legacyAnyTLSCAValue(name string) (string, error) {
	var cfg model.ViteConfig
	err := dbpkg.DB.Where("name = ?", name).Order("id desc").First(&cfg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cfg.Value, nil
}

func ensureAnyTLSCA() (*x509.Certificate, any, string, error) {
	if dbpkg.DB == nil {
		return nil, nil, "", fmt.Errorf("shared CA database unavailable")
	}
	// Legacy DB pair, if present, is the authoritative migration source. Do
	// not recover from local files, which may differ between controllers.
	var row model.AnyTLSCAAuthority
	err := dbpkg.DB.Where("id = ?", 1).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		certPEM, readErr := legacyAnyTLSCAValue(anyTLSCACertDBKey)
		if readErr != nil {
			return nil, nil, "", readErr
		}
		keyPEM, readErr := legacyAnyTLSCAValue(anyTLSCAKeyDBKey)
		if readErr != nil {
			return nil, nil, "", readErr
		}
		if (certPEM == "") != (keyPEM == "") {
			return nil, nil, "", fmt.Errorf("partial legacy AnyTLS CA pair")
		}
		if certPEM != "" {
			if _, _, err := parseAnyTLSCA(certPEM, keyPEM); err != nil {
				return nil, nil, "", err
			}
			row = model.AnyTLSCAAuthority{ID: 1, CertPEM: certPEM, KeyPEM: keyPEM}
		} else {
			// Local-only CA is ambiguous in a multi-controller deployment.
			// Refuse to mint a different CA until an operator imports the
			// existing authority into the shared store.
			for _, path := range []string{filepath.Join(anyTLSCertDir(), anyTLSCACertFileName), filepath.Join(anyTLSCertDir(), anyTLSCAKeyFileName)} {
				_, statErr := os.Stat(path)
				if statErr == nil {
					return nil, nil, "", fmt.Errorf("legacy local AnyTLS CA requires explicit shared migration")
				}
				if !errors.Is(statErr, os.ErrNotExist) {
					return nil, nil, "", statErr
				}
			}
			certPEM, keyPEM, err = createAnyTLSCA()
			if err != nil {
				return nil, nil, "", err
			}
			row = model.AnyTLSCAAuthority{ID: 1, CertPEM: certPEM, KeyPEM: keyPEM}
		}
		if err := dbpkg.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return nil, nil, "", err
		}
	} else if err != nil {
		return nil, nil, "", err
	}
	cert, key, selectedPEM, err := readSharedAnyTLSCA()
	if err != nil {
		return nil, nil, "", err
	}
	return cert, key, selectedPEM, nil
}

func createAnyTLSCA() (string, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   anyTLSCertCommonName,
			Organization: []string{anyTLSCertOrganization},
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(anyTLSCAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, pub, priv)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	newCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	newKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return string(newCertPEM), string(newKeyPEM), nil
}

func generateAnyTLSCA() (*x509.Certificate, any, string, error) {
	if dbpkg.DB == nil {
		return nil, nil, "", fmt.Errorf("shared CA database unavailable")
	}
	certPEM, keyPEM, err := createAnyTLSCA()
	if err != nil {
		return nil, nil, "", err
	}
	// Force rotation is an explicit operation; update the shared pair as one
	// row rather than independently overwriting two legacy config values.
	row := model.AnyTLSCAAuthority{ID: 1, CertPEM: certPEM, KeyPEM: keyPEM}
	if err := dbpkg.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
		return nil, nil, "", err
	}
	cert, key, selectedPEM, err := readSharedAnyTLSCA()
	if err != nil {
		return nil, nil, "", err
	}
	return cert, key, selectedPEM, nil
}

func anyTLSCAStatus() (map[string]any, error) {
	enabled := isAnyTLSCertFeatureEnabled()
	info := map[string]any{
		"enabled":             enabled,
		"caStorage":           "shared_database",
		"caExists":            false,
		"keyExists":           false,
		"caRefreshBeforeMs":   int64(anyTLSCARefreshBefore / time.Millisecond),
		"nodeCertValidityMs":  int64(anyTLSLeafValidity / time.Millisecond),
		"agentRefreshHours":   anyTLSAgentRefreshHours,
		"certCommonName":      anyTLSCertCommonName,
		"certOrganization":    anyTLSCertOrganization,
		"domainSuffix":        anyTLSDomainSuffix,
		"randomPrefixLength":  anyTLSRandomPrefixSize,
		"domainFormatExample": fmt.Sprintf("<nodeId>js.<prefix>.%s", anyTLSDomainSuffix),
	}
	if !enabled {
		return info, nil
	}

	cert, _, _, err := readSharedAnyTLSCA()
	if err != nil {
		info["parseError"] = err.Error()
		return info, err
	}
	info["caExists"] = true
	info["keyExists"] = true
	now := time.Now().UTC()
	info["serialNumber"] = cert.SerialNumber.String()
	info["subject"] = cert.Subject.String()
	info["issuer"] = cert.Issuer.String()
	info["notBeforeMs"] = cert.NotBefore.UnixMilli()
	info["notAfterMs"] = cert.NotAfter.UnixMilli()
	info["daysRemaining"] = int64(time.Until(cert.NotAfter).Hours() / 24)
	info["isExpired"] = cert.NotAfter.Before(now)
	info["refreshAtMs"] = cert.NotAfter.Add(-anyTLSCARefreshBefore).UnixMilli()
	info["isCA"] = cert.IsCA
	info["publicKeyAlgorithm"] = cert.PublicKeyAlgorithm.String()
	info["signatureAlgorithm"] = cert.SignatureAlgorithm.String()
	if len(cert.Subject.Organization) > 0 {
		info["subjectOrganization"] = cert.Subject.Organization[0]
	}
	info["subjectCommonName"] = cert.Subject.CommonName
	if len(cert.DNSNames) > 0 {
		info["dnsNames"] = cert.DNSNames
	}
	return info, nil
}

func issueAnyTLSNodeCert(nodeID int64, rawDomain string) (*anyTLSNodeCertBundle, error) {
	if nodeID <= 0 {
		return nil, fmt.Errorf("invalid node id")
	}
	anyTLSLeafCertMu.Lock()
	defer anyTLSLeafCertMu.Unlock()
	domain, err := ensureAnyTLSCertDomain(nodeID, rawDomain)
	if err != nil {
		return nil, err
	}
	caCert, caKey, caPEM, err := ensureAnyTLSCA()
	if err != nil {
		return nil, err
	}

	revision := getAnyTLSCertRevision(nodeID)
	key := anyTLSLeafDBKey(nodeID, domain, revision, caCert)
	if cached, ok := readAnyTLSNodeCertDB(key, nodeID, domain, revision, caCert, caPEM); ok {
		return cached, nil
	}

	now := time.Now().UTC()
	notBeforeWithSkew, notAfter := currentAnyTLSLeafWindow(now)
	notBefore := notBeforeWithSkew.Add(1 * time.Hour)
	leafPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	leafPub := &leafPriv.PublicKey
	serialSeed := fmt.Sprintf("%d|%s|%d|%s|%d|%d", nodeID, domain, notBefore.Unix(), caCert.SerialNumber.String(), revision, time.Now().UnixNano())
	serialHash := sha256.Sum256([]byte("serial|" + serialSeed))
	serial := new(big.Int).SetBytes(serialHash[:16])
	if serial.Sign() <= 0 {
		serial = big.NewInt(1)
	}

	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   anyTLSCertCommonName,
			Organization: []string{anyTLSCertOrganization},
			SerialNumber: strconv.FormatInt(revision, 10),
		},
		NotBefore:             notBeforeWithSkew,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{domain},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, leafPub, caKey)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafPriv)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	row := model.AnyTLSNodeLeaf{Key: key, CertPEM: string(certPEM), KeyPEM: string(keyPEM)}
	return publishSharedAnyTLSLeaf(row, nodeID, domain, revision, caCert, caPEM)
}

func publishSharedAnyTLSLeaf(row model.AnyTLSNodeLeaf, nodeID int64, domain string, revision int64, caCert *x509.Certificate, caPEM string) (*anyTLSNodeCertBundle, error) {
	key := row.Key
	// A leaf near expiry must be replaced under the same key. Use a compare-and-
	// swap so only one controller wins, then return precisely the shared winner.
	var prior model.AnyTLSNodeLeaf
	switch err := dbpkg.DB.Where("leaf_key = ?", key).First(&prior).Error; {
	case err == nil:
		// Another controller may have renewed after our initial cache miss.
		// Never replace its valid winner with our already-generated candidate.
		if selected, ok := selectSharedAnyTLSLeaf(prior, nodeID, domain, revision, caCert, caPEM); ok {
			return selected, nil
		}
		res := dbpkg.DB.Model(&model.AnyTLSNodeLeaf{}).Where("leaf_key = ? AND cert_pem = ? AND key_pem = ?", key, prior.CertPEM, prior.KeyPEM).
			Updates(map[string]interface{}{"cert_pem": row.CertPEM, "key_pem": row.KeyPEM})
		if res.Error != nil {
			return nil, res.Error
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		if err := dbpkg.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	selected, ok := readAnyTLSNodeCertDB(key, nodeID, domain, revision, caCert, caPEM)
	if !ok {
		return nil, fmt.Errorf("shared AnyTLS leaf unavailable or invalid")
	}
	return selected, nil
}

func anyTLSCertPayload(nodeID int64, rawDomain string) (map[string]any, error) {
	if !isAnyTLSCertFeatureEnabled() {
		return nil, nil
	}
	bundle, err := issueAnyTLSNodeCert(nodeID, rawDomain)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"certDomain":      bundle.Domain,
		"certPem":         bundle.CertPEM,
		"keyPem":          bundle.KeyPEM,
		"caPem":           bundle.CAPEM,
		"certNotBeforeMs": bundle.NotBeforeMS,
		"certNotAfterMs":  bundle.NotAfterMS,
		"enforceVerify":   true,
	}, nil
}

// AgentAnyTLSCert provides AnyTLS server certificate material for the node.
// POST /api/v1/agent/anytls-cert {secret}
func AgentAnyTLSCert(c *gin.Context) {
	var p struct {
		Secret string `json:"secret" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	secret := strings.TrimSpace(p.Secret)
	if secret == "" {
		c.JSON(http.StatusOK, response.ErrMsg("secret 不能为空"))
		return
	}
	var node model.Node
	if err := dbpkg.DB.Select("id").Where("secret = ?", secret).First(&node).Error; err != nil || node.ID == 0 {
		c.JSON(http.StatusUnauthorized, response.ErrMsg("invalid secret"))
		return
	}
	if !isAnyTLSCertFeatureEnabled() {
		c.JSON(http.StatusOK, response.Ok(gin.H{"enabled": false, "reason": "controller_disabled"}))
		return
	}
	var st model.AnyTLSSetting
	if err := dbpkg.DB.Select("id").Where("node_id = ?", node.ID).First(&st).Error; err != nil || st.ID == 0 {
		c.JSON(http.StatusOK, response.Ok(gin.H{"enabled": false}))
		return
	}
	payload, err := anyTLSCertPayload(node.ID, "")
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("证书生成失败: "+err.Error()))
		return
	}
	payload["enabled"] = true
	c.JSON(http.StatusOK, response.Ok(payload))
}

// SubscriptionAnyTLSCACheck checks whether downloaded CA content is import-ready.
// GET /api/v1/subscription/anytls-ca/check
func SubscriptionAnyTLSCACheck(c *gin.Context) {
	token := extractToken(c)
	if token == "" || !util.ValidateToken(token) {
		c.JSON(http.StatusUnauthorized, response.ErrMsg("未登录或token无效"))
		return
	}
	if !isAnyTLSCertFeatureEnabled() {
		c.JSON(http.StatusOK, response.ErrMsg("控制器未启用 AnyTLS 证书"))
		return
	}

	_, _, caPEM, err := ensureAnyTLSCA()
	if err != nil || strings.TrimSpace(caPEM) == "" {
		c.JSON(http.StatusOK, response.ErrMsg("获取站点 CA 失败"))
		return
	}

	firstLine := ""
	for _, ln := range strings.Split(caPEM, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		firstLine = ln
		break
	}
	headerOK := strings.HasPrefix(firstLine, "-----BEGIN CERTIFICATE-----")
	ret := map[string]any{
		"pemFirstLine":  firstLine,
		"pemHeaderOK":   headerOK,
		"parseOK":       false,
		"downloadReady": false,
	}

	block, _ := pem.Decode([]byte(caPEM))
	if block == nil {
		ret["error"] = "invalid pem format"
		c.JSON(http.StatusOK, response.Ok(ret))
		return
	}
	cert, parseErr := x509.ParseCertificate(block.Bytes)
	if parseErr != nil {
		ret["error"] = parseErr.Error()
		c.JSON(http.StatusOK, response.Ok(ret))
		return
	}

	ret["parseOK"] = true
	ret["downloadReady"] = headerOK
	ret["subjectCommonName"] = cert.Subject.CommonName
	if len(cert.Subject.Organization) > 0 {
		ret["subjectOrganization"] = cert.Subject.Organization[0]
	}
	ret["notBeforeMs"] = cert.NotBefore.UnixMilli()
	ret["notAfterMs"] = cert.NotAfter.UnixMilli()
	ret["publicKeyAlgorithm"] = cert.PublicKeyAlgorithm.String()
	ret["signatureAlgorithm"] = cert.SignatureAlgorithm.String()
	ret["isCA"] = cert.IsCA
	c.JSON(http.StatusOK, response.Ok(ret))
}

// SubscriptionAnyTLSCAStatus returns CA status for admin panel.
// GET /api/v1/subscription/anytls-ca/status
func SubscriptionAnyTLSCAStatus(c *gin.Context) {
	info, err := anyTLSCAStatus()
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("获取CA状态失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, response.Ok(info))
}

// SubscriptionAnyTLSCAGenerate creates or rotates controller CA.
// POST /api/v1/subscription/anytls-ca/generate {force?}
func SubscriptionAnyTLSCAGenerate(c *gin.Context) {
	var p struct {
		Force bool `json:"force"`
	}
	_ = c.ShouldBindJSON(&p)

	rotated := false
	if p.Force {
		if _, _, _, err := generateAnyTLSCA(); err != nil {
			c.JSON(http.StatusOK, response.ErrMsg("生成CA失败: "+err.Error()))
			return
		}
		rotated = true
	} else {
		if _, _, _, err := ensureAnyTLSCA(); err != nil {
			c.JSON(http.StatusOK, response.ErrMsg("初始化CA失败: "+err.Error()))
			return
		}
	}
	info, err := anyTLSCAStatus()
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("获取CA状态失败: "+err.Error()))
		return
	}
	info["rotated"] = rotated
	c.JSON(http.StatusOK, response.Ok(info))
}
