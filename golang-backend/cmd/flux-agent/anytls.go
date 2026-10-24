package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
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
	anytlsCertPath   = "/etc/gost/docker_site_cert.pem"
	anytlsKeyPath    = "/etc/gost/docker_site_key.pem"
	anytlsCAPath     = "/etc/gost/docker_site_ca.pem"
	anytlsMetaPath   = "/etc/gost/docker_site_cert_meta.json"

	legacyAnyTLSCertPath = "/etc/gost/anytls_cert.pem"
	legacyAnyTLSKeyPath  = "/etc/gost/anytls_key.pem"
	legacyAnyTLSCAPath   = "/etc/gost/anytls_ca.pem"
	legacyAnyTLSMetaPath = "/etc/gost/anytls_cert_meta.json"
)

type anytlsConfig struct {
	Port       int    `json:"port"`
	Password   string `json:"password"`
	BaseUserID int64  `json:"baseUserId,omitempty"`
	ExitIP     string `json:"exitIp,omitempty"`
	CertDomain string `json:"certDomain,omitempty"`
	// EnforceVerify requires controller-issued cert files to be present.
	EnforceVerify bool `json:"enforceVerify,omitempty"`
	// AllowFallback enables IPv4/IPv6 fallback when exitIp family doesn't have DNS record.
	AllowFallback bool             `json:"allowFallback,omitempty"`
	Users         []anytlsUserRule `json:"users,omitempty"`
}

type anytlsConfigFile struct {
	Port          int              `json:"port,omitempty"`
	Password      string           `json:"password,omitempty"`
	BaseUserID    int64            `json:"baseUserId,omitempty"`
	ExitIP        string           `json:"exitIp,omitempty"`
	CertDomain    string           `json:"certDomain,omitempty"`
	EnforceVerify bool             `json:"enforceVerify,omitempty"`
	AllowFallback bool             `json:"allowFallback,omitempty"`
	Users         []anytlsUserRule `json:"users,omitempty"`
	Instances     []anytlsConfig   `json:"instances,omitempty"`
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
	tlsConfig        *tls.Config
	authRules        []anytlsAuthRule
	authIndex        map[[sha256.Size]byte]anytlsAuthRule
	localTCPAddr     *net.TCPAddr
	localUDPAddr     *net.UDPAddr
	certDomain       string
	allowFallback    bool
	fallbackMode     string
	connSem          chan struct{}
	handshakeTO      time.Duration
	acceptBackoff    time.Duration
	acceptBackoffMax time.Duration
	activeConns      int64
	rejectedConns    uint64
	acceptErrs       uint64
	sessionSeen      uint64
	authFailSeen     uint64
}

type anytlsClientSettingReader interface {
	ClientSetting(key string) string
}

type anytlsRuntime struct {
	listeners       []net.Listener
	cancel          context.CancelFunc
	current         anytlsConfig
	certFingerprint string
}

type anytlsFlowDelta struct {
	userID   int64
	inBytes  int64
	outBytes int64
}

type anytlsCertMeta struct {
	Domain      string `json:"domain,omitempty"`
	NotBeforeMS int64  `json:"notBeforeMs,omitempty"`
	NotAfterMS  int64  `json:"notAfterMs,omitempty"`
}

type anytlsInstalledCertInfo struct {
	Hash        string
	Subject     string
	Issuer      string
	Serial      string
	CommonName  string
	DNSNames    []string
	NotBeforeMS int64
	NotAfterMS  int64
}

var (
	anytlsMu          sync.Mutex
	anytlsRuntimes    = map[int]*anytlsRuntime{}
	anytlsConfigs     = map[int]anytlsConfig{}
	anytlsPanelAddr   string
	anytlsPanelSecret string
	anytlsPanelScheme string
	anytlsFlowOnce    sync.Once
	anytlsFlowCh      chan anytlsFlowDelta
	anytlsNAT64Once   sync.Once
	anytlsNAT64Prefs  []netip.Prefix
	anytlsTuneOnce    sync.Once
	anytlsTune        anytlsTuning
)

type anytlsTuning struct {
	maxConns         int
	handshakeTO      time.Duration
	acceptBackoff    time.Duration
	acceptBackoffMax time.Duration
	egressStrict     bool
}

func getenvInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func loadAnyTLSTuning() anytlsTuning {
	anytlsTuneOnce.Do(func() {
		t := anytlsTuning{
			maxConns:         8192,
			handshakeTO:      30 * time.Second,
			acceptBackoff:    50 * time.Millisecond,
			acceptBackoffMax: time.Second,
			egressStrict:     false,
		}
		if n := getenvInt("ANYTLS_MAX_CONNS", t.maxConns); n > 0 {
			t.maxConns = n
		}
		if sec := getenvInt("ANYTLS_HANDSHAKE_TIMEOUT_SEC", int(t.handshakeTO/time.Second)); sec >= 0 {
			t.handshakeTO = time.Duration(sec) * time.Second
		}
		if ms := getenvInt("ANYTLS_ACCEPT_BACKOFF_MS", int(t.acceptBackoff/time.Millisecond)); ms > 0 {
			t.acceptBackoff = time.Duration(ms) * time.Millisecond
		}
		if ms := getenvInt("ANYTLS_ACCEPT_BACKOFF_MAX_MS", int(t.acceptBackoffMax/time.Millisecond)); ms > 0 {
			t.acceptBackoffMax = time.Duration(ms) * time.Millisecond
		}
		if t.acceptBackoffMax < t.acceptBackoff {
			t.acceptBackoffMax = t.acceptBackoff
		}
		if v := strings.TrimSpace(strings.ToLower(os.Getenv("ANYTLS_EGRESS_STRICT"))); v != "" {
			switch v {
			case "1", "true", "yes", "on":
				t.egressStrict = true
			case "0", "false", "no", "off":
				t.egressStrict = false
			}
		}
		anytlsTune = t
		log.Printf("{\"event\":\"edge_tuning\",\"maxConns\":%d,\"handshakeTimeoutSec\":%d,\"acceptBackoffMs\":%d,\"acceptBackoffMaxMs\":%d,\"egressStrict\":%v}",
			t.maxConns,
			int(t.handshakeTO/time.Second),
			int(t.acceptBackoff/time.Millisecond),
			int(t.acceptBackoffMax/time.Millisecond),
			t.egressStrict,
		)
	})
	return anytlsTune
}

func startAnyTLSFlowWorker() {
	anytlsFlowOnce.Do(func() {
		anytlsFlowCh = make(chan anytlsFlowDelta, 4096)
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			pending := map[int64]anytlsFlowDelta{}
			flush := func() {
				if len(pending) == 0 {
					return
				}
				for uid, d := range pending {
					reportAnyTLSFlow(uid, d.inBytes, d.outBytes)
					delete(pending, uid)
				}
			}
			for {
				select {
				case d := <-anytlsFlowCh:
					if d.userID <= 0 || (d.inBytes <= 0 && d.outBytes <= 0) {
						continue
					}
					cur := pending[d.userID]
					cur.userID = d.userID
					cur.inBytes += d.inBytes
					cur.outBytes += d.outBytes
					pending[d.userID] = cur
				case <-ticker.C:
					flush()
				}
			}
		}()
	})
}

func enqueueAnyTLSFlow(userID int64, inBytes int64, outBytes int64) {
	if userID <= 0 || (inBytes <= 0 && outBytes <= 0) {
		return
	}
	if anytlsPanelAddr == "" || anytlsPanelSecret == "" {
		return
	}
	startAnyTLSFlowWorker()
	select {
	case anytlsFlowCh <- anytlsFlowDelta{userID: userID, inBytes: inBytes, outBytes: outBytes}:
	default:
		// Backpressure protection: drop oversized burst instead of blocking data path.
		log.Printf("{\"event\":\"edge_flow_drop\",\"userId\":%d,\"inBytes\":%d,\"outBytes\":%d}", userID, inBytes, outBytes)
	}
}

func emitAnyTLSRuntimeLog(step, message string, data map[string]any) {
	step = strings.TrimSpace(step)
	if step == "" {
		return
	}
	emitOpLog("anytls_"+step, message, data)
}

func shouldSampleAnyTLS(counter uint64, warmup uint64, every uint64) bool {
	if counter <= warmup {
		return true
	}
	if every <= 1 {
		return true
	}
	return counter%every == 0
}

func safeRemoteAddr(c net.Conn) string {
	if c == nil || c.RemoteAddr() == nil {
		return ""
	}
	return c.RemoteAddr().String()
}

func safeLocalAddr(c net.Conn) string {
	if c == nil || c.LocalAddr() == nil {
		return ""
	}
	return c.LocalAddr().String()
}

func classifyAnyTLSError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "eof"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "connection reset by peer"):
		return "conn_reset_by_peer"
	case strings.Contains(msg, "broken pipe"):
		return "broken_pipe"
	case strings.Contains(msg, "connection refused"):
		return "conn_refused"
	case strings.Contains(msg, "network is unreachable"):
		return "net_unreachable"
	case strings.Contains(msg, "no route to host"):
		return "no_route"
	case strings.Contains(msg, "i/o timeout"):
		return "io_timeout"
	case strings.Contains(msg, "tls:"):
		return "tls"
	case strings.Contains(msg, "handshake"):
		return "handshake"
	}
	return "other"
}

func anytlsErrorFields(err error) map[string]any {
	if err == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"error":     err.Error(),
		"kind":      classifyAnyTLSError(err),
		"errorType": fmt.Sprintf("%T", err),
	}
	var ne net.Error
	if errors.As(err, &ne) {
		out["timeout"] = ne.Timeout()
		out["temporary"] = ne.Temporary()
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		if strings.TrimSpace(oe.Op) != "" {
			out["op"] = oe.Op
		}
		if strings.TrimSpace(oe.Net) != "" {
			out["network"] = oe.Net
		}
		if oe.Source != nil {
			out["source"] = oe.Source.String()
		}
		if oe.Addr != nil {
			out["addr"] = oe.Addr.String()
		}
	}
	return out
}

