package controller

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/util"
)

// No fixture address is dialed. Public literals are classification inputs only.
func TestManagedAnyTLSSNIEndpointScope(t *testing.T) {
	cases := []struct {
		name, inIP, tcp, udp, nodeIP, serverIP, wantHost string
		public                                           bool
	}{
		{name: "public-v4", inIP: "23.141.52.113", wantHost: "23.141.52.113", public: true},
		{name: "public-v6", inIP: "2606:4700:4700::1111", wantHost: "2606:4700:4700::1111", public: true},
		{name: "dns", inIP: "relay.example.net", wantHost: "relay.example.net"},
		{name: "private-v4", inIP: "10.23.4.5", wantHost: "10.23.4.5"},
		{name: "private-v6", inIP: "fd00::1", wantHost: "fd00::1"},
		{name: "loopback", inIP: "127.0.0.1", wantHost: "127.0.0.1"},
		{name: "documentation", inIP: "198.51.100.9", wantHost: "198.51.100.9"},
		{name: "shared", inIP: "100.64.0.1", wantHost: "100.64.0.1"},
		{name: "unknown", inIP: "not-an-address", wantHost: "not-an-address"},
		{name: "tcp-public", inIP: "0.0.0.0", tcp: "23.141.52.113:443", nodeIP: "10.0.0.1", wantHost: "23.141.52.113", public: true},
		{name: "tcp-dns", inIP: "::", tcp: "relay.example.net:443", nodeIP: "23.141.52.113", wantHost: "relay.example.net"},
		{name: "udp-public-v6", inIP: "::", tcp: "[::]:443", udp: "[2606:4700:4700::1111]:443", wantHost: "2606:4700:4700::1111", public: true},
		{name: "node-public", inIP: "0.0.0.0", nodeIP: "23.141.52.113", wantHost: "23.141.52.113", public: true},
		{name: "node-private", inIP: "::", nodeIP: "192.168.1.2", serverIP: "23.141.52.113", wantHost: "192.168.1.2"},
		{name: "server-public", inIP: "::", serverIP: "23.141.52.113", wantHost: "23.141.52.113", public: true},
		{name: "server-dns", serverIP: "relay.example.net", wantHost: "relay.example.net"},
	}
	for _, tc := range cases {
		for _, separateExit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/exit-%t", tc.name, separateExit), func(t *testing.T) {
				db, tun, fwd, pin := setupAnyTLSSNIScopeFixture(t, separateExit)
				tun.InIP = tc.inIP
				if tc.tcp != "" {
					tun.TCPListenAddr = &tc.tcp
				}
				if tc.udp != "" {
					tun.UDPListenAddr = &tc.udp
				}
				if err := db.Save(&tun).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&model.Node{}).Where("id = ?", tun.InNodeID).Updates(map[string]interface{}{"ip": tc.nodeIP, "server_ip": tc.serverIP}).Error; err != nil {
					t.Fatal(err)
				}
				items, skipped := scopeSubscriptionItems(t)
				if len(items) != 1 || len(skipped) != 0 {
					t.Fatalf("items=%d skipped=%v", len(items), skipped)
				}
				item := items[0]
				wantSNI := "node.example.com"
				if tc.public {
					wantSNI = ""
				}
				if item.Server != tc.wantHost {
					t.Fatalf("endpoint want=%q got=%q", tc.wantHost, item.Server)
				}
				assertScopeSNIAndPin(t, item, pin, wantSNI)
				// Exercise the owned probe entry point independently, not only its serializer.
				probe, _, err := buildSingboxOutboundForForward(fwd, tun, 0)
				assertScopeProbe(t, probe, err, tc.wantHost, pin, wantSNI)
			})
		}
	}
}

