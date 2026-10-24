package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func anyTLSClientPinTestCertificate(t *testing.T) (tls.Certificate, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// Deliberately unrelated to the dial address: pinning does not require PKIX/SNI.
		DNSNames: []string{"unrelated.invalid"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(der)
	spki := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(pin[:]), hex.EncodeToString(spki[:])
}

func TestAnyTLSClientPinAliasesAndFailClosed(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	for _, alias := range []string{"cert-sha256", "cert_sha256", "fingerprint", "fingerprint-sha256", "fingerprint_sha256"} {
		for _, nested := range []bool{false, true} {
			values := map[string]interface{}{alias: "SHA256:" + strings.TrimSuffix(strings.Repeat("AB:", 32), ":")}
			if nested {
				values = map[string]interface{}{"tls": values}
			}
			got, err := anyTLSClientCertificatePin(values)
			if err != nil || hex.EncodeToString(got) != pin {
				t.Fatalf("alias %s nested=%v: %x %v", alias, nested, got, err)
			}
		}
	}
	for name, values := range map[string]map[string]interface{}{
		"missing":                         {},
		"client fingerprint is not a pin": {"client-fingerprint": "chrome"},
		"wrong type":                      {"cert-sha256": 123},
		"short":                           {"cert-sha256": "ab"},
		"invalid hex":                     {"cert-sha256": strings.Repeat("z", 64)},
		"nil alias":                       {"cert-sha256": pin, "fingerprint": nil},
		"conflicting alias":               {"cert-sha256": pin, "fingerprint": strings.Repeat("cd", 32)},
		"conflicting nested alias":        {"cert-sha256": pin, "tls": map[string]interface{}{"cert_sha256": strings.Repeat("cd", 32)}},
		"insecure":                        {"cert-sha256": pin, "insecure": true},
		"skip verify":                     {"cert-sha256": pin, "skip-cert-verify": true},
		"nested insecure":                 {"cert-sha256": pin, "tls": map[string]interface{}{"insecure": true}},
		"nested allow insecure":           {"cert-sha256": pin, "tls": map[string]interface{}{"allow-insecure": "true"}},
		"malformed insecure":              {"cert-sha256": pin, "insecure": nil},
		"TLS disabled":                    {"cert-sha256": pin, "tls": false},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := anyTLSClientCertificatePin(values); err == nil {
				t.Fatal("unsafe trust accepted")
			}
		})
	}
	if _, err := anyTLSClientCertificatePin(map[string]interface{}{"cert-sha256": pin, "fingerprint": strings.ToUpper(pin), "tls": map[string]interface{}{"insecure": false}}); err != nil {
		t.Fatal(err)
	}
}

func TestAnyTLSClientMissingPinDoesNotDial(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dialAnyTLSStream(ctx, map[string]interface{}{"server": "127.0.0.1", "server_port": address.Port, "password": " \tu12:synthetic-pin-test \t"}, "tcp", "example.invalid:80", "")
	if conn != nil {
		conn.Close()
		t.Fatal("missing pin returned a connection")
	}
	if err == nil || !strings.Contains(err.Error(), "requires a leaf certificate") {
		t.Fatalf("missing pin error: %v", err)
	}
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	unexpected, err := listener.Accept()
	if unexpected != nil {
		unexpected.Close()
		t.Fatal("missing pin initiated TCP")
	}
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("accept error: %v", err)
	}
}

// Exercises the actual dialer against real TLS. The positive case checks the
// authenticated preface, not an end-to-end AnyTLS proxy: an intentionally invalid
// destination stops the client immediately after its session is created.
func TestAnyTLSClientRealTLSCertificatePinBeforeAuthentication(t *testing.T) {
	cert, pin, spki := anyTLSClientPinTestCertificate(t)
	for _, tc := range []struct {
		name, pin     string
		authenticated bool
	}{
		{"correct leaf DER pin", pin, true},
		{"wrong pin", strings.Repeat("00", 32), false},
		{"SPKI is not leaf DER", spki, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			type observation struct {
				auth         []byte
				handshakeErr error
			}
			observed := make(chan observation, 1)
			go func() {
				raw, err := listener.Accept()
				if err != nil {
					observed <- observation{handshakeErr: err}
					return
				}
				defer raw.Close()
				_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
				server := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				handshakeErr := server.Handshake()
				buf := make([]byte, 34)
				n, _ := io.ReadFull(server, buf)
				observed <- observation{auth: buf[:n], handshakeErr: handshakeErr}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			outbound := map[string]interface{}{"server": "127.0.0.1", "server_port": listener.Addr().(*net.TCPAddr).Port, "password": " \tu12:synthetic-pin-test \t", "cert-sha256": tc.pin}
			conn, dialErr := dialAnyTLSStream(ctx, outbound, "tcp", "intentionally-invalid-destination", "")
			if conn != nil {
				conn.Close()
				t.Fatal("invalid destination unexpectedly succeeded")
			}
			if dialErr == nil {
				t.Fatal("expected pin mismatch or intentional destination error")
			}
			var result observation
			select {
			case result = <-observed:
			case <-ctx.Done():
				t.Fatal("TLS server did not finish")
			}
			if tc.authenticated {
				if result.handshakeErr != nil {
					t.Fatalf("correct pin TLS failed: %v", result.handshakeErr)
				}
				if !strings.Contains(dialErr.Error(), "missing port") {
					t.Fatalf("correct pin did not reach destination parsing: %v", dialErr)
				}
				hash := sha256.Sum256([]byte(" \tu12:synthetic-pin-test \t"))
				want := append(hash[:], 0, 0)
				if !bytes.Equal(result.auth, want) {
					t.Fatalf("correct pin authentication preface: got %x", result.auth)
				}
			} else {
				if !strings.Contains(dialErr.Error(), "pin mismatch") {
					t.Fatalf("wrong pin error: %v", dialErr)
				}
				if len(result.auth) != 0 {
					t.Fatalf("wrong pin leaked %d authentication bytes", len(result.auth))
				}
			}
		})
	}
}
