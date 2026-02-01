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
	"io"
	"log"
	"math/big"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	"golang.org/x/time/rate"
)

const (
	anytlsConfigPath = "/etc/gost/anytls.json"
	anytlsCertPath   = "/etc/gost/anytls_cert.pem"
	anytlsKeyPath    = "/etc/gost/anytls_key.pem"
)

type anytlsConfig struct {
	Port       int    `json:"port"`
	Password   string `json:"password"`
	BaseUserID int64  `json:"baseUserId,omitempty"`
	ExitIP     string `json:"exitIp,omitempty"`
	// AllowFallback enables IPv4/IPv6 fallback when exitIp family doesn't have DNS record.
	AllowFallback bool             `json:"allowFallback,omitempty"`
	Users         []anytlsUserRule `json:"users,omitempty"`
}

type anytlsUserRule struct {
	UserID   int64  `json:"userId"`
	Password string `json:"password"`
	SpeedBps int64  `json:"speedBps,omitempty"`
}

type anytlsAuthRule struct {
	userID   int64
	hash     []byte
	speedBps int64
}

type anytlsServer struct {
	tlsConfig     *tls.Config
	authRules     []anytlsAuthRule
	localTCPAddr  *net.TCPAddr
	localUDPAddr  *net.UDPAddr
	allowFallback bool
}

var (
	anytlsMu          sync.Mutex
	anytlsListeners   []net.Listener
	anytlsCancel      context.CancelFunc
	anytlsCurrent     anytlsConfig
	anytlsServerRef   *anytlsServer
	anytlsPanelAddr   string
	anytlsPanelSecret string
	anytlsPanelScheme string
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

func applyAnyTLSConfig(port int, password string, exitIP string, allowFallback bool, baseUserID int64, users []anytlsUserRule) error {
	cfg := anytlsConfig{
		Port:          port,
		Password:      password,
		BaseUserID:    baseUserID,
		ExitIP:        strings.TrimSpace(exitIP),
		AllowFallback: allowFallback,
		Users:         users,
	}
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
	if len(anytlsListeners) > 0 &&
		cfg.Port == anytlsCurrent.Port &&
		cfg.Password == anytlsCurrent.Password &&
		cfg.ExitIP == anytlsCurrent.ExitIP &&
		cfg.AllowFallback == anytlsCurrent.AllowFallback &&
		reflect.DeepEqual(cfg.Users, anytlsCurrent.Users) {
		return nil
	}
	stopAnyTLSLocked()

	var localTCP *net.TCPAddr
	var localUDP *net.UDPAddr
	if strings.TrimSpace(cfg.ExitIP) != "" {
		ip := net.ParseIP(strings.TrimSpace(cfg.ExitIP))
		if ip == nil {
			return fmt.Errorf("invalid exitIp")
		}
		if !isLocalIP(ip) {
			log.Printf("{\"event\":\"anytls_exitip_not_local\",\"exitIp\":%q}", cfg.ExitIP)
		} else {
			if ip4 := ip.To4(); ip4 != nil {
				ip = ip4
			} else if ip16 := ip.To16(); ip16 != nil {
				ip = ip16
			}
			localTCP = &net.TCPAddr{IP: ip}
			localUDP = &net.UDPAddr{IP: ip}
		}
	}

	cert, err := ensureAnyTLSCert()
	if err != nil {
		return err
	}
	lns, err := listenAnyTLSSockets(cfg.ExitIP, cfg.Port)
	if err != nil {
		return err
	}
	server := &anytlsServer{
		tlsConfig:     &tls.Config{Certificates: []tls.Certificate{*cert}},
		authRules:     buildAnyTLSRules(cfg),
		localTCPAddr:  localTCP,
		localUDPAddr:  localUDP,
		allowFallback: cfg.AllowFallback,
	}
	ctx, cancel := context.WithCancel(context.Background())
	anytlsListeners = lns
	anytlsCancel = cancel
	anytlsCurrent = cfg
	anytlsServerRef = server
	for _, ln := range lns {
		go anytlsAcceptLoop(ctx, ln, server)
	}
	listenerNets := make([]string, 0, len(lns))
	for _, ln := range lns {
		if ln == nil || ln.Addr() == nil {
			continue
		}
		listenerNets = append(listenerNets, ln.Addr().Network())
	}
	log.Printf("{\"event\":\"anytls_start\",\"port\":%d,\"exitIp\":%q,\"allowFallback\":%v,\"listeners\":%v}", cfg.Port, cfg.ExitIP, cfg.AllowFallback, listenerNets)
	return nil
}

func listenAnyTLSSockets(exitIP string, port int) ([]net.Listener, error) {
	tryV4 := true
	tryV6 := true
	var lns []net.Listener
	bind := func(network, addr string) {
		if ln, err := net.Listen(network, addr); err == nil {
			lns = append(lns, ln)
		} else {
			log.Printf("{\"event\":\"anytls_listen_err\",\"network\":%q,\"addr\":%q,\"error\":%q}", network, addr, err.Error())
		}
	}
	if tryV6 {
		bind("tcp6", fmt.Sprintf("[::]:%d", port))
	}
	if tryV4 {
		bind("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	}
	if len(lns) == 0 {
		return nil, fmt.Errorf("listen anytls failed on port %d", port)
	}
	return lns, nil
}

func isLocalIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		switch v := addr.(type) {
		case *net.IPNet:
			if v.IP.Equal(ip) {
				return true
			}
		case *net.IPAddr:
			if v.IP.Equal(ip) {
				return true
			}
		}
	}
	return false
}

func stopAnyTLSLocked() {
	if anytlsCancel != nil {
		anytlsCancel()
		anytlsCancel = nil
	}
	if len(anytlsListeners) > 0 {
		for _, ln := range anytlsListeners {
			if ln != nil {
				_ = ln.Close()
			}
		}
		anytlsListeners = nil
	}
}

func setAnyTLSPanelContext(addr, secret, scheme string) {
	anytlsPanelAddr = strings.TrimSpace(addr)
	anytlsPanelSecret = strings.TrimSpace(secret)
	anytlsPanelScheme = strings.TrimSpace(scheme)
}

func buildAnyTLSRules(cfg anytlsConfig) []anytlsAuthRule {
	rules := make([]anytlsAuthRule, 0, 1+len(cfg.Users))
	if cfg.Password != "" {
		sum := sha256.Sum256([]byte(cfg.Password))
		uid := cfg.BaseUserID
		if uid < 0 {
			uid = 0
		}
		rules = append(rules, anytlsAuthRule{userID: uid, hash: sum[:], speedBps: 0})
	}
	for _, u := range cfg.Users {
		pass := strings.TrimSpace(u.Password)
		if pass == "" {
			continue
		}
		sum := sha256.Sum256([]byte(pass))
		rules = append(rules, anytlsAuthRule{userID: u.UserID, hash: sum[:], speedBps: u.SpeedBps})
	}
	return rules
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
	rule, ok := s.matchRule(by)
	if err != nil || !ok {
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
			_ = s.proxyOutboundUoT(ctx, stream, destination, rule)
		} else {
			_ = s.proxyOutboundTCP(ctx, stream, destination, rule)
		}
	}, &padding.DefaultPaddingFactory)
	sess.Run()
	sess.Close()
}

