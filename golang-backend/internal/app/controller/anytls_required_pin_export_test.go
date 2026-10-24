package controller

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"network-panel/golang-backend/internal/app/model"
)

func TestRequiredPinExportRejectsUnsafeTrust(t *testing.T) {
	pin := strings.Repeat("a", 64)
	cases := map[string]map[string]interface{}{
		"missing": {},
		"invalid": {"cert-sha256": "wrong"},
		"nonstring": {"cert-sha256": 123},
		"conflict": {"cert-sha256": pin, "fingerprint": strings.Repeat("b", 64)},
		"invalid alias": {"cert-sha256": pin, "fingerprint": "invalid"},
		"hidden skip": {"cert-sha256": pin, "skip-cert-verify": false, "insecure": true},
		"nested skip": {"cert-sha256": pin, "tls": map[string]interface{}{"insecure": true}},
		"raw uri": {"cert-sha256": pin, "uri": "anytls://bad@host?insecure=1"},
		"raw surge": {"cert-sha256": pin, "surge": "unsafe = anytls, host, 443, skip-cert-verify=true"},
		"raw clash": {"cert-sha256": pin, "clash": map[string]interface{}{"skip-cert-verify": true}},
		"raw singbox": {"cert-sha256": pin, "singbox": `{"type":"anytls","tls":{"insecure":true}}`},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			it := subProxy{Name: "blocked", Group: "blocked-group", Type: "anytls", Server: "192.0.2.1", Port: 443, Password: "secret", Params: params}
			if got := buildAnyTLSURI(it.Password, it, params); got != "" { t.Errorf("URI accepted unsafe trust: %s", got) }
			if got := buildSurgeAnyTLSLine(it.Name, it.Server, it.Port, it.Password, params); got != "" { t.Errorf("Surge accepted unsafe trust: %s", got) }
			if got := buildQXLine("anytls", it, params); got != "" { t.Errorf("QX accepted unsafe trust: %s", got) }
			if got, groups := renderClashProxies([]subProxy{it}); strings.Contains(got, "type: anytls") || len(groups) != 0 { t.Errorf("Clash leaked node or ref: %s %#v", got, groups) }
			if got, groups := renderSurgeProxies([]subProxy{it}); got != "" || len(groups) != 0 { t.Errorf("Surge leaked node or ref: %s %#v", got, groups) }
			if ob := buildSingboxOutbound("anytls", it, params); len(ob) != 0 { t.Errorf("sing-box accepted unsafe trust: %#v", ob) }
		})
	}
}

func TestRequiredPinExportIPOnlyIPv6AndAliases(t *testing.T) {
	pin := strings.Repeat("a", 64)
	params := map[string]interface{}{"cert-sha256": strings.ToUpper(pin), "fingerprint": pin, "skip-cert-verify": false}
	it := subProxy{Name: "ip-only", Type: "anytls", Server: "2001:db8::1", Port: 443, Password: "secret", Params: params}
	raw := buildAnyTLSURI(it.Password, it, params)
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != it.Server || u.Port() != "443" || u.Query().Get("cert-sha256") != pin || u.Query().Get("insecure") != "0" || u.Query().Get("sni") != "" { t.Fatalf("invalid pin-only IPv6 URI: %q err=%v", raw, err) }
	got, groups := renderClashProxies([]subProxy{it})
	if !strings.Contains(got, "fingerprint: \""+pin+"\"") || !strings.Contains(got, "skip-cert-verify: false") || len(groups) == 0 { t.Fatalf("missing mandatory Clash leaf pin: %s", got) }
}

func TestRequiredPinSingboxDoesNotPretendCAIsPin(t *testing.T) {
	it := subProxy{Name: "pinned", Group: "pin-group", Type: "anytls", Server: "192.0.2.1", Port: 443, Password: "secret", Params: map[string]interface{}{"cert-sha256": strings.Repeat("a", 64), "ca-cert": "CA does not enforce a leaf pin"}}
	if ob := buildSingboxOutbound("anytls", it, it.Params); len(ob) != 0 { t.Fatalf("CA-only AnyTLS was exported: %#v", ob) }
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(buildSingboxConfig([]subProxy{it}, nil)), &cfg); err != nil { t.Fatal(err) }
	obs, ok := cfg["outbounds"].([]interface{})
	if !ok || len(obs) != 0 || cfg["__skipped__"] == nil { t.Fatalf("unsupported pin must be reported without orphan selectors: %#v", cfg) }
}

func TestRequiredPinV2rayAndQXRawCannotBypass(t *testing.T) {
	params := map[string]interface{}{"v2ray": "anytls://unsafe@host?insecure=1", "qx": "anytls=unsafe:443,tls-verification=false"}
	it := subProxy{Name: "blocked", Type: "anytls", Server: "192.0.2.1", Port: 443, Password: "secret", Params: params}
	raw, err := base64.StdEncoding.DecodeString(buildV2raySubscription([]subProxy{it}, nil))
	if err != nil || strings.Contains(string(raw), "anytls://") { t.Fatalf("raw v2ray bypass: %q %v", raw, err) }
	if got := buildQXLine("anytls", it, params); got != "" { t.Fatalf("raw QX bypass: %s", got) }
}

func TestRequiredPinExternalIdentityNotReplacedByEntry(t *testing.T) {
	proto := "anytls"
	config := `{"password":"secret","cert-sha256":"` + strings.Repeat("a", 64) + `"}`
	_, _, _, params := resolveExitProtocol(model.Tunnel{}, &model.ExitNodeExternal{Protocol: &proto, Config: &config}, nil, nil)
	if params["cert-sha256"] != strings.Repeat("a", 64) || params["sni"] != nil || params["skip-cert-verify"] != nil { t.Fatalf("imported pin-only identity was changed: %#v", params) }
}

func TestRequiredPinOtherProtocolRawStillWorks(t *testing.T) {
	it := subProxy{Name: "other", Type: "trojan"}
	if got := buildV2rayURI("trojan", it, map[string]interface{}{"uri": "trojan://unchanged"}); got != "trojan://unchanged" { t.Fatalf("unrelated raw URI changed: %s", got) }
}
