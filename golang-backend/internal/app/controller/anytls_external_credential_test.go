package controller

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"network-panel/golang-backend/internal/app/model"
)

func TestAnyTLSExternalCredentialPreservedAcrossRenderers(t *testing.T) {
	for _, external := range []bool{true, false} {
		name := "managed"
		want := "secret"
		params := map[string]interface{}{"password": "u12:secret", "cert-sha256": strings.Repeat("a", 64)}
		password := "u12:secret"
		if external {
			name, want = "external", "u12:secret"
			protocol := "anytls"
			config, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			raw := string(config)
			_, _, password, params = resolveExitProtocol(model.Tunnel{}, &model.ExitNodeExternal{Protocol: &protocol, Config: &raw}, nil, nil)
			if password != want {
				t.Fatalf("resolver changed external password: %q", password)
			}
		}
		t.Run(name, func(t *testing.T) {
			it := subProxy{Name: "credential-node", Type: "anytls", Server: "192.0.2.1", Port: 443, Password: password, Params: params}
			for uriName, raw := range map[string]string{
				"direct": buildAnyTLSURI(password, it, params),
				"v2ray":  buildV2rayURI("anytls", it, params),
			} {
				u, err := url.Parse(raw)
				if err != nil || u.User == nil || u.User.Username() != want {
					t.Errorf("%s password changed: %q (%v)", uriName, raw, err)
				}
			}
			clash, _ := renderClashProxies([]subProxy{it})
			rules, err := buildShadowrocketRuleConfig([]subProxy{it})
			if err != nil {
				t.Fatal(err)
			}
			for renderer, output := range map[string]string{
				"clash":              clash,
				"surge":              buildSurgeAnyTLSLine(it.Name, it.Server, it.Port, password, params),
				"qx":                 buildQXLine("anytls", it, params),
				"shadowrocket":       buildShadowrocket([]subProxy{it}, nil),
				"shadowrocket-rules": rules,
			} {
				if !strings.Contains(output, "password="+want) && !strings.Contains(output, "password: \""+want+"\"") && !strings.Contains(output, "anytls://"+url.QueryEscape(want)+"@") {
					t.Errorf("%s lost credential: %s", renderer, output)
				}
				if !external && strings.Contains(output, "u12:secret") {
					t.Errorf("%s changed managed prefix semantics", renderer)
				}
				if strings.Contains(output, "_anytls_external_credential") {
					t.Errorf("%s leaked internal marker", renderer)
				}
			}
		})
	}
}