func anytlsFallback(ctx context.Context, c net.Conn) {
	_ = ctx
	_ = c
}

func (s *anytlsServer) matchRule(hash []byte) (anytlsAuthRule, bool) {
	if len(hash) == 0 {
		return anytlsAuthRule{}, false
	}
	for _, r := range s.authRules {
		if bytes.Equal(hash, r.hash) {
			return r, true
		}
	}
	return anytlsAuthRule{}, false
}

func (s *anytlsServer) proxyOutboundTCP(ctx context.Context, conn net.Conn, destination M.Socksaddr, rule anytlsAuthRule) error {
	if s.localTCPAddr != nil {
		d := &net.Dialer{LocalAddr: s.localTCPAddr}
		if ip4 := s.localTCPAddr.IP.To4(); ip4 != nil {
			if destination.IsIPv6() {
				return fmt.Errorf("destination is ipv6 but exitIp is ipv4")
			}
			if destination.IsFqdn() {
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", destination.Fqdn)
				if err != nil || len(ips) == 0 {
					return fmt.Errorf("resolve ipv4 failed: %v", err)
				}
				var lastErr error
				for _, ip := range ips {
					addr := net.JoinHostPort(ip.String(), strconv.Itoa(int(destination.Port)))
					c, err := d.DialContext(ctx, "tcp4", addr)
					if err != nil {
						lastErr = err
						continue
					}
					if err = N.ReportHandshakeSuccess(conn); err != nil {
						return err
					}
					_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
					return copyErr
				}
				if lastErr != nil {
					lastErr = E.Errors(lastErr, N.ReportHandshakeFailure(conn, lastErr))
					return lastErr
				}
				return fmt.Errorf("resolve ipv4 failed")
			}
			c, err := d.DialContext(ctx, "tcp4", destination.String())
			if err != nil {
				err = E.Errors(err, N.ReportHandshakeFailure(conn, err))
				return err
			}
			if err = N.ReportHandshakeSuccess(conn); err != nil {
				return err
			}
				_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
				return copyErr
		}
		if ip6 := s.localTCPAddr.IP.To16(); ip6 != nil {
			if destination.IsIPv4() {
				if s.allowFallback {
					c, err := proxy.SystemDialer.DialContext(ctx, "tcp4", destination.String())
					if err != nil {
						err = E.Errors(err, N.ReportHandshakeFailure(conn, err))
						return err
					}
					if err = N.ReportHandshakeSuccess(conn); err != nil {
						return err
					}
						_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
						return copyErr
				}
				return fmt.Errorf("destination is ipv4 but exitIp is ipv6")
			}
			if destination.IsFqdn() {
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip6", destination.Fqdn)
				if err != nil || len(ips) == 0 {
					if s.allowFallback {
						ip4s, err4 := net.DefaultResolver.LookupIP(ctx, "ip4", destination.Fqdn)
						if err4 != nil || len(ip4s) == 0 {
							return fmt.Errorf("resolve ipv6 failed: %v", err)
						}
						var lastErr error
						for _, ip := range ip4s {
							addr := net.JoinHostPort(ip.String(), strconv.Itoa(int(destination.Port)))
							c, err := proxy.SystemDialer.DialContext(ctx, "tcp4", addr)
							if err != nil {
								lastErr = err
								continue
							}
							if err = N.ReportHandshakeSuccess(conn); err != nil {
								return err
							}
							_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
							return copyErr
						}
						if lastErr != nil {
							lastErr = E.Errors(lastErr, N.ReportHandshakeFailure(conn, lastErr))
							return lastErr
						}
						return fmt.Errorf("resolve ipv6 failed")
					}
					return fmt.Errorf("resolve ipv6 failed: %v", err)
				}
				var lastErr error
				for _, ip := range ips {
					addr := net.JoinHostPort(ip.String(), strconv.Itoa(int(destination.Port)))
					c, err := d.DialContext(ctx, "tcp6", addr)
					if err != nil {
						lastErr = err
						continue
					}
					if err = N.ReportHandshakeSuccess(conn); err != nil {
						return err
					}
					_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
					return copyErr
				}
				if lastErr != nil {
					lastErr = E.Errors(lastErr, N.ReportHandshakeFailure(conn, lastErr))
					return lastErr
				}
				return fmt.Errorf("resolve ipv6 failed")
			}
			c, err := d.DialContext(ctx, "tcp6", destination.String())
			if err != nil {
				err = E.Errors(err, N.ReportHandshakeFailure(conn, err))
				return err
			}
			if err = N.ReportHandshakeSuccess(conn); err != nil {
				return err
			}
				_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
				return copyErr
		}
		return fmt.Errorf("invalid exitIp")
	}
	c, err := proxy.SystemDialer.DialContext(ctx, "tcp", destination.String())
	if err != nil {
		err = E.Errors(err, N.ReportHandshakeFailure(conn, err))
		return err
	}

	if err = N.ReportHandshakeSuccess(conn); err != nil {
		return err
	}
		_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
		return copyErr
}