func setupAnyTLSSNIScopeFixture(t *testing.T, separateExit bool) (*gorm.DB, model.Tunnel, model.Forward, string) {
	t.Helper()
	t.Setenv("JWT_SECRET", "sni-scope-test-only")
	t.Setenv("ANYTLS_CERT_NODE_IDS", "")
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	gin.SetMode(gin.TestMode)
	db := setupTunnelExternalTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.Forward{}, &model.ExitSetting{}, &model.AnyTLSSetting{}, &model.AnyTLSPortEgress{}, &model.NodeAnyTLSCertLog{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ViteConfig{Name: anyTLSCertEnabledDBKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	status := 1
	entity := func(id int64) model.BaseEntity { return model.BaseEntity{ID: id, Status: &status} }
	if err := db.Create(&model.User{BaseEntity: entity(101), User: "scope-user", Pwd: "x", RoleID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{201, 202} {
		if err := db.Create(&model.Node{BaseEntity: entity(id), Name: "scope-node", IP: "23.141.52.113", ServerIP: "23.141.52.113"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	exitID := int64(201)
	if separateExit {
		exitID = 202
	}
	proto := "anytls"
	tun := model.Tunnel{BaseEntity: entity(301), Name: "scope-tunnel", InNodeID: 201, InIP: "23.141.52.113", Type: 1, Flow: 1, Protocol: &proto}
	if separateExit {
		tun.OutNodeID = &exitID
	}
	if err := db.Create(&tun).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AnyTLSSetting{BaseEntity: entity(401), NodeID: exitID, Port: 10086, Password: "scope-password"}).Error; err != nil {
		t.Fatal(err)
	}
	bundle, err := issueAnyTLSNodeCert(exitID, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	pin := anyTLSCertPinFromPEM(bundle.CertPEM)
	insertAnyTLSTestBoot(t, db, exitID)
	if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: exitID, Step: "installed_check", TimeMs: time.Now().UnixMilli(), Data: fmt.Sprintf(`{"processId":"test-agent","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin)}).Error; err != nil {
		t.Fatal(err)
	}
	fwd := model.Forward{BaseEntity: entity(501), UserID: 101, UserName: "scope-user", Name: "scope-forward", Group: "scope", TunnelID: tun.ID, InPort: 44006, RemoteAddr: "example.com:443"}
	if err := db.Create(&fwd).Error; err != nil {
		t.Fatal(err)
	}
	return db, tun, fwd, pin
}

func scopeSubscriptionItems(t *testing.T) ([]subProxy, []subSkip) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/v1/subscription/clash?token="+util.GenerateToken(101, "scope-user", 1), nil)
	_, items, skipped, ok := subscriptionItems(c)
	if !ok {
		t.Fatalf("subscription failed: %s", w.Body.String())
	}
	return items, skipped
}

func assertScopeSNIAndPin(t *testing.T, item subProxy, pin, wantSNI string) {
	t.Helper()
	gotSNI := paramStringFirst(item.Params, "sni", "server_name")
	if gotSNI != wantSNI {
		t.Errorf("scope host=%s SNI want=%q got=%q", item.Server, wantSNI, gotSNI)
	}
	if got, err := requiredAnyTLSCertificatePin(item.Params); err != nil || got != pin || item.Params["skip-cert-verify"] != false {
		t.Fatalf("strict leaf trust changed: %s %v", got, err)
	}
	probe, err := buildPinnedAnyTLSAgentOutbound(item, item.Params)
	assertScopeProbe(t, probe, err, item.Server, pin, wantSNI)
	for format, output := range map[string]string{
		"clash": buildClashConfig([]subProxy{item}, nil),
		"surge": buildSurgeConfig([]subProxy{item}, nil, "6", ""),
		"uri":   buildAnyTLSURI(item.Password, item, item.Params),
		"qx":    buildQXConfig([]subProxy{item}, nil),
	} {
		if !strings.Contains(output, pin) {
			t.Errorf("%s lost pin", format)
		}
		if strings.Contains(output, "node.example.com") != (wantSNI == "node.example.com") {
			t.Errorf("%s changed synthesized name: %s", format, output)
		}
		if wantSNI == "operator.example.com" && !strings.Contains(output, wantSNI) {
			t.Errorf("%s lost explicit name", format)
		}
	}
}

func assertScopeProbe(t *testing.T, probe map[string]interface{}, err error, host, pin, wantSNI string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	tls := probe["tls"].(map[string]interface{})
	gotSNI, _ := tls["server_name"].(string)
	if probe["server"] != host || gotSNI != wantSNI || probe["cert-sha256"] != pin || tls["insecure"] != false {
		t.Errorf("probe host=%s SNI want=%q got=%q strict-pin=%t", host, wantSNI, gotSNI, probe["cert-sha256"] == pin && tls["insecure"] == false)
	}
}

func TestManagedAnyTLSSNIProbeLoopbackScope(t *testing.T) {
	_, tun, fwd, pin := setupAnyTLSSNIScopeFixture(t, false)
	probe, _, err := buildSingboxOutboundForForward(fwd, tun, tun.InNodeID)
	assertScopeProbe(t, probe, err, "127.0.0.1", pin, "node.example.com")
}

func TestAnyTLSSNIExternalScopeAndInvalidPins(t *testing.T) {
	for _, key := range []string{"sni", "server_name"} {
		for _, rawPin := range []string{strings.Repeat("ab", 32), "", "malformed"} {
			t.Run(key+"/"+rawPin, func(t *testing.T) {
				db, tun, fwd, _ := setupAnyTLSSNIScopeFixture(t, false)
				config, err := json.Marshal(map[string]interface{}{"password": " u12:external ", "cert-sha256": rawPin, key: "operator.example.com", "skip-cert-verify": false})
				if err != nil {
					t.Fatal(err)
				}
				raw, proto := string(config), "anytls"
				ext := model.ExitNodeExternal{BaseEntity: model.BaseEntity{ID: 601}, Name: "external", Host: "23.141.52.113", Port: 443, Protocol: &proto, Config: &raw}
				if err := db.Create(&ext).Error; err != nil {
					t.Fatal(err)
				}
				tun.OutExitID = &ext.ID
				if err := db.Save(&tun).Error; err != nil {
					t.Fatal(err)
				}
				items, skipped := scopeSubscriptionItems(t)
				probe, _, probeErr := buildSingboxOutboundForForward(fwd, tun, 0)
				if rawPin == "" || rawPin == "malformed" {
					if len(items) != 0 || len(skipped) != 1 || probeErr == nil {
						t.Fatalf("invalid external pin accepted: items=%d skipped=%d probeErr=%v", len(items), len(skipped), probeErr)
					}
					return
				}
				if len(items) != 1 || len(skipped) != 0 {
					t.Fatalf("external items=%d skipped=%v", len(items), skipped)
				}
				if items[0].Password != " u12:external " {
					t.Fatalf("external credential changed: %q", items[0].Password)
				}
				assertScopeSNIAndPin(t, items[0], rawPin, "operator.example.com")
				assertScopeProbe(t, probe, probeErr, tun.InIP, rawPin, "operator.example.com")
			})
		}
	}
}

func TestManagedAnyTLSSNIUnconfirmedAndExcludedFailClosed(t *testing.T) {
	for _, mode := range []string{"unconfirmed", "excluded"} {
		t.Run(mode, func(t *testing.T) {
			db, tun, fwd, _ := setupAnyTLSSNIScopeFixture(t, false)
			if mode == "excluded" {
				t.Setenv("ANYTLS_CERT_NODE_IDS", "202")
			} else {
				// A newer boot invalidates the previous installed confirmation.
				if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: 201, Step: "agent_boot", TimeMs: time.Now().UnixMilli() + 1, Data: `{"processId":"new-process"}`}).Error; err != nil {
					t.Fatal(err)
				}
			}
			items, skipped := scopeSubscriptionItems(t)
			_, _, err := buildSingboxOutboundForForward(fwd, tun, 0)
			if len(items) != 0 || len(skipped) != 1 || err == nil {
				t.Fatalf("managed trust not fail-closed: items=%d skipped=%d err=%v", len(items), len(skipped), err)
			}
		})
	}
}
