package controller

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

func insertAnyTLSTestBoot(t *testing.T, db *gorm.DB, nodeID int64) {
	t.Helper()
	if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: nodeID, Step: "agent_boot", TimeMs: time.Now().UnixMilli() - 1000, Data: `{"processId":"test-agent"}`}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestAnyTLSNodePinRejectsOldOrOtherProcessConfirmation(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_pin_process?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}, &model.NodeAnyTLSCertLog{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	bundle, err := issueAnyTLSNodeCert(77, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	pin := anyTLSCertPinFromPEM(bundle.CertPEM)
	now := time.Now().UnixMilli()
	log := func(step string, at int64, data string) {
		t.Helper()
		if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: 77, Step: step, TimeMs: at, Data: data}).Error; err != nil {
			t.Fatal(err)
		}
	}
	log("agent_boot", now-1000, `{"processId":"new"}`)
	log("installed_check", now, fmt.Sprintf(`{"processId":"other","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin))
	if got := anyTLSNodeCertPin(77, bundle.Domain); got != "" {
		t.Fatalf("wrong process accepted: %q", got)
	}
	log("installed_check", now+1, fmt.Sprintf(`{"processId":"new","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin))
	if got := anyTLSNodeCertPin(77, bundle.Domain); got != pin {
		t.Fatalf("current process rejected: %q", got)
	}
	log("installed_check", now+2, fmt.Sprintf(`{"processId":"new","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin))
	// The latest confirmation is recent now; move it into the past to simulate
	// a disconnected agent without any later clean shutdown or boot log.
	if err := db.Model(&model.NodeAnyTLSCertLog{}).Where("node_id = ?", 77).Update("time_ms", now-int64((3*time.Minute)/time.Millisecond)).Error; err != nil {
		t.Fatal(err)
	}
	if got := anyTLSNodeCertPin(77, bundle.Domain); got != "" {
		t.Fatalf("stale confirmation accepted: %q", got)
	}
}

func TestAnyTLSLeafSharedAcrossControllerDirectories(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_shared_leaf?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}, &model.NodeAnyTLSCertLog{}); err != nil {
		t.Fatal(err)
	}
	firstDir, secondDir := t.TempDir(), t.TempDir()
	t.Setenv(anyTLSCertDirEnv, firstDir)
	first, err := issueAnyTLSNodeCert(71, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, secondDir)
	second, err := issueAnyTLSNodeCert(71, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if second.CertPEM != first.CertPEM || second.KeyPEM != first.KeyPEM {
		t.Fatal("controllers sharing DB issued different leaves")
	}
	pin := anyTLSCertPinFromPEM(first.CertPEM)
	insertAnyTLSTestBoot(t, db, 71)
	if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: 71, Step: "installed_check", TimeMs: time.Now().UnixMilli(), Data: fmt.Sprintf(`{"processId":"test-agent","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin)}).Error; err != nil {
		t.Fatal(err)
	}
	if got := anyTLSNodeCertPin(71, first.Domain); got != pin {
		t.Fatalf("other controller could not publish confirmed pin: %q", got)
	}
}

func TestAnyTLSNodePinRequiresInstalledConfirmation(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_pin_confirmation?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}, &model.NodeAnyTLSCertLog{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	bundle, err := issueAnyTLSNodeCert(42, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	pin := anyTLSCertPinFromPEM(bundle.CertPEM)
	if got := anyTLSNodeCertPin(42, bundle.Domain); got != "" {
		t.Fatal("unconfirmed certificate was published")
	}
	insertAnyTLSTestBoot(t, db, 42)
	report := fmt.Sprintf(`{"processId":"test-agent","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin)
	if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: 42, Step: "installed_check", TimeMs: time.Now().UnixMilli(), Data: report}).Error; err != nil {
		t.Fatal(err)
	}
	if got := anyTLSNodeCertPin(42, bundle.Domain); got != pin {
		t.Fatalf("confirmed certificate not published: %q", got)
	}
	if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: 42, Step: "reload_err", TimeMs: time.Now().UnixMilli() + 2, Data: `{}`}).Error; err != nil {
		t.Fatal(err)
	}
	if got := anyTLSNodeCertPin(42, bundle.Domain); got != "" {
		t.Fatal("newer reload failure did not invalidate confirmation")
	}
	if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: 42, Step: "installed_check", TimeMs: time.Now().UnixMilli() + 3, Data: fmt.Sprintf(`{"processId":"test-agent","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin)}).Error; err != nil {
		t.Fatal(err)
	}
	if got := anyTLSNodeCertPin(42, bundle.Domain); got != pin {
		t.Fatal("new post-reload confirmation did not restore pin")
	}
	if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: 42, Step: "installed_check", TimeMs: time.Now().UnixMilli() + 4, Data: fmt.Sprintf(`{"processId":"test-agent","context":"post_reload","matchLatest":false,"installed":{"fingerprintSha256":%q}}`, pin)}).Error; err != nil {
		t.Fatal(err)
	}
	if got := anyTLSNodeCertPin(42, bundle.Domain); got != "" {
		t.Fatal("stale confirmation survived a newer mismatch")
	}
	if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: 42, Step: "installed_check", TimeMs: time.Now().UnixMilli() + 5, Data: fmt.Sprintf(`{"processId":"test-agent","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin)}).Error; err != nil {
		t.Fatal(err)
	}
	if got := anyTLSNodeCertPin(42, bundle.Domain); got != pin {
		t.Fatal("fresh post-reload confirmation did not restore pin")
	}
	if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: 42, Step: "agent_boot", TimeMs: time.Now().UnixMilli() + 6, Data: `{"processId":"replacement"}`}).Error; err != nil {
		t.Fatal(err)
	}
	if got := anyTLSNodeCertPin(42, bundle.Domain); got != "" {
		t.Fatal("previous process confirmation survived agent boot")
	}
}

func TestAnyTLSCAForPinRejectsMismatchedAuthority(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_ca_pin_pair?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	bundle, err := issueAnyTLSNodeCert(61, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	pin := anyTLSCertPinFromPEM(bundle.CertPEM)
	if got := anyTLSCAForPin(61, bundle.Domain, pin); got != bundle.CAPEM {
		t.Fatal("matching CA/pin pair unavailable")
	}
	if _, _, _, err := generateAnyTLSCA(); err != nil {
		t.Fatal(err)
	}
	if got := anyTLSCAForPin(61, bundle.Domain, pin); got != "" {
		t.Fatal("old leaf pin paired with newly rotated CA")
	}
}

func TestAnyTLSCARejectsPartialLegacyPair(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_partial_ca?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	cert, _, err := createAnyTLSCA()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ViteConfig{Name: anyTLSCACertDBKey, Value: cert}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ensureAnyTLSCA(); err == nil {
		t.Fatal("partial legacy CA was silently replaced")
	}
}

func TestAnyTLSIssuedLeafCompletesVerifiedTLSHandshake(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_real_tls_handshake?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	bundle, err := issueAnyTLSNodeCert(62, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(bundle.CAPEM)) {
		t.Fatal("cannot import CA")
	}
	for _, tc := range []struct {
		name, domain string
		wantOK       bool
	}{{"valid", bundle.Domain, true}, {"wrong_sni", "other.example.com", false}} {
		t.Run(tc.name, func(t *testing.T) {
			served := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					served <- err
					return
				}
				defer conn.Close()
				served <- conn.(*tls.Conn).Handshake()
			}()
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", listener.Addr().String(), &tls.Config{ServerName: tc.domain, RootCAs: roots, MinVersion: tls.VersionTLS12})
			if tc.wantOK {
				if err != nil {
					t.Fatal(err)
				}
				got := sha256.Sum256(conn.ConnectionState().PeerCertificates[0].Raw)
				if hex.EncodeToString(got[:]) != anyTLSCertPinFromPEM(bundle.CertPEM) {
					t.Fatal("served leaf differs from subscription pin")
				}
				conn.Close()
			} else if err == nil {
				conn.Close()
				t.Fatal("wrong SNI was accepted")
			}
			select {
			case <-served:
			case <-time.After(3 * time.Second):
				t.Fatal("server handshake stalled")
			}
		})
	}
}

func TestAnyTLSCARefusesMintWhenLegacyConfigUnreadable(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_unreadable_legacy?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.AnyTLSCAAuthority{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	if _, _, _, err := ensureAnyTLSCA(); err == nil {
		t.Fatal("CA minted without readable legacy config table")
	}
	var count int64
	if err := db.Model(&model.AnyTLSCAAuthority{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("authority created despite unreadable legacy config: count=%d err=%v", count, err)
	}
}

func TestAnyTLSCARefusesSilentReplacementOfLocalLegacyAuthority(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_local_legacy?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	certPEM, keyPEM, err := createAnyTLSCA()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anyTLSCertDir(), anyTLSCACertFileName), []byte(certPEM), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anyTLSCertDir(), anyTLSCAKeyFileName), []byte(keyPEM), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ensureAnyTLSCA(); err == nil {
		t.Fatal("legacy local CA silently replaced without migration")
	}
	var count int64
	if err := db.Model(&model.AnyTLSCAAuthority{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("new authority created despite legacy CA, rows=%d err=%v", count, err)
	}
}

func TestAnyTLSCAConcurrentInitialElection(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_ca_concurrent?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		pem string
		err error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, _, pemText, err := ensureAnyTLSCA()
			results <- outcome{pem: pemText, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	var winner string
	for _, r := range []outcome{first, second} {
		if r.err != nil {
			continue // a DB lock may fail closed; it must never select a different CA
		}
		if winner != "" && r.pem != winner {
			t.Fatal("concurrent controllers selected different authorities")
		}
		winner = r.pem
	}
	if winner == "" {
		t.Fatalf("neither contender selected a shared authority: %v, %v", first.err, second.err)
	}
	_, _, persisted, err := readSharedAnyTLSCA()
	if err != nil || persisted != winner {
		t.Fatalf("successful selection differs from shared authority: %v", err)
	}
}

func TestAnyTLSCAConvergesAcrossControllers(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_ca_converges?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	first, _, firstPEM, err := ensureAnyTLSCA()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	second, _, secondPEM, err := ensureAnyTLSCA()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Raw, second.Raw) || firstPEM != secondPEM {
		t.Fatal("controllers selected different CAs")
	}
	var rows int64
	if err := db.Model(&model.AnyTLSCAAuthority{}).Count(&rows).Error; err != nil || rows != 1 {
		t.Fatalf("shared CA row count=%d err=%v", rows, err)
	}
}

func TestAnyTLSCAUnavailableDatabaseDoesNotReturnLocalAuthority(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_ca_fail_closed?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	if _, _, _, err := ensureAnyTLSCA(); err == nil {
		t.Fatal("CA returned despite unavailable shared database")
	}
}

func TestAnyTLSNodeCertificateRenewsNearExpiry(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_leaf_expiry?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	first, err := issueAnyTLSNodeCert(60, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	ca, caKey, _, err := ensureAnyTLSCA()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(first.CertPEM), []byte(first.KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	leaf.SerialNumber = big.NewInt(6789)
	leaf.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, leaf.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	shortPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	key := anyTLSLeafDBKey(60, first.Domain, 0, ca)
	if err := db.Model(&model.AnyTLSNodeLeaf{}).Where("leaf_key = ?", key).Update("cert_pem", shortPEM).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := readAnyTLSNodeCertDB(key, 60, first.Domain, 0, ca, first.CAPEM); ok {
		t.Fatal("near-expiry fixture incorrectly accepted")
	}
	renewed, err := issueAnyTLSNodeCert(60, first.Domain)
	if err != nil {
		t.Fatalf("near-expiry leaf could not renew: %v", err)
	}
	if renewed.CertPEM == shortPEM || renewed.CertPEM == first.CertPEM {
		t.Fatal("near-expiry leaf was not replaced")
	}
	other, err := issueAnyTLSNodeCert(60, first.Domain)
	if err != nil || other.CertPEM != renewed.CertPEM {
		t.Fatalf("renewal not stable: %v", err)
	}
}

func TestAnyTLSNodeCertificateReusedUntilRevision(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:anytls_pin_stability?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	first, err := issueAnyTLSNodeCert(41, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := issueAnyTLSNodeCert(41, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first.CertPEM != second.CertPEM || first.KeyPEM != second.KeyPEM {
		t.Fatal("unchanged node certificate was reissued")
	}
	if _, err := bumpAnyTLSCertRevision(41); err != nil {
		t.Fatal(err)
	}
	third, err := issueAnyTLSNodeCert(41, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if first.CertPEM == third.CertPEM {
		t.Fatal("revision must rotate leaf and pin")
	}
}

func TestSurgeAnyTLSUsesPinnedLeafAndNeverSkipsVerify(t *testing.T) {
	raw := []byte("test-certificate-der")
	sum := sha256.Sum256(raw)
	pin := hex.EncodeToString(sum[:])
	params := map[string]interface{}{"cert-sha256": pin, "skip-cert-verify": false, "sni": "node.example.com"}
	got := buildSurgeAnyTLSLine("node", "192.0.2.1", 443, "secret", params)
	if !strings.Contains(got, "server-cert-fingerprint-sha256="+pin) || !strings.Contains(got, "skip-cert-verify=false") {
		t.Fatalf("pin/verification absent: %s", got)
	}
	if strings.Contains(got, "skip-cert-verify=true") {
		t.Fatal("pinned line permits insecure override")
	}
}

func TestClashMetaAnyTLSCertificateFingerprint(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open("file:clash_meta_pin?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}); err != nil {
		t.Fatal(err)
	}
	pin := strings.Repeat("ab", 32)
	items := []subProxy{{ID: 1, Name: "node", Group: "default", Type: "anytls", Server: "192.0.2.1", Port: 443, Password: "u1:secret", Params: map[string]interface{}{
		"cert-sha256":        "SHA256:" + strings.ToUpper(pin),
		"fingerprint_sha256": pin,
		"client-fingerprint": "chrome", "sni": "node.example.com", "skip-cert-verify": false,
	}}}
	for name, got := range map[string]string{
		"legacy":           buildClashConfigLegacy(items, nil),
		"template-proxies": func() string { out, _ := renderClashProxies(items); return out }(),
		"clash-meta":       buildClashMetaConfig(items, nil),
	} {
		if strings.Count(got, "fingerprint: "+yamlQuote(pin)) != 1 || strings.Count(got, "client-fingerprint: "+yamlQuote("chrome")) != 1 || !strings.Contains(got, "skip-cert-verify: false") {
			t.Errorf("%s missing unique certificate pin / client fingerprint or verification: %s", name, got)
		}
		for _, wrong := range []string{"    cert-sha256:", "    fingerprint_sha256:", "    fingerprint-sha256:", "    cert_sha256:", "bad-alias-must-not-appear"} {
			if strings.Contains(got, wrong) {
				t.Errorf("%s leaked alias %q: %s", name, wrong, got)
			}
		}
	}
}

func TestClashMetaAnyTLSRejectsMalformedPin(t *testing.T) {
	items := []subProxy{{Name: "node", Group: "default", Type: "anytls", Server: "192.0.2.1", Port: 443, Password: "secret", Params: map[string]interface{}{
		"cert-sha256": "invalid", "skip-cert-verify": false,
	}}}
	for _, got := range []string{buildClashConfigLegacy(items, nil), func() string { out, _ := renderClashProxies(items); return out }()} {
		if strings.Contains(got, "    fingerprint:") || strings.Contains(got, "    cert-sha256:") {
			t.Fatalf("invalid pin exported to Mihomo config: %s", got)
		}
	}
}

func TestCertificatePinMatchesDER(t *testing.T) {
	// Pin the exact leaf DER, not the PEM text or CA certificate.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	block, _ := pem.Decode(raw)
	sum := sha256.Sum256(block.Bytes)
	want := hex.EncodeToString(sum[:])
	if got := anyTLSCertPinFromPEM(string(raw)); got != want {
		t.Fatalf("pin=%s want=%s", got, want)
	}
	if got := anyTLSCertPinFromPEM("broken"); got != "" {
		t.Fatalf("invalid cert yielded pin %q", got)
	}
}
