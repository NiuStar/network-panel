package controller

import (
	"encoding/json"
	"network-panel/golang-backend/internal/app/model"
	"strings"
	"testing"
)

func TestAnyTLSReviewNestedArrayGuard(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	for _, key := range []string{"insecure", "allow-insecure", "clash", "sing-box", "qx", "fingerprint"} {
		bad := interface{}(true)
		if key == "fingerprint" {
			bad = pin
		}
		params := map[string]interface{}{"cert-sha256": pin, "tls": map[string]interface{}{"children": []interface{}{[]interface{}{[]interface{}{map[string]interface{}{key: bad}}}}}}
		if _, err := requiredAnyTLSCertificatePin(params); err == nil {
			t.Errorf("nested %s accepted", key)
		}
	}
	if _, err := requiredAnyTLSCertificatePin(map[string]interface{}{"cert-sha256": pin, "children": []interface{}{[]interface{}{map[string]interface{}{"insecure": false}}}}); err != nil {
		t.Fatal(err)
	}
}

func TestAnyTLSReviewQXDocumentedTLSFields(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	for _, sni := range []string{"example.invalid", ""} {
		line := buildQXLine("anytls", subProxy{Name: "review", Server: "192.0.2.1", Port: 443, Password: "test"}, map[string]interface{}{"cert-sha256": pin, "sni": sni})
		fields := map[string]string{}
		for _, field := range strings.Split(line, ",") {
			pair := strings.SplitN(strings.TrimSpace(field), "=", 2)
			if len(pair) == 2 {
				fields[pair[0]] = pair[1]
			}
		}
		if fields["over-tls"] != "true" || fields["tls-host"] != sni || fields["tls-verification"] != "true" || fields["tls-cert-sha256"] != pin {
			t.Fatalf("invalid documented QX TLS fields: %s", line)
		}
		if _, ok := fields["sni"]; ok {
			t.Fatal("unsupported sni key")
		}
	}
}

func TestAnyTLSReviewProbeExternalCredentialExact(t *testing.T) {
	const password = " \tu12:secret \t"
	proto := "anytls"
	rawBytes, _ := json.Marshal(map[string]interface{}{"password": password, "cert-sha256": strings.Repeat("ab", 32)})
	raw := string(rawBytes)
	_, _, pass, params := resolveExitProtocol(model.Tunnel{}, &model.ExitNodeExternal{Protocol: &proto, Config: &raw}, nil, nil)
	for _, itemPassword := range []string{pass, ""} {
		out, err := buildPinnedAnyTLSAgentOutbound(subProxy{Server: "192.0.2.1", Port: 443, Password: itemPassword}, params)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var received map[string]interface{}
		if err = json.Unmarshal(wire, &received); err != nil {
			t.Fatal(err)
		}
		if received["password"] != password {
			t.Fatalf("probe changed exact external credential: %q", received["password"])
		}
	}
	out, err := buildPinnedAnyTLSAgentOutbound(subProxy{Password: "u12:secret"}, map[string]interface{}{"cert-sha256": strings.Repeat("ab", 32)})
	if err != nil || out["password"] != "secret" {
		t.Fatalf("managed normalization changed: %v %v", out, err)
	}
}