func (s *anytlsServer) proxyOutboundUoT(ctx context.Context, conn net.Conn, destination M.Socksaddr, rule anytlsAuthRule) error {
	request, err := uot.ReadRequest(conn)
	if err != nil {
		return err
	}

	addr := ""
	network := "udp"
	if s.localUDPAddr != nil {
		if ip4 := s.localUDPAddr.IP.To4(); ip4 != nil {
			if destination.IsIPv6() {
				return fmt.Errorf("destination is ipv6 but exitIp is ipv4")
			}
			network = "udp4"
			addr = s.localUDPAddr.String()
		} else if s.localUDPAddr.IP.To16() != nil {
			if destination.IsIPv4() {
				if s.allowFallback {
					network = "udp4"
					addr = ""
				} else {
					return fmt.Errorf("destination is ipv4 but exitIp is ipv6")
				}
			} else {
				network = "udp6"
				addr = s.localUDPAddr.String()
			}
			if destination.IsFqdn() {
				if network == "udp6" {
					ips, err := net.DefaultResolver.LookupIP(ctx, "ip6", destination.Fqdn)
					if err != nil || len(ips) == 0 {
						if s.allowFallback {
							ip4s, err4 := net.DefaultResolver.LookupIP(ctx, "ip4", destination.Fqdn)
							if err4 != nil || len(ip4s) == 0 {
								return fmt.Errorf("resolve ipv6 failed: %v", err)
							}
							addrIP, ok := ipToNetipAddr(ip4s[0])
							if !ok {
								return fmt.Errorf("invalid ipv4 address")
							}
							request.Destination = M.Socksaddr{Addr: addrIP, Port: destination.Port}
							network = "udp4"
							addr = ""
						} else {
							return fmt.Errorf("resolve ipv6 failed: %v", err)
						}
					} else {
						addrIP, ok := ipToNetipAddr(ips[0])
						if !ok {
							return fmt.Errorf("invalid ipv6 address")
						}
						request.Destination = M.Socksaddr{Addr: addrIP, Port: destination.Port}
					}
				} else if network == "udp4" {
					ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", destination.Fqdn)
					if err != nil || len(ips) == 0 {
						return fmt.Errorf("resolve ipv4 failed: %v", err)
					}
					addrIP, ok := ipToNetipAddr(ips[0])
					if !ok {
						return fmt.Errorf("invalid ipv4 address")
					}
					request.Destination = M.Socksaddr{Addr: addrIP, Port: destination.Port}
				}
			}
		}
	}
	c, err := net.ListenPacket(network, addr)
	if err != nil {
		err = E.Errors(err, N.ReportHandshakeFailure(conn, err))
		return err
	}

	if err = N.ReportHandshakeSuccess(conn); err != nil {
		return err
	}
	_, _, copyErr := copyPacketConnWithLimiter(ctx, uot.NewConn(conn, *request), bufio.NewPacketConn(c), rule.speedBps, rule.userID)
	return copyErr
}

