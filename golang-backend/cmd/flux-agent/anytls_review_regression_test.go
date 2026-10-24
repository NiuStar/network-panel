package main

import (
	"strings"
	"testing"
)

func TestAnyTLSReviewAgentRawOverridesAndArrays(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	for _, key := range []string{"clash", "clash-meta", "sing-box", "surge", "uri", "url", "shadowrocket", "loon", "qx", "quantumult-x", "stash", "insecure", "allow-insecure"} {
		for depth := 0; depth < 4; depth++ {
			var nested interface{} = map[string]interface{}{key: true}
			for i := 0; i < depth; i++ {
				nested = []interface{}{nested}
			}
			values := map[string]interface{}{"cert-sha256": pin, "tls": map[string]interface{}{"children": nested}}
			if _, err := anyTLSClientCertificatePin(values); err == nil {
				t.Errorf("accepted %s at array depth %d", key, depth)
			}
		}
		if key != "insecure" && key != "allow-insecure" {
			if _, err := anyTLSClientCertificatePin(map[string]interface{}{"cert-sha256": pin, key: map[string]interface{}{}}); err == nil {
				t.Errorf("accepted root raw override %s", key)
			}
		}
	}
	if _, err := anyTLSClientCertificatePin(map[string]interface{}{"cert-sha256": pin, "children": []interface{}{[]interface{}{map[string]interface{}{"insecure": false}}}}); err != nil {
		t.Fatal(err)
	}
}