func mergeAny(base map[string]any, extras ...map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	for _, ex := range extras {
		for k, v := range ex {
			base[k] = v
		}
	}
	return base
}

func tlsVersionText(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLS1.3"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS10:
		return "TLS1.0"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

func tlsVersionsText(vs []uint16) []string {
	if len(vs) == 0 {
		return nil
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, tlsVersionText(v))
	}
	return out
}

func loadAnyTLSConfigs() (map[int]anytlsConfig, bool) {
	out := map[int]anytlsConfig{}
	var fileCfg anytlsConfigFile
	b, err := os.ReadFile(anytlsConfigPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("{\"event\":\"edge_config_read_err\",\"path\":%q,\"error\":%q}", anytlsConfigPath, err.Error())
		}
		return out, false
	}
	if err := json.Unmarshal(b, &fileCfg); err != nil {
		log.Printf("{\"event\":\"edge_config_parse_err\",\"path\":%q,\"error\":%q}", anytlsConfigPath, err.Error())
		return out, false
	}
	defaultAllowFallback := !bytes.Contains(b, []byte(`"allowFallback"`))
	for _, cfg := range fileCfg.Instances {
		if cfg.Port <= 0 || cfg.Port > 65535 || strings.TrimSpace(cfg.Password) == "" {
			continue
		}
		if defaultAllowFallback {
			cfg.AllowFallback = true
		}
		cfg.ExitIP = strings.TrimSpace(cfg.ExitIP)
		// Backward compatible default: old configs with certDomain imply managed cert verify.
		if !bytes.Contains(b, []byte(`"enforceVerify"`)) && strings.TrimSpace(cfg.CertDomain) != "" {
			cfg.EnforceVerify = true
		}
		out[cfg.Port] = cfg
	}
	// backward compatibility for old single-instance schema
	if fileCfg.Port > 0 && fileCfg.Port <= 65535 && strings.TrimSpace(fileCfg.Password) != "" {
		cfg := anytlsConfig{
			Port:          fileCfg.Port,
			Password:      fileCfg.Password,
			BaseUserID:    fileCfg.BaseUserID,
			ExitIP:        strings.TrimSpace(fileCfg.ExitIP),
			CertDomain:    strings.TrimSpace(fileCfg.CertDomain),
			EnforceVerify: fileCfg.EnforceVerify,
			AllowFallback: fileCfg.AllowFallback || defaultAllowFallback,
			Users:         fileCfg.Users,
		}
		if !bytes.Contains(b, []byte(`"enforceVerify"`)) && cfg.CertDomain != "" {
			cfg.EnforceVerify = true
		}
		out[cfg.Port] = cfg
	}
	if len(out) == 0 {
		log.Printf("{\"event\":\"edge_config_empty\",\"path\":%q}", anytlsConfigPath)
		return out, false
	}
	return out, true
}

func saveAnyTLSConfigs(cfgs map[int]anytlsConfig) error {
	if err := os.MkdirAll(filepath.Dir(anytlsConfigPath), 0o755); err != nil {
		return err
	}
	fileCfg := anytlsConfigFile{}
	ports := make([]int, 0, len(cfgs))
	for p := range cfgs {
		if p > 0 {
			ports = append(ports, p)
		}
	}
	sort.Ints(ports)
	if len(ports) == 1 {
		cfg := cfgs[ports[0]]
		fileCfg.Port = cfg.Port
		fileCfg.Password = cfg.Password
		fileCfg.BaseUserID = cfg.BaseUserID
		fileCfg.ExitIP = strings.TrimSpace(cfg.ExitIP)
		fileCfg.CertDomain = strings.TrimSpace(cfg.CertDomain)
		fileCfg.EnforceVerify = cfg.EnforceVerify
		fileCfg.AllowFallback = cfg.AllowFallback
		fileCfg.Users = cfg.Users
	} else if len(ports) > 1 {
		fileCfg.Instances = make([]anytlsConfig, 0, len(ports))
		for _, p := range ports {
			cfg := cfgs[p]
			cfg.ExitIP = strings.TrimSpace(cfg.ExitIP)
			fileCfg.Instances = append(fileCfg.Instances, cfg)
		}
	}
	b, err := json.Marshal(fileCfg)
	if err != nil {
		return err
	}
	return os.WriteFile(anytlsConfigPath, b, 0o600)
}

func applyAnyTLSConfig(port int, password string, exitIP string, enforceVerify bool, allowFallback bool, baseUserID int64, users []anytlsUserRule, certDomain string, certPEM string, keyPEM string, caPEM string, certNotBeforeMS int64, certNotAfterMS int64) error {
	cfg := anytlsConfig{
		Port:          port,
		Password:      password,
		BaseUserID:    baseUserID,
		ExitIP:        strings.TrimSpace(exitIP),
		CertDomain:    strings.TrimSpace(certDomain),
		EnforceVerify: enforceVerify,
		AllowFallback: allowFallback,
		Users:         users,
	}
	certChanged, err := writeAnyTLSCertMaterial(certDomain, certPEM, keyPEM, caPEM, certNotBeforeMS, certNotAfterMS)
	if err != nil {
		return err
	}
	if err := startAnyTLS(cfg); err != nil {
		return err
	}
	anytlsMu.Lock()
	err = saveAnyTLSConfigs(anytlsConfigs)
	anytlsMu.Unlock()
	if err != nil {
		return err
	}
	// A no-op config update must still verify the post-reload certificate.
	// The configured port loaded the leaf in startAnyTLS; reload the others.
	if certChanged {
		reloadAnyTLSRuntimesForCert(port)
	} else {
		emitAnyTLSInstalledCheck("post_reload", "", 0, "")
	}
	return nil
}

func removeAnyTLSConfig(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid anytls port")
	}
	anytlsMu.Lock()
	defer anytlsMu.Unlock()
	stopAnyTLSPortLocked(port)
	delete(anytlsConfigs, port)
	return saveAnyTLSConfigs(anytlsConfigs)
}

func anytlsCertChangedSinceRuntime(rt *anytlsRuntime) bool {
	if rt == nil || rt.certFingerprint == "" {
		return true
	}
	cert, _, err := readCompatFile(anytlsCertPath, legacyAnyTLSCertPath)
	return err != nil || rt.certFingerprint != certFingerprintSHA256(string(cert))
}

// startSavedAnyTLSConfigs retains all desired ports, including failed starts.
// Startup must run outside anytlsMu because startAnyTLS takes that lock.
func startSavedAnyTLSConfigs(cfgs map[int]anytlsConfig, start func(anytlsConfig) error) bool {
	anytlsMu.Lock()
	for port, cfg := range cfgs {
		anytlsConfigs[port] = cfg
	}
	anytlsMu.Unlock()
	ports := make([]int, 0, len(cfgs))
	for port := range cfgs {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	allStarted := len(ports) > 0
	for _, port := range ports {
		if err := start(cfgs[port]); err != nil {
			allStarted = false
			log.Printf("{\"event\":\"edge_boot_err\",\"port\":%d,\"error\":%q}", port, err.Error())
		}
	}
	return allStarted
}

func startAnyTLS(cfg anytlsConfig) error {
	if cfg.Port <= 0 || cfg.Port > 65535 || cfg.Password == "" {
		return fmt.Errorf("invalid anytls config")
	}
	tuning := loadAnyTLSTuning()
	anytlsMu.Lock()
	defer anytlsMu.Unlock()
	if rt, ok := anytlsRuntimes[cfg.Port]; ok &&
		rt != nil &&
		!anytlsCertChangedSinceRuntime(rt) &&
		cfg.Password == rt.current.Password &&
		cfg.ExitIP == rt.current.ExitIP &&
		cfg.CertDomain == rt.current.CertDomain &&
		cfg.EnforceVerify == rt.current.EnforceVerify &&
		cfg.AllowFallback == rt.current.AllowFallback &&
		cfg.BaseUserID == rt.current.BaseUserID &&
		reflect.DeepEqual(cfg.Users, rt.current.Users) {
		anytlsConfigs[cfg.Port] = cfg
		return nil
	}

	var localTCP *net.TCPAddr
	var localUDP *net.UDPAddr
	if strings.TrimSpace(cfg.ExitIP) != "" {
		ip := net.ParseIP(strings.TrimSpace(cfg.ExitIP))
		if ip == nil {
			return fmt.Errorf("invalid exitIp")
		}
		if !isLocalIP(ip) {
			log.Printf("{\"event\":\"edge_exitip_not_local\",\"exitIp\":%q}", cfg.ExitIP)
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

	cert, err := ensureAnyTLSCert(cfg.CertDomain, cfg.EnforceVerify)
	if err != nil {
		return err
	}
	fallbackMode := strings.ToLower(strings.TrimSpace(getenv("EDGE_TLS_FALLBACK_MODE", "http")))
	switch fallbackMode {
	case "off", "close", "none", "http":
	default:
		fallbackMode = "http"
	}
	tlsCfg := newAnyTLSServerTLSConfig(*cert)
	var helloSeen uint64
	tlsCfg.GetConfigForClient = func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
		n := atomic.AddUint64(&helloSeen, 1)
		if shouldSampleAnyTLS(n, 20, 100) {
			emitAnyTLSRuntimeLog("tls_client_hello", "AnyTLS 收到 TLS ClientHello", map[string]any{
				"remote":    safeRemoteAddr(chi.Conn),
				"local":     safeLocalAddr(chi.Conn),
				"sni":       chi.ServerName,
				"alpn":      chi.SupportedProtos,
				"versions":  tlsVersionsText(chi.SupportedVersions),
				"count":     n,
				"sniPolicy": "ignored",
			})
		}
		return nil, nil
	}
	stopAnyTLSPortLocked(cfg.Port)
	lns, err := listenAnyTLSSockets(cfg.Port)
	if err != nil {
		return err
	}
	rules := buildAnyTLSRules(cfg)
	server := &anytlsServer{
		tlsConfig:        tlsCfg,
		authRules:        rules,
		authIndex:        buildAnyTLSIndex(rules),
		localTCPAddr:     localTCP,
		localUDPAddr:     localUDP,
		certDomain:       cfg.CertDomain,
		allowFallback:    cfg.AllowFallback,
		fallbackMode:     fallbackMode,
		connSem:          make(chan struct{}, tuning.maxConns),
		handshakeTO:      tuning.handshakeTO,
		acceptBackoff:    tuning.acceptBackoff,
		acceptBackoffMax: tuning.acceptBackoffMax,
	}
	ctx, cancel := context.WithCancel(context.Background())
	anytlsRuntimes[cfg.Port] = &anytlsRuntime{
		listeners:       lns,
		cancel:          cancel,
		current:         cfg,
		certFingerprint: fmt.Sprintf("%x", sha256.Sum256(cert.Certificate[0])),
	}
	anytlsConfigs[cfg.Port] = cfg
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
	log.Printf("{\"event\":\"edge_start\",\"port\":%d,\"exitIp\":%q,\"enforceVerify\":%v,\"allowFallback\":%v,\"listeners\":%v,\"maxConns\":%d,\"handshakeTimeoutSec\":%d}",
		cfg.Port, cfg.ExitIP, cfg.EnforceVerify, cfg.AllowFallback, listenerNets, tuning.maxConns, int(tuning.handshakeTO/time.Second))
	emitAnyTLSRuntimeLog("start", "AnyTLS 服务已启动", map[string]any{
		"port":                cfg.Port,
		"exitIp":              cfg.ExitIP,
		"enforceVerify":       cfg.EnforceVerify,
		"allowFallback":       cfg.AllowFallback,
		"listeners":           listenerNets,
		"maxConns":            tuning.maxConns,
		"handshakeTimeoutSec": int(tuning.handshakeTO / time.Second),
	})
	return nil
}

func newAnyTLSServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		},
		CurvePreferences: []tls.CurveID{
			tls.X25519,
			tls.CurveP256,
			tls.CurveP384,
		},
		SessionTicketsDisabled: true,
	}
}

