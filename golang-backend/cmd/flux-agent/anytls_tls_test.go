package main

import (
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAnyTLSInstalledCertValidityRejectsExpiredAndNotYetValid(t *testing.T) {
	now := time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		info *anytlsInstalledCertInfo
		want bool
	}{
		{"expired", &anytlsInstalledCertInfo{NotBeforeMS: now.Add(-90 * 24 * time.Hour).UnixMilli(), NotAfterMS: now.Add(-time.Second).UnixMilli()}, false},
		{"future", &anytlsInstalledCertInfo{NotBeforeMS: now.Add(time.Second).UnixMilli(), NotAfterMS: now.Add(24 * time.Hour).UnixMilli()}, false},
		{"current", &anytlsInstalledCertInfo{NotBeforeMS: now.Add(-time.Hour).UnixMilli(), NotAfterMS: now.Add(time.Hour).UnixMilli()}, true},
		{"missing", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyTLSInstalledCertValidAt(tc.info, now); got != tc.want {
				t.Fatalf("valid=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestAnyTLSWithoutControllerCertificateFailsClosed(t *testing.T) {
	_, err := ensureAnyTLSCert("node.example.com", false)
	if err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("unenforced AnyTLS unexpectedly accepted: %v", err)
	}
}

func TestAnyTLSRuntimeCertificateRequiresEveryConfiguredPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfgs := map[int]anytlsConfig{443: {Port: 443}, 8443: {Port: 8443}}
	rts := map[int]*anytlsRuntime{443: {certFingerprint: "pin", listeners: []net.Listener{listener}}}
	if anyTLSRuntimeCertificateMatches(cfgs, rts, "pin") {
		t.Fatal("missing configured listener accepted as fully loaded")
	}
	rts[8443] = &anytlsRuntime{certFingerprint: "pin", listeners: []net.Listener{listener}}
	if !anyTLSRuntimeCertificateMatches(cfgs, rts, "pin") {
		t.Fatal("fully loaded configured listeners rejected")
	}
	rts[8443].certFingerprint = "different"
	if anyTLSRuntimeCertificateMatches(cfgs, rts, "pin") {
		t.Fatal("mismatched listener accepted")
	}
}

func TestAnyTLSServerTLSConfigIsHardened(t *testing.T) {
	cfg := newAnyTLSServerTLSConfig(tls.Certificate{})
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %#x, want TLS1.2", cfg.MinVersion)
	}
	if !cfg.SessionTicketsDisabled {
		t.Fatalf("server TLS session tickets should be disabled by default")
	}
	if len(cfg.CipherSuites) == 0 {
		t.Fatalf("expected explicit TLS1.2 cipher allowlist")
	}
	if len(cfg.CurvePreferences) == 0 || cfg.CurvePreferences[0] != tls.X25519 {
		t.Fatalf("expected X25519-first curve preferences, got %#v", cfg.CurvePreferences)
	}
	if len(cfg.NextProtos) != 2 || cfg.NextProtos[0] != "h2" || cfg.NextProtos[1] != "http/1.1" {
		t.Fatalf("unexpected ALPN list: %#v", cfg.NextProtos)
	}
}