func newRateLimiter(bps int64) (*rate.Limiter, int) {
	if bps <= 0 {
		return nil, 32 * 1024
	}
	burst := int(bps / 2)
	if burst < 4*1024 {
		burst = 4 * 1024
	}
	if burst > 1<<20 {
		burst = 1 << 20
	}
	return rate.NewLimiter(rate.Limit(bps), burst), burst
}

func copyConnWithLimiter(ctx context.Context, client net.Conn, remote net.Conn, bps int64, userID int64) (int64, int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	limUp, bufUp := newRateLimiter(bps)
	limDown, bufDown := newRateLimiter(bps)
	var inBytes int64
	var outBytes int64
	var reportedIn int64
	var reportedOut int64
	errCh := make(chan error, 2)
	stopCh := make(chan struct{})
	if userID > 0 {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					curIn := atomic.LoadInt64(&inBytes)
					curOut := atomic.LoadInt64(&outBytes)
					deltaIn := curIn - atomic.LoadInt64(&reportedIn)
					deltaOut := curOut - atomic.LoadInt64(&reportedOut)
					if deltaIn > 0 || deltaOut > 0 {
						reportAnyTLSFlow(userID, deltaIn, deltaOut)
						atomic.AddInt64(&reportedIn, deltaIn)
						atomic.AddInt64(&reportedOut, deltaOut)
					}
				case <-stopCh:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		errCh <- copyStreamLimited(ctx, remote, client, limUp, bufUp, &inBytes)
	}()
	go func() {
		errCh <- copyStreamLimited(ctx, client, remote, limDown, bufDown, &outBytes)
	}()
	err := <-errCh
	cancel()
	err2 := <-errCh
	close(stopCh)
	if userID > 0 {
		curIn := atomic.LoadInt64(&inBytes)
		curOut := atomic.LoadInt64(&outBytes)
		deltaIn := curIn - atomic.LoadInt64(&reportedIn)
		deltaOut := curOut - atomic.LoadInt64(&reportedOut)
		if deltaIn > 0 || deltaOut > 0 {
			reportAnyTLSFlow(userID, deltaIn, deltaOut)
		}
	}
	if err == nil {
		err = err2
	}
	return inBytes, outBytes, err
}

func copyStreamLimited(ctx context.Context, dst net.Conn, src net.Conn, limiter *rate.Limiter, bufSize int, counter *int64) error {
	if bufSize <= 0 {
		bufSize = 32 * 1024
	}
	buf := make([]byte, bufSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if limiter != nil {
				if err2 := limiter.WaitN(ctx, n); err2 != nil {
					return err2
				}
			}
			written := 0
			for written < n {
				wn, werr := dst.Write(buf[written:n])
				if wn > 0 {
					atomic.AddInt64(counter, int64(wn))
					written += wn
				}
				if werr != nil {
					return werr
				}
				if wn == 0 {
					break
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				N.CloseWrite(dst)
			}
			return err
		}
	}
}

