package controller

import (
	"strings"
	"testing"

	"network-panel/golang-backend/internal/app/model"
)

func TestAnyTLSPublicIPEndpointClassification(t *testing.T) {
	for _, host := range []string{"23.141.52.113", "8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		t.Run(host, func(t *testing.T) {
			if !isAnyTLSPublicIPEndpoint(host) {
				t.Fatalf("ordinary public literal rejected: %q", host)
			}
		})
	}
	for _, host := range []string{
		"", "relay.example.net", "not-an-address", "23.141.52.113:443", "[2606:4700:4700::1111]",
		"0.0.0.0", "0.1.2.3", "10.1.2.3", "100.64.0.1", "100.127.255.254", "127.0.0.1", "169.254.1.2", "172.16.0.1", "172.31.255.254", "192.168.1.2",
		"192.0.0.9", "192.0.2.1", "192.88.99.1", "198.18.0.1", "198.19.255.254", "198.51.100.9", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::ffff:23.141.52.113", "fc00::1", "fd00::1", "fe80::1", "ff02::1", "2606:4700::1%eth0", "64:ff9b::808:808", "100::1", "2001::1", "2001:2::1", "2001:20::1", "2001:db8::1", "2002:1700::1", "3ffe::1", "3fff::1", "4000::1",
	} {
		t.Run("nonpublic/"+host, func(t *testing.T) {
			if isAnyTLSPublicIPEndpoint(host) {
				t.Fatalf("non-target literal accepted: %q", host)
			}
		})
	}
}

func TestAnyTLSScopedDefaultsPreserveGenericAndExplicitNames(t *testing.T) {
	_, tun, _, pin := setupAnyTLSSNIScopeFixture(t, false)
	generic := applyAnyTLSVerifyDefaults(map[string]interface{}{"cert-sha256": pin}, "node.example.com")
	if generic["sni"] != "node.example.com" {
		t.Fatal("generic default silently opted into endpoint scope")
	}
	_, _, _, unknown := resolveExitProtocol(tun, nil, nil, map[int64]model.AnyTLSSetting{201: {Password: "secret"}})
	if unknown["sni"] != "node.example.com" {
		t.Fatal("resolver without final endpoint dropped legacy name")
	}
	for _, host := range []string{"23.141.52.113", "relay.example.net", "10.23.4.5", ""} {
		for _, key := range []string{"sni", "server_name"} {
			params := map[string]interface{}{"cert-sha256": pin, key: "operator.example.com"}
			for range 2 {
				params = applyManagedAnyTLSVerifyDefaults(params, "node.example.com", host)
			}
			if paramStringFirst(params, "sni", "server_name") != "operator.example.com" || params[key] != "operator.example.com" {
				t.Fatalf("explicit name lost: %v", params)
			}
		}
	}
	for _, raw := range []string{"", "malformed", strings.Repeat("ab", 31)} {
		params := applyManagedAnyTLSVerifyDefaults(map[string]interface{}{"cert-sha256": raw}, "node.example.com", "23.141.52.113")
		if params["sni"] != "node.example.com" {
			t.Fatal("invalid pin suppressed legacy name")
		}
		if _, err := requiredAnyTLSCertificatePin(params); err == nil {
			t.Fatal("invalid pin became trusted")
		}
		item := subProxy{Name: "bad-pin", Type: "anytls", Server: "23.141.52.113", Port: 443, Password: "secret", Params: params}
		if buildAnyTLSURI(item.Password, item, params) != "" {
			t.Fatal("invalid pin exported")
		}
		if _, err := buildPinnedAnyTLSAgentOutbound(item, params); err == nil {
			t.Fatal("invalid pin accepted by probe")
		}
	}
	params := map[string]interface{}{"cert-sha256": pin}
	for range 2 {
		params = applyManagedAnyTLSVerifyDefaults(params, "node.example.com", "23.141.52.113")
	}
	if params["sni"] != nil || params["server_name"] != nil {
		t.Fatal("repeated scoped defaults reintroduced name")
	}
}
