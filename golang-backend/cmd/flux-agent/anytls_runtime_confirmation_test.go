package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Use the production report builder and queue, but never /etc certificate files.
func runtimeConfirmationFixture(t *testing.T) (string, string) {
	t.Helper()
	anytlsMu.Lock()
	oldConfigs, oldRuntimes := anytlsConfigs, anytlsRuntimes
	anytlsConfigs, anytlsRuntimes = map[int]anytlsConfig{}, map[int]*anytlsRuntime{}
	anytlsMu.Unlock()
	oldLogs := certLogCh
	oldProcessID := anyTLSProcessID
	anyTLSProcessID = "runtime-confirmation-test"
	certLogCh = make(chan map[string]any, 256)
	t.Cleanup(func() {
		anytlsMu.Lock()
		anytlsConfigs, anytlsRuntimes = oldConfigs, oldRuntimes
		anytlsMu.Unlock()
		certLogCh = oldLogs
		anyTLSProcessID = oldProcessID
	})
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "node.example.com"}, DNSNames: []string{"node.example.com"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	path := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(path, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return path, certFingerprintSHA256(string(certPEM))
}

func confirmationReport(t *testing.T, path string) map[string]any {
	t.Helper()
	emitAnyTLSInstalledCheckFromPaths("post_reload", "", 0, "", path, path)
	select {
	case report := <-certLogCh:
		if report["step"] != "installed_check" {
			t.Fatalf("unexpected report: %#v", report)
		}
		return report["data"].(map[string]any)
	default:
		t.Fatal("installed check did not enqueue report")
		return nil
	}
}

func TestAnyTLSBootstrapRetainsFailedDesiredPort(t *testing.T) {
	path, pin := runtimeConfirmationFixture(t)
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	failedPort := occupied.Addr().(*net.TCPAddr).Port
	available, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	goodPort := available.Addr().(*net.TCPAddr).Port
	available.Close()
	cfgs := map[int]anytlsConfig{failedPort: {Port: failedPort, Password: "test"}, goodPort: {Port: goodPort, Password: "test"}}
	listeners := map[int]net.Listener{}
	defer func() {
		for _, listener := range listeners {
			listener.Close()
		}
	}()
	// Bootstrap seam uses real bind failures, not production certificate paths
	// or AnyTLS TLS startup. startAnyTLS owns this same mutex in production.
	start := func(cfg anytlsConfig) error {
		anytlsMu.Lock()
		defer anytlsMu.Unlock()
		if len(anytlsConfigs) != len(cfgs) {
			t.Fatalf("desired configs missing before start: %d", len(anytlsConfigs))
		}
		if listeners[cfg.Port] != nil {
			return nil
		}
		listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
		if err != nil {
			return err
		}
		listeners[cfg.Port] = listener
		anytlsConfigs[cfg.Port] = cfg
		anytlsRuntimes[cfg.Port] = &anytlsRuntime{certFingerprint: pin, listeners: []net.Listener{listener}}
		return nil
	}
	if startSavedAnyTLSConfigs(cfgs, start) {
		t.Fatal("partially failed bootstrap reported success")
	}
	for _, phase := range []string{"post-connect", "periodic"} {
		data := confirmationReport(t, path)
		if data["matchLatest"] != false || data["configuredPorts"] != 2 {
			t.Fatalf("%s falsely confirmed partial bootstrap: %#v", phase, data)
		}
	}
	occupied.Close()
	if !startSavedAnyTLSConfigs(cfgs, start) {
		t.Fatal("retry failed after occupied port was released")
	}
	if data := confirmationReport(t, path); data["matchLatest"] != true || data["configuredPorts"] != 2 {
		t.Fatalf("fully loaded desired ports not confirmed: %#v", data)
	}
}

func TestAnyTLSInstalledReportConcurrentDesiredChanges(t *testing.T) {
	path, _ := runtimeConfirmationFixture(t)
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})
	defer func() { close(done); wg.Wait() }()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			anytlsMu.Lock()
			anytlsConfigs[8443] = anytlsConfig{Port: 8443}
			anytlsMu.Unlock()
			anytlsMu.Lock()
			delete(anytlsConfigs, 8443)
			anytlsMu.Unlock()
		}
	}()
	for i := 0; i < 1000; i++ {
		data := confirmationReport(t, path)
		if data["matchLatest"] != false {
			t.Fatal("missing runtimes falsely confirmed")
		}
		if n := data["configuredPorts"].(int); n != 0 && n != 1 {
			t.Fatalf("invalid captured port count: %d", n)
		}
	}
}
