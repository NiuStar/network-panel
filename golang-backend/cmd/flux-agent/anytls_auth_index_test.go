package main

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestAnyTLSAuthIndexMatchesFirstRule(t *testing.T) {
	cfg := anytlsConfig{Password: "base", BaseUserID: 1}
	for id := int64(2); id <= 1001; id++ {
		cfg.Users = append(cfg.Users, anytlsUserRule{UserID: id, Password: fmt.Sprintf("user-%d", id)})
	}
	rules := buildAnyTLSRules(cfg)
	server := anytlsServer{authRules: rules, authIndex: buildAnyTLSIndex(rules)}
	for _, i := range []int{0, 500, 1000} {
		r, ok := server.matchRule(rules[i].hash)
		if !ok || r.userID != rules[i].userID {
			t.Fatalf("missing rule %d", i)
		}
	}
	if _, ok := server.matchRule([]byte("short")); ok {
		t.Fatal("malformed hash accepted")
	}
	missing := sha256.Sum256([]byte("unknown"))
	if _, ok := server.matchRule(missing[:]); ok {
		t.Fatal("unknown password accepted")
	}
	// Preserve first-match behavior when passwords collide.
	duplicate := append(rules, anytlsAuthRule{userID: 99999, hash: rules[0].hash})
	server.authIndex = buildAnyTLSIndex(duplicate)
	if r, ok := server.matchRule(rules[0].hash); !ok || r.userID != 1 {
		t.Fatal("duplicate password changed identity")
	}
}

func BenchmarkAnyTLSAuthLookup(b *testing.B) {
	cfg := anytlsConfig{Password: "base", BaseUserID: 1}
	for i := 0; i < 2000; i++ {
		cfg.Users = append(cfg.Users, anytlsUserRule{UserID: int64(i + 2), Password: fmt.Sprintf("user-%d", i)})
	}
	rules := buildAnyTLSRules(cfg)
	server := anytlsServer{authRules: rules, authIndex: buildAnyTLSIndex(rules)}
	hash := rules[len(rules)-1].hash
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := server.matchRule(hash); !ok {
			b.Fatal("lookup failed")
		}
	}
}