func listenAnyTLSSockets(port int) ([]net.Listener, error) {
	tryV4 := true
	tryV6 := true
	var lns []net.Listener
	bind := func(network, addr string) {
		if ln, err := net.Listen(network, addr); err == nil {
			lns = append(lns, ln)
		} else {
			log.Printf("{\"event\":\"edge_listen_err\",\"network\":%q,\"addr\":%q,\"error\":%q}", network, addr, err.Error())
			emitAnyTLSRuntimeLog("listen_err", "AnyTLS 监听失败", map[string]any{
				"network": network,
				"addr":    addr,
				"error":   err.Error(),
			})
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

func stopAnyTLSPortLocked(port int) {
	rt, ok := anytlsRuntimes[port]
	if !ok || rt == nil {
		return
	}
	if rt.cancel != nil {
		rt.cancel()
	}
	if len(rt.listeners) > 0 {
		for _, ln := range rt.listeners {
			if ln != nil {
				_ = ln.Close()
			}
		}
	}
	delete(anytlsRuntimes, port)
}

func stopAnyTLSAllLocked() {
	ports := make([]int, 0, len(anytlsRuntimes))
	for p := range anytlsRuntimes {
		ports = append(ports, p)
	}
	for _, p := range ports {
		stopAnyTLSPortLocked(p)
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

func buildAnyTLSIndex(rules []anytlsAuthRule) map[[sha256.Size]byte]anytlsAuthRule {
	index := make(map[[sha256.Size]byte]anytlsAuthRule, len(rules))
	for _, rule := range rules {
		if len(rule.hash) != sha256.Size {
			continue
		}
		var key [sha256.Size]byte
		copy(key[:], rule.hash)
		if _, exists := index[key]; !exists {
			index[key] = rule
		}
	}
	return index
}

func anytlsAcceptLoop(ctx context.Context, ln net.Listener, s *anytlsServer) {
	backoff := s.acceptBackoff
	if backoff <= 0 {
		backoff = 50 * time.Millisecond
	}
	maxBackoff := s.acceptBackoffMax
	if maxBackoff < backoff {
		maxBackoff = time.Second
	}
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
			n := atomic.AddUint64(&s.acceptErrs, 1)
			if n <= 5 || n%100 == 0 {
				log.Printf("{\"event\":\"edge_accept_err\",\"error\":%q,\"count\":%d,\"backoffMs\":%d}", err.Error(), n, int(backoff/time.Millisecond))
				emitAnyTLSRuntimeLog("accept_err", "AnyTLS accept 错误", map[string]any{
					"error":     err.Error(),
					"count":     n,
					"backoffMs": int(backoff / time.Millisecond),
				})
			}
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = s.acceptBackoff
		if backoff <= 0 {
			backoff = 50 * time.Millisecond
		}
		if !s.tryAcquireConnSlot() {
			_ = c.Close()
			continue
		}
		go s.handleConn(ctx, c)
	}
}

func (s *anytlsServer) tryAcquireConnSlot() bool {
	if s == nil || s.connSem == nil {
		return true
	}
	select {
	case s.connSem <- struct{}{}:
		atomic.AddInt64(&s.activeConns, 1)
		return true
	default:
		n := atomic.AddUint64(&s.rejectedConns, 1)
		if n <= 5 || n%200 == 0 {
			active := atomic.LoadInt64(&s.activeConns)
			log.Printf("{\"event\":\"edge_conn_reject\",\"reason\":\"max_conns\",\"active\":%d,\"limit\":%d,\"count\":%d}", active, cap(s.connSem), n)
			emitAnyTLSRuntimeLog("conn_reject", "AnyTLS 连接被拒绝（达到并发上限）", map[string]any{
				"active": active,
				"limit":  cap(s.connSem),
				"count":  n,
			})
		}
		return false
	}
}

func (s *anytlsServer) releaseConnSlot() {
	if s == nil || s.connSem == nil {
		return
	}
	select {
	case <-s.connSem:
	default:
	}
	atomic.AddInt64(&s.activeConns, -1)
}

func (s *anytlsServer) handleConn(ctx context.Context, c net.Conn) {
	remote := safeRemoteAddr(c)
	defer func() {
		s.releaseConnSlot()
		if r := recover(); r != nil {
			log.Printf("{\"event\":\"edge_panic\",\"error\":%q}", fmt.Sprint(r))
			log.Printf("{\"event\":\"edge_panic_stack\",\"stack\":%q}", string(debug.Stack()))
			emitAnyTLSRuntimeLog("panic", "AnyTLS 连接处理 panic", map[string]any{
				"remote": remote,
				"error":  fmt.Sprint(r),
			})
		}
	}()

	startAt := time.Now()
	sessionSeq := atomic.AddUint64(&s.sessionSeen, 1)
	tlsConn := tls.Server(c, s.tlsConfig)
	c = tlsConn
	defer c.Close()
	if s.handshakeTO > 0 {
		_ = c.SetDeadline(time.Now().Add(s.handshakeTO))
	}
	if err := tlsConn.Handshake(); err != nil {
		emitAnyTLSRuntimeLog("tls_handshake_err", "AnyTLS TLS握手失败", mergeAny(map[string]any{
			"remote":    remote,
			"local":     safeLocalAddr(c),
			"stage":     "tls_handshake",
			"sniPolicy": "ignored",
		}, anytlsErrorFields(err)))
		return
	}
	if s.handshakeTO > 0 {
		// Refresh deadline for AnyTLS auth frame after TLS handshake completes.
		_ = c.SetDeadline(time.Now().Add(s.handshakeTO))
	}

	b := buf.NewPacket()
	defer b.Release()
	const anytlsMinAuthFrame = 34 // 32-byte auth hash + 2-byte padding length
	n64, err := b.ReadAtLeastFrom(c, anytlsMinAuthFrame)
	n := int(n64)
	if err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			log.Printf("{\"event\":\"edge_handshake_timeout\",\"timeoutSec\":%d}", int(s.handshakeTO/time.Second))
			emitAnyTLSRuntimeLog("handshake_timeout", "AnyTLS 握手超时", map[string]any{
				"timeoutSec": int(s.handshakeTO / time.Second),
				"remote":     remote,
				"local":      safeLocalAddr(c),
				"stage":      "first_packet",
			})
		} else {
			emitAnyTLSRuntimeLog("read_err", "AnyTLS 首包读取失败", mergeAny(map[string]any{
				"remote": remote,
				"local":  safeLocalAddr(c),
				"stage":  "first_packet",
				"want":   anytlsMinAuthFrame,
				"read":   n,
			}, anytlsErrorFields(err)))
		}
		return
	}
	c = bufio.NewCachedConn(c, b)

	by, err := b.ReadBytes(32)
	rule, ok := s.matchRule(by)
	if err != nil || !ok {
		failSeq := atomic.AddUint64(&s.authFailSeen, 1)
		if shouldSampleAnyTLS(failSeq, 20, 100) {
			emitAnyTLSRuntimeLog("auth_fail", "AnyTLS 鉴权失败", map[string]any{
				"remote": remote,
				"count":  failSeq,
			})
		}
		b.Resize(0, n)
		s.fallbackConn(ctx, c)
		return
	}
	by, err = b.ReadBytes(2)
	if err != nil {
		b.Resize(0, n)
		s.fallbackConn(ctx, c)
		return
	}
	paddingLen := binary.BigEndian.Uint16(by)
	if paddingLen > 0 {
		if b.Len() < int(paddingLen) {
			need := int(paddingLen) - b.Len()
			moreN, readErr := b.ReadAtLeastFrom(c, need)
			n += int(moreN)
			if readErr != nil {
				emitAnyTLSRuntimeLog("read_err", "AnyTLS 读取 padding 失败", mergeAny(map[string]any{
					"remote": remote,
					"local":  safeLocalAddr(c),
					"stage":  "padding",
					"need":   need,
					"read":   n,
				}, anytlsErrorFields(readErr)))
				b.Resize(0, n)
				s.fallbackConn(ctx, c)
				return
			}
		}
		_, err = b.ReadBytes(int(paddingLen))
		if err != nil {
			emitAnyTLSRuntimeLog("read_err", "AnyTLS 读取 padding 失败", mergeAny(map[string]any{
				"remote": remote,
				"local":  safeLocalAddr(c),
				"stage":  "padding",
				"want":   int(paddingLen),
			}, anytlsErrorFields(err)))
			b.Resize(0, n)
			s.fallbackConn(ctx, c)
			return
		}
	}
	_ = c.SetDeadline(time.Time{})
	if shouldSampleAnyTLS(sessionSeq, 20, 100) {
		emitAnyTLSRuntimeLog("session_open", "AnyTLS 会话已建立", map[string]any{
			"remote": remote,
			"seq":    sessionSeq,
			"userId": rule.userID,
		})
	}

	var streamTotal int64
	var streamErrs int64
	sess := session.NewServerSession(c, func(stream *session.Stream) {
		atomic.AddInt64(&streamTotal, 1)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("{\"event\":\"edge_stream_panic\",\"error\":%q}", fmt.Sprint(r))
				log.Printf("{\"event\":\"edge_stream_panic_stack\",\"stack\":%q}", string(debug.Stack()))
				atomic.AddInt64(&streamErrs, 1)
				emitAnyTLSRuntimeLog("stream_panic", "AnyTLS stream panic", map[string]any{
					"remote": remote,
					"error":  fmt.Sprint(r),
				})
			}
		}()
		defer stream.Close()

		destination, err := M.SocksaddrSerializer.ReadAddrPort(stream)
		if err != nil {
			atomic.AddInt64(&streamErrs, 1)
			emitAnyTLSRuntimeLog("stream_err", "AnyTLS 读取目标地址失败", map[string]any{
				"remote": remote,
				"error":  err.Error(),
			})
			return
		}

		var proxyErr error
		if strings.Contains(destination.String(), "udp-over-tcp.arpa") {
			proxyErr = s.proxyOutboundUoT(ctx, stream, destination, rule)
		} else {
			proxyErr = s.proxyOutboundTCP(ctx, stream, destination, rule)
		}
		if proxyErr != nil {
			atomic.AddInt64(&streamErrs, 1)
			emitAnyTLSRuntimeLog("stream_err", "AnyTLS 转发失败", mergeAny(map[string]any{
				"remote":      remote,
				"local":       safeLocalAddr(c),
				"userId":      rule.userID,
				"destination": destination.String(),
			}, anytlsErrorFields(proxyErr)))
		}
	}, &padding.DefaultPaddingFactory)
	sess.Run()
	sess.Close()
	durMs := time.Since(startAt).Milliseconds()
	errCount := atomic.LoadInt64(&streamErrs)
	totalCount := atomic.LoadInt64(&streamTotal)
	if errCount > 0 || shouldSampleAnyTLS(sessionSeq, 20, 100) {
		step := "session_done"
		msg := "AnyTLS 会话结束"
		if errCount > 0 {
			msg = "AnyTLS 会话结束（含错误）"
		}
		emitAnyTLSRuntimeLog(step, msg, map[string]any{
			"remote":      remote,
			"seq":         sessionSeq,
			"userId":      rule.userID,
			"durationMs":  durMs,
			"streamTotal": totalCount,
			"streamErr":   errCount,
		})
	}
}

