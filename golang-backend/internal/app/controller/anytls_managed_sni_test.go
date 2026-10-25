package controller

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"network-panel/golang-backend/internal/app/model"
)

// Exercise the real managed resolver with an issued leaf and current installed
// confirmation, not a synthetic pin added after defaults have already run.
func TestManagedAnyTLSPinDoesNotInjectSNI(t *testing.T) {
	_, _, _, pin := setupAnyTLSSNIScopeFixture(t, false)
	items, skipped := scopeSubscriptionItems(t)
	if len(items) != 1 || len(skipped) != 0 {
		t.Fatalf("items=%d skipped=%v", len(items), skipped)
	}
	item := items[0]
	params, password := item.Params, item.Password
	if params["sni"] != nil || params["server_name"] != nil {
		t.Errorf("managed public-IP leaf pin injected SNI: %#v", params)
	}
	for name, render := range map[string]func() string{
		"clash":        func() string { return buildClashConfig([]subProxy{item}, nil) },
		"clash-meta":   func() string { return buildClashMetaConfig([]subProxy{item}, nil) },
		"clash-legacy": func() string { return buildClashConfigLegacy([]subProxy{item}, nil) },
		"surge":        func() string { return buildSurgeConfig([]subProxy{item}, nil, "6", "") },
		"uri":          func() string { return buildAnyTLSURI(password, item, params) },
		"v2ray":        func() string { return buildV2raySubscription([]subProxy{item}, nil) },
		"shadowrocket": func() string { return buildShadowrocket([]subProxy{item}, nil) },
		"shadowrocket-rules": func() string {
			out, err := buildShadowrocketRuleConfig([]subProxy{item})
			if err != nil {
				t.Fatal(err)
			}
			return out
		},
		"qx": func() string { return buildQXConfig([]subProxy{item}, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			out := render()
			if name == "v2ray" {
				decoded, err := base64.StdEncoding.DecodeString(out)
				if err != nil {
					t.Fatal(err)
				}
				out = string(decoded)
			}
			if !strings.Contains(out, pin) {
				t.Fatalf("export lost pin: %s", out)
			}
			if strings.Contains(out, "node.example.com") || strings.Contains(out, defaultAnyTLSSNI) {
				t.Fatalf("export injected SNI: %s", out)
			}
		})
	}
	t.Run("singbox-fails-closed", func(t *testing.T) {
		var out map[string]interface{}
		if err := json.Unmarshal([]byte(buildSingboxConfig([]subProxy{item}, nil)), &out); err != nil {
			t.Fatal(err)
		}
		if obs, ok := out["outbounds"].([]interface{}); !ok || len(obs) != 0 || out["__skipped__"] == nil {
			t.Fatalf("sing-box exported unsupported leaf trust: %#v", out)
		}
	})
	t.Run("agent-probe", func(t *testing.T) {
		out, err := buildPinnedAnyTLSAgentOutbound(item, params)
		if err != nil {
			t.Fatal(err)
		}
		tls := out["tls"].(map[string]interface{})
		if tls["server_name"] != nil || tls["insecure"] != false || out["cert-sha256"] != pin {
			t.Fatalf("probe changed identity: %#v", out)
		}
	})
}

func TestAnyTLSDefaultsKeepExplicitSNIAndPKIXName(t *testing.T) {
	db := setupTunnelExternalTestDB(t)
	if err := db.AutoMigrate(&model.ViteConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ViteConfig{Name: anyTLSCertEnabledDBKey, Value: "1"}).Error; err != nil {
		t.Fatal(err)
	}
	pin := strings.Repeat("ab", 32)
	for _, key := range []string{"sni", "server_name"} {
		t.Run(key, func(t *testing.T) {
			params := applyAnyTLSVerifyDefaults(map[string]interface{}{"cert-sha256": pin, key: "operator.example.com"}, "issued.example.com")
			if got := paramString(params, key); got != "operator.example.com" {
				t.Fatalf("explicit name replaced: %#v", params)
			}
			if key == "server_name" && params["sni"] != nil {
				t.Fatalf("explicit alias shadowed: %#v", params)
			}
			protocol := "anytls"
			rawBytes, _ := json.Marshal(map[string]interface{}{"password": "external", "cert-sha256": pin, key: "operator.example.com"})
			raw := string(rawBytes)
			_, _, password, external := resolveExitProtocol(model.Tunnel{}, &model.ExitNodeExternal{Protocol: &protocol, Config: &raw}, nil, nil)
			item := subProxy{Name: "external", Type: protocol, Server: "192.0.2.1", Port: 443, Password: password, Params: external}
			for format, output := range map[string]string{
				"clash": buildClashConfig([]subProxy{item}, nil),
				"surge": buildSurgeConfig([]subProxy{item}, nil, "6", ""),
				"uri":   buildAnyTLSURI(password, item, external),
				"qx":    buildQXConfig([]subProxy{item}, nil),
			} {
				if !strings.Contains(output, "operator.example.com") || !strings.Contains(output, pin) {
					t.Errorf("%s lost external SNI/pin: %s", format, output)
				}
			}
			probe, err := buildPinnedAnyTLSAgentOutbound(item, external)
			if err != nil || probe["tls"].(map[string]interface{})["server_name"] != "operator.example.com" {
				t.Fatalf("probe lost explicit SNI: %v %v", probe, err)
			}
		})
	}
	for _, raw := range []string{"", "malformed"} {
		t.Run("pkix-"+raw, func(t *testing.T) {
			params := applyAnyTLSVerifyDefaults(map[string]interface{}{"cert-sha256": raw}, "issued.example.com")
			if params["sni"] != "issued.example.com" || params["skip-cert-verify"] != false {
				t.Fatalf("PKIX lost name: %#v", params)
			}
		})
	}
}
