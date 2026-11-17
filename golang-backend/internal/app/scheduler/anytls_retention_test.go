package scheduler

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"network-panel/golang-backend/internal/anytlsreadiness"
	"network-panel/golang-backend/internal/app/controller"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/util"
	dbpkg "network-panel/golang-backend/internal/db"
)

// Exercise actual pruning and the actual public renderer, not a mocked gate.
func TestAnyTLSSubscriptionSurvivesActualDiagnosticPruning(t *testing.T) {
	for _, mode := range []string{"row_cap", "time_window"} {
		t.Run(mode, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			old := dbpkg.DB
			dbpkg.DB = db
			t.Cleanup(func() { dbpkg.DB = old })
			if err = db.AutoMigrate(&model.User{}, &model.Node{}, &model.Tunnel{}, &model.Forward{}, &model.ViteConfig{}, &model.AnyTLSSetting{}, &model.AnyTLSPortEgress{}, &model.ExitSetting{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}, &model.NodeAnyTLSCertLog{}, &model.AnyTLSRuntimeState{}, &model.NodeOpLog{}, &model.NodeDiagResult{}, &model.EasyTierResult{}, &model.NodeProbeResult{}, &model.NodeSysInfo{}, &model.FlowTimeseries{}, &model.NQResult{}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("JWT_SECRET", "retention-dummy-only-test-secret")
			t.Setenv("GIN_MODE", "release")
			t.Setenv("ANYTLS_CERT_ENABLED", "true")
			t.Setenv("ANYTLS_CERT_NODE_IDS", "")
			t.Setenv("ANYTLS_CERT_DIR", t.TempDir())
			now := time.Now()
			caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "retention-test-ca"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
			caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
			if err != nil {
				t.Fatal(err)
			}
			caCert, _ := x509.ParseCertificate(caDER)
			caKeyDER, _ := x509.MarshalECPrivateKey(caKey)
			caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
			caPrivate := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: caKeyDER}))
			domain := "retention.example.com"
			leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: domain, SerialNumber: "0"}, DNSNames: []string{domain}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &leafKey.PublicKey, caKey)
			if err != nil {
				t.Fatal(err)
			}
			leafKeyDER, _ := x509.MarshalECPrivateKey(leafKey)
			key := sha256.Sum256([]byte(fmt.Sprintf("201|%s|0|%x", domain, caDER)))
			pinRaw := sha256.Sum256(leafDER)
			pin := hex.EncodeToString(pinRaw[:])
			status := 1
			nid := int64(201)
			rows := []any{
				&model.AnyTLSCAAuthority{ID: 1, CertPEM: caPEM, KeyPEM: caPrivate},
				&model.AnyTLSNodeLeaf{Key: hex.EncodeToString(key[:]), CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})), KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER}))},
				&model.ViteConfig{Name: "anytls_cert_enabled", Value: "true"}, &model.ViteConfig{Name: "anytls_cert_domain_201", Value: domain},
				&model.User{BaseEntity: model.BaseEntity{ID: 101, Status: &status}, User: "retention-user", Pwd: "dummy", RoleID: 1},
				&model.Node{BaseEntity: model.BaseEntity{ID: nid, Status: &status}, Name: "retention-node", ServerIP: "203.0.113.20", IP: "203.0.113.20", PortSta: 1000, PortEnd: 65535},
				&model.Tunnel{BaseEntity: model.BaseEntity{ID: 301, Status: &status}, Name: "retention-tunnel", InNodeID: nid, InIP: "198.51.100.9", OutNodeID: &nid, Type: 1, Flow: 1},
				&model.AnyTLSSetting{BaseEntity: model.BaseEntity{ID: 401, Status: &status}, NodeID: nid, Port: 10086, Password: "fixture-only"},
				&model.Forward{BaseEntity: model.BaseEntity{ID: 501, Status: &status}, UserID: 101, UserName: "retention-user", Name: "retention-forward", TunnelID: 301, InPort: 44006, RemoteAddr: "example.com:443"},
			}
			for _, r := range rows {
				if err = db.Create(r).Error; err != nil {
					t.Fatal(err)
				}
			}
			event := func(step string, at int64, data string) {
				t.Helper()
				if err := anytlsreadiness.Persist(db, nid, model.NodeAnyTLSCertLog{NodeID: nid, Step: step, TimeMs: at, Data: data}); err != nil {
					t.Fatal(err)
				}
			}
			event("agent_boot", now.Add(-96*time.Hour).UnixMilli(), `{"processId":"retained-process"}`)
			confirmation := fmt.Sprintf(`{"processId":"retained-process","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin)
			for i := 0; i < 8; i++ {
				event("installed_check", now.UnixMilli()-int64(10-i), confirmation)
			}
			token := util.GenerateToken(101, "retention-user", 1)
			render := func() string {
				t.Helper()
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest("GET", "/api/v1/subscription/shadowrocket?token="+token, nil)
				controller.SubscriptionShadowrocket(c)
				if w.Code != 200 {
					t.Fatalf("HTTP %d", w.Code)
				}
				return w.Body.String()
			}
			assertReady := func(when string) {
				t.Helper()
				body := render()
				if !strings.Contains(body, "anytls://") || !strings.Contains(body, "cert-sha256="+pin) || strings.Contains(body, "证书验证或指纹尚未就绪") {
					t.Fatalf("%s: strict pinned subscription unavailable after diagnostic cleanup", when)
				}
			}
			assertReady("before")
			if mode == "row_cap" {
				pruneOldDataOnce(db, -1, 3)
			} else {
				pruneOldDataOnce(db, now.Add(-72*time.Hour).UnixMilli(), 0)
			}
			var count int64
			if err = db.Model(&model.NodeAnyTLSCertLog{}).Where("node_id=? AND step=?", nid, "agent_boot").Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("cleanup did not actually remove boot audit evidence")
			}
			assertReady("after")
			assertBlocked := func(when string) {
				t.Helper()
				body := render()
				if strings.Contains(body, "anytls://") || !strings.Contains(body, "证书验证或指纹尚未就绪") {
					t.Fatalf("%s: invalid readiness escaped the real subscription gate", when)
				}
			}
			// Removing all diagnostics must neither lose current readiness nor
			// erase a later invalidation. The independent state remains bounded.
			pruneOldDataOnce(db, now.Add(time.Hour).UnixMilli(), 0)
			assertReady("all diagnostics removed")
			event("reload_err", now.UnixMilli()+1, `{"processId":"retained-process"}`)
			pruneOldDataOnce(db, now.Add(time.Hour).UnixMilli(), 0)
			assertBlocked("failure diagnostic removed")
			event("installed_check", now.UnixMilli()+2, confirmation)
			assertReady("fresh confirmation after failure")
			event("agent_boot", now.UnixMilli()+3, `{"processId":"new-process"}`)
			pruneOldDataOnce(db, now.Add(time.Hour).UnixMilli(), 0)
			assertBlocked("new boot diagnostic removed")
			event("installed_check", now.UnixMilli()+4, confirmation)
			assertBlocked("old process cannot recover new boot")
			var states int64
			if err := db.Model(&model.AnyTLSRuntimeState{}).Where("node_id = ?", nid).Count(&states).Error; err != nil || states != 1 {
				t.Fatalf("durable state not bounded to one row: count=%d error=%v", states, err)
			}
		})
	}
}