func (s *anytlsServer) fallbackConn(ctx context.Context, c net.Conn) {
	_ = ctx
	if s == nil || c == nil {
		return
	}
	if s.fallbackMode == "off" || s.fallbackMode == "close" || s.fallbackMode == "none" {
		return
	}
	body := "<html><head><title>404 Not Found</title></head><body><center><h1>404 Not Found</h1></center></body></html>\n"
	resp := fmt.Sprintf(
		"HTTP/1.1 404 Not Found\r\nDate: %s\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\nServer: nginx\r\nStrict-Transport-Security: max-age=31536000; includeSubDomains\r\n\r\n%s",
		time.Now().UTC().Format(http.TimeFormat),
		len(body),
		body,
	)
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.WriteString(c, resp)
}

func (s *anytlsServer) matchRule(hash []byte) (anytlsAuthRule, bool) {
	if len(hash) != sha256.Size {
		return anytlsAuthRule{}, false
	}
	var key [sha256.Size]byte
	copy(key[:], hash)
	rule, ok := s.authIndex[key]
	return rule, ok
}

func clientSettingFromConn(conn net.Conn, key string) string {
	reader, ok := conn.(anytlsClientSettingReader)
	if !ok {
		return ""
	}
	return strings.TrimSpace(reader.ClientSetting(key))
}

func clientSettingFromConnFirst(conn net.Conn, keys ...string) string {
	for _, key := range keys {
		if v := clientSettingFromConn(conn, key); v != "" {
			return v
		}
	}
	return ""
}

func splitEgressRuleEntries(ruleRaw string) []string {
	return strings.FieldsFunc(ruleRaw, func(r rune) bool {
		return r == ';' || r == ',' || r == '\n'
	})
}

func parseEgressRuleEntry(entry string) (pattern, egressIP string, ok bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", "", false
	}
	kv := strings.SplitN(entry, "=", 2)
	if len(kv) != 2 {
		return "", "", false
	}
	pattern = strings.TrimSpace(kv[0])
	egressIP = strings.TrimSpace(kv[1])
	if pattern == "" || egressIP == "" {
		return "", "", false
	}
	ip := net.ParseIP(egressIP)
	if ip == nil {
		return "", "", false
	}
	return pattern, ip.String(), true
}

func matchEgressRulePattern(pattern string, destination M.Socksaddr) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "" {
		return false
	}
	if p == "*" || p == "default" {
		return true
	}

	destIsDomain := destination.IsFqdn()
	destDomain := strings.ToLower(strings.TrimSpace(destination.Fqdn))
	destIsIP := destination.IsIP()
	destIP := destination.Addr.Unmap()

	switch {
	case strings.HasPrefix(p, "domain:"):
		match := strings.TrimSpace(strings.TrimPrefix(p, "domain:"))
		return destIsDomain && destDomain == match
	case strings.HasPrefix(p, "suffix:"):
		match := strings.TrimSpace(strings.TrimPrefix(p, "suffix:"))
		if !destIsDomain || match == "" {
			return false
		}
		return destDomain == match || strings.HasSuffix(destDomain, "."+match)
	case strings.HasPrefix(p, "ip:"):
		match := strings.TrimSpace(strings.TrimPrefix(p, "ip:"))
		return matchEgressIPPattern(match, destIsIP, destIP)
	case strings.HasPrefix(p, "cidr:"):
		match := strings.TrimSpace(strings.TrimPrefix(p, "cidr:"))
		return matchEgressCIDRPattern(match, destIsIP, destIP)
	}

	if strings.HasPrefix(p, "*.") {
		match := strings.TrimPrefix(p, "*.")
		if !destIsDomain || match == "" {
			return false
		}
		return destDomain == match || strings.HasSuffix(destDomain, "."+match)
	}
	if strings.Contains(p, "/") {
		return matchEgressCIDRPattern(p, destIsIP, destIP)
	}
	if matchEgressIPPattern(p, destIsIP, destIP) {
		return true
	}
	return destIsDomain && destDomain == p
}

func matchEgressIPPattern(pattern string, destIsIP bool, destIP netip.Addr) bool {
	if !destIsIP {
		return false
	}
	ip, err := netip.ParseAddr(pattern)
	if err != nil {
		return false
	}
	return destIP == ip.Unmap()
}

func matchEgressCIDRPattern(pattern string, destIsIP bool, destIP netip.Addr) bool {
	if !destIsIP {
		return false
	}
	prefix, err := netip.ParsePrefix(pattern)
	if err != nil {
		return false
	}
	return prefix.Contains(destIP)
}

func resolveConnEgressIP(conn net.Conn, destination M.Socksaddr) (string, string) {
	ruleRaw := clientSettingFromConnFirst(conn, "egress-rule", "egress_rule")
	if ruleRaw != "" {
		for _, entry := range splitEgressRuleEntries(ruleRaw) {
			pattern, egressIP, ok := parseEgressRuleEntry(entry)
			if !ok {
				continue
			}
			if matchEgressRulePattern(pattern, destination) {
				return egressIP, "egress-rule"
			}
		}
	}
	egressIP := clientSettingFromConnFirst(conn, "egress-ip", "egress_ip")
	if egressIP != "" {
		return egressIP, "egress-ip"
	}
	return "", ""
}

func normalizeLocalBindIP(raw string) (net.IP, error) {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return nil, fmt.Errorf("invalid egress-ip: %s", raw)
	}
	if !isLocalIP(ip) {
		return nil, fmt.Errorf("egress-ip is not local: %s", raw)
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4, nil
	}
	if ip16 := ip.To16(); ip16 != nil {
		return ip16, nil
	}
	return nil, fmt.Errorf("invalid egress-ip: %s", raw)
}

func (s *anytlsServer) resolveLocalBindAddrs(conn net.Conn, destination M.Socksaddr) (*net.TCPAddr, *net.UDPAddr, string, error) {
	if egressIP, source := resolveConnEgressIP(conn, destination); egressIP != "" {
		ip, err := normalizeLocalBindIP(egressIP)
		if err != nil {
			if loadAnyTLSTuning().egressStrict {
				return nil, nil, source, err
			}
			log.Printf("{\"event\":\"edge_egress_relax\",\"source\":%q,\"destination\":%q,\"egressIp\":%q,\"reason\":\"invalid_or_non_local\",\"error\":%q}", source, destination.String(), egressIP, err.Error())
			emitAnyTLSRuntimeLog("egress_relax", "AnyTLS 出口IP校验失败，已放宽为默认出口", map[string]any{
				"source":      source,
				"destination": destination.String(),
				"egressIp":    egressIP,
				"reason":      "invalid_or_non_local",
				"error":       err.Error(),
			})
			if s.localTCPAddr != nil {
				return s.localTCPAddr, s.localUDPAddr, "exitIp", nil
			}
			return nil, nil, "default", nil
		}
		return &net.TCPAddr{IP: ip}, &net.UDPAddr{IP: ip}, source, nil
	}
	if s.localTCPAddr != nil {
		return s.localTCPAddr, s.localUDPAddr, "exitIp", nil
	}
	return nil, nil, "default", nil
}

