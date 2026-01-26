package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"anytls/proxy"
	"anytls/proxy/padding"
	"anytls/proxy/session"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/uot"
)

const (
	anytlsConfigPath = "/etc/gost/anytls.json"
	anytlsCertPath   = "/etc/gost/anytls_cert.pem"
	anytlsKeyPath    = "/etc/gost/anytls_key.pem"
)

type anytlsConfig struct {
	Port     int    `json:"port"`
	Password string `json:"password"`
}

type anytlsServer struct {
	tlsConfig     *tls.Config
	passwordSha256 []byte
}

var (
	anytlsMu       sync.Mutex
	anytlsListener net.Listener
	anytlsCancel   context.CancelFunc
	anytlsCurrent  anytlsConfig
)

func loadAnyTLSConfig() (anytlsConfig, bool) {
	var cfg anytlsConfig
	b, err := os.ReadFile(anytlsConfigPath)
	if err != nil {
		return cfg, false
	}
	if json.Unmarshal(b, &cfg) != nil {
		return cfg, false
	}
	if cfg.Port <= 0 || cfg.Port > 65535 || cfg.Password == "" {
		return cfg, false
	}
	return cfg, true
}

func saveAnyTLSConfig(cfg anytlsConfig) error {
	if err := os.MkdirAll(filepath.Dir(anytlsConfigPath), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(anytlsConfigPath, b, 0o600)
}

func applyAnyTLSConfig(port int, password string) error {
	cfg := anytlsConfig{Port: port, Password: password}
	if err := startAnyTLS(cfg); err != nil {
		return err
	}
	return saveAnyTLSConfig(cfg)
}

func startAnyTLS(cfg anytlsConfig) error {
	if cfg.Port <= 0 || cfg.Port > 65535 || cfg.Password == "" {
		return fmt.Errorf("invalid anytls config")
	}
	anytlsMu.Lock()
	defer anytlsMu.Unlock()
	if anytlsListener != nil && cfg.Port == anytlsCurrent.Port && cfg.Password == anytlsCurrent.Password {
		return nil
	}
	stopAnyTLSLocked()

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return err
	}
	cert, err := ensureAnyTLSCert()
	if err != nil {
		_ = ln.Close()
		return err
	}
	sum := sha256.Sum256([]byte(cfg.Password))
	server := &anytlsServer{
		tlsConfig:     &tls.Config{Certificates: []tls.Certificate{*cert}},
		passwordSha256: sum[:],
	}
	ctx, cancel := context.WithCancel(context.Background())
	anytlsListener = ln
	anytlsCancel = cancel
	anytlsCurrent = cfg
	go anytlsAcceptLoop(ctx, ln, server)
	log.Printf("{\"event\":\"anytls_start\",\"port\":%d}", cfg.Port)
	return nil
}

func stopAnyTLSLocked() {
	if anytlsCancel != nil {
		anytlsCancel()
		anytlsCancel = nil
	}
	if anytlsListener != nil {
		_ = anytlsListener.Close()
		anytlsListener = nil
	}
}

func anytlsAcceptLoop(ctx context.Context, ln net.Listener, s *anytlsServer) {
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("{\"event\":\"anytls_accept_err\",\"error\":%q}", err.Error())
			continue
		}
		go s.handleConn(ctx, c)
	}
}

func (s *anytlsServer) handleConn(ctx context.Context, c net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("{\"event\":\"anytls_panic\",\"error\":%q}", fmt.Sprint(r))
			log.Printf("{\"event\":\"anytls_panic_stack\",\"stack\":%q}", string(debug.Stack()))
		}
	}()

	c = tls.Server(c, s.tlsConfig)
	defer c.Close()

	b := buf.NewPacket()
	defer b.Release()

	n, err := b.ReadOnceFrom(c)
	if err != nil {
		return
	}
	c = bufio.NewCachedConn(c, b)

	by, err := b.ReadBytes(32)
	if err != nil || !bytes.Equal(by, s.passwordSha256) {
		b.Resize(0, n)
		anytlsFallback(ctx, c)
		return
	}
	by, err = b.ReadBytes(2)
	if err != nil {
		b.Resize(0, n)
		anytlsFallback(ctx, c)
		return
	}
	paddingLen := binary.BigEndian.Uint16(by)
	if paddingLen > 0 {
		_, err = b.ReadBytes(int(paddingLen))
		if err != nil {
			b.Resize(0, n)
			anytlsFallback(ctx, c)
			return
		}
	}

	sess := session.NewServerSession(c, func(stream *session.Stream) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("{\"event\":\"anytls_stream_panic\",\"error\":%q}", fmt.Sprint(r))
				log.Printf("{\"event\":\"anytls_stream_panic_stack\",\"stack\":%q}", string(debug.Stack()))
			}
		}()
		defer stream.Close()

		destination, err := M.SocksaddrSerializer.ReadAddrPort(stream)
		if err != nil {
			return
		}

		if strings.Contains(destination.String(), "udp-over-tcp.arpa") {
			_ = anytlsProxyOutboundUoT(ctx, stream, destination)
		} else {
			_ = anytlsProxyOutboundTCP(ctx, stream, destination)
		}
	}, &padding.DefaultPaddingFactory)
	sess.Run()
	sess.Close()
}

func anytlsFallback(ctx context.Context, c net.Conn) {
	_ = ctx
	_ = c
}

func anytlsProxyOutboundTCP(ctx context.Context, conn net.Conn, destination M.Socksaddr) error {
	c, err := proxy.SystemDialer.DialContext(ctx, "tcp", destination.String())
	if err != nil {
		err = E.Errors(err, N.ReportHandshakeFailure(conn, err))
		return err
	}

	if err = N.ReportHandshakeSuccess(conn); err != nil {
		return err
	}
	return bufio.CopyConn(ctx, conn, c)
}

func anytlsProxyOutboundUoT(ctx context.Context, conn net.Conn, destination M.Socksaddr) error {
	request, err := uot.ReadRequest(conn)
	if err != nil {
		return err
	}

	c, err := net.ListenPacket("udp", "")
	if err != nil {
		err = E.Errors(err, N.ReportHandshakeFailure(conn, err))
		return err
	}

	if err = N.ReportHandshakeSuccess(conn); err != nil {
		return err
	}
	return bufio.CopyPacketConn(ctx, uot.NewConn(conn, *request), bufio.NewPacketConn(c))
}

func ensureAnyTLSCert() (*tls.Certificate, error) {
	if certPEM, err := os.ReadFile(anytlsCertPath); err == nil {
		if keyPEM, err := os.ReadFile(anytlsKeyPath); err == nil {
			if pair, err := tls.X509KeyPair(certPEM, keyPEM); err == nil {
				return &pair, nil
			}
		}
	}

	certPEM, keyPEM, err := generateSelfSignedCert("anytls")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(anytlsCertPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(anytlsCertPath, certPEM, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(anytlsKeyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &pair, nil
}

func generateSelfSignedCert(commonName string) ([]byte, []byte, error) {
	if commonName == "" {
		commonName = "anytls"
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serialNumber,
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(3650 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		DNSNames: []string{commonName},
	}
	publicDer, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, nil, err
	}
	privateDer, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	publicPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: publicDer})
	privPem := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDer})
	return publicPem, privPem, nil
}
