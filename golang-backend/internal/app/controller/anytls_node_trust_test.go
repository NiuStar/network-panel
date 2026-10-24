package controller

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
)

func TestRequiredAnyTLSCertificatePin(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name string
		params map[string]interface{}
		valid bool
	}{
		{"missing", nil, false},
		{"invalid", map[string]interface{}{"cert-sha256": "chrome"}, false},
		{"pin only IP self signed", map[string]interface{}{"cert-sha256": pin}, true},
		{"normalized alias", map[string]interface{}{"cert_sha256": "SHA256:" + strings.ToUpper(pin)}, true},
		{"matching aliases", map[string]interface{}{"cert-sha256": pin, "fingerprint_sha256": pin}, true},
		{"conflicting aliases", map[string]interface{}{"cert-sha256": pin, "fingerprint_sha256": strings.Repeat("cd", 32)}, false},
		{"invalid secondary alias", map[string]interface{}{"cert-sha256": pin, "cert_sha256": "bad"}, false},
		{"client fingerprint is not certificate", map[string]interface{}{"client-fingerprint": "chrome"}, false},
		{"separate client fingerprint", map[string]interface{}{"cert-sha256": pin, "client-fingerprint": "chrome"}, true},
		{"hidden insecure alias", map[string]interface{}{"cert-sha256": pin, "skip-cert-verify": false, "allowInsecure": true}, false},
		{"nested insecure", map[string]interface{}{"cert-sha256": pin, "tls": map[string]interface{}{"insecure": true}}, false},
		{"malformed insecure", map[string]interface{}{"cert-sha256": pin, "insecure": []interface{}{}}, false},
		{"raw override", map[string]interface{}{"cert-sha256": pin, "sing-box": map[string]interface{}{"tls": false}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requiredAnyTLSCertificatePin(tc.params)
			if (err == nil) != tc.valid { t.Fatalf("valid=%v, err=%v", tc.valid, err) }
			if tc.valid && got != pin { t.Fatal("pin not canonical") }
		})
	}
}

func TestAnyTLSExternalNodeAPITrust(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTunnelExternalTestDB(t)
	router := gin.New()
	router.POST("/api/v1/exit/external/create", ExitExternalCreate)
	router.POST("/api/v1/exit/external/update", ExitExternalUpdate)
	call := func(path string, body map[string]interface{}) bool {
		t.Helper()
		b, err := json.Marshal(body)
		if err != nil { t.Fatal(err) }
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		var result struct { Code int `json:"code"` }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil { t.Fatal(err) }
		return result.Code == 0
	}
	body := map[string]interface{}{"name": "external", "host": "192.0.2.1", "port": 443, "protocol": " Any_TLS "}
	if call("/api/v1/exit/external/create", body) { t.Fatal("missing pin accepted") }
	for _, cfg := range []interface{}{
		map[string]interface{}{"cert-sha256": "bad"},
		map[string]interface{}{"cert-sha256": strings.Repeat("ab", 32), "insecure": true},
		map[string]interface{}{"cert-sha256": strings.Repeat("ab", 32), "cert_sha256": strings.Repeat("cd", 32)},
		map[string]interface{}{"cert-sha256": strings.Repeat("ab", 32), "uri": "raw"},
	} {
		body["config"] = cfg
		if call("/api/v1/exit/external/create", body) { t.Fatal("invalid create accepted") }
	}
	var count int64
	db.Model(&model.ExitNodeExternal{}).Count(&count)
	if count != 0 { t.Fatal("invalid create persisted") }
	pin := strings.Repeat("ab", 32)
	body["config"] = map[string]interface{}{"cert-sha256": pin}
	if !call("/api/v1/exit/external/create", body) { t.Fatal("pin-only public IP node rejected") }
	var saved model.ExitNodeExternal
	if err := db.First(&saved).Error; err != nil { t.Fatal(err) }
	body["id"] = saved.ID
	for _, cfg := range []interface{}{
		nil, map[string]interface{}{"cert-sha256": "bad"},
		map[string]interface{}{"cert-sha256": pin, "insecure": true},
		map[string]interface{}{"cert-sha256": pin, "cert_sha256": strings.Repeat("cd", 32)},
		map[string]interface{}{"cert-sha256": pin, "surge": "raw"},
	} {
		body["config"] = cfg
		if call("/api/v1/exit/external/update", body) { t.Fatal("invalid update accepted") }
		var after model.ExitNodeExternal
		if err := db.First(&after, saved.ID).Error; err != nil { t.Fatal(err) }
		if after.Config == nil || *after.Config != *saved.Config { t.Fatal("rejected update changed stored trust") }
	}
	// Old rows inserted before enforcement cannot reach config apply.
	bad := `{ "password": "test" }`
	db.Model(&saved).Update("config", bad)
	if _, ok := loadExternalExit(model.Tunnel{OutExitID: &saved.ID}); ok { t.Fatal("invalid stored AnyTLS node reached apply") }
	body["protocol"] = "ss"
	body["config"] = map[string]interface{}{"password": "test", "cipher": "aes-128-gcm"}
	if !call("/api/v1/exit/external/create", body) { t.Fatal("non-TLS SS incorrectly requires a pin") }
}

func TestPinnedAnyTLSAgentOutbound(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	item := subProxy{Server: "192.0.2.1", Port: 443, Password: "test"}
	out, err := buildPinnedAnyTLSAgentOutbound(item, map[string]interface{}{"cert-sha256": pin})
	if err != nil { t.Fatal(err) }
	if out["cert-sha256"] != pin || out["server"] != item.Server || out["server_port"] != item.Port { t.Fatal("agent probe lost pinned identity") }
	tls, ok := out["tls"].(map[string]interface{})
	if !ok || tls["insecure"] != false { t.Fatal("agent probe disabled verification") }
	if _, exists := tls["server_name"]; exists { t.Fatal("pin-only IP probe forced a server name") }
	if _, err := buildPinnedAnyTLSAgentOutbound(item, nil); err == nil { t.Fatal("probe without trust accepted") }
	if _, err := buildPinnedAnyTLSAgentOutbound(item, map[string]interface{}{"cert-sha256": pin, "insecure": true}); err == nil { t.Fatal("probe bypass accepted") }
}

func TestExternalAnyTLSProtocolInference(t *testing.T) {
	for _, key := range []string{"protocol", "type"} {
		raw, _ := json.Marshal(map[string]interface{}{key: "Any-TLS"})
		if validateExternalAnyTLSConfig(nil, raw) == nil { t.Fatal("inferred AnyTLS bypassed trust guard") }
	}
}