func isUnspecifiedSocksaddr(addr M.Socksaddr) bool {
	if addr.Port != 0 {
		return false
	}
	if addr.IsIP() {
		return addr.Addr.IsValid() && addr.Addr.IsUnspecified()
	}
	if addr.IsFqdn() {
		return strings.TrimSpace(addr.Fqdn) == ""
	}
	return false
}

func mappedIPv6FromKnownIPv4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is4() {
		return netip.Addr{}, false
	}
	switch ip.String() {
	case "1.1.1.1":
		v6, _ := netip.ParseAddr("2606:4700:4700::1111")
		return v6, true
	case "1.0.0.1":
		v6, _ := netip.ParseAddr("2606:4700:4700::1001")
		return v6, true
	case "8.8.8.8":
		v6, _ := netip.ParseAddr("2001:4860:4860::8888")
		return v6, true
	case "8.8.4.4":
		v6, _ := netip.ParseAddr("2001:4860:4860::8844")
		return v6, true
	case "9.9.9.9":
		v6, _ := netip.ParseAddr("2620:fe::fe")
		return v6, true
	}
	return netip.Addr{}, false
}

type ipv6DialCandidate struct {
	endpoint string
	reason   string
}

func loadAnyTLSNAT64Prefixes() []netip.Prefix {
	anytlsNAT64Once.Do(func() {
		// RFC6052 WKP first; can be overridden/extended by env.
		raw := strings.TrimSpace(os.Getenv("ANYTLS_NAT64_PREFIXES"))
		if raw == "" {
			raw = "64:ff9b::/96"
		}
		parts := strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
		})
		seen := map[string]struct{}{}
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			pfx, err := netip.ParsePrefix(p)
			if err != nil || !pfx.Addr().Is6() {
				continue
			}
			// Keep implementation simple/safe: synthesize only /96.
			if pfx.Bits() != 96 {
				continue
			}
			k := pfx.String()
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			anytlsNAT64Prefs = append(anytlsNAT64Prefs, pfx)
		}
		if len(anytlsNAT64Prefs) == 0 {
			if pfx, err := netip.ParsePrefix("64:ff9b::/96"); err == nil {
				anytlsNAT64Prefs = append(anytlsNAT64Prefs, pfx)
			}
		}
	})
	return anytlsNAT64Prefs
}

func synthesizeIPv6FromIPv4WithPrefix(ip4 netip.Addr, pfx netip.Prefix) (netip.Addr, bool) {
	if !ip4.Is4() || !pfx.Addr().Is6() || pfx.Bits() != 96 {
		return netip.Addr{}, false
	}
	base := pfx.Addr().As16()
	v4 := ip4.As4()
	var out [16]byte
	copy(out[:], base[:])
	out[12] = v4[0]
	out[13] = v4[1]
	out[14] = v4[2]
	out[15] = v4[3]
	return netip.AddrFrom16(out), true
}

func mappedIPv6CandidatesFromIPv4(ip netip.Addr, port uint16) []ipv6DialCandidate {
	if !ip.Is4() {
		return nil
	}
	var out []ipv6DialCandidate
	seen := map[string]struct{}{}
	add := func(v6 netip.Addr, reason string) {
		endpoint := net.JoinHostPort(v6.String(), strconv.Itoa(int(port)))
		if _, ok := seen[endpoint]; ok {
			return
		}
		seen[endpoint] = struct{}{}
		out = append(out, ipv6DialCandidate{endpoint: endpoint, reason: reason})
	}
	if v6, ok := mappedIPv6FromKnownIPv4(ip); ok {
		add(v6, "known_ipv4_mapped_to_ipv6")
	}
	for _, pfx := range loadAnyTLSNAT64Prefixes() {
		if v6, ok := synthesizeIPv6FromIPv4WithPrefix(ip, pfx); ok {
			add(v6, "nat64_prefix_synthesized")
		}
	}
	return out
}

func (s *anytlsServer) fallbackDialTCP(ctx context.Context, conn net.Conn, destination M.Socksaddr, rule anytlsAuthRule, cause error) error {
	if (!(s.allowFallback || !loadAnyTLSTuning().egressStrict)) || isUnspecifiedSocksaddr(destination) {
		if cause != nil {
			cause = E.Errors(cause, N.ReportHandshakeFailure(conn, cause))
		}
		return cause
	}
	log.Printf("{\"event\":\"edge_egress_fallback\",\"destination\":%q,\"reason\":%q}", destination.String(), cause.Error())
	dialCtx := ctx
	cancel := func() {}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < 2*time.Second {
		dialCtx, cancel = context.WithTimeout(context.Background(), 6*time.Second)
		log.Printf("{\"event\":\"edge_egress_fallback_ctx_refresh\",\"destination\":%q,\"timeoutSec\":6}", destination.String())
	}
	defer cancel()
	c, err := proxy.SystemDialer.DialContext(dialCtx, "tcp", destination.String())
	if err != nil {
		err = E.Errors(err, N.ReportHandshakeFailure(conn, err))
		return err
	}
	log.Printf("{\"event\":\"edge_egress_fallback_ok\",\"destination\":%q}", destination.String())
	if err = N.ReportHandshakeSuccess(conn); err != nil {
		return err
	}
	_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
	return copyErr
}

