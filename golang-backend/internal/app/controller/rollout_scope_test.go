package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

func TestAgentAutoUpgradeEnabled(t *testing.T) {
	t.Setenv("AGENT_AUTO_UPGRADE", "")
	if err := os.Unsetenv("AGENT_AUTO_UPGRADE"); err != nil {
		t.Fatal(err)
	}
	if !agentAutoUpgradeEnabled() {
		t.Fatal("unset must preserve historical default")
	}
	for _, value := range []string{"true", "1", "yes", "on", " TRUE "} {
		t.Setenv("AGENT_AUTO_UPGRADE", value)
		if !agentAutoUpgradeEnabled() {
			t.Fatalf("rejected %q", value)
		}
	}
	for _, value := range []string{"false", "0", "no", "off", " FALSE ", "", " ", "truthy", "2"} {
		t.Setenv("AGENT_AUTO_UPGRADE", value)
		if agentAutoUpgradeEnabled() {
			t.Fatalf("enabled for %q", value)
		}
	}
	// The real websocket trigger, not just its helper, must contain the guard.
	file, err := parser.ParseFile(token.NewFileSet(), "ws.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	guarded := false
	ast.Inspect(file, func(n ast.Node) bool {
		statement, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		upgrade, enabled := false, false
		ast.Inspect(statement.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok || name.Name != "sendWSCommand" {
				return true
			}
			for _, arg := range call.Args {
				if literal, ok := arg.(*ast.BasicLit); ok && literal.Value == `"UpgradeAgent"` {
					upgrade = true
				}
			}
			return true
		})
		ast.Inspect(statement.Cond, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "agentAutoUpgradeEnabled" {
					enabled = true
				}
			}
			return true
		})
		guarded = guarded || (upgrade && enabled)
		return true
	})
	if !guarded {
		t.Fatal("real automatic UpgradeAgent dispatch is not guarded")
	}
}

func TestAnyTLSCertNodeAllowlist(t *testing.T) {
	t.Setenv("ANYTLS_CERT_NODE_IDS", "")
	if err := os.Unsetenv("ANYTLS_CERT_NODE_IDS"); err != nil {
		t.Fatal(err)
	}
	if !isAnyTLSCertNodeAllowed(29) {
		t.Fatal("unset changed legacy behavior")
	}
	for _, value := range []string{"", "  ", "28", "28,29", "9223372036854775807,28"} {
		t.Setenv("ANYTLS_CERT_NODE_IDS", value)
		if !isAnyTLSCertNodeAllowed(28) {
			t.Fatalf("valid scope rejected: %q", value)
		}
		if isAnyTLSCertNodeAllowed(0) || isAnyTLSCertNodeAllowed(-1) {
			t.Fatal("invalid node allowed")
		}
	}
	for _, value := range []string{"29", "28,", ",28", "28,bad", "28,28", "28,029", "028", "+28", "-28", "0,28", "28, 29", "28,9223372036854775808"} {
		t.Setenv("ANYTLS_CERT_NODE_IDS", value)
		if isAnyTLSCertNodeAllowed(28) {
			t.Fatalf("bad/excluded scope granted access: %q", value)
		}
	}
}