func copyPacketConnWithLimiter(ctx context.Context, source N.PacketConn, destination N.PacketConn, bps int64, userID int64) (int64, int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		_ = source.Close()
		_ = destination.Close()
	}()
	limUp, _ := newRateLimiter(bps)
	limDown, _ := newRateLimiter(bps)
	var inBytes int64
	var outBytes int64
	var reportedIn int64
	var reportedOut int64
	errCh := make(chan error, 2)
	stopCh := make(chan struct{})
	if userID > 0 {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					curIn := atomic.LoadInt64(&inBytes)
					curOut := atomic.LoadInt64(&outBytes)
					deltaIn := curIn - atomic.LoadInt64(&reportedIn)
					deltaOut := curOut - atomic.LoadInt64(&reportedOut)
					if deltaIn > 0 || deltaOut > 0 {
						reportAnyTLSFlow(userID, deltaIn, deltaOut)
						atomic.AddInt64(&reportedIn, deltaIn)
						atomic.AddInt64(&reportedOut, deltaOut)
					}
				case <-stopCh:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		errCh <- copyPacketLimited(ctx, source, destination, limUp, &inBytes)
	}()
	go func() {
		errCh <- copyPacketLimited(ctx, destination, source, limDown, &outBytes)
	}()
	err := <-errCh
	cancel()
	err2 := <-errCh
	close(stopCh)
	if userID > 0 {
		curIn := atomic.LoadInt64(&inBytes)
		curOut := atomic.LoadInt64(&outBytes)
		deltaIn := curIn - atomic.LoadInt64(&reportedIn)
		deltaOut := curOut - atomic.LoadInt64(&reportedOut)
		if deltaIn > 0 || deltaOut > 0 {
			reportAnyTLSFlow(userID, deltaIn, deltaOut)
		}
	}
	if err == nil {
		err = err2
	}
	return inBytes, outBytes, err
}

func copyPacketLimited(ctx context.Context, source N.PacketReader, destination N.PacketWriter, limiter *rate.Limiter, counter *int64) error {
	options := N.NewReadWaitOptions(source, destination)
	for {
		buffer := options.NewPacketBuffer()
		dest, err := source.ReadPacket(buffer)
		if err != nil {
			buffer.Release()
			return err
		}
		dataLen := buffer.Len()
		if limiter != nil {
			if err := limiter.WaitN(ctx, dataLen); err != nil {
				buffer.Release()
				return err
			}
		}
		err = destination.WritePacket(buffer, dest)
		if err != nil {
			buffer.Leak()
			return err
		}
		atomic.AddInt64(counter, int64(dataLen))
	}
}

func reportAnyTLSFlow(userID int64, inBytes int64, outBytes int64) {
	if userID <= 0 || (inBytes <= 0 && outBytes <= 0) {
		return
	}
	if anytlsPanelAddr == "" || anytlsPanelSecret == "" {
		return
	}
	debug := strings.EqualFold(strings.TrimSpace(os.Getenv("ANYTLS_FLOW_DEBUG")), "1") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("ANYTLS_FLOW_DEBUG")), "true")
	payload := map[string]any{
		"userId":   userID,
		"inBytes":  inBytes,
		"outBytes": outBytes,
	}
	u := apiURL(anytlsPanelScheme, anytlsPanelAddr, "/api/v1/flow/anytls")
	u = u + "?secret=" + url.QueryEscape(anytlsPanelSecret)
	if debug {
		log.Printf("{\"event\":\"anytls_flow_report_try\",\"userId\":%d,\"inBytes\":%d,\"outBytes\":%d,\"url\":%q}", userID, inBytes, outBytes, u)
	}
	code, body, err := httpPostJSON(u, payload)
	if err != nil {
		log.Printf("{\"event\":\"anytls_flow_report_err\",\"error\":%q}", err.Error())
		return
	}
	if code/100 != 2 {
		log.Printf("{\"event\":\"anytls_flow_report_err\",\"code\":%d,\"body\":%q}", code, string(body))
		return
	}
	if debug {
		log.Printf("{\"event\":\"anytls_flow_report_ok\",\"code\":%d,\"body\":%q}", code, string(body))
	}
}

func ipToNetipAddr(ip net.IP) (netip.Addr, bool) {
	if ip == nil {
		return netip.Addr{}, false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return netip.AddrFrom4([4]byte{ip4[0], ip4[1], ip4[2], ip4[3]}), true
	}
	if ip16 := ip.To16(); ip16 != nil {
		var b [16]byte
		copy(b[:], ip16)
		return netip.AddrFrom16(b), true
	}
	return netip.Addr{}, false
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