func (s *anytlsServer) proxyOutboundTCP(ctx context.Context, conn net.Conn, destination M.Socksaddr, rule anytlsAuthRule) error {
	localTCPAddr, _, bindSource, bindErr := s.resolveLocalBindAddrs(conn, destination)
	fallbackAllowed := s.allowFallback || !loadAnyTLSTuning().egressStrict
	if bindErr != nil {
		err := E.Errors(bindErr, N.ReportHandshakeFailure(conn, bindErr))
		return err
	}
	if localTCPAddr != nil {
		logBoundSelect := func() {
			if (bindSource == "egress-ip" || bindSource == "egress-rule") && !isUnspecifiedSocksaddr(destination) {
				log.Printf("{\"event\":\"edge_egress_select\",\"source\":%q,\"destination\":%q,\"ip\":%q}", bindSource, destination.String(), localTCPAddr.IP.String())
			}
		}
		logDialErr := func(network, addr string, err error) {
			if bindSource == "egress-ip" || bindSource == "egress-rule" {
				log.Printf("{\"event\":\"edge_egress_dial_err\",\"source\":%q,\"network\":%q,\"addr\":%q,\"error\":%q}", bindSource, network, addr, err.Error())
				emitAnyTLSRuntimeLog("egress_dial_err", "AnyTLS 出口拨号失败", mergeAny(map[string]any{
					"source":      bindSource,
					"network":     network,
					"addr":        addr,
					"destination": destination.String(),
					"local":       safeLocalAddr(conn),
					"stage":       "egress_dial",
				}, anytlsErrorFields(err)))
			}
		}
		logBypass := func(network, reason string) {
			if bindSource == "egress-ip" || bindSource == "egress-rule" {
				log.Printf("{\"event\":\"edge_egress_bypass\",\"source\":%q,\"destination\":%q,\"network\":%q,\"reason\":%q}", bindSource, destination.String(), network, reason)
				emitAnyTLSRuntimeLog("egress_bypass", "AnyTLS 出口绑定已旁路", map[string]any{
					"source":      bindSource,
					"network":     network,
					"destination": destination.String(),
					"reason":      reason,
				})
			}
		}
		logDialOK := func(network, addr string, bound bool) {
			if bindSource == "egress-ip" || bindSource == "egress-rule" {
				log.Printf("{\"event\":\"edge_egress_dial_ok\",\"source\":%q,\"network\":%q,\"addr\":%q,\"bound\":%v}", bindSource, network, addr, bound)
				emitAnyTLSRuntimeLog("egress_dial_ok", "AnyTLS 出口拨号成功", map[string]any{
					"source":      bindSource,
					"network":     network,
					"addr":        addr,
					"destination": destination.String(),
					"bound":       bound,
				})
			}
		}
		d := &net.Dialer{LocalAddr: localTCPAddr}
		if ip4 := localTCPAddr.IP.To4(); ip4 != nil {
			_ = ip4
			if destination.IsIPv6() && !isUnspecifiedSocksaddr(destination) {
				if fallbackAllowed {
					logBypass("tcp6", "family_mismatch_ipv4_exit_to_ipv6_destination")
					c, err := proxy.SystemDialer.DialContext(ctx, "tcp6", destination.String())
					if err != nil {
						err = E.Errors(err, N.ReportHandshakeFailure(conn, err))
						return err
					}
					logDialOK("tcp6", destination.String(), false)
					if err = N.ReportHandshakeSuccess(conn); err != nil {
						return err
					}
					_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
					return copyErr
				}
				return fmt.Errorf("destination is ipv6 but exitIp is ipv4")
			}
			if destination.IsFqdn() {
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", destination.Fqdn)
				if err != nil || len(ips) == 0 {
					return s.fallbackDialTCP(ctx, conn, destination, rule, fmt.Errorf("resolve ipv4 failed: %v", err))
				}
				var lastErr error
				for _, ip := range ips {
					addr := net.JoinHostPort(ip.String(), strconv.Itoa(int(destination.Port)))
					c, err := d.DialContext(ctx, "tcp4", addr)
					if err != nil {
						lastErr = err
						logDialErr("tcp4", addr, err)
						continue
					}
					logBoundSelect()
					logDialOK("tcp4", addr, true)
					if err = N.ReportHandshakeSuccess(conn); err != nil {
						return err
					}
					_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
					return copyErr
				}
				if lastErr != nil {
					return s.fallbackDialTCP(ctx, conn, destination, rule, lastErr)
				}
				return fmt.Errorf("resolve ipv4 failed")
			}
			c, err := d.DialContext(ctx, "tcp4", destination.String())
			if err != nil {
				logDialErr("tcp4", destination.String(), err)
				return s.fallbackDialTCP(ctx, conn, destination, rule, err)
			}
			logBoundSelect()
			logDialOK("tcp4", destination.String(), true)
			if err = N.ReportHandshakeSuccess(conn); err != nil {
				return err
			}
			_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
			return copyErr
		}
		if ip6 := localTCPAddr.IP.To16(); ip6 != nil {
			_ = ip6
			if destination.IsIPv4() {
				if fallbackAllowed {
					for _, cand := range mappedIPv6CandidatesFromIPv4(destination.Addr, destination.Port) {
						logBypass("tcp6", cand.reason)
						c6, err6 := d.DialContext(ctx, "tcp6", cand.endpoint)
						if err6 == nil {
							logDialOK("tcp6", cand.endpoint, true)
							if hErr := N.ReportHandshakeSuccess(conn); hErr != nil {
								return hErr
							}
							_, _, copyErr := copyConnWithLimiter(ctx, conn, c6, rule.speedBps, rule.userID)
							return copyErr
						}
						logDialErr("tcp6", cand.endpoint, err6)
					}
					logBypass("tcp4", "family_mismatch_ipv6_exit_to_ipv4_destination")
					c, err := proxy.SystemDialer.DialContext(ctx, "tcp4", destination.String())
					if err != nil {
						logDialErr("tcp4", destination.String(), err)
						err = E.Errors(err, N.ReportHandshakeFailure(conn, err))
						return err
					}
					logDialOK("tcp4", destination.String(), false)
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
					if fallbackAllowed {
						ip4s, err4 := net.DefaultResolver.LookupIP(ctx, "ip4", destination.Fqdn)
						if err4 != nil || len(ip4s) == 0 {
							return fmt.Errorf("resolve ipv6 failed: %v", err)
						}
						var lastErr error
						for _, ip := range ip4s {
							addr := net.JoinHostPort(ip.String(), strconv.Itoa(int(destination.Port)))
							if ip4Addr, ok := ipToNetipAddr(ip); ok {
								for _, cand := range mappedIPv6CandidatesFromIPv4(ip4Addr, destination.Port) {
									logBypass("tcp6", "ipv6_dns_failed_"+cand.reason)
									c6, err6 := d.DialContext(ctx, "tcp6", cand.endpoint)
									if err6 == nil {
										logDialOK("tcp6", cand.endpoint, true)
										if hErr := N.ReportHandshakeSuccess(conn); hErr != nil {
											return hErr
										}
										_, _, copyErr := copyConnWithLimiter(ctx, conn, c6, rule.speedBps, rule.userID)
										return copyErr
									}
									logDialErr("tcp6", cand.endpoint, err6)
								}
							}
							c, err := proxy.SystemDialer.DialContext(ctx, "tcp4", addr)
							if err != nil {
								lastErr = err
								logDialErr("tcp4", addr, err)
								continue
							}
							logBypass("tcp4", "ipv6_dns_failed_fallback_ipv4")
							logDialOK("tcp4", addr, false)
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
						logDialErr("tcp6", addr, err)
						continue
					}
					logBoundSelect()
					logDialOK("tcp6", addr, true)
					if err = N.ReportHandshakeSuccess(conn); err != nil {
						return err
					}
					_, _, copyErr := copyConnWithLimiter(ctx, conn, c, rule.speedBps, rule.userID)
					return copyErr
				}
				if lastErr != nil {
					return s.fallbackDialTCP(ctx, conn, destination, rule, lastErr)
				}
				return fmt.Errorf("resolve ipv6 failed")
			}
			c, err := d.DialContext(ctx, "tcp6", destination.String())
			if err != nil {
				logDialErr("tcp6", destination.String(), err)
				return s.fallbackDialTCP(ctx, conn, destination, rule, err)
			}
			logBoundSelect()
			logDialOK("tcp6", destination.String(), true)
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
		emitAnyTLSRuntimeLog("outbound_err", "AnyTLS 出口拨号失败", mergeAny(map[string]any{
			"network":     "tcp",
			"destination": destination.String(),
			"local":       safeLocalAddr(conn),
			"stage":       "direct_dial",
		}, anytlsErrorFields(err)))
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
	fallbackAllowed := s.allowFallback || !loadAnyTLSTuning().egressStrict
	target := request.Destination
	unspecifiedTarget := isUnspecifiedSocksaddr(target)

	addr := ""
	network := "udp"
	_, localUDPAddr, bindSource, bindErr := s.resolveLocalBindAddrs(conn, target)
	if bindErr != nil {
		err := E.Errors(bindErr, N.ReportHandshakeFailure(conn, bindErr))
		return err
	}
	if localUDPAddr != nil {
		if (bindSource == "egress-ip" || bindSource == "egress-rule") && !unspecifiedTarget {
			log.Printf("{\"event\":\"edge_egress_select\",\"source\":%q,\"destination\":%q,\"ip\":%q}", bindSource, target.String(), localUDPAddr.IP.String())
			emitAnyTLSRuntimeLog("egress_select", "AnyTLS 指定出口IP", map[string]any{
				"source":      bindSource,
				"destination": target.String(),
				"ip":          localUDPAddr.IP.String(),
				"network":     "udp",
			})
		}
		if ip4 := localUDPAddr.IP.To4(); ip4 != nil {
			if target.IsIPv6() && !unspecifiedTarget {
				if fallbackAllowed {
					network = "udp6"
					addr = ""
				} else {
					return fmt.Errorf("destination is ipv6 but exitIp is ipv4")
				}
			} else {
				network = "udp4"
				addr = localUDPAddr.String()
			}
		} else if localUDPAddr.IP.To16() != nil {
			if target.IsIPv4() && !unspecifiedTarget {
				if fallbackAllowed {
					network = "udp4"
					addr = ""
				} else {
					return fmt.Errorf("destination is ipv4 but exitIp is ipv6")
				}
			} else {
				network = "udp6"
				addr = localUDPAddr.String()
			}
		}

		if target.IsFqdn() {
			if network == "udp6" {
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip6", target.Fqdn)
				if err != nil || len(ips) == 0 {
					if fallbackAllowed {
						ip4s, err4 := net.DefaultResolver.LookupIP(ctx, "ip4", target.Fqdn)
						if err4 != nil || len(ip4s) == 0 {
							return fmt.Errorf("resolve ipv6 failed: %v", err)
						}
						addrIP, ok := ipToNetipAddr(ip4s[0])
						if !ok {
							return fmt.Errorf("invalid ipv4 address")
						}
						request.Destination = M.Socksaddr{Addr: addrIP, Port: target.Port}
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
					request.Destination = M.Socksaddr{Addr: addrIP, Port: target.Port}
				}
			} else if network == "udp4" {
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", target.Fqdn)
				if err != nil || len(ips) == 0 {
					return fmt.Errorf("resolve ipv4 failed: %v", err)
				}
				addrIP, ok := ipToNetipAddr(ips[0])
				if !ok {
					return fmt.Errorf("invalid ipv4 address")
				}
				request.Destination = M.Socksaddr{Addr: addrIP, Port: target.Port}
			}
		}
	}
	c, err := net.ListenPacket(network, addr)
	if err != nil {
		emitAnyTLSRuntimeLog("outbound_err", "AnyTLS UoT 监听失败", mergeAny(map[string]any{
			"network": network,
			"addr":    addr,
			"local":   safeLocalAddr(conn),
			"stage":   "uot_listen_packet",
		}, anytlsErrorFields(err)))
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
	errCh := make(chan error, 2)
	go func() {
		pending := int64(0)
		lastFlush := time.Now()
		errCh <- copyStreamLimited(ctx, remote, client, limUp, bufUp, &inBytes, func(written int) {
			if userID <= 0 || written <= 0 {
				return
			}
			pending += int64(written)
			now := time.Now()
			if pending >= 256*1024 || now.Sub(lastFlush) >= 5*time.Second {
				enqueueAnyTLSFlow(userID, pending, 0)
				pending = 0
				lastFlush = now
			}
		})
		if pending > 0 {
			enqueueAnyTLSFlow(userID, pending, 0)
		}
	}()
	go func() {
		pending := int64(0)
		lastFlush := time.Now()
		errCh <- copyStreamLimited(ctx, client, remote, limDown, bufDown, &outBytes, func(written int) {
			if userID <= 0 || written <= 0 {
				return
			}
			pending += int64(written)
			now := time.Now()
			if pending >= 256*1024 || now.Sub(lastFlush) >= 5*time.Second {
				enqueueAnyTLSFlow(userID, 0, pending)
				pending = 0
				lastFlush = now
			}
		})
		if pending > 0 {
			enqueueAnyTLSFlow(userID, 0, pending)
		}
	}()
	err := <-errCh
	cancel()
	_ = client.Close()
	_ = remote.Close()
	select {
	case err2 := <-errCh:
		if err == nil {
			err = err2
		}
	case <-time.After(200 * time.Millisecond):
	}
	return inBytes, outBytes, err
}

func copyStreamLimited(ctx context.Context, dst net.Conn, src net.Conn, limiter *rate.Limiter, bufSize int, counter *int64, onWrite func(int)) error {
	if bufSize <= 0 {
		bufSize = 32 * 1024
	}
	buf := make([]byte, bufSize)
	const readPoll = 2 * time.Second
	for {
		if dl, ok := any(src).(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = dl.SetReadDeadline(time.Now().Add(readPoll))
		}
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
					if onWrite != nil {
						onWrite(wn)
					}
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
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
					continue
				}
			}
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
	errCh := make(chan error, 2)
	go func() {
		pending := int64(0)
		lastFlush := time.Now()
		errCh <- copyPacketLimited(ctx, source, destination, limUp, &inBytes, func(bytes int) {
			if userID <= 0 || bytes <= 0 {
				return
			}
			pending += int64(bytes)
			now := time.Now()
			if pending >= 256*1024 || now.Sub(lastFlush) >= 5*time.Second {
				enqueueAnyTLSFlow(userID, pending, 0)
				pending = 0
				lastFlush = now
			}
		})
		if pending > 0 {
			enqueueAnyTLSFlow(userID, pending, 0)
		}
	}()
	go func() {
		pending := int64(0)
		lastFlush := time.Now()
		errCh <- copyPacketLimited(ctx, destination, source, limDown, &outBytes, func(bytes int) {
			if userID <= 0 || bytes <= 0 {
				return
			}
			pending += int64(bytes)
			now := time.Now()
			if pending >= 256*1024 || now.Sub(lastFlush) >= 5*time.Second {
				enqueueAnyTLSFlow(userID, 0, pending)
				pending = 0
				lastFlush = now
			}
		})
		if pending > 0 {
			enqueueAnyTLSFlow(userID, 0, pending)
		}
	}()
	err := <-errCh
	cancel()
	_ = source.Close()
	_ = destination.Close()
	select {
	case err2 := <-errCh:
		if err == nil {
			err = err2
		}
	case <-time.After(200 * time.Millisecond):
	}
	return inBytes, outBytes, err
}

func copyPacketLimited(ctx context.Context, source N.PacketReader, destination N.PacketWriter, limiter *rate.Limiter, counter *int64, onWrite func(int)) error {
	options := N.NewReadWaitOptions(source, destination)
	const readPoll = 2 * time.Second
	for {
		if dl, ok := any(source).(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = dl.SetReadDeadline(time.Now().Add(readPoll))
		}
		buffer := options.NewPacketBuffer()
		dest, err := source.ReadPacket(buffer)
		if err != nil {
			buffer.Release()
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
					continue
				}
			}
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
		if onWrite != nil {
			onWrite(dataLen)
		}
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
	paths := []string{
		"/api/v1/flow/docker",
		"/flow/docker",
		"/api/v1/flow/anytls",
		"/flow/anytls",
	}
	var lastCode int
	var lastBody []byte
	var lastErr error
	all404 := true
	for i, p := range paths {
		u := apiURL(anytlsPanelScheme, anytlsPanelAddr, p) + "?secret=" + url.QueryEscape(anytlsPanelSecret)
		if debug {
			log.Printf("{\"event\":\"edge_flow_report_try\",\"attempt\":%d,\"path\":%q,\"userId\":%d,\"inBytes\":%d,\"outBytes\":%d,\"url\":%q}", i+1, p, userID, inBytes, outBytes, u)
		}
		code, body, err := httpPostJSON(u, payload)
		lastCode, lastBody, lastErr = code, body, err
		if err != nil {
			continue
		}
		if code/100 == 2 {
			if debug {
				log.Printf("{\"event\":\"edge_flow_report_ok\",\"attempt\":%d,\"path\":%q,\"code\":%d,\"body\":%q}", i+1, p, code, string(body))
			}
			return
		}
		// 404 indicates route mismatch, continue to next compatible path.
		if code == 404 {
			continue
		}
		all404 = false
		// Non-404 non-2xx means server handled request but rejected it.
		log.Printf("{\"event\":\"edge_flow_report_err\",\"attempt\":%d,\"path\":%q,\"code\":%d,\"body\":%q}", i+1, p, code, string(body))
		return
	}
	if lastErr != nil {
		log.Printf("{\"event\":\"edge_flow_report_err\",\"error\":%q}", lastErr.Error())
		return
	}
	if all404 {
		log.Printf("{\"event\":\"edge_flow_report_route_not_found\",\"paths\":%q,\"hint\":\"controller is missing docker/legacy flow endpoints, please upgrade server\"}", paths)
	}
	log.Printf("{\"event\":\"edge_flow_report_err\",\"code\":%d,\"body\":%q}", lastCode, string(lastBody))
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

func anyTLSConfigured() bool {
	anytlsMu.Lock()
	defer anytlsMu.Unlock()
	return len(anytlsConfigs) > 0
}

func certFingerprintSHA256(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return fmt.Sprintf("%x", sum[:])
}

func certHashShort(certPEM string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(certPEM)))
	return fmt.Sprintf("%x", sum[:8])
}

func readCompatFile(primary, legacy string) ([]byte, string, error) {
	if b, err := os.ReadFile(primary); err == nil {
		return b, primary, nil
	}
	if b, err := os.ReadFile(legacy); err == nil {
		return b, legacy, nil
	}
	return nil, primary, os.ErrNotExist
}

func readCompatContent(primary, legacy string) []byte {
	if b, _, err := readCompatFile(primary, legacy); err == nil {
		return b
	}
	return nil
}

func mirrorLegacyFile(path, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	_ = os.WriteFile(path, []byte(content+"\n"), 0o600)
}

func readInstalledAnyTLSCertInfo(certPath, legacyCertPath string) (*anytlsInstalledCertInfo, error) {
	certRaw, _, err := readCompatFile(certPath, legacyCertPath)
	if err != nil {
		return nil, err
	}
	certPEM := strings.TrimSpace(string(certRaw))
	if certPEM == "" {
		return nil, fmt.Errorf("empty cert file")
	}
	block, _ := pem.Decode([]byte(certPEM + "\n"))
	if block == nil {
		return nil, fmt.Errorf("decode cert pem failed")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	info := &anytlsInstalledCertInfo{
		Hash:        certHashShort(certPEM),
		Subject:     cert.Subject.String(),
		Issuer:      cert.Issuer.String(),
		Serial:      cert.SerialNumber.String(),
		CommonName:  strings.TrimSpace(cert.Subject.CommonName),
		DNSNames:    append([]string(nil), cert.DNSNames...),
		NotBeforeMS: cert.NotBefore.UnixMilli(),
		NotAfterMS:  cert.NotAfter.UnixMilli(),
	}
	return info, nil
}

func certDomainMatched(info *anytlsInstalledCertInfo, domain string) bool {
	if info == nil {
		return false
	}
	want := strings.TrimSpace(strings.ToLower(domain))
	if want == "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(info.CommonName), want) {
		return true
	}
	for _, d := range info.DNSNames {
		if strings.EqualFold(strings.TrimSpace(d), want) {
			return true
		}
	}
	return false
}

func anyTLSInstalledCertValidAt(info *anytlsInstalledCertInfo, now time.Time) bool {
	if info == nil || info.NotBeforeMS <= 0 || info.NotAfterMS <= 0 {
		return false
	}
	ms := now.UnixMilli()
	return ms >= info.NotBeforeMS && ms < info.NotAfterMS
}

func anyTLSRuntimeCertificateMatches(configs map[int]anytlsConfig, runtimes map[int]*anytlsRuntime, pin string) bool {
	if pin == "" || len(configs) == 0 || len(configs) != len(runtimes) {
		return false
	}
	for port := range configs {
		rt := runtimes[port]
		if rt == nil || rt.certFingerprint != pin || len(rt.listeners) == 0 {
			return false
		}
	}
	return true
}

func emitAnyTLSInstalledCheck(context string, expectedDomain string, expectedNotAfterMS int64, expectedHash string) {
	emitAnyTLSInstalledCheckFromPaths(context, expectedDomain, expectedNotAfterMS, expectedHash, anytlsCertPath, legacyAnyTLSCertPath)
}

func emitAnyTLSInstalledCheckFromPaths(context string, expectedDomain string, expectedNotAfterMS int64, expectedHash string, primaryPath, legacyPath string) {
	info, err := readInstalledAnyTLSCertInfo(primaryPath, legacyPath)
	_, certPath, _ := readCompatFile(primaryPath, legacyPath)
	if certPath == "" {
		certPath = primaryPath
	}
	if err != nil {
		emitAnyTLSCertLog("installed_check_err", "本地证书检测失败", map[string]any{
			"context": context,
			"error":   err.Error(),
			"path":    certPath,
		})
		return
	}

	matchLatest := true
	mismatch := make([]string, 0, 3)
	expectedHash = strings.TrimSpace(expectedHash)
	if expectedHash != "" && !strings.EqualFold(expectedHash, info.Hash) {
		matchLatest = false
		mismatch = append(mismatch, "hash_mismatch")
	}
	if strings.TrimSpace(expectedDomain) != "" && !certDomainMatched(info, expectedDomain) {
		matchLatest = false
		mismatch = append(mismatch, "domain_mismatch")
	}
	if expectedNotAfterMS > 0 && info.NotAfterMS != expectedNotAfterMS {
		matchLatest = false
		mismatch = append(mismatch, "not_after_mismatch")
	}
	if !anyTLSInstalledCertValidAt(info, time.Now()) {
		matchLatest = false
		mismatch = append(mismatch, "expired_or_not_yet_valid")
	}

	installedPEM, _, _ := readCompatFile(primaryPath, legacyPath)
	installedPin := certFingerprintSHA256(string(installedPEM))
	// Validate the configured port set, not just surviving runtimes: a failed
	// reload may have removed a listener from anytlsRuntimes.
	anytlsMu.Lock()
	runtimeMatch := anyTLSRuntimeCertificateMatches(anytlsConfigs, anytlsRuntimes, installedPin)
	configuredPorts := len(anytlsConfigs)
	anytlsMu.Unlock()
	if !runtimeMatch {
		matchLatest = false
		mismatch = append(mismatch, "runtime_cert_mismatch")
	}
	data := map[string]any{
		"context":         context,
		"processId":       anyTLSProcessID,
		"configuredPorts": configuredPorts,
		"installed": map[string]any{
			"path":              certPath,
			"hash":              info.Hash,
			"fingerprintSha256": certFingerprintSHA256(string(installedPEM)),
			"commonName":        info.CommonName,
			"dnsNames":          info.DNSNames,
			"subject":           info.Subject,
			"issuer":            info.Issuer,
			"serial":            info.Serial,
			"notBeforeMs":       info.NotBeforeMS,
			"notAfterMs":        info.NotAfterMS,
		},
		"matchLatest": matchLatest,
	}
	if expectedHash != "" || strings.TrimSpace(expectedDomain) != "" || expectedNotAfterMS > 0 {
		data["expected"] = map[string]any{
			"hash":       expectedHash,
			"domain":     strings.TrimSpace(expectedDomain),
			"notAfterMs": expectedNotAfterMS,
		}
	}
	if len(mismatch) > 0 {
		data["mismatch"] = mismatch
	}
	msg := "本地证书检测完成（已是最新版）"
	if !matchLatest {
		msg = "本地证书检测完成（与目标版本不一致）"
	}
	emitAnyTLSCertLog("installed_check", msg, data)
}

func writeAnyTLSCertMeta(domain string, notBeforeMS int64, notAfterMS int64) {
	meta := anytlsCertMeta{
		Domain:      strings.TrimSpace(domain),
		NotBeforeMS: notBeforeMS,
		NotAfterMS:  notAfterMS,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return
	}
	_ = os.WriteFile(anytlsMetaPath, b, 0o600)
	_ = os.WriteFile(legacyAnyTLSMetaPath, b, 0o600)
}

func writeAnyTLSCertMaterial(domain string, certPEM string, keyPEM string, caPEM string, notBeforeMS int64, notAfterMS int64) (bool, error) {
	certPEM = strings.TrimSpace(certPEM)
	keyPEM = strings.TrimSpace(keyPEM)
	caPEM = strings.TrimSpace(caPEM)
	if certPEM == "" || keyPEM == "" {
		return false, nil
	}
	if _, err := tls.X509KeyPair([]byte(certPEM+"\n"), []byte(keyPEM+"\n")); err != nil {
		return false, fmt.Errorf("invalid tls cert payload: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(anytlsCertPath), 0o755); err != nil {
		return false, err
	}
	oldCert := readCompatContent(anytlsCertPath, legacyAnyTLSCertPath)
	oldKey := readCompatContent(anytlsKeyPath, legacyAnyTLSKeyPath)
	oldCA := readCompatContent(anytlsCAPath, legacyAnyTLSCAPath)
	changed := strings.TrimSpace(string(oldCert)) != certPEM ||
		strings.TrimSpace(string(oldKey)) != keyPEM ||
		(caPEM != "" && strings.TrimSpace(string(oldCA)) != caPEM)
	hashShort := certHashShort(certPEM)
	if changed {
		if err := os.WriteFile(anytlsCertPath, []byte(certPEM+"\n"), 0o600); err != nil {
			emitAnyTLSCertLog("material_write_err", "写入证书文件失败", map[string]any{"error": err.Error(), "path": anytlsCertPath})
			return false, err
		}
		mirrorLegacyFile(legacyAnyTLSCertPath, certPEM)
		if err := os.WriteFile(anytlsKeyPath, []byte(keyPEM+"\n"), 0o600); err != nil {
			emitAnyTLSCertLog("material_write_err", "写入私钥文件失败", map[string]any{"error": err.Error(), "path": anytlsKeyPath})
			return false, err
		}
		mirrorLegacyFile(legacyAnyTLSKeyPath, keyPEM)
		if caPEM != "" {
			if err := os.WriteFile(anytlsCAPath, []byte(caPEM+"\n"), 0o600); err != nil {
				emitAnyTLSCertLog("material_write_err", "写入CA文件失败", map[string]any{"error": err.Error(), "path": anytlsCAPath})
				return false, err
			}
			mirrorLegacyFile(legacyAnyTLSCAPath, caPEM)
		}
		writeAnyTLSCertMeta(domain, notBeforeMS, notAfterMS)
		log.Printf("{\"event\":\"edge_cert_material_updated\",\"domain\":%q,\"notAfterMs\":%d,\"hash\":%q}", strings.TrimSpace(domain), notAfterMS, hashShort)
		emitAnyTLSCertLog("material_updated", "证书文件已更新", map[string]any{
			"domain":      strings.TrimSpace(domain),
			"notBeforeMs": notBeforeMS,
			"notAfterMs":  notAfterMS,
			"hash":        hashShort,
		})
	} else {
		log.Printf("{\"event\":\"edge_cert_material_unchanged\",\"domain\":%q,\"notAfterMs\":%d,\"hash\":%q}", strings.TrimSpace(domain), notAfterMS, hashShort)
		emitAnyTLSCertLog("material_unchanged", "证书文件无变化", map[string]any{
			"domain":      strings.TrimSpace(domain),
			"notBeforeMs": notBeforeMS,
			"notAfterMs":  notAfterMS,
			"hash":        hashShort,
		})
	}
	emitAnyTLSInstalledCheck("post_material_write", domain, notAfterMS, hashShort)
	return changed, nil
}

func reloadAnyTLSRuntimesForCert(skipPort int) {
	anytlsMu.Lock()
	cfgs := make([]anytlsConfig, 0, len(anytlsConfigs))
	for _, cfg := range anytlsConfigs {
		cfgs = append(cfgs, cfg)
	}
	anytlsMu.Unlock()
	if len(cfgs) == 0 {
		return
	}
	sort.Slice(cfgs, func(i, j int) bool { return cfgs[i].Port < cfgs[j].Port })
	if skipPort > 0 {
		filtered := make([]anytlsConfig, 0, len(cfgs))
		for _, cfg := range cfgs {
			if cfg.Port == skipPort {
				continue
			}
			filtered = append(filtered, cfg)
		}
		cfgs = filtered
	}
	if len(cfgs) == 0 {
		emitAnyTLSInstalledCheck("post_reload", "", 0, "")
		return
	}
	okCount := 0
	for _, cfg := range cfgs {
		anytlsMu.Lock()
		stopAnyTLSPortLocked(cfg.Port)
		anytlsMu.Unlock()
		if err := startAnyTLS(cfg); err != nil {
			log.Printf("{\"event\":\"edge_cert_reload_err\",\"port\":%d,\"error\":%q}", cfg.Port, err.Error())
			emitAnyTLSCertLog("reload_err", "证书热重载失败", map[string]any{"port": cfg.Port, "error": err.Error()})
		} else {
			okCount++
		}
	}
	emitAnyTLSCertLog("reload_done", "证书热重载完成", map[string]any{"total": len(cfgs), "ok": okCount, "failed": len(cfgs) - okCount})
	if okCount == len(cfgs) {
		emitAnyTLSInstalledCheck("post_reload", "", 0, "")
	}
}

func pullAnyTLSCertFromPanel(addr, secret, scheme string) (bool, error) {
	addr = strings.TrimSpace(addr)
	secret = strings.TrimSpace(secret)
	if addr == "" || secret == "" {
		return false, nil
	}
	paths := []string{
		"/api/v1/agent/docker-cert",
		"/api/v1/agent/anytls-cert",
	}
	var (
		code    int
		body    []byte
		err     error
		usedURL string
	)
	for _, p := range paths {
		usedURL = apiURL(scheme, addr, p)
		code, body, err = httpPostJSON(usedURL, map[string]any{"secret": secret})
		if err != nil {
			continue
		}
		if code == http.StatusNotFound {
			continue
		}
		break
	}
	if err != nil {
		emitAnyTLSCertLog("pull_err", "拉取证书请求失败", map[string]any{"error": err.Error()})
		return false, err
	}
	if code/100 != 2 {
		emitAnyTLSCertLog("pull_err", "拉取证书返回非2xx", map[string]any{"status": code, "url": usedURL})
		return false, fmt.Errorf("http %d", code)
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Enabled       bool   `json:"enabled"`
			CertDomain    string `json:"certDomain"`
			CertPEM       string `json:"certPem"`
			KeyPEM        string `json:"keyPem"`
			CAPEM         string `json:"caPem"`
			NotBeforeMS   int64  `json:"certNotBeforeMs"`
			NotAfterMS    int64  `json:"certNotAfterMs"`
			EnforceVerify bool   `json:"enforceVerify"`
		} `json:"data"`
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		emitAnyTLSCertLog("pull_err", "解析证书响应失败", map[string]any{"error": err.Error()})
		return false, err
	}
	if resp.Code != 0 {
		emitAnyTLSCertLog("pull_err", "控制器返回证书错误", map[string]any{"code": resp.Code, "message": resp.Msg})
		return false, errors.New(resp.Msg)
	}
	if !resp.Data.Enabled {
		emitAnyTLSCertLog("pull_skip", "控制器未启用站点证书", nil)
		return false, nil
	}
	return writeAnyTLSCertMaterial(
		resp.Data.CertDomain,
		resp.Data.CertPEM,
		resp.Data.KeyPEM,
		resp.Data.CAPEM,
		resp.Data.NotBeforeMS,
		resp.Data.NotAfterMS,
	)
}

func periodicAnyTLSCertRefresh(addr, secret, scheme string, done <-chan struct{}) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	tryRefresh := func() {
		if !anyTLSConfigured() {
			log.Printf("{\"event\":\"edge_cert_pull_skip\",\"reason\":\"no_edge_config\"}")
			emitAnyTLSCertLog("pull_skip", "未配置出口服务，跳过证书拉取", map[string]any{"reason": "no_tls_config"})
			return
		}
		changed, err := pullAnyTLSCertFromPanel(addr, secret, scheme)
		if err != nil {
			log.Printf("{\"event\":\"edge_cert_pull_err\",\"error\":%q}", err.Error())
			emitAnyTLSCertLog("pull_err", "周期证书拉取失败", map[string]any{"error": err.Error()})
			return
		}
		if changed {
			log.Printf("{\"event\":\"edge_cert_updated\"}")
			emitAnyTLSCertLog("updated", "周期证书拉取发现更新", nil)
			reloadAnyTLSRuntimesForCert(0)
		} else {
			log.Printf("{\"event\":\"edge_cert_unchanged\"}")
			emitAnyTLSCertLog("unchanged", "周期证书拉取无变化", nil)
			emitAnyTLSInstalledCheck("post_reload", "", 0, "")
		}
	}
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		tryRefresh()
	}
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			tryRefresh()
		}
	}
}

func ensureAnyTLSCert(certDomain string, enforceVerify bool) (*tls.Certificate, error) {
	if !enforceVerify || strings.TrimSpace(certDomain) == "" {
		return nil, fmt.Errorf("controller-issued certificate with domain and enforcement required")
	}
	certPEM, _, err := readCompatFile(anytlsCertPath, legacyAnyTLSCertPath)
	if err != nil {
		return nil, fmt.Errorf("controller-issued certificate missing: %w", err)
	}
	keyPEM, _, err := readCompatFile(anytlsKeyPath, legacyAnyTLSKeyPath)
	if err != nil {
		return nil, fmt.Errorf("controller-issued certificate key missing: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, fmt.Errorf("invalid controller-issued certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.VerifyHostname(certDomain) != nil || time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		return nil, fmt.Errorf("controller-issued certificate does not match domain or is expired")
	}
	return &pair, nil
}