func TestAnyTLSCertRolloutScopeBoundaries(t *testing.T) {
	oldDB := dbpkg.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "scope.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = oldDB })
	if err := db.AutoMigrate(&model.Node{}, &model.AnyTLSSetting{}, &model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}, &model.NodeAnyTLSCertLog{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANYTLS_CERT_NODE_IDS", "")
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	if err := db.Create(&model.ViteConfig{Name: anyTLSCertEnabledDBKey, Value: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	var other *anyTLSNodeCertBundle
	for _, id := range []int64{28, 29} {
		if err := db.Create(&model.Node{BaseEntity: model.BaseEntity{ID: id}, Name: fmt.Sprint(id), Secret: fmt.Sprint(id)}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.AnyTLSSetting{NodeID: id, Port: 18443, Password: "secret"}).Error; err != nil {
			t.Fatal(err)
		}
		bundle, err := issueAnyTLSNodeCert(id, fmt.Sprintf("node%d.example.com", id))
		if err != nil {
			t.Fatal(err)
		}
		if id == 29 {
			other = bundle
		}
		insertAnyTLSTestBoot(t, db, id)
		if err := db.Create(&model.NodeAnyTLSCertLog{NodeID: id, Step: "installed_check", TimeMs: time.Now().UnixMilli(), Data: fmt.Sprintf(`{"processId":"test-agent","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, anyTLSCertPinFromPEM(bundle.CertPEM))}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if anyTLSNodeCertPin(29, other.Domain) == "" {
		t.Fatal("positive pre-scope pin unavailable")
	}
	t.Setenv("ANYTLS_CERT_NODE_IDS", "28")
	if _, err := issueAnyTLSNodeCert(28, ""); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"28", "28,bad"} {
		t.Setenv("ANYTLS_CERT_NODE_IDS", scope)
		if bundle, err := issueAnyTLSNodeCert(29, "changed.example.com"); bundle != nil || err == nil {
			t.Fatal("scope bypass on issuance")
		}
		if payload, err := anyTLSCertPayload(29, ""); payload != nil || err == nil {
			t.Fatal("scope bypass on payload")
		}
		if status, err := anyTLSNodeCertStatus(29, true); status != nil || err == nil {
			t.Fatal("scope bypass on preview")
		}
		if _, err := bumpAnyTLSCertRevision(29); err == nil {
			t.Fatal("scope bypass on revision")
		}
		if _, err := ensureAnyTLSCertDomain(29, "changed.example.com"); err == nil {
			t.Fatal("scope bypass on domain")
		}
		if pin := anyTLSNodeCertPin(29, other.Domain); pin != "" {
			t.Fatal("persisted excluded pin published")
		}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/agent/anytls-cert", bytes.NewBufferString(`{"secret":"29"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		AgentAnyTLSCert(c)
		var result struct {
			Data struct {
				Enabled bool   `json:"enabled"`
				Reason  string `json:"reason"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusOK || result.Data.Enabled || result.Data.Reason != "node_scope_disabled" || strings.Contains(recorder.Body.String(), "certPem") {
			t.Fatalf("unsafe agent response: %s", recorder.Body.String())
		}
	}
	t.Setenv("ANYTLS_CERT_NODE_IDS", "28")
	if got := getAnyTLSCertDomain(29); got != other.Domain {
		t.Fatalf("excluded domain changed: %s", got)
	}
	outID := int64(29)
	protocol := "anytls"
	proto, _, password, params := resolveExitProtocol(model.Tunnel{OutNodeID: &outID, Protocol: &protocol}, nil, nil, map[int64]model.AnyTLSSetting{29: {Password: "secret"}})
	if proto != "anytls" || password != "secret" {
		t.Fatal("managed endpoint unexpectedly changed")
	}
	if _, err := requiredAnyTLSCertificatePin(params); err == nil {
		t.Fatal("excluded managed subscription has pin")
	}
	items, skipped := filterPinnedAnyTLSExports([]subProxy{{ID: 29, Type: proto, Password: password, Params: params}, {ID: 30, Type: "ss"}}, nil, "clash")
	if len(items) != 1 || items[0].ID != 30 || len(skipped) != 1 || skipped[0].ID != 29 {
		t.Fatal("scope must omit only unpinned AnyTLS")
	}
	extProto := "anytls"
	config := `{"password":"external","cert-sha256":"` + strings.Repeat("ab", 32) + `"}`
	_, _, _, external := resolveExitProtocol(model.Tunnel{}, &model.ExitNodeExternal{Protocol: &extProto, Config: &config}, nil, nil)
	if _, err := requiredAnyTLSCertificatePin(external); err != nil {
		t.Fatalf("scope restricted external pin: %v", err)
	}
	if _, _, _, err := ensureAnyTLSCA(); err != nil {
		t.Fatalf("node scope disabled shared CA: %v", err)
	}
}
