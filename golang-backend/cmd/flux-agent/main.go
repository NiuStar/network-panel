package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	crand "crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	rdebug "runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"debug/elf"

	"sync"
	"sync/atomic"

	bt "network-panel/golang-backend/internal/pkg/backtrace"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var (
	newline         = []byte{'\n'}
	space           = []byte{' '}
	opLogCh         = make(chan map[string]any, 128)
	certLogCh       = make(chan map[string]any, 256)
	anyTLSProcessID string
	wsWriteMu       sync.Map // map[*websocket.Conn]*sync.Mutex
	// Reuse one HTTP client for local GOST API calls to reduce allocations.
	gostAPIHTTPClient = &http.Client{Timeout: 5 * time.Second}
	// Reuse one HTTP client for panel APIs to avoid repeated allocations.
	panelHTTPClient = &http.Client{Timeout: 6 * time.Second}
	// Verbose body logging is disabled by default to reduce memory churn.
	gostAPIVerboseBody int32 = 0
	gostAPIBodyCap     int32 = 2048
	// Sample high-frequency successful API logs (not errors).
	gostAPISuccessLogEvery  int32  = 12
	gostAPIOpLogOK          int32  = 0
	gostSvcGetRespCount     uint64 = 0
	gostAPIServicesMaxItems int32  = 4096
	gostConfigMaxBytes      int64  = 4 * 1024 * 1024
	// Unknown message payload logging cap to avoid memory/log spikes.
	unknownMsgPayloadCap int32 = 512
)

func wsWriteMuFor(c *websocket.Conn) *sync.Mutex {
	if c == nil {
		return &sync.Mutex{}
	}
	if v, ok := wsWriteMu.Load(c); ok {
		if mu, ok2 := v.(*sync.Mutex); ok2 {
			return mu
		}
	}
	mu := &sync.Mutex{}
	wsWriteMu.Store(c, mu)
	return mu
}

func wsWriteJSON(c *websocket.Conn, v any) error {
	mu := wsWriteMuFor(c)
	mu.Lock()
	defer mu.Unlock()
	return c.WriteJSON(v)
}

func wsWriteMessage(c *websocket.Conn, mt int, data []byte) error {
	mu := wsWriteMuFor(c)
	mu.Lock()
	defer mu.Unlock()
	return c.WriteMessage(mt, data)
}

func wsWriteControl(c *websocket.Conn, mt int, data []byte, deadline time.Time) error {
	mu := wsWriteMuFor(c)
	mu.Lock()
	defer mu.Unlock()
	return c.WriteControl(mt, data, deadline)
}

// versionBase is the agent semantic version (without role prefix).
// final reported version is: go-agent-<versionBase> or go-agent2-<versionBase>
var versionBase = "2.0.0.31"
var version = ""      // computed in main()
var apiBootDone int32 // 0=not attempted, 1=attempted
var apiUse int32      // 1=Web API usable

// terminal session (single) state
type shellSession struct {
	id     string
	cmd    *exec.Cmd
	ptmx   *os.File
	buf    bytes.Buffer
	closed bool
	mu     sync.Mutex
}

var (
	shellMu       sync.Mutex
	activeShell   *shellSession
	defaultRows   = 24
	defaultCols   = 80
	shellBufLimit = 256 * 1024 // 256KB history
)

func isAgent2Binary() bool {
	base := filepath.Base(os.Args[0])
	return strings.Contains(base, "flux-agent2")
}

type DiagnoseData struct {
	RequestID string                 `json:"requestId"`
	Host      string                 `json:"host"`
	Port      int                    `json:"port,omitempty"`
	Protocol  string                 `json:"protocol,omitempty"`
	Mode      string                 `json:"mode,omitempty"` // icmp|iperf3|tcp(default)
	Count     int                    `json:"count,omitempty"`
	TimeoutMs int                    `json:"timeoutMs,omitempty"`
	Reverse   bool                   `json:"reverse,omitempty"`
	Duration  int                    `json:"duration,omitempty"`
	Server    bool                   `json:"server,omitempty"`
	Client    bool                   `json:"client,omitempty"`
	Ctx       map[string]interface{} `json:"ctx,omitempty"`
}

type QueryServicesReq struct {
	RequestID string `json:"requestId"`
	Filter    string `json:"filter,omitempty"` // e.g. "ss"
}

// Control message from server; Data varies by Type
type Message struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type Message2 struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data"`
}

type SuggestPortsReq struct {
	RequestID string `json:"requestId"`
	Base      int    `json:"base"`
	Count     int    `json:"count"`
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func tuneRuntimeMemory() {
	if v := strings.TrimSpace(getenv("AGENT_GOGC", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rdebug.SetGCPercent(n)
			log.Printf("{\"event\":\"runtime_gc_tuned\",\"gogc\":%d}", n)
		}
	}
	if v := strings.TrimSpace(getenv("AGENT_MEM_LIMIT_MB", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit := int64(n) * 1024 * 1024
			rdebug.SetMemoryLimit(limit)
			log.Printf("{\"event\":\"runtime_mem_limit_tuned\",\"mb\":%d}", n)
		}
	}
}

func startPprofServer() {
	addr := strings.TrimSpace(getenv("AGENT_PPROF_ADDR", ""))
	if addr == "" {
		return
	}
	if _, err := startAgentPprof(addr); err != nil {
		log.Printf("{\"event\":\"pprof_boot_err\",\"addr\":%q,\"error\":%q}", addr, err.Error())
	}
}

func envBool(k string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(getenv(k, "")))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func cappedPayloadString(b []byte, max int) string {
	if max < 0 {
		max = 0
	}
	if max == 0 {
		return ""
	}
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}

func logUnknownMsg(parseErr error, msg []byte) {
	capN := int(atomic.LoadInt32(&unknownMsgPayloadCap))
	if capN < 0 {
		capN = 0
	}
	payload := cappedPayloadString(msg, capN)
	if parseErr != nil {
		log.Printf("{\"event\":\"unknown_msg\",\"error\":%q,\"payload\":%q}", parseErr.Error(), payload)
		return
	}
	log.Printf("{\"event\":\"unknown_msg\",\"payload\":%q}", payload)
}

func singleAgentMode() bool {
	v := strings.ToLower(strings.TrimSpace(getenv("SINGLE_AGENT", "1")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// ensureAgentID returns a stable unique id, preferring machine-id; otherwise persists to /etc/gost/agent_uid.
func ensureAgentID() string {
	paths := []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			id := strings.TrimSpace(string(b))
			if id != "" {
				return "mid-" + id
			}
		}
	}
	store := "/etc/gost/agent_uid"
	if b, err := os.ReadFile(store); err == nil && len(b) > 0 {
		return strings.TrimSpace(string(b))
	}
	_ = os.MkdirAll("/etc/gost", 0o755)
	if ip := fetchExternalIP(); ip != "" {
		sum := md5.Sum([]byte(ip))
		id := fmt.Sprintf("hip-%x", sum[:4])
		_ = os.WriteFile(store, []byte(id), 0o644)
		return id
	}
	id := fmt.Sprintf("uid-%d-%d", time.Now().UnixNano(), rand.Int63())
	_ = os.WriteFile(store, []byte(id), 0o644)
	return id
}

// ensureCreatedAt records first heartbeat time to keep CreatedAtMs stable across restarts.
func ensureCreatedAt() int64 {
	store := "/etc/gost/agent_created_ms"
	if b, err := os.ReadFile(store); err == nil {
		if ms, err2 := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err2 == nil && ms > 0 {
			return ms
		}
	}
	now := time.Now().UnixMilli()
	_ = os.WriteFile(store, []byte(fmt.Sprintf("%d", now)), 0o644)
	return now
}

// fetchExternalIP attempts to read public IP (used for stable ID and reporting).
func fetchExternalIP() string {
	url := getenv("IP_LOOKUP_URL", "https://api.ip.sb/ip")
	client := &http.Client{Timeout: 3 * time.Second}
	if resp, err := client.Get(url); err == nil {
		defer resp.Body.Close()
		if b, err2 := io.ReadAll(io.LimitReader(resp.Body, 4096)); err2 == nil {
			ip := strings.TrimSpace(string(b))
			if ip != "" {
				return ip
			}
		}
	}
	return ""
}

// heartbeatLoop posts heartbeat periodically (hourly) to the panel.
func heartbeatLoop(url, uid, ver string, createdMs int64) {
	tick := time.NewTicker(1 * time.Hour)
	defer tick.Stop()
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	send := func() {
		ip := fetchExternalIP()
		body := map[string]any{
			"kind":        "agent",
			"uniqueId":    uid,
			"version":     ver,
			"os":          runtime.GOOS,
			"arch":        runtime.GOARCH,
			"createdAtMs": createdMs,
			"installMode": "agent",
			"ip":          ip,
		}
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
	send()
	for range tick.C {
		send()
	}
}

func disableService(name string) {
	if _, err := exec.LookPath("systemctl"); err == nil {
		_ = exec.Command("systemctl", "disable", name).Run()
		_ = exec.Command("systemctl", "stop", name).Run()
		_ = exec.Command("systemctl", "daemon-reload").Run()
	} else if _, err := exec.LookPath("service"); err == nil {
		_ = exec.Command("service", name, "stop").Run()
	}
}

func readPanelConfig() (addr, secret string) {
	// fallback order:
	// 1) /etc/gost/config.json {addr, secret}
	// 2) /etc/default/flux-agent (ADDR/SECRET)
	// 3) /etc/default/flux-agent2 (ADDR/SECRET)
	if a, s := readAddrSecretFromJSON("/etc/gost/config.json"); a != "" && s != "" {
		return a, s
	}
	if a, s := readAddrSecretFromEnvFile("/etc/default/flux-agent"); a != "" && s != "" {
		return a, s
	}
	if a, s := readAddrSecretFromEnvFile("/etc/default/flux-agent2"); a != "" && s != "" {
		return a, s
	}
	return "", ""
}

func readAddrSecretFromJSON(path string) (addr, secret string) {
	f, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var m map[string]any
	if json.Unmarshal(f, &m) != nil {
		return "", ""
	}
	if v, ok := m["addr"].(string); ok {
		addr = strings.TrimSpace(v)
	}
	if v, ok := m["secret"].(string); ok {
		secret = strings.TrimSpace(v)
	}
	return addr, secret
}

func readAddrSecretFromEnvFile(path string) (addr, secret string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.Trim(strings.TrimSpace(v), `"'`)
		switch key {
		case "ADDR":
			if addr == "" {
				addr = val
			}
		case "SECRET":
			if secret == "" {
				secret = val
			}
		}
		if addr != "" && secret != "" {
			return addr, secret
		}
	}
	return addr, secret
}

func main() {
	var (
		flagAddr    = flag.String("a", "", "panel addr:port")
		flagSecret  = flag.String("s", "", "node secret")
		flagScheme  = flag.String("S", "", "ws or wss")
		flagVersion = flag.Bool("v", false, "print version and exit")
	)
	flag.Parse()

	// compute version and role by binary name
	if isAgent2Binary() {
		version = "go-agent2-" + versionBase
	} else {
		version = "go-agent-" + versionBase
	}
	if *flagVersion {
		fmt.Println(version)
		return
	}
	tuneRuntimeMemory()
	startPprofServer()
	go ensureNodeJournalLimits()

	addr := getenv("ADDR", *flagAddr)
	secret := getenv("SECRET", *flagSecret)
	scheme := getenv("SCHEME", *flagScheme)
	if scheme == "" {
		scheme = "ws"
	}
	for addr == "" || secret == "" {
		a2, s2 := readPanelConfig()
		if addr == "" {
			addr = a2
		}
		if secret == "" {
			secret = s2
		}
		if addr != "" && secret != "" {
			break
		}
		log.Printf("{\"event\":\"bootstrap_wait_config\",\"addr\":%t,\"secret\":%t,\"retrySec\":5}", addr != "", secret != "")
		time.Sleep(5 * time.Second)
	}
	if envBool("AGENT_GOST_API_VERBOSE", false) {
		atomic.StoreInt32(&gostAPIVerboseBody, 1)
	}
	if v := strings.TrimSpace(getenv("AGENT_GOST_API_BODY_CAP", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			atomic.StoreInt32(&gostAPIBodyCap, int32(n))
		}
	}
	if v := strings.TrimSpace(getenv("AGENT_GOST_API_SUCCESS_LOG_EVERY", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			atomic.StoreInt32(&gostAPISuccessLogEvery, int32(n))
		}
	}
	if v := strings.TrimSpace(getenv("AGENT_GOST_API_SERVICES_MAX_ITEMS", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			atomic.StoreInt32(&gostAPIServicesMaxItems, int32(n))
		}
	}
	if v := strings.TrimSpace(getenv("AGENT_GOST_CONFIG_MAX_BYTES", "")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			atomic.StoreInt64(&gostConfigMaxBytes, n)
		}
	}
	if envBool("AGENT_GOST_API_OPLOG_OK", false) {
		atomic.StoreInt32(&gostAPIOpLogOK, 1)
	}
	if v := strings.TrimSpace(getenv("AGENT_UNKNOWN_PAYLOAD_CAP", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			atomic.StoreInt32(&unknownMsgPayloadCap, int32(n))
		}
	}

	// In single-agent mode, prevent agent2 from running persistently
	if isAgent2Binary() && singleAgentMode() {
		log.Printf("{\"event\":\"single_agent_mode\",\"action\":\"agent2_exit\"}")
		disableService("flux-agent2")
		os.Exit(0)
	}

	agentID := ensureAgentID()
	createdMs := ensureCreatedAt()
	heartbeatURL := getenv("HEARTBEAT_ENDPOINT", "")
	if heartbeatURL == "" {
		heartbeatURL = "https://flux.199028.xyz/api/v1/stats/heartbeat"
	}
	go heartbeatLoop(heartbeatURL, agentID, version, createdMs)

	u := url.URL{Scheme: scheme, Host: addr, Path: "/system-info"}
	q := u.Query()
	q.Set("type", "1")
	q.Set("secret", secret)
	q.Set("version", version)
	if isAgent2Binary() {
		q.Set("role", "agent2")
	} else {
		q.Set("role", "agent1")
	}
	u.RawQuery = q.Encode()

	setAnyTLSPanelContext(addr, secret, scheme)
	var processErr error
	anyTLSProcessID, processErr = newAnyTLSProcessID()
	if processErr != nil {
		log.Printf("anytls process identity unavailable: %v", processErr)
		return
	}

	// 不再自动启用 Web API，仅做报告（前端可手动触发启用）。
	// 但如果已配置 API 且绑定为 :PORT / 0.0.0.0，则自动纠正到 127.0.0.1:PORT。
	go func() {
		time.Sleep(900 * time.Millisecond)
		_ = normalizeConfiguredGostAPIBindLocalOnly()
	}()
	cfgs, ok := loadAnyTLSConfigs()
	emitAnyTLSCertLog("agent_boot", "进程启动，旧证书运行时确认失效", nil)
	if changed, err := pullAnyTLSCertFromPanel(addr, secret, scheme); err != nil {
		log.Printf("{\"event\":\"edge_cert_boot_pull_err\",\"error\":%q}", err.Error())
		emitAnyTLSCertLog("boot_pull_err", "启动时证书拉取失败", map[string]any{"error": err.Error()})
	} else if changed {
		log.Printf("{\"event\":\"edge_cert_boot_pulled\"}")
		emitAnyTLSCertLog("boot_pulled", "启动时证书已更新", nil)
	} else {
		log.Printf("{\"event\":\"edge_cert_boot_unchanged\"}")
		emitAnyTLSCertLog("boot_unchanged", "启动时证书无变化", nil)
	}
	if ok {
		if startSavedAnyTLSConfigs(cfgs, startAnyTLS) {
			emitAnyTLSInstalledCheck("post_reload", "", 0, "")
		}
	} else {
		log.Printf("{\"event\":\"edge_boot_skip\",\"reason\":\"no_valid_edge_config\"}")
	}

	for {
		if err := runOnce(u.String(), addr, secret, scheme); err != nil {
			log.Printf("{\"event\":\"agent_error\",\"error\":%q}", err.Error())
		}
		time.Sleep(3 * time.Second)
	}
}

// dialWSWithFamily dials websocket with IP family preference: "4", "6", or "auto" (default).
func dialWSWithFamily(d *websocket.Dialer, wsURL string, family string) (*websocket.Conn, *http.Response, error) {
	fam := strings.TrimSpace(strings.ToLower(family))
	if fam == "4" || fam == "ipv4" {
		// force IPv4
		dialer := *d
		nd := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		dialer.NetDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return nd.DialContext(ctx, "tcp4", address)
		}
		return dialer.Dial(wsURL, nil)
	}
	if fam == "6" || fam == "ipv6" {
		// force IPv6
		dialer := *d
		nd := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		dialer.NetDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return nd.DialContext(ctx, "tcp6", address)
		}
		return dialer.Dial(wsURL, nil)
	}
	// auto: prefer IPv4 then IPv6
	if c, r, err := dialWSWithFamily(d, wsURL, "4"); err == nil {
		return c, r, nil
	}
	return dialWSWithFamily(d, wsURL, "6")
}

func runOnce(wsURL, addr, secret, scheme string) error {
	log.Printf("{\"event\":\"connecting\",\"url\":%q}", wsURL)
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	// TCP keepalive & proxy follow defaults via NetDialContext inside dialWSWithFamily
	if strings.HasPrefix(wsURL, "wss://") {
		d.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	// IP family preference via env WS_IP_FAMILY: "4", "6", or "auto"
	fam := getenv("WS_IP_FAMILY", "auto")
	c, _, err := dialWSWithFamily(&d, wsURL, fam)
	if err != nil {
		return err
	}
	defer c.Close()
	wsWriteMu.Store(c, &sync.Mutex{})
	defer wsWriteMu.Delete(c)
	log.Printf("{\"event\":\"connected\"}")
	// A new websocket epoch invalidates confirmations from an earlier connection.
	// A fresh installed check is queued only after the boot barrier.
	emitAnyTLSCertLog("agent_boot", "连接建立，旧运行时确认失效", nil)
	emitAnyTLSInstalledCheck("post_reload", "", 0, "")

	// 不在重连时自动启用/重启 GOST，仅保持心跳与命令通道

	// On connect, perform a best-effort reconcile to ensure desired services exist in gost.json.
	// Only adds missing services; doesn't delete unless STRICT_RECONCILE=1.
	go func() { time.Sleep(1200 * time.Millisecond); reconcile(addr, secret, scheme) }()
	done := make(chan struct{})
	defer close(done)
	// background probes and system info reporting
	go periodicProbe(addr, secret, scheme, done)
	go periodicSystemInfo(c, done)
	// Optional periodic reconcile via RECONCILE_INTERVAL (seconds, <=0 to disable). Default 300s.
	go periodicReconcile(addr, secret, scheme, done)
	// Periodically report local gost services snapshot to server for forward status aggregation
	go periodicReportServices(addr, secret, scheme, done)
	// Pull AnyTLS cert from controller periodically and hot-reload runtimes when rotated.
	go periodicAnyTLSCertRefresh(addr, secret, scheme, done)
	// Keep runtime/port confirmation fresh; a historical success expires when
	// the agent stops reporting, even if the websocket dies without a boot log.
	go func() {
		ticker := time.NewTicker(45 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				emitAnyTLSInstalledCheck("post_reload", "", 0, "")
			}
		}
	}()
	// OpLog forwarder: send queued op logs to server as {type:"OpLog", step, message, data}
	go func() {
		for {
			select {
			case m := <-opLogCh:
				if m == nil {
					continue
				}
				m["type"] = "OpLog"
				_ = wsWriteJSON(c, m)
			case <-done:
				return
			}
		}
	}()
	// TLSCertLog forwarder: send dedicated cert install/refresh logs.
	go func() {
		for {
			select {
			case m := <-certLogCh:
				if m == nil {
					continue
				}
				m["type"] = "TLSCertLog"
				_ = wsWriteJSON(c, m)
			case <-done:
				return
			}
		}
	}()
	// In single-agent mode, keep only agent1 active and ensure agent2 service is disabled.
	if !isAgent2Binary() && singleAgentMode() {
		go func() { disableService("flux-agent2") }()
	} else {
		// dual-agent behavior: cross-check counterpart version
		go func() {
			a1, a2 := getExpectedVersions(addr, scheme)
			if isAgent2Binary() {
				if a1 != "" {
					_ = upgradeAgent1(addr, scheme, a1)
				}
			} else {
				if a2 != "" {
					_ = upgradeAgent2(addr, scheme, a2)
				}
			}
		}()
	}

	// read loop
	c.SetReadLimit(1 << 20)
	// tighter ping/pong to reduce idle disconnects
	deadlineSec := 45
	if v := getenv("WS_DEADLINE_SEC", ""); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			deadlineSec = n
		}
	}
	c.SetReadDeadline(time.Now().Add(time.Duration(deadlineSec) * time.Second))
	c.SetPongHandler(func(string) error {
		c.SetReadDeadline(time.Now().Add(time.Duration(deadlineSec) * time.Second))
		return nil
	})

	go func() {
		pingSec := 15
		if v := getenv("WS_PING_SEC", ""); v != "" {
			if n, _ := strconv.Atoi(v); n > 0 {
				pingSec = n
			}
		}
		ticker := time.NewTicker(time.Duration(pingSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := wsWriteControl(c, websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second)); err != nil {
					// read loop will reconnect; stop this stale ping goroutine.
					return
				}
			}
		}
	}()

	for {

		_, msg, err := c.ReadMessage()
		if err != nil {
			return err
		}
		msg = bytes.TrimSpace(bytes.Replace(msg, newline, space, -1))
		var m *Message
		var m2 *Message2
		// primary parse
		if err := json.Unmarshal(msg, &m); err != nil {
			if es1 := json.Unmarshal(msg, &m2); es1 != nil {

				// fallback 1: double-encoded JSON string
				var s string
				if e2 := json.Unmarshal(msg, &s); e2 == nil && s != "" {
					if e3 := json.Unmarshal([]byte(s), &m); e3 == nil {
						// ok
					} else {
						// fallback 2: best-effort trim to first '{' and last '}'
						if i := strings.IndexByte(s, '{'); i >= 0 {
							if j := strings.LastIndexByte(s, '}'); j > i {
								if e4 := json.Unmarshal([]byte(s[i:j+1]), &m); e4 == nil {
									// ok
								} else {
									logUnknownMsg(e4, msg)
									continue
								}
							} else {
								logUnknownMsg(e3, msg)
								continue
							}
						} else {
							logUnknownMsg(e3, msg)
							continue
						}
					}
				} else {
					// fallback 3: raw bytes trim to first '{'..'}'
					bs := string(msg)
					if i := strings.IndexByte(bs, '{'); i >= 0 {
						if j := strings.LastIndexByte(bs, '}'); j > i {
							if e5 := json.Unmarshal([]byte(bs[i:j+1]), &m); e5 == nil {
								// ok
							} else {
								logUnknownMsg(e5, msg)
								continue
							}
						} else {
							logUnknownMsg(err, msg)
							continue
						}
					} else {
						logUnknownMsg(err, msg)
						continue
					}
				}
			}
		}
		if m == nil && m2 != nil {
			log.Printf("{\"event\":\"message2\",\"ok\":%q}", m2.Type)
			// convert Message2 to Message
			b, _ := json.Marshal(m2.Data)
			m = &Message{Type: m2.Type, Data: b}
		} else {
			log.Printf("{\"event\":\"message\",\"ok\":%q}", m.Type)
		}
		switch m.Type {
		case "Diagnose":
			var d DiagnoseData
			_ = json.Unmarshal(m.Data, &d)
			log.Printf("{\"event\":\"recv_diagnose\",\"data\":%s}", string(mustJSON(d)))
			go handleDiagnose(c, &d)
		case "AddService":
			var services []map[string]any
			reqID := ""
			if err := json.Unmarshal(m.Data, &services); err != nil {
				// try wrapped payload {requestId, services:[...]}
				var wrap struct {
					RequestID string           `json:"requestId"`
					Services  []map[string]any `json:"services"`
				}
				if err2 := json.Unmarshal(m.Data, &wrap); err2 == nil && len(wrap.Services) > 0 {
					services = wrap.Services
					reqID = wrap.RequestID
				} else {
					log.Printf("{\"event\":\"svc_cmd_parse_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
					continue
				}
			}
			if err := addOrUpdateServices(services, false); err != nil {
				log.Printf("{\"event\":\"svc_cmd_apply_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
				emitOpLog("gost_api_err", "apply AddService failed", map[string]any{"error": err.Error()})
				if reqID != "" {
					_ = wsWriteJSON(c, map[string]any{"type": "AddServiceResult", "requestId": reqID, "data": map[string]any{"success": false, "message": err.Error()}})
				}
			} else {
				log.Printf("{\"event\":\"svc_cmd_applied\",\"type\":%q,\"count\":%d}", m.Type, len(services))
				if reqID != "" {
					_ = wsWriteJSON(c, map[string]any{"type": "AddServiceResult", "requestId": reqID, "data": map[string]any{"success": true, "message": "ok"}})
				}
			}
		case "SetEdgeTLS", "SetAnyTLS":
			var req struct {
				RequestID     string           `json:"requestId"`
				Port          int              `json:"port"`
				Password      string           `json:"password"`
				BaseUserID    int64            `json:"baseUserId"`
				ExitIP        string           `json:"exitIp"`
				CertDomain    string           `json:"certDomain"`
				CertPEM       string           `json:"certPem"`
				KeyPEM        string           `json:"keyPem"`
				CAPEM         string           `json:"caPem"`
				CertNotBefore int64            `json:"certNotBeforeMs"`
				CertNotAfter  int64            `json:"certNotAfterMs"`
				EnforceVerify *bool            `json:"enforceVerify"`
				AllowFallback *bool            `json:"allowFallback"`
				Remove        bool             `json:"remove"`
				Users         []anytlsUserRule `json:"users"`
			}
			_ = json.Unmarshal(m.Data, &req)
			// Backward compatibility: if controller payload has no cert material,
			// pull cert once from panel before applying config.
			if !req.Remove && (strings.TrimSpace(req.CertPEM) == "" || strings.TrimSpace(req.KeyPEM) == "") {
				changed, perr := pullAnyTLSCertFromPanel(addr, secret, scheme)
				if perr != nil {
					log.Printf("{\"event\":\"edge_cert_set_pull_err\",\"error\":%q}", perr.Error())
					emitAnyTLSCertLog("set_pull_err", "应用出口配置前证书拉取失败", map[string]any{"error": perr.Error()})
				} else if changed {
					log.Printf("{\"event\":\"edge_cert_set_pulled\"}")
					emitAnyTLSCertLog("set_pulled", "应用出口配置前证书已更新", nil)
				} else {
					log.Printf("{\"event\":\"edge_cert_set_unchanged\"}")
					emitAnyTLSCertLog("set_unchanged", "应用出口配置前证书无变化", nil)
				}
			}
			allowFallback := true
			if req.AllowFallback != nil {
				allowFallback = *req.AllowFallback
			}
			enforceVerify := false
			if req.EnforceVerify != nil {
				enforceVerify = *req.EnforceVerify
			} else if strings.TrimSpace(req.CertPEM) != "" || strings.TrimSpace(req.KeyPEM) != "" {
				enforceVerify = true
			}
			log.Printf("{\"event\":\"edge_set\",\"port\":%d,\"exitIp\":%q,\"enforceVerify\":%v,\"allowFallback\":%v,\"baseUserId\":%d,\"remove\":%v,\"certDomain\":%q}", req.Port, req.ExitIP, enforceVerify, allowFallback, req.BaseUserID, req.Remove, req.CertDomain)
			var err error
			if req.Remove {
				err = removeAnyTLSConfig(req.Port)
			} else {
				err = applyAnyTLSConfig(
					req.Port,
					req.Password,
					req.ExitIP,
					enforceVerify,
					allowFallback,
					req.BaseUserID,
					req.Users,
					req.CertDomain,
					req.CertPEM,
					req.KeyPEM,
					req.CAPEM,
					req.CertNotBefore,
					req.CertNotAfter,
				)
			}
			msg := "ok"
			if err != nil {
				msg = err.Error()
			}
			if req.RequestID != "" {
				respType := "SetAnyTLSResult"
				if m.Type == "SetEdgeTLS" {
					respType = "SetEdgeTLSResult"
				}
				_ = wsWriteJSON(c, map[string]any{"type": respType, "requestId": req.RequestID, "data": map[string]any{"success": err == nil, "message": msg}})
			}
		case "SingboxTest":
			var req singboxTestReq
			_ = json.Unmarshal(m.Data, &req)
			if req.RequestID == "" || req.Outbound == nil {
				var raw map[string]any
				if err := json.Unmarshal(m.Data, &raw); err == nil {
					if req.RequestID == "" {
						if v, ok := raw["requestId"].(string); ok {
							req.RequestID = v
						}
					}
					if req.Outbound == nil {
						if v, ok := raw["outbound"].(map[string]any); ok {
							req.Outbound = v
						}
					}
				}
			}
			go func() {
				log.Printf("{\"event\":\"singbox_test\",\"mode\":%q,\"url\":%q,\"requestId\":%q}", req.Mode, req.URL, req.RequestID)
				res := runSingboxTest(req)
				log.Printf("{\"event\":\"singbox_test_done\",\"requestId\":%q,\"success\":%v,\"message\":%q}", req.RequestID, res["success"], res["message"])
				if req.RequestID == "" {
					log.Printf("{\"event\":\"singbox_test_no_request_id\"}")
					return
				}
				if err := wsWriteJSON(c, map[string]any{"type": "SingboxTestResult", "requestId": req.RequestID, "data": res}); err != nil {
					log.Printf("{\"event\":\"singbox_test_send_err\",\"requestId\":%q,\"error\":%q}", req.RequestID, err.Error())
				}
			}()
		case "LogCaptureStart":
			var req struct {
				RequestID string `json:"requestId"`
				Target    string `json:"target"`
			}
			_ = json.Unmarshal(m.Data, &req)
			_ = startLogCapture(req.RequestID, req.Target)
		case "LogCaptureStop":
			var req struct {
				RequestID string `json:"requestId"`
				Target    string `json:"target"`
			}
			_ = json.Unmarshal(m.Data, &req)
			data := stopLogCapture(req.RequestID, req.Target)
			if req.RequestID != "" {
				_ = wsWriteJSON(c, map[string]any{"type": "LogCaptureResult", "requestId": req.RequestID, "data": data})
			}
		case "UpdateService":
			var services []map[string]any
			if err := json.Unmarshal(m.Data, &services); err != nil {
				log.Printf("{\"event\":\"svc_cmd_parse_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
				continue
			}
			if err := addOrUpdateServices(services, true); err != nil {
				log.Printf("{\"event\":\"svc_cmd_apply_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
				emitOpLog("gost_api_err", "apply UpdateService failed", map[string]any{"error": err.Error()})
			} else {
				log.Printf("{\"event\":\"svc_cmd_applied\",\"type\":%q,\"count\":%d}", m.Type, len(services))
			}
		case "UpsertLimiters":
			var limiters []map[string]any
			if err := json.Unmarshal(m.Data, &limiters); err != nil {
				log.Printf("{\"event\":\"svc_cmd_parse_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
				continue
			}
			if err := apiConfigLimiters(limiters, false); err != nil {
				log.Printf("{\"event\":\"svc_cmd_apply_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
				emitOpLog("gost_api_err", "apply UpsertLimiters failed", map[string]any{"error": err.Error()})
			} else {
				log.Printf("{\"event\":\"svc_cmd_applied\",\"type\":%q,\"count\":%d}", m.Type, len(limiters))
			}
		case "GetService":
			var req struct {
				RequestID string `json:"requestId"`
				Name      string `json:"name"`
			}
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				var svc map[string]any
				if req.Name != "" {
					if m, _, err := apiGetByName("services", req.Name); err == nil && m != nil {
						svc = m
					}
				}
				// fallback: scan list
				if svc == nil {
					list := queryServices("")
					for _, it := range list {
						if n, _ := it["name"].(string); n == req.Name {
							svc = it
							break
						}
					}
				}
				resp := map[string]any{"type": "GetServiceResult", "requestId": req.RequestID}
				if svc != nil {
					resp["data"] = svc
				} else {
					resp["data"] = nil
				}
				_ = wsWriteJSON(c, resp)
			}()
		case "DeleteService":
			var req struct {
				Services []string `json:"services"`
			}
			if err := json.Unmarshal(m.Data, &req); err != nil {
				log.Printf("{\"event\":\"svc_cmd_parse_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
				continue
			}
			if err := deleteServices(req.Services); err != nil {
				log.Printf("{\"event\":\"svc_cmd_apply_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
			} else {
				log.Printf("{\"event\":\"svc_cmd_applied\",\"type\":%q,\"count\":%d}", m.Type, len(req.Services))
			}
		case "PauseService":
			var req struct {
				Services []string `json:"services"`
			}
			if err := json.Unmarshal(m.Data, &req); err != nil {
				log.Printf("{\"event\":\"svc_cmd_parse_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
				continue
			}
			if err := markServicesPaused(req.Services, true); err != nil {
				log.Printf("{\"event\":\"svc_cmd_apply_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
			} else {
				log.Printf("{\"event\":\"svc_cmd_applied\",\"type\":%q,\"count\":%d}", m.Type, len(req.Services))
			}
		case "ResumeService":
			var req struct {
				Services []string `json:"services"`
			}
			if err := json.Unmarshal(m.Data, &req); err != nil {
				log.Printf("{\"event\":\"svc_cmd_parse_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
				continue
			}
			if err := markServicesPaused(req.Services, false); err != nil {
				log.Printf("{\"event\":\"svc_cmd_apply_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
			} else {
				log.Printf("{\"event\":\"svc_cmd_applied\",\"type\":%q,\"count\":%d}", m.Type, len(req.Services))
			}
		case "QueryServices":
			var q QueryServicesReq
			_ = json.Unmarshal(m.Data, &q)
			list := queryServices(q.Filter)
			out := map[string]any{"type": "QueryServicesResult", "requestId": q.RequestID, "data": list}
			_ = wsWriteJSON(c, out)
			log.Printf("{\"event\":\"send_qs_result\",\"count\":%d}", len(list))
		case "SuggestPorts":
			var req SuggestPortsReq
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				ports := suggestPorts(req.Base, req.Count)
				resp := map[string]any{"type": "SuggestPortsResult", "requestId": req.RequestID, "data": map[string]any{"ports": ports}}
				_ = wsWriteJSON(c, resp)
			}()
		case "ProbePort":
			var req struct {
				RequestID string `json:"requestId"`
				Port      int    `json:"port"`
			}
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				listening := portListening(req.Port)
				resp := map[string]any{"type": "ProbePortResult", "requestId": req.RequestID, "data": map[string]any{"port": req.Port, "listening": listening}}
				_ = wsWriteJSON(c, resp)
			}()
		case "EnableGostAPI":
			var req struct {
				Port int `json:"port"`
			}
			_ = json.Unmarshal(m.Data, &req)
			port := resolveGostAPIPort(req.Port)
			cfgPath := resolveGostConfigPathForWrite()
			// 手动启用 GOST 顶层 API 并重启服务
			emitOpLog("gost_api", "enable start", map[string]any{"message": "write top-level api and restart gost", "port": port, "bind": gostAPIBindAddr(port), "configPath": cfgPath})
			if err := ensureGostAPITopLevel(port); err != nil {
				emitOpLog("gost_api_err", "enable failed (write)", map[string]any{"error": err.Error()})
				log.Printf("{\"event\":\"enable_api_failed\",\"error\":%q}", err.Error())
				continue
			}
			if err := restartGostService(); err != nil {
				emitOpLog("gost_api_err", "enable failed (restart)", map[string]any{"error": err.Error()})
				continue
			}
			time.Sleep(600 * time.Millisecond)
			loopbackOK, detail := verifyGostAPIBindLoopbackOnly(port)
			emitOpLog("gost_api", "bind verify", map[string]any{
				"port":         port,
				"loopbackOnly": loopbackOK,
				"detail":       detail,
			})
			if !loopbackOK {
				emitOpLog("gost_api_err", "bind verify failed, try normalize services", map[string]any{
					"port":   port,
					"detail": detail,
				})
				cfg := readGostConfig()
				removed := removeLegacyAPIServices(cfg, port)
				if removed > 0 || !gostAPILocalOnlyFromConfig(cfg) {
					u, p := apiCreds()
					cfg["api"] = map[string]any{
						"addr":       gostAPIBindAddr(port),
						"pathPrefix": "/api",
						"accesslog":  true,
						"auth": map[string]any{
							"username": u,
							"password": p,
						},
					}
					if err := writeGostConfig(cfg); err == nil {
						_ = restartGostService()
						time.Sleep(800 * time.Millisecond)
						loopbackOK, detail = verifyGostAPIBindLoopbackOnly(port)
						emitOpLog("gost_api", "bind verify retry", map[string]any{
							"port":         port,
							"loopbackOnly": loopbackOK,
							"detail":       detail,
							"removed":      removed,
						})
					}
				}
			}
			enforceGostAPIFirewall(port)
			time.Sleep(1200 * time.Millisecond)
			ok := apiAvailable()
			emitOpLog("gost_api", "enable done", map[string]any{"available": ok, "port": port, "bind": gostAPIBindAddr(port), "configPath": cfgPath})
			log.Printf("{\"event\":\"enable_api_done\",\"ok\":%v,\"port\":%d,\"bind\":%q,\"configPath\":%q}", ok, port, gostAPIBindAddr(port), cfgPath)
			// update cached usable state
			if ok {
				atomic.StoreInt32(&apiUse, 1)
			}
			continue
		case "UpgradeAgent":
			// optional payload: {to: "go-agent-1.x.y"}
			if !envBool("AGENT_AUTO_UPGRADE", true) {
				log.Printf("{\"event\":\"agent_upgrade_skip\",\"reason\":\"disabled_by_env\"}")
			} else {
				go func() { _ = selfUpgrade(addr, scheme) }()
			}
		case "UpgradeAgent1":
			if !envBool("AGENT_AUTO_UPGRADE", true) {
				log.Printf("{\"event\":\"agent_upgrade_skip\",\"reason\":\"disabled_by_env\",\"target\":\"agent1\"}")
			} else {
				go func() { _ = upgradeAgent1(addr, scheme, "") }()
			}
		case "UpgradeAgent2":
			if !envBool("AGENT_AUTO_UPGRADE", true) {
				log.Printf("{\"event\":\"agent_upgrade_skip\",\"reason\":\"disabled_by_env\",\"target\":\"agent2\"}")
			} else {
				go func() { _ = upgradeAgent2(addr, scheme, "") }()
			}
		case "RestartGost":
			go func() { _ = restartGostService() }()
		case "UninstallAgent":
			go func() {
				_ = uninstallSelf()
			}()
		case "ShellStart":
			var req struct {
				SessionID string `json:"sessionId"`
				Rows      int    `json:"rows"`
				Cols      int    `json:"cols"`
			}
			_ = json.Unmarshal(m.Data, &req)
			go startShellSession(req.SessionID, req.Rows, req.Cols, c)
		case "ShellInput":
			var req struct {
				SessionID string `json:"sessionId"`
				Data      string `json:"data"`
			}
			_ = json.Unmarshal(m.Data, &req)
			go shellInput(req.SessionID, req.Data, c)
		case "ShellResize":
			var req struct {
				SessionID string `json:"sessionId"`
				Rows      int    `json:"rows"`
				Cols      int    `json:"cols"`
			}
			_ = json.Unmarshal(m.Data, &req)
			go shellResize(req.SessionID, req.Rows, req.Cols, c)
		case "ShellStop":
			var req struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(m.Data, &req)
			go shellStop(req.SessionID, c)
		case "RunScript":
			var req map[string]any
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				reqID, _ := req["requestId"].(string)
				content, _ := req["content"].(string)
				urlStr, _ := req["url"].(string)
				log.Printf("{\"event\":\"run_script_recv\",\"hasContent\":%t,\"contentLen\":%d,\"url\":%q}", content != "", len(content), urlStr)
				_ = wsWriteJSON(c, map[string]any{"type": "OpLog", "step": "run_script_recv", "message": fmt.Sprintf("RunScript recv hasContent=%t contentLen=%d url=%s content=%s", content != "", len(content), urlStr, content)})
				res := map[string]any{"type": "RunScriptResult", "requestId": reqID, "data": runScript(req)}
				_ = wsWriteJSON(c, res)
				if d, ok := res["data"].(map[string]any); ok {
					_ = wsWriteJSON(c, map[string]any{"type": "OpLog", "step": "run_script_done", "message": fmt.Sprintf("RunScript done success=%v message=%v stdout=%v stderr=%v", d["success"], d["message"], d["stdout"], d["stderr"])})
				}
			}()
		case "RunStreamScript":
			var req map[string]any
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				reqID, _ := req["requestId"].(string)
				content, _ := req["content"].(string)
				urlStr, _ := req["url"].(string)
				endpoint, _ := req["endpoint"].(string)
				secret, _ := req["secret"].(string)
				kind, _ := req["type"].(string)
				log.Printf("{\"event\":\"run_stream_script_recv\",\"hasContent\":%t,\"contentLen\":%d,\"url\":%q,\"endpoint\":%q}", content != "", len(content), urlStr, endpoint)
				runStreamScript(reqID, content, urlStr, endpoint, secret, kind)
			}()
		case "BacktraceTest":
			var req map[string]any
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				reqID, _ := req["requestId"].(string)
				endpoint, _ := req["endpoint"].(string)
				secret, _ := req["secret"].(string)
				kind, _ := req["type"].(string)
				log.Printf("{\"event\":\"backtrace_recv\",\"endpoint\":%q}", endpoint)
				runBacktraceStream(reqID, endpoint, secret, kind)
			}()
		case "WriteFile":
			var req map[string]any
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				reqID, _ := req["requestId"].(string)
				path, _ := req["path"].(string)
				content, _ := req["content"].(string)
				log.Printf("{\"event\":\"write_file_recv\",\"path\":%q,\"contentLen\":%d}", path, len(content))
				_ = wsWriteJSON(c, map[string]any{"type": "OpLog", "step": "write_file_recv", "message": fmt.Sprintf("WriteFile recv path=%s bytes=%d content=%s", path, len(content), content)})
				res := map[string]any{"type": "WriteFileResult", "requestId": reqID, "data": writeFileOp(req)}
				_ = wsWriteJSON(c, res)
				if d, ok := res["data"].(map[string]any); ok {
					_ = wsWriteJSON(c, map[string]any{"type": "OpLog", "step": "write_file_done", "message": fmt.Sprintf("WriteFile done path=%s success=%v message=%v", path, d["success"], d["message"])})
				}
			}()
		case "RestartService":
			var req map[string]any
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				reqID, _ := req["requestId"].(string)
				name, _ := req["name"].(string)
				log.Printf("{\"event\":\"restart_service_recv\",\"name\":%q}", name)
				_ = wsWriteJSON(c, map[string]any{"type": "OpLog", "step": "restart_service_recv", "message": fmt.Sprintf("RestartService recv name=%s", name)})
				ok := tryRestartService(name)
				res := map[string]any{"type": "RestartServiceResult", "requestId": reqID, "data": map[string]any{"success": ok}}
				_ = wsWriteJSON(c, res)
				_ = wsWriteJSON(c, map[string]any{"type": "OpLog", "step": "restart_service_done", "message": fmt.Sprintf("RestartService done name=%s success=%v", name, ok)})
			}()
		case "StopService":
			var req map[string]any
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				reqID, _ := req["requestId"].(string)
				name, _ := req["name"].(string)
				ok := tryStopService(name)
				res := map[string]any{"type": "StopServiceResult", "requestId": reqID, "data": map[string]any{"success": ok}}
				_ = wsWriteJSON(c, res)
			}()
		case "PprofControl":
			var req struct {
				RequestID string `json:"requestId"`
				Action    string `json:"action"`
				Addr      string `json:"addr"`
			}
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				action := strings.ToLower(strings.TrimSpace(req.Action))
				if action == "" {
					action = "status"
				}
				out := map[string]any{"success": true, "action": action}
				switch action {
				case "enable", "start":
					addr, err := startAgentPprof(req.Addr)
					if err != nil {
						out["success"] = false
						out["message"] = err.Error()
					} else {
						out["enabled"] = true
						out["addr"] = addr
						out["message"] = "pprof enabled"
					}
				case "disable", "stop":
					if err := stopAgentPprof(); err != nil {
						out["success"] = false
						out["message"] = err.Error()
					} else {
						out["enabled"] = false
						out["message"] = "pprof disabled"
					}
				case "status":
					st := agentPprofStatus()
					for k, v := range st {
						out[k] = v
					}
				default:
					out["success"] = false
					out["message"] = "unsupported action"
				}
				if req.RequestID != "" {
					_ = wsWriteJSON(c, map[string]any{
						"type":      "PprofControlResult",
						"requestId": req.RequestID,
						"data":      out,
					})
				}
			}()
		case "PprofFetch":
			var req struct {
				RequestID string `json:"requestId"`
				Profile   string `json:"profile"`
				Debug     int    `json:"debug"`
			}
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				out := map[string]any{"success": true, "profile": req.Profile}
				content, addr, err := fetchAgentPprofText(req.Profile, req.Debug)
				if err != nil {
					out["success"] = false
					out["message"] = err.Error()
				} else {
					out["addr"] = addr
					out["content"] = content
				}
				if req.RequestID != "" {
					_ = wsWriteJSON(c, map[string]any{
						"type":      "PprofFetchResult",
						"requestId": req.RequestID,
						"data":      out,
					})
				}
			}()
		default:
			// ignore unknown
		}
	}
}

// apiEnsureIfUnavailable tries to enable Web API if not currently available (single attempt per call).
func apiEnsureIfUnavailable() bool {
	cfg := readGostConfig()
	if apiAvailable() && gostAPILocalOnlyFromConfig(cfg) {
		atomic.StoreInt32(&apiUse, 1)
		return true
	}
	if !gostAPILocalOnlyFromConfig(cfg) {
		log.Printf("{\"event\":\"gost_api_rebind_local\",\"from\":%v}", cfg["api"])
	}
	if err := ensureGostAPITopLevel(0); err != nil {
		return false
	}
	if !tryRestartService("gost") {
		return false
	}
	time.Sleep(1500 * time.Millisecond)
	ok := apiAvailable()
	if ok {
		atomic.StoreInt32(&apiUse, 1)
	}
	return ok
}

// normalizeConfiguredGostAPIBindLocalOnly rewrites api.addr to 127.0.0.1:<port>
// when API is already configured but bound to wildcard/non-loopback.
// It does not enable API for nodes that never configured API.
func normalizeConfiguredGostAPIBindLocalOnly() bool {
	cfg := readGostConfig()
	if cfg == nil {
		return false
	}
	apiRaw, hasAPI := cfg["api"]
	if !hasAPI || apiRaw == nil {
		return false
	}
	if gostAPILocalOnlyFromConfig(cfg) {
		return false
	}
	oldAPI := fmt.Sprintf("%v", apiRaw)
	port := resolveGostAPIPort(0)
	forceLocalGostAPITopLevel(cfg, port)
	if err := writeGostConfig(cfg); err != nil {
		log.Printf("{\"event\":\"gost_api_localize_err\",\"error\":%q}", err.Error())
		emitOpLog("gost_api_err", "localize configured api failed", map[string]any{
			"error": err.Error(),
		})
		return false
	}
	log.Printf("{\"event\":\"gost_api_localize\",\"from\":%q,\"to\":%q,\"port\":%d}", oldAPI, gostAPIBindAddr(port), port)
	emitOpLog("gost_api", "localize configured api", map[string]any{
		"from": oldAPI,
		"to":   gostAPIBindAddr(port),
		"port": port,
	})
	if !tryRestartService("gost") {
		log.Printf("{\"event\":\"gost_api_localize_restart_err\"}")
		emitOpLog("gost_api_err", "localize configured api restart failed", map[string]any{
			"port": port,
		})
		return false
	}
	time.Sleep(700 * time.Millisecond)
	okLoop, detail := verifyGostAPIBindLoopbackOnly(port)
	emitOpLog("gost_api", "bind verify", map[string]any{
		"port":         port,
		"loopbackOnly": okLoop,
		"detail":       detail,
	})
	if okLoop {
		enforceGostAPIFirewall(port)
	}
	return okLoop
}

// ---- Periodic system info reporting over WS ----

type cpuTimes struct{ idle, total uint64 }

var lastCPU *cpuTimes

func readCPUTimes() (*cpuTimes, error) {
	b, err := ioutil.ReadFile("/proc/stat")
	if err != nil {
		return nil, err
	}
	// first line: cpu  user nice system idle iowait irq softirq steal guest guest_nice
	// we sum all except the first token label
	line := strings.SplitN(string(b), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return nil, fmt.Errorf("bad /proc/stat")
	}
	var total uint64
	for i := 1; i < len(fields); i++ {
		v, _ := strconv.ParseUint(fields[i], 10, 64)
		total += v
	}
	idle, _ := strconv.ParseUint(fields[4], 10, 64)
	return &cpuTimes{idle: idle, total: total}, nil
}

func cpuUsagePercent() float64 {
	cur, err := readCPUTimes()
	if err != nil {
		return 0
	}
	if lastCPU == nil {
		lastCPU = cur
		return 0
	}
	idle := float64(cur.idle - lastCPU.idle)
	total := float64(cur.total - lastCPU.total)
	lastCPU = cur
	if total <= 0 {
		return 0
	}
	used := (1.0 - idle/total) * 100.0
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	return used
}

func memUsagePercent() float64 {
	b, err := ioutil.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	lines := strings.Split(string(b), "\n")
	var total, avail float64
	for _, ln := range lines {
		if strings.HasPrefix(ln, "MemTotal:") {
			parts := strings.Fields(ln)
			if len(parts) >= 2 {
				v, _ := strconv.ParseFloat(parts[1], 64)
				total = v
			}
		} else if strings.HasPrefix(ln, "MemAvailable:") {
			parts := strings.Fields(ln)
			if len(parts) >= 2 {
				v, _ := strconv.ParseFloat(parts[1], 64)
				avail = v
			}
		}
	}
	if total <= 0 {
		return 0
	}
	used := (total - avail) / total * 100.0
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	return used
}

func netBytes() (rx, tx uint64) {
	b, err := ioutil.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	lines := strings.Split(string(b), "\n")
	for _, ln := range lines[2:] { // skip headers
		parts := strings.Fields(strings.TrimSpace(ln))
		if len(parts) < 17 {
			continue
		}
		// parts[0]=iface: ; rx bytes=parts[1]; tx bytes=parts[9]
		// strip trailing ':' in iface
		// sum over all interfaces
		rxb, _ := strconv.ParseUint(parts[1], 10, 64)
		txb, _ := strconv.ParseUint(parts[9], 10, 64)
		rx += rxb
		tx += txb
	}
	return
}

func uptimeSeconds() int64 {
	b, err := ioutil.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	parts := strings.Fields(string(b))
	if len(parts) == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(parts[0], 64)
	return int64(f)
}

type agentRuntimeMetrics struct {
	HeapAllocMB   float64
	HeapInuseMB   float64
	StackInuseMB  float64
	SysMB         float64
	RSSMB         float64
	NumGC         uint32
	LastPauseMs   float64
	GCCPUPercent  float64
	GoRoutines    int
	CollectedAtMs int64
}

func readProcessRSSMB() float64 {
	// Linux: /proc/self/statm: size resident shared text lib data dt
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	parts := strings.Fields(string(b))
	if len(parts) < 2 {
		return 0
	}
	pages, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || pages <= 0 {
		return 0
	}
	return pages * float64(os.Getpagesize()) / (1024 * 1024)
}

func readAgentRuntimeMetrics() agentRuntimeMetrics {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	lastPause := 0.0
	if ms.NumGC > 0 {
		lastPause = float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e6
	}
	return agentRuntimeMetrics{
		HeapAllocMB:   float64(ms.HeapAlloc) / (1024 * 1024),
		HeapInuseMB:   float64(ms.HeapInuse) / (1024 * 1024),
		StackInuseMB:  float64(ms.StackInuse) / (1024 * 1024),
		SysMB:         float64(ms.Sys) / (1024 * 1024),
		RSSMB:         readProcessRSSMB(),
		NumGC:         ms.NumGC,
		LastPauseMs:   lastPause,
		GCCPUPercent:  ms.GCCPUFraction * 100.0,
		GoRoutines:    runtime.NumGoroutine(),
		CollectedAtMs: time.Now().UnixMilli(),
	}
}

func periodicSystemInfo(c *websocket.Conn, done <-chan struct{}) {
	sec := 10
	if v := getenv("AGENT_SYSINFO_SEC", ""); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			sec = n
		}
	}
	usedSec := 30
	if v := getenv("AGENT_USED_PORTS_SEC", ""); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			usedSec = n
		}
	}
	ifaceSec := 300
	if v := getenv("AGENT_IFACES_SEC", ""); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			ifaceSec = n
		}
	}
	agentMemSec := 30
	if v := getenv("AGENT_AGENT_MEM_SEC", ""); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			agentMemSec = n
		}
	}
	usedMax := 128
	if v := getenv("AGENT_USED_PORTS_MAX", ""); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			usedMax = n
		}
	}
	logSysinfo := false
	switch strings.ToLower(strings.TrimSpace(getenv("AGENT_SYSINFO_LOG", "0"))) {
	case "1", "true", "yes", "on":
		logSysinfo = true
	}
	ticker := time.NewTicker(time.Duration(sec) * time.Second)
	defer ticker.Stop()
	var lastPorts []int
	var lastPortsAt time.Time
	var lastIfaces []string
	var lastIfacesAt time.Time
	var lastAgentMem agentRuntimeMetrics
	var lastAgentMemAt time.Time
	for {
		rx, tx := netBytes()
		// refresh interface list periodically (best-effort)
		if time.Since(lastIfacesAt) >= time.Duration(ifaceSec)*time.Second || lastIfacesAt.IsZero() {
			lastIfaces = getInterfaces()
			lastIfacesAt = time.Now()
		}
		ifaces := lastIfaces
		// refresh used ports snapshot periodically
		if time.Since(lastPortsAt) >= time.Duration(usedSec)*time.Second || lastPortsAt.IsZero() {
			used := getUsedListeningPorts()
			list := make([]int, 0, len(used))
			for p := range used {
				list = append(list, p)
			}
			sort.Ints(list)
			if usedMax > 0 && len(list) > usedMax {
				list = list[:usedMax]
			}
			lastPorts = list
			lastPortsAt = time.Now()
		}
		if time.Since(lastAgentMemAt) >= time.Duration(agentMemSec)*time.Second || lastAgentMemAt.IsZero() {
			lastAgentMem = readAgentRuntimeMetrics()
			lastAgentMemAt = time.Now()
		}
		payload := map[string]any{
			"Uptime": uptimeSeconds(),
		}
		payload["BytesReceived"] = int64(rx)
		payload["BytesTransmitted"] = int64(tx)
		payload["CPUUsage"] = cpuUsagePercent()
		payload["MemoryUsage"] = memUsagePercent()
		// basic health: gost service & web api
		payload["GostAPI"] = apiAvailable()
		payload["GostRunning"] = gostRunning()
		payload["GostAPIConfigured"] = apiConfigured()
		if st, port, pid := iperf3Status(); st != "" {
			payload["Iperf3Status"] = st
			if port > 0 {
				payload["Iperf3Port"] = port
			}
			if pid > 0 {
				payload["Iperf3Pid"] = pid
			}
		}
		payload["Interfaces"] = ifaces
		payload["UsedPorts"] = lastPorts
		payload["AgentHeapAllocMB"] = lastAgentMem.HeapAllocMB
		payload["AgentHeapInuseMB"] = lastAgentMem.HeapInuseMB
		payload["AgentStackInuseMB"] = lastAgentMem.StackInuseMB
		payload["AgentSysMB"] = lastAgentMem.SysMB
		payload["AgentRSSMB"] = lastAgentMem.RSSMB
		payload["AgentNumGC"] = lastAgentMem.NumGC
		payload["AgentLastGCPauseMs"] = lastAgentMem.LastPauseMs
		payload["AgentGCCPUPercent"] = lastAgentMem.GCCPUPercent
		payload["AgentGoRoutines"] = lastAgentMem.GoRoutines
		payload["AgentMemCollectedAtMs"] = lastAgentMem.CollectedAtMs
		b, _ := json.Marshal(payload)
		if logSysinfo {
			log.Printf("{\"event\":\"sysinfo_report\",\"payload\":%s}", string(b))
		}
		if err := wsWriteMessage(c, websocket.TextMessage, b); err != nil {
			log.Printf("{\"event\":\"sysinfo_report_error\",\"error\":%q}", err.Error())
			_ = c.Close()
			return
		}
		select {
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

// gostRunning checks if gost is running via service manager or pid tools (best-effort)
func gostRunning() bool {
	if ok, known := isServiceActive("gost"); known {
		return ok
	}
	if _, err := exec.LookPath("pidof"); err == nil {
		if err := exec.Command("pidof", "gost").Run(); err == nil {
			return true
		}
	}
	if _, err := exec.LookPath("pgrep"); err == nil {
		if err := exec.Command("pgrep", "-x", "gost").Run(); err == nil {
			return true
		}
	}
	// fallback: if API reachable, consider running
	return apiAvailable()
}

func getInterfaces() []string {
	// try `ip -o -4 addr show up scope global`
	var out []byte
	if b, err := exec.Command("sh", "-c", "ip -o -4 addr show up scope global | awk '{print $4}' | cut -d/ -f1").Output(); err == nil {
		out = append(out, b...)
	}
	if b, err := exec.Command("sh", "-c", "ip -o -6 addr show up scope global | awk '{print $4}' | cut -d/ -f1").Output(); err == nil {
		if len(out) > 0 && len(b) > 0 {
			out = append(out, '\n')
		}
		out = append(out, b...)
	}
	if len(out) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	ips := []string{}
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if s != "" {
			ips = append(ips, s)
		}
	}
	return ips
}

func periodicReconcile(addr, secret, scheme string, done <-chan struct{}) {
	interval := 300
	if v := getenv("RECONCILE_INTERVAL", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = n
		}
	}
	if interval <= 0 {
		return
	}
	t := time.NewTicker(time.Duration(interval) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			reconcile(addr, secret, scheme)
		}
	}
}

func reconcile(addr, secret, scheme string) {
	// read local gost.json service names and panel-managed flag
	present := map[string]struct{}{}
	managed := map[string]bool{}
	if b, err := readFileLimited(resolveGostConfigPathForRead(), maxGostConfigFileBytes()); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			if arr, ok := m["services"].([]any); ok {
				for _, it := range arr {
					if obj, ok := it.(map[string]any); ok {
						if n, ok := obj["name"].(string); ok && n != "" {
							present[n] = struct{}{}
							if meta, _ := obj["metadata"].(map[string]any); meta != nil {
								if v, ok2 := meta["managedBy"].(string); ok2 && v == "network-panel" {
									managed[n] = true
								}
							}
						}
					}
				}
			}
		}
	}
	//addr := getenv("ADDR", "")
	//secret := getenv("SECRET", "")
	//scheme := getenv("SCHEME", "ws")
	proto := "http"
	if scheme == "wss" {
		proto = "https"
	}
	desiredURL := fmt.Sprintf("%s://%s/api/v1/agent/desired-services", proto, addr)
	body, _ := json.Marshal(map[string]string{"secret": secret})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", desiredURL, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("{\"event\":\"reconcile_error\",\"step\":\"desired\",\"error\":%q}", err.Error())
		return
	}
	defer resp.Body.Close()
	var res struct {
		Code int              `json:"code"`
		Data []map[string]any `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&res)
	if res.Code != 0 {
		log.Printf("{\"event\":\"reconcile_error\",\"step\":\"desired\",\"code\":%d}", res.Code)
		return
	}
	missing := make([]map[string]any, 0)
	desiredNames := map[string]struct{}{}
	for _, svc := range res.Data {
		if n, ok := svc["name"].(string); ok {
			desiredNames[n] = struct{}{}
			if _, ok2 := present[n]; !ok2 {
				missing = append(missing, svc)
			}
		}
	}
	// compute extras if STRICT_RECONCILE=true (only for panel-managed services)
	extras := make([]string, 0)
	strict := false
	if v := strings.ToLower(getenv("STRICT_RECONCILE", "false")); v == "true" || v == "1" {
		strict = true
	}
	if strict {
		for n := range present {
			if _, ok := desiredNames[n]; !ok {
				if managed[n] {
					extras = append(extras, n)
				}
			}
		}
	}
	if len(missing) == 0 && len(extras) == 0 {
		log.Printf("{\"event\":\"reconcile_ok\",\"missing\":0,\"extras\":0}")
		return
	}
	if len(missing) > 0 {
		pushURL := fmt.Sprintf("%s://%s/api/v1/agent/push-services", proto, addr)
		pb, _ := json.Marshal(map[string]any{"secret": secret, "services": missing})
		req2, _ := http.NewRequestWithContext(ctx, "POST", pushURL, strings.NewReader(string(pb)))
		req2.Header.Set("Content-Type", "application/json")
		if resp2, err := http.DefaultClient.Do(req2); err != nil {
			log.Printf("{\"event\":\"reconcile_error\",\"step\":\"push\",\"error\":%q}", err.Error())
		} else {
			resp2.Body.Close()
			log.Printf("{\"event\":\"reconcile_push\",\"count\":%d}", len(missing))
		}
	}
	if strict && len(extras) > 0 {
		rmURL := fmt.Sprintf("%s://%s/api/v1/agent/remove-services", proto, addr)
		rb, _ := json.Marshal(map[string]any{"secret": secret, "services": extras})
		req3, _ := http.NewRequestWithContext(ctx, "POST", rmURL, strings.NewReader(string(rb)))
		req3.Header.Set("Content-Type", "application/json")
		if resp3, err := http.DefaultClient.Do(req3); err != nil {
			log.Printf("{\"event\":\"reconcile_error\",\"step\":\"remove\",\"error\":%q}", err.Error())
		} else {
			resp3.Body.Close()
			log.Printf("{\"event\":\"reconcile_remove\",\"count\":%d}", len(extras))
		}
	}
}

func handleDiagnose(c *websocket.Conn, d *DiagnoseData) {
	// defaults
	if d.Count <= 0 {
		d.Count = 3
	}
	if d.TimeoutMs <= 0 {
		d.TimeoutMs = 1500
	}

	var resp map[string]any
	switch strings.ToLower(d.Mode) {
	case "icmp":
		avg, loss := runICMP(d.Host, d.Count, d.TimeoutMs)
		ok := loss < 100
		msg := "ok"
		if !ok {
			msg = "unreachable"
		}
		resp = map[string]any{"success": ok, "averageTime": avg, "packetLoss": loss, "message": msg, "ctx": d.Ctx}
	case "iperf3":
		if d.Server {
			port := d.Port
			if port == 0 {
				port = pickPort()
			}
			ok := startIperf3Server(port)
			msg := "server started"
			if !ok {
				msg = "failed to start server"
			}
			resp = map[string]any{"success": ok, "port": port, "message": msg, "ctx": d.Ctx}
		} else if d.Client {
			if d.Duration <= 0 {
				d.Duration = 5
			}
			// allow reverse mode via payload Reverse flag
			bw, msg := runIperf3ClientVerbose(d.Host, d.Port, d.Duration, d.Reverse)
			ok := bw > 0
			m := map[string]any{"success": ok, "bandwidthMbps": bw, "ctx": d.Ctx}
			if msg != "" {
				m["message"] = msg
			}
			resp = m
		} else {
			resp = map[string]any{"success": false, "message": "unknown iperf3 mode", "ctx": d.Ctx}
		}
	default:
		// tcp connect
		avg, loss := runTCP(d.Host, d.Port, d.Count, d.TimeoutMs)
		ok := loss < 100
		msg := "ok"
		if !ok {
			msg = "connect fail"
		}
		resp = map[string]any{"success": ok, "averageTime": avg, "packetLoss": loss, "message": msg, "ctx": d.Ctx}
	}
	out := map[string]any{"type": "DiagnoseResult", "requestId": d.RequestID, "data": resp}
	_ = wsWriteJSON(c, out)
	log.Printf("{\"event\":\"send_result\",\"requestId\":%q,\"data\":%s}", d.RequestID, string(mustJSON(resp)))
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// --- gost.json helpers ---
// prefer runtime gost process config path, then common locations
var gostConfigPathCandidates = []string{
	"/etc/gost/gost.json",
	"/usr/local/gost/gost.json",
	"./gost.json",
}

func normalizeGostConfigPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.Trim(p, `"'`)
	if p == "" {
		return ""
	}
	if !strings.HasSuffix(strings.ToLower(p), ".json") {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Clean(p)
	}
	return p
}

func parseGostConfigPathFromArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		if a == "" {
			continue
		}
		if a == "-C" || a == "--config" {
			if i+1 < len(args) {
				if p := normalizeGostConfigPath(args[i+1]); p != "" {
					return p
				}
			}
			continue
		}
		if strings.HasPrefix(a, "-C=") {
			if p := normalizeGostConfigPath(strings.TrimSpace(strings.TrimPrefix(a, "-C="))); p != "" {
				return p
			}
			continue
		}
		if strings.HasPrefix(a, "--config=") {
			if p := normalizeGostConfigPath(strings.TrimSpace(strings.TrimPrefix(a, "--config="))); p != "" {
				return p
			}
			continue
		}
	}
	return ""
}

func detectGostConfigPathFromProcess() string {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		pid := ent.Name()
		if pid == "" || pid[0] < '0' || pid[0] > '9' {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		parts := strings.Split(string(b), "\x00")
		args := make([]string, 0, len(parts))
		hasGost := false
		for _, p := range parts {
			s := strings.TrimSpace(p)
			if s == "" {
				continue
			}
			args = append(args, s)
			base := strings.ToLower(filepath.Base(s))
			if base == "gost" {
				hasGost = true
			}
		}
		if !hasGost || len(args) == 0 {
			continue
		}
		if p := parseGostConfigPathFromArgs(args); p != "" {
			return p
		}
	}
	return ""
}

func resolvedGostConfigPath() string {
	if p := normalizeGostConfigPath(getenv("GOST_CONFIG_PATH", "")); p != "" {
		return p
	}
	if p := detectGostConfigPathFromProcess(); p != "" {
		return p
	}
	for _, p := range gostConfigPathCandidates {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return p
		}
	}
	return "/etc/gost/gost.json"
}

func resolveGostConfigPathForRead() string {
	return resolvedGostConfigPath()
}

func resolveGostConfigPathForWrite() string { return resolvedGostConfigPath() }

func readGostConfig() map[string]any {
	path := resolveGostConfigPathForRead()
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return map[string]any{}
	}
	return m
}

func writeGostConfig(m map[string]any) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := resolveGostConfigPathForWrite()
	// ensure dir exists best-effort
	if dir := strings.TrimSuffix(path, "/gost.json"); dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}
	return os.WriteFile(path, b, 0600)
}

// --- Web API (gost) helpers ---

const defaultGostAPIPort = 18080
const gostAPIGuardStatePath = "/etc/gost/gost_api_guard_port"

func parseGostAPIPort(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > 65535 {
		return 0, false
	}
	return n, true
}

func gostAPIPortFromEnv() (int, bool) {
	return parseGostAPIPort(getenv("GOST_API_PORT", ""))
}

func gostAPIPortFromConfig(cfg map[string]any) (int, bool) {
	if cfg == nil {
		return 0, false
	}
	apiRaw, ok := cfg["api"]
	if !ok {
		return 0, false
	}
	apiObj, ok := apiRaw.(map[string]any)
	if !ok || apiObj == nil {
		return 0, false
	}
	addr, _ := apiObj["addr"].(string)
	_, port, ok := parseGostAPIAddr(addr)
	if !ok {
		return 0, false
	}
	return port, true
}

func parseGostAPIAddr(addr string) (host string, port int, ok bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0, false
	}
	if strings.HasPrefix(addr, ":") {
		if p, ok := parseGostAPIPort(strings.TrimPrefix(addr, ":")); ok {
			return "", p, true
		}
		return "", 0, false
	}
	if p, ok := parseGostAPIPort(addr); ok {
		return "", p, true
	}
	h, ps, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, false
	}
	p, ok := parseGostAPIPort(ps)
	if !ok {
		return "", 0, false
	}
	return strings.TrimSpace(strings.Trim(h, "[]")), p, true
}

func parseHostPortLoose(addr string) (host string, port int, ok bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0, false
	}
	if strings.HasPrefix(addr, "[") {
		h, ps, err := net.SplitHostPort(addr)
		if err != nil {
			return "", 0, false
		}
		p, ok := parseGostAPIPort(ps)
		if !ok {
			return "", 0, false
		}
		return strings.TrimSpace(strings.Trim(h, "[]")), p, true
	}
	idx := strings.LastIndex(addr, ":")
	if idx <= 0 || idx == len(addr)-1 {
		return "", 0, false
	}
	p, ok := parseGostAPIPort(addr[idx+1:])
	if !ok {
		return "", 0, false
	}
	h := strings.TrimSpace(strings.Trim(addr[:idx], "[]"))
	return h, p, true
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]")))
	if h == "" {
		return false
	}
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func verifyGostAPIBindLoopbackOnly(port int) (bool, string) {
	ps := strconv.Itoa(port)
	// 1) prefer ss on linux
	if _, err := exec.LookPath("ss"); err == nil {
		cmd := exec.Command("ss", "-ltnH")
		out, err := runCommandCombinedLimited(cmd, 64*1024)
		if err == nil {
			found := 0
			var bad []string
			sc := bufio.NewScanner(strings.NewReader(out))
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) < 4 {
					continue
				}
				local := fields[3]
				host, p, ok := parseHostPortLoose(local)
				if !ok || p != port {
					continue
				}
				found++
				if !isLoopbackHost(host) {
					bad = append(bad, local)
				}
			}
			if found == 0 {
				return false, "ss: no listener found on :" + ps
			}
			if len(bad) > 0 {
				return false, "ss: non-loopback listeners " + strings.Join(bad, ",")
			}
			return true, "ss: loopback-only"
		}
	}
	// 2) fallback to lsof heuristic
	if _, err := exec.LookPath("lsof"); err == nil {
		cmd := exec.Command("lsof", "-nP", "-iTCP:"+ps, "-sTCP:LISTEN")
		out, err := runCommandCombinedLimited(cmd, 64*1024)
		if err != nil {
			return false, "lsof exec failed"
		}
		txt := out
		lines := strings.Split(txt, "\n")
		found := 0
		for _, ln := range lines {
			ln = strings.TrimSpace(ln)
			if ln == "" || strings.HasPrefix(ln, "COMMAND") {
				continue
			}
			found++
			l := strings.ToLower(ln)
			if strings.Contains(l, "*:"+ps) || strings.Contains(l, "0.0.0.0:"+ps) || strings.Contains(l, "[::]:"+ps) {
				return false, "lsof: non-loopback listener " + ln
			}
		}
		if found == 0 {
			return false, "lsof: no listener found on :" + ps
		}
		return true, "lsof: loopback-only"
	}
	return false, "no ss/lsof available"
}

func serviceAddrPort(svc map[string]any) (int, bool) {
	if svc == nil {
		return 0, false
	}
	addr, _ := svc["addr"].(string)
	_, p, ok := parseGostAPIAddr(addr)
	if !ok {
		return 0, false
	}
	return p, true
}

func isLegacyAPISvc(svc map[string]any, apiPort int) bool {
	if svc == nil {
		return false
	}
	p, ok := serviceAddrPort(svc)
	if !ok || p != apiPort {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", svc["name"])))
	if name == "gost_api" || name == "api" || strings.Contains(name, "gost_api") {
		return true
	}
	if hm, ok := svc["handler"].(map[string]any); ok && hm != nil {
		ht := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", hm["type"])))
		if ht == "api" || ht == "webapi" {
			return true
		}
	}
	return false
}

func removeLegacyAPIServices(cfg map[string]any, apiPort int) int {
	if cfg == nil {
		return 0
	}
	arr, ok := cfg["services"].([]any)
	if !ok || len(arr) == 0 {
		return 0
	}
	out := make([]any, 0, len(arr))
	removed := 0
	for _, it := range arr {
		svc, ok := it.(map[string]any)
		if !ok {
			out = append(out, it)
			continue
		}
		if isLegacyAPISvc(svc, apiPort) {
			removed++
			continue
		}
		out = append(out, it)
	}
	if removed > 0 {
		cfg["services"] = out
	}
	return removed
}

func gostAPILocalOnlyFromConfig(cfg map[string]any) bool {
	if cfg == nil {
		return false
	}
	apiRaw, ok := cfg["api"]
	if !ok {
		return false
	}
	apiObj, ok := apiRaw.(map[string]any)
	if !ok || apiObj == nil {
		return false
	}
	addr, _ := apiObj["addr"].(string)
	host, _, ok := parseGostAPIAddr(addr)
	if !ok {
		return false
	}
	return host == "127.0.0.1"
}

func resolveGostAPIPort(preferred int) int {
	if preferred > 0 && preferred <= 65535 {
		return preferred
	}
	if p, ok := gostAPIPortFromEnv(); ok {
		return p
	}
	if p, ok := gostAPIPortFromConfig(readGostConfig()); ok {
		return p
	}
	return defaultGostAPIPort
}

func gostAPIBindAddr(port int) string {
	p := resolveGostAPIPort(port)
	return fmt.Sprintf("127.0.0.1:%d", p)
}

func readGuardedGostAPIPort() int {
	b, err := os.ReadFile(gostAPIGuardStatePath)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n <= 0 || n > 65535 {
		return 0
	}
	return n
}

func writeGuardedGostAPIPort(port int) {
	if port <= 0 || port > 65535 {
		return
	}
	_ = os.MkdirAll("/etc/gost", 0o755)
	_ = os.WriteFile(gostAPIGuardStatePath, []byte(strconv.Itoa(port)), 0o600)
}

func runLocalCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := runCommandCombinedLimited(cmd, 32*1024)
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg != "" {
			return fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func removeFirewallDropRule(port int, bin string) {
	if _, err := exec.LookPath(bin); err != nil {
		return
	}
	ps := strconv.Itoa(port)
	// Remove repeatedly in case duplicated rules were inserted.
	for i := 0; i < 8; i++ {
		if err := runLocalCmd(bin, "-D", "INPUT", "!", "-i", "lo", "-p", "tcp", "--dport", ps, "-j", "DROP"); err != nil {
			break
		}
	}
}

func ensureFirewallDropRule(port int, bin string) error {
	if _, err := exec.LookPath(bin); err != nil {
		return nil
	}
	ps := strconv.Itoa(port)
	if err := runLocalCmd(bin, "-C", "INPUT", "!", "-i", "lo", "-p", "tcp", "--dport", ps, "-j", "DROP"); err == nil {
		return nil
	}
	// Insert at top so it takes effect before broad ACCEPT rules.
	return runLocalCmd(bin, "-I", "INPUT", "1", "!", "-i", "lo", "-p", "tcp", "--dport", ps, "-j", "DROP")
}

func enforceGostAPIFirewall(port int) {
	if port <= 0 || port > 65535 {
		return
	}
	if runtime.GOOS != "linux" {
		return
	}
	if !envBool("GOST_API_ENFORCE_LOOPBACK_FIREWALL", true) {
		return
	}

	prevPort := readGuardedGostAPIPort()
	if prevPort > 0 && prevPort != port {
		removeFirewallDropRule(prevPort, "iptables")
		removeFirewallDropRule(prevPort, "ip6tables")
	}

	var errs []string
	if err := ensureFirewallDropRule(port, "iptables"); err != nil {
		errs = append(errs, err.Error())
	}
	if err := ensureFirewallDropRule(port, "ip6tables"); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		msg := strings.Join(errs, " | ")
		log.Printf("{\"event\":\"gost_api_firewall_err\",\"port\":%d,\"error\":%q}", port, msg)
		emitOpLog("gost_api_err", "loopback firewall guard failed", map[string]any{
			"port":  port,
			"error": msg,
		})
		return
	}

	writeGuardedGostAPIPort(port)
	log.Printf("{\"event\":\"gost_api_firewall_ok\",\"port\":%d}", port)
	emitOpLog("gost_api", "loopback firewall guard applied", map[string]any{
		"port": port,
	})
}

func apiBaseURL() string { return fmt.Sprintf("http://%s/api", gostAPIBindAddr(0)) }

func md5String(s string) string {
	sum := md5.Sum([]byte(strings.TrimSpace(s)))
	return fmt.Sprintf("%x", sum)
}

func apiCreds() (user, pass string) {
	user = "networkpanel"
	hn, _ := os.Hostname()
	if hn == "" {
		hn = "node"
	}
	pass = md5String(hn)
	return
}

func apiAvailable() bool {
	req, _ := http.NewRequest("GET", apiBaseURL()+"/config", nil)
	u, p := apiCreds()
	req.SetBasicAuth(u, p)
	cli := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := cli.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode/100 == 2
}

// apiConfigured best-effort checks gost.json for top-level api section.
func apiConfigured() bool {
	m := readGostConfig()
	if m == nil {
		return false
	}
	if _, ok := m["api"]; ok {
		return true
	}
	return false
}

func iperf3Status() (string, int, int) {
	pidBytes, _ := os.ReadFile("/tmp/np_iperf3.pid")
	portBytes, _ := os.ReadFile("/tmp/np_iperf3.port")
	pidStr := strings.TrimSpace(string(pidBytes))
	portStr := strings.TrimSpace(string(portBytes))
	pid, _ := strconv.Atoi(pidStr)
	port, _ := strconv.Atoi(portStr)
	if pid > 0 {
		if processAlive(pid) {
			return "running", port, pid
		}
		return "stopped", port, pid
	}
	if port > 0 {
		return "stopped", port, 0
	}
	return "stopped", 0, 0
}

// ensureGostAPITopLevel writes top-level api config (not as a service).
func ensureGostAPITopLevel(preferredPort int) error {
	cfg := readGostConfig()
	forceLocalGostAPITopLevel(cfg, preferredPort)
	return writeGostConfig(cfg)
}

func forceLocalGostAPITopLevel(cfg map[string]any, preferredPort int) int {
	if cfg == nil {
		cfg = map[string]any{}
	}
	u, p := apiCreds()
	port := resolveGostAPIPort(preferredPort)
	cfg["api"] = map[string]any{
		"addr":       gostAPIBindAddr(port),
		"pathPrefix": "/api",
		"accesslog":  true,
		"auth": map[string]any{
			"username": u,
			"password": p,
		},
	}
	_ = removeLegacyAPIServices(cfg, port)
	return port
}

// isApiUsable returns last known state for Web API.
func isApiUsable() bool {
	if atomic.LoadInt32(&apiUse) == 1 {
		return true
	}
	if apiAvailable() {
		atomic.StoreInt32(&apiUse, 1)
		return true
	}
	return false
}

// apiBootstrapOnce tries to enable Web API exactly once.
func apiBootstrapOnce() bool {
	if !atomic.CompareAndSwapInt32(&apiBootDone, 0, 1) {
		return isApiUsable()
	}
	cfg := readGostConfig()
	if apiAvailable() && gostAPILocalOnlyFromConfig(cfg) {
		if p, ok := gostAPIPortFromConfig(cfg); ok {
			okLoop, detail := verifyGostAPIBindLoopbackOnly(p)
			emitOpLog("gost_api", "bind verify", map[string]any{"port": p, "loopbackOnly": okLoop, "detail": detail})
		}
		if p, ok := gostAPIPortFromConfig(cfg); ok {
			enforceGostAPIFirewall(p)
		} else {
			enforceGostAPIFirewall(resolveGostAPIPort(0))
		}
		atomic.StoreInt32(&apiUse, 1)
		log.Printf("{\"event\":\"api_available\",\"ok\":true}")
		return true
	}
	if !gostAPILocalOnlyFromConfig(cfg) {
		log.Printf("{\"event\":\"gost_api_rebind_local\",\"from\":%v}", cfg["api"])
	}
	port := resolveGostAPIPort(0)
	if err := ensureGostAPITopLevel(0); err != nil {
		log.Printf("{\"event\":\"api_bootstrap_failed\",\"error\":%q}", err.Error())
		return false
	}
	if !tryRestartService("gost") {
		log.Printf("{\"event\":\"api_restart_gost_failed\"}")
		return false
	}
	time.Sleep(600 * time.Millisecond)
	okLoop, detail := verifyGostAPIBindLoopbackOnly(port)
	emitOpLog("gost_api", "bind verify", map[string]any{"port": port, "loopbackOnly": okLoop, "detail": detail})
	if !okLoop {
		emitOpLog("gost_api_err", "bind verify failed, try normalize services", map[string]any{"port": port, "detail": detail})
		cfg2 := readGostConfig()
		removed := removeLegacyAPIServices(cfg2, port)
		if removed > 0 || !gostAPILocalOnlyFromConfig(cfg2) {
			u, p := apiCreds()
			cfg2["api"] = map[string]any{
				"addr":       gostAPIBindAddr(port),
				"pathPrefix": "/api",
				"accesslog":  true,
				"auth": map[string]any{
					"username": u,
					"password": p,
				},
			}
			if err := writeGostConfig(cfg2); err == nil {
				_ = tryRestartService("gost")
				time.Sleep(800 * time.Millisecond)
				okLoop, detail = verifyGostAPIBindLoopbackOnly(port)
				emitOpLog("gost_api", "bind verify retry", map[string]any{
					"port":         port,
					"loopbackOnly": okLoop,
					"detail":       detail,
					"removed":      removed,
				})
			}
		}
	}
	enforceGostAPIFirewall(port)
	time.Sleep(1500 * time.Millisecond)
	ok := apiAvailable()
	if ok {
		atomic.StoreInt32(&apiUse, 1)
	}
	log.Printf("{\"event\":\"api_available\",\"ok\":%v}", ok)
	return ok
}

func apiDo(method, path string, body []byte) (int, []byte, error) {
	fullURL := apiBaseURL() + path
	capN := int(atomic.LoadInt32(&gostAPIBodyCap))
	if capN <= 0 {
		capN = 2048
	}
	verbose := atomic.LoadInt32(&gostAPIVerboseBody) == 1
	// log request (masked)
	if body != nil && len(body) > 0 {
		if verbose {
			rb := maskJSONSecrets(body)
			if len(rb) > capN {
				rb = rb[:capN]
			}
			log.Printf("{\"event\":\"gost_api_call\",\"method\":%q,\"url\":%q,\"body\":%s,\"bodyBytes\":%d}", method, maskURLSecrets(fullURL), string(rb), len(body))
		} else {
			log.Printf("{\"event\":\"gost_api_call\",\"method\":%q,\"url\":%q,\"bodyBytes\":%d}", method, maskURLSecrets(fullURL), len(body))
		}
	} else {
		log.Printf("{\"event\":\"gost_api_call\",\"method\":%q,\"url\":%q}", method, maskURLSecrets(fullURL))
	}
	req, _ := http.NewRequest(method, fullURL, bytes.NewReader(body))
	u, p := apiCreds()
	req.SetBasicAuth(u, p)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := gostAPIHTTPClient.Do(req)
	if err != nil {
		log.Printf("{\"event\":\"gost_api_err\",\"method\":%q,\"url\":%q,\"error\":%q}", method, maskURLSecrets(fullURL), err.Error())
		emitOpLog("gost_api", "request error", map[string]any{"method": method, "url": maskURLSecrets(fullURL), "error": err.Error()})
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode/100 == 2 {
		atomic.StoreInt32(&apiUse, 1)
	}
	if resp.StatusCode/100 == 2 {
		logSuccess := true
		if strings.EqualFold(method, "GET") && path == "/config/services" {
			every := int(atomic.LoadInt32(&gostAPISuccessLogEvery))
			if every > 1 {
				n := atomic.AddUint64(&gostSvcGetRespCount, 1)
				logSuccess = n%uint64(every) == 1
			}
		}
		if verbose && logSuccess {
			rb := maskJSONSecrets(out)
			if len(rb) > capN {
				rb = rb[:capN]
			}
			log.Printf("{\"event\":\"gost_api_resp\",\"method\":%q,\"url\":%q,\"status\":%d,\"bytes\":%d,\"body\":%s}", method, maskURLSecrets(fullURL), resp.StatusCode, len(out), string(rb))
		} else if logSuccess {
			log.Printf("{\"event\":\"gost_api_resp\",\"method\":%q,\"url\":%q,\"status\":%d,\"bytes\":%d}", method, maskURLSecrets(fullURL), resp.StatusCode, len(out))
		}
		if atomic.LoadInt32(&gostAPIOpLogOK) == 1 || !strings.EqualFold(method, "GET") {
			emitOpLog("gost_api", fmt.Sprintf("%s %s status=%d", method, path, resp.StatusCode), map[string]any{"method": method, "url": maskURLSecrets(fullURL), "status": resp.StatusCode, "bytes": len(out)})
		}
	} else {
		rb := maskJSONSecrets(out)
		if len(rb) > capN {
			rb = rb[:capN]
		}
		log.Printf("{\"event\":\"gost_api_resp\",\"method\":%q,\"url\":%q,\"status\":%d,\"bytes\":%d,\"body\":%s}", method, maskURLSecrets(fullURL), resp.StatusCode, len(out), string(rb))
		emitOpLog("gost_api_err", fmt.Sprintf("%s %s status=%d", method, path, resp.StatusCode), map[string]any{"method": method, "url": maskURLSecrets(fullURL), "status": resp.StatusCode, "bytes": len(out), "body": string(rb)})
	}
	return resp.StatusCode, out, nil
}

// apiGetServicesList streams /config/services response to reduce temporary allocations.
func apiGetServicesList() ([]map[string]any, error) {
	fullURL := apiBaseURL() + "/config/services"
	req, _ := http.NewRequest("GET", fullURL, nil)
	u, p := apiCreds()
	req.SetBasicAuth(u, p)
	resp, err := gostAPIHTTPClient.Do(req)
	if err != nil {
		log.Printf("{\"event\":\"gost_api_err\",\"method\":%q,\"url\":%q,\"error\":%q}", "GET", maskURLSecrets(fullURL), err.Error())
		emitOpLog("gost_api", "request error", map[string]any{"method": "GET", "url": maskURLSecrets(fullURL), "error": err.Error()})
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		rb = maskJSONSecrets(rb)
		log.Printf("{\"event\":\"gost_api_resp\",\"method\":%q,\"url\":%q,\"status\":%d,\"bytes\":%d,\"body\":%s}", "GET", maskURLSecrets(fullURL), resp.StatusCode, len(rb), string(rb))
		emitOpLog("gost_api_err", "GET /config/services non-2xx", map[string]any{"method": "GET", "url": maskURLSecrets(fullURL), "status": resp.StatusCode, "body": string(rb)})
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	atomic.StoreInt32(&apiUse, 1)
	maxItems := maxGostAPIServiceItems()
	listCap := 128
	if maxItems > 0 && maxItems < listCap {
		listCap = maxItems
	}
	list := make([]map[string]any, 0, listCap)
	dec := json.NewDecoder(resp.Body)
	start, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := start.(json.Delim); !ok || delim != '[' {
		return nil, fmt.Errorf("expected services array")
	}
	for dec.More() {
		if maxItems > 0 && len(list) >= maxItems {
			return nil, fmt.Errorf("too many services: limit %d", maxItems)
		}
		var svc map[string]any
		if err := dec.Decode(&svc); err != nil {
			return nil, err
		}
		list = append(list, svc)
	}
	end, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := end.(json.Delim); !ok || delim != ']' {
		return nil, fmt.Errorf("expected services array end")
	}
	every := int(atomic.LoadInt32(&gostAPISuccessLogEvery))
	if every <= 1 || atomic.AddUint64(&gostSvcGetRespCount, 1)%uint64(every) == 1 {
		log.Printf("{\"event\":\"gost_api_resp\",\"method\":%q,\"url\":%q,\"status\":%d,\"items\":%d,\"mode\":%q}", "GET", maskURLSecrets(fullURL), resp.StatusCode, len(list), "stream")
	}
	return list, nil
}

func maxGostAPIServiceItems() int {
	if v := strings.TrimSpace(getenv("AGENT_GOST_API_SERVICES_MAX_ITEMS", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	n := int(atomic.LoadInt32(&gostAPIServicesMaxItems))
	if n <= 0 {
		return 4096
	}
	return n
}

func maxGostConfigFileBytes() int64 {
	if v := strings.TrimSpace(getenv("AGENT_GOST_CONFIG_MAX_BYTES", "")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	n := atomic.LoadInt64(&gostConfigMaxBytes)
	if n <= 0 {
		return 4 * 1024 * 1024
	}
	return n
}

func readFileLimited(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 4 * 1024 * 1024
	}
	if st, err := os.Stat(path); err == nil && st.Size() > maxBytes {
		return nil, fmt.Errorf("file too large: %d > %d bytes", st.Size(), maxBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("file too large: over %d bytes", maxBytes)
	}
	return b, nil
}

// apiListServiceNames returns the list of service names from local GOST via Web API.
func apiListServiceNames() ([]string, error) {
	arr, err := apiGetServicesList()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		if n, _ := it["name"].(string); n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

// canonical string for stable hashing
func canonicalString(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%g", t)
	case int, int64, int32:
		return fmt.Sprintf("%v", t)
	case map[string]any:
		// sort keys
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b := strings.Builder{}
		for i, k := range keys {
			if i > 0 {
				b.WriteByte('|')
			}
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(canonicalString(t[k]))
		}
		return b.String()
	case []any:
		b := strings.Builder{}
		for i := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(canonicalString(t[i]))
		}
		return b.String()
	default:
		// numbers decoded from JSON come as float64; fall back to fmt
		return fmt.Sprintf("%v", t)
	}
}

func md5Bytes(b []byte) string { s := md5.Sum(b); return fmt.Sprintf("%x", s) }

// normalize subset for hashing comparison with server
func subsetForHash(svc map[string]any) map[string]any {
	out := map[string]any{}
	name, _ := svc["name"].(string)
	if name != "" {
		out["name"] = name
	}
	if m, _ := svc["listener"].(map[string]any); m != nil {
		if t, _ := m["type"].(string); t != "" {
			out["listener"] = map[string]any{"type": t}
		}
	}
	if m, _ := svc["handler"].(map[string]any); m != nil {
		if t, _ := m["type"].(string); t != "" {
			out["handler"] = map[string]any{"type": t}
		}
	}
	// forwarder nodes -> addrs sorted
	// For mid services (*_mid_i), forwarder target指向下一跳端口，端口不可预测，忽略 forwarder 以避免误判
	if !strings.Contains(name, "_mid_") {
		if m, _ := svc["forwarder"].(map[string]any); m != nil {
			if arr, _ := m["nodes"].([]any); arr != nil {
				addrs := make([]string, 0, len(arr))
				for _, it := range arr {
					if mm, ok := it.(map[string]any); ok {
						if a, _ := mm["addr"].(string); a != "" {
							addrs = append(addrs, a)
						}
					}
				}
				sort.Strings(addrs)
				out["forwarder"] = map[string]any{"addrs": addrs}
			}
		}
	}
	if m, _ := svc["metadata"].(map[string]any); m != nil {
		if ip, _ := m["interface"].(string); ip != "" {
			out["metadata"] = map[string]any{"interface": ip}
		}
	}
	return out
}

func hashServiceSubset(svc map[string]any) string {
	sub := subsetForHash(svc)
	s := canonicalString(sub)
	return md5Bytes([]byte(s))
}

func apiListServiceHashes() (map[string]string, error) {
	arr, err := apiGetServicesList()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(arr))
	for _, it := range arr {
		name, _ := it["name"].(string)
		if name == "" {
			continue
		}
		out[name] = hashServiceSubset(it)
	}
	return out, nil
}

// periodicReportServices polls local GOST services and reports to server on change.
func periodicReportServices(addr, secret, scheme string, done <-chan struct{}) {
	sec := 10
	if v := getenv("AGENT_SVC_REPORT_SEC", ""); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			sec = n
		}
	}
	heartbeatSec := 300
	if v := getenv("AGENT_SVC_REPORT_HEARTBEAT_SEC", ""); v != "" {
		if n, _ := strconv.Atoi(v); n > 0 {
			heartbeatSec = n
		}
	}
	ticker := time.NewTicker(time.Duration(sec) * time.Second)
	defer ticker.Stop()
	var lastSig string
	lastSentAt := time.Time{}
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if !apiAvailable() {
				continue
			}
			hashes, err := apiListServiceHashes()
			if err != nil {
				continue
			}
			names := make([]string, 0, len(hashes))
			for k := range hashes {
				names = append(names, k)
			}
			sort.Strings(names)
			var bld strings.Builder
			bld.Grow(len(names) * 48)
			for _, n := range names {
				bld.WriteString(n)
				bld.WriteByte('=')
				bld.WriteString(hashes[n])
				bld.WriteByte(';')
			}
			sig := bld.String()
			if sig == lastSig && !lastSentAt.IsZero() && time.Since(lastSentAt) < time.Duration(heartbeatSec)*time.Second {
				continue
			}
			payload := map[string]any{"secret": secret, "services": names, "hashes": hashes, "timeMs": time.Now().UnixMilli()}
			b, _ := json.Marshal(payload)
			proto := "http"
			if scheme == "wss" {
				proto = "https"
			}
			url := fmt.Sprintf("%s://%s/api/v1/agent/report-services", proto, addr)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			_, _ = http.DefaultClient.Do(req)
			cancel()
			lastSig = sig
			lastSentAt = time.Now()
		}
	}
}

// persistGostConfigSnapshot fetches full config via Web API and writes to /etc/gost/gost.json.
func persistGostConfigSnapshot() error {
	code, body, err := apiDo("GET", "/config", nil)
	if err != nil {
		emitOpLog("gost_api_err", "snapshot fetch failed", map[string]any{"error": err.Error()})
		return err
	}
	if code/100 != 2 {
		emitOpLog("gost_api_err", "snapshot fetch non-2xx", map[string]any{"status": code, "body": string(maskJSONSecrets(body))})
		return fmt.Errorf("snapshot fetch status %d", code)
	}
	var cfg map[string]any
	if e := json.Unmarshal(body, &cfg); e != nil {
		emitOpLog("gost_api_err", "snapshot unmarshal failed", map[string]any{"error": e.Error()})
		return e
	}
	// Preserve existing top-level api if missing (defensive)
	old := readGostConfig()
	if _, ok := cfg["api"]; !ok {
		if v, ok2 := old["api"]; ok2 {
			cfg["api"] = v
		}
	}
	// Always force local-only API bind into snapshot to avoid reverting to :18080 or wildcard.
	forcePort := resolveGostAPIPort(0)
	if p, ok := gostAPIPortFromConfig(old); ok {
		forcePort = p
	} else if p, ok := gostAPIPortFromConfig(cfg); ok {
		forcePort = p
	}
	forceLocalGostAPITopLevel(cfg, forcePort)
	// Write to /etc/gost/gost.json
	// Use our writer to ensure directory exists
	if err := writeGostConfig(cfg); err != nil {
		emitOpLog("gost_api_err", "snapshot write failed", map[string]any{"error": err.Error()})
		return err
	}
	emitOpLog("gost_api", "snapshot written", map[string]any{"path": resolveGostConfigPathForWrite()})
	return nil
}

// persistGostConfigServer saves current config back to GOST server via Web API.
// Strategy: GET /config to obtain normalized config, then PUT /config?format=json
// so GOST writes its configuration in JSON format to its configured storage.
func persistGostConfigServer() error {
	// Per API: POST /config?format=json with empty body to persist current runtime config.
	code, body, err := apiDo("POST", "/config?format=json", nil)
	if err != nil {
		emitOpLog("gost_api_err", "persist server save error", map[string]any{"error": err.Error()})
		return err
	}
	if code/100 != 2 {
		emitOpLog("gost_api_err", "persist server save non-2xx", map[string]any{"status": code, "body": string(maskJSONSecrets(body))})
		return fmt.Errorf("save config status %d", code)
	}
	emitOpLog("gost_api", "persist server saved", map[string]any{"status": code})
	return nil
}

// apiGetByName fetches a single resource object from /config/{res}/{name}.
func apiGetByName(res, name string) (map[string]any, int, error) {
	code, body, err := apiDo("GET", "/config/"+res+"/"+url.PathEscape(name), nil)
	if err != nil {
		return nil, 0, err
	}
	if code/100 != 2 {
		return nil, code, nil
	}
	// Response shapes seen:
	// 1) direct object: {"name":"obs_x", ...}
	// 2) wrapped: {"data": {...}} or {"data": null}
	var v any
	if json.Unmarshal(body, &v) != nil {
		return nil, code, nil
	}
	if obj, ok := v.(map[string]any); ok {
		if d, ok2 := obj["data"]; ok2 {
			if d == nil {
				// explicitly indicates not found
				return nil, code, nil
			}
			if dm, ok3 := d.(map[string]any); ok3 {
				return dm, code, nil
			}
			// non-object data – treat as not found
			return nil, code, nil
		}
		return obj, code, nil
	}
	return nil, code, nil
}

// apiExistsByName checks if a resource with a given name exists via Web API.
// It tries item endpoints first, then falls back to listing and scanning by name.
func apiExistsByName(res, name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	// Only /config endpoints are valid per requirements
	if code, _, err := apiDo("GET", "/config/"+res+"/"+url.PathEscape(name), nil); err == nil {
		if code == 200 {
			return true
		}
		if code == 404 {
			return false
		}
	}
	return false
}

// normalizeJSONAny marshals and unmarshals a value to JSON to normalize number types, etc.
func normalizeJSONAny(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

// equalBySubset returns true if all fields in want are present and equal in have (recursively), ignoring extra fields in have.
func equalBySubset(want any, have any) bool {
	switch w := want.(type) {
	case map[string]any:
		hv, ok := have.(map[string]any)
		if !ok {
			return false
		}
		for k, wv := range w {
			if !equalBySubset(wv, hv[k]) {
				return false
			}
		}
		return true
	case []any:
		hv, ok := have.([]any)
		if !ok {
			return false
		}
		if len(w) != len(hv) {
			return false
		}
		for i := range w {
			if !equalBySubset(w[i], hv[i]) {
				return false
			}
		}
		return true
	default:
		// numbers may be float64 after unmarshal
		// normalize via JSON roundtrip
		wn := normalizeJSONAny(w)
		hn := normalizeJSONAny(have)
		return reflect.DeepEqual(wn, hn)
	}
}

// maskURLSecrets redacts known sensitive query params in URLs (e.g., secret, password)
func maskURLSecrets(u string) string {
	if !strings.Contains(u, "?") {
		return u
	}
	parts := strings.SplitN(u, "?", 2)
	base, qs := parts[0], parts[1]
	vals, err := url.ParseQuery(qs)
	if err != nil {
		return u
	}
	for k := range vals {
		lk := strings.ToLower(k)
		if lk == "secret" || lk == "password" || lk == "token" {
			vals[k] = []string{"***"}
		}
	}
	return base + "?" + vals.Encode()
}

// maskJSONSecrets best-effort redacts secret/password fields in JSON payloads
func maskJSONSecrets(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	v2 := redactValue(v)
	out, err := json.Marshal(v2)
	if err != nil {
		return b
	}
	return out
}

func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m2 := make(map[string]any, len(x))
		for k, val := range x {
			lk := strings.ToLower(k)
			if lk == "password" || lk == "secret" || lk == "authorization" {
				m2[k] = "***"
				continue
			}
			m2[k] = redactValue(val)
		}
		return m2
	case []any:
		arr := make([]any, len(x))
		for i := range x {
			arr[i] = redactValue(x[i])
		}
		return arr
	default:
		return v
	}
}

func newAnyTLSProcessID() (string, error) {
	var raw [16]byte
	if _, err := crand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// emitOpLog queues an OpLog frame to panel via WS sender (runOnce).
func emitOpLog(step, message string, data map[string]any) {
	select {
	case opLogCh <- map[string]any{"step": step, "message": message, "data": data}:
	default:
		// drop when busy
	}
}

// emitAnyTLSCertLog queues dedicated AnyTLS certificate logs to panel via WS sender (runOnce).
func emitAnyTLSCertLog(step, message string, data map[string]any) {
	if anyTLSProcessID == "" {
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	data["processId"] = anyTLSProcessID
	select {
	case certLogCh <- map[string]any{"step": step, "message": message, "data": data}:
	default:
		// drop when busy
	}
}

// apiConfigChains upserts chains via GOST Web API.
// When updateOnly is true, it will only update existing chains; otherwise upsert.
func apiConfigChains(chains []map[string]any, updateOnly bool) error {
	if len(chains) == 0 {
		return nil
	}
	// single-object only (see below)
	// Strict single-object per call; GET existence decides PUT or POST
	okCount := 0
	for _, c := range chains {
		name, _ := c["name"].(string)
		target := normalizeJSONAny(c)
		if cur, code, _ := apiGetByName("chains", name); code == 200 && cur != nil {
			if equalBySubset(target, cur) {
				log.Printf(`{"event":"gost_api_skip_put","res":"chains","name":%q}`, name)
				okCount++
				continue
			}
			body, _ := json.Marshal(c)
			if code2, _, err := apiDo("PUT", "/config/chains/"+url.PathEscape(name), body); err == nil && code2/100 == 2 {
				okCount++
				continue
			}
		} else {
			body, _ := json.Marshal(c)
			if code2, _, err := apiDo("POST", "/config/chains", body); err == nil && code2/100 == 2 {
				okCount++
				continue
			}
		}
	}
	if okCount == len(chains) {
		// persist to server (best-effort)
		if err := persistGostConfigServer(); err != nil {
			log.Printf("{\"event\":\"gost_server_persist_err\",\"error\":%q}", err.Error())
		}
		return nil
	}
	return fmt.Errorf("chains api partial/failed: %d/%d", okCount, len(chains))
}

// apiConfigObservers upserts observers via GOST Web API.
// When updateOnly is true, it will only update existing observers; otherwise upsert.
func apiConfigObservers(observers []map[string]any, updateOnly bool) error {
	if len(observers) == 0 {
		return nil
	}
	// single-object only (see below)
	// Strict single-object per call; GET existence decides PUT or POST
	okCount := 0
	for _, o := range observers {
		name, _ := o["name"].(string)
		target := normalizeJSONAny(o)
		if cur, code, _ := apiGetByName("observers", name); code == 200 && cur != nil {
			if equalBySubset(target, cur) {
				log.Printf(`{"event":"gost_api_skip_put","res":"observers","name":%q}`, name)
				okCount++
				continue
			}
			body, _ := json.Marshal(o)
			if code2, _, err := apiDo("PUT", "/config/observers/"+url.PathEscape(name), body); err == nil && code2/100 == 2 {
				okCount++
				continue
			}
		} else {
			body, _ := json.Marshal(o)
			if code2, _, err := apiDo("POST", "/config/observers", body); err == nil && code2/100 == 2 {
				okCount++
				continue
			}
		}
	}
	if okCount == len(observers) {
		// persist to server (best-effort)
		if err := persistGostConfigServer(); err != nil {
			log.Printf("{\"event\":\"gost_server_persist_err\",\"error\":%q}", err.Error())
		}
		return nil
	}
	return fmt.Errorf("observers api partial/failed: %d/%d", okCount, len(observers))
}

// apiConfigLimiters upserts limiters via GOST Web API.
// When updateOnly is true, it will only update existing limiters; otherwise upsert.
func apiConfigLimiters(limiters []map[string]any, updateOnly bool) error {
	if len(limiters) == 0 {
		return nil
	}
	if !isApiUsable() && !apiBootstrapOnce() {
		return fmt.Errorf("gost web api unavailable: please enable on node")
	}
	okCount := 0
	for _, lm := range limiters {
		name, _ := lm["name"].(string)
		target := normalizeJSONAny(lm)
		if cur, code, _ := apiGetByName("limiters", name); code == 200 && cur != nil {
			if equalBySubset(target, cur) {
				log.Printf(`{"event":"gost_api_skip_put","res":"limiters","name":%q}`, name)
				okCount++
				continue
			}
			body, _ := json.Marshal(lm)
			if code2, _, err := apiDo("PUT", "/config/limiters/"+url.PathEscape(name), body); err == nil && code2/100 == 2 {
				okCount++
				continue
			}
		}
		if updateOnly {
			continue
		}
		body, _ := json.Marshal(lm)
		if code2, _, err := apiDo("POST", "/config/limiters", body); err == nil && code2/100 == 2 {
			okCount++
			continue
		}
	}
	if okCount == len(limiters) {
		if err := persistGostConfigServer(); err != nil {
			log.Printf("{\"event\":\"gost_server_persist_err\",\"error\":%q}", err.Error())
		}
		return nil
	}
	return fmt.Errorf("limiters api partial/failed: %d/%d", okCount, len(limiters))
}

// queryServices returns a summary list of services, optionally filtered by handler type.
func queryServices(filter string) []map[string]any {
	// Prefer Web API if available
	if isApiUsable() {
		if list, err := apiGetServicesList(); err == nil {
			// optionally filter by handler
			if filter != "" {
				out := make([]map[string]any, 0, len(list))
				for _, m := range list {
					h, _ := m["handler"].(string)
					if strings.EqualFold(h, filter) {
						out = append(out, m)
					}
				}
				return out
			}
			return list
		}
	}
	cfg := readGostConfig()
	arrAny, _ := cfg["services"].([]any)
	out := make([]map[string]any, 0, len(arrAny))
	for _, it := range arrAny {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		addr, _ := m["addr"].(string)
		handler, _ := m["handler"].(map[string]any)
		htype := ""
		if handler != nil {
			if v, ok := handler["type"].(string); ok {
				htype = v
			}
		}
		if filter != "" && strings.ToLower(htype) != strings.ToLower(filter) {
			continue
		}
		limiter, _ := m["limiter"].(string)
		rlimiter, _ := m["rlimiter"].(string)
		meta, _ := m["metadata"].(map[string]any)
		port := parsePort(addr)
		listening := false
		if port > 0 {
			listening = portListening(port)
		}
		out = append(out, map[string]any{
			"name":      name,
			"addr":      addr,
			"handler":   htype,
			"port":      port,
			"listening": listening,
			"limiter":   limiter,
			"rlimiter":  rlimiter,
			"metadata":  meta,
		})
	}
	return out
}

func parsePort(addr string) int {
	if addr == "" {
		return 0
	}
	// common formats: ":8080", "0.0.0.0:8080", "[::]:8080"
	a := strings.TrimSpace(addr)
	if strings.HasPrefix(a, "[") {
		// [host]:port
		if i := strings.LastIndex(a, "]:"); i >= 0 && i+2 < len(a) {
			p := a[i+2:]
			n, _ := strconv.Atoi(p)
			return n
		}
		return 0
	}
	if i := strings.LastIndexByte(a, ':'); i >= 0 && i+1 < len(a) {
		n, _ := strconv.Atoi(a[i+1:])
		return n
	}
	return 0
}

func portListening(port int) bool {
	if port <= 0 {
		return false
	}
	to := 200 * time.Millisecond
	// try ipv4 loopback
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), to)
	if err == nil {
		c.Close()
		return true
	}
	// try ipv6 loopback
	c2, err2 := net.DialTimeout("tcp", fmt.Sprintf("[::1]:%d", port), to)
	if err2 == nil {
		c2.Close()
		return true
	}
	return false
}

// getUsedListeningPorts lists TCP/UDP LISTEN ports via ss; fallback to /proc/net; then probe
func getUsedListeningPorts() map[int]bool {
	used := map[int]bool{}
	// Prefer /proc on Linux to avoid spawning `ss` and large command output buffers.
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6", "/proc/net/udp", "/proc/net/udp6"} {
		if ports := parseProcNetPorts(p); len(ports) > 0 {
			for k := range ports {
				used[k] = true
			}
		}
	}
	if len(used) > 0 {
		return used
	}
	// Fallback: try ss when /proc parsing is unavailable.
	if p, err := exec.LookPath("ss"); err == nil {
		cmd := exec.Command(p, "-lntuH")
		out, err := runCommandCombinedLimited(cmd, 128*1024)
		if err == nil {
			for _, ln := range strings.Split(out, "\n") {
				if ln == "" {
					continue
				}
				fields := strings.Fields(ln)
				for _, f := range fields {
					if strings.HasSuffix(f, ":*") {
						continue
					}
					if i := strings.LastIndex(f, ":"); i >= 0 && i+1 < len(f) {
						if n, err2 := strconv.Atoi(f[i+1:]); err2 == nil {
							used[n] = true
						}
					}
				}
			}
			if len(used) > 0 {
				return used
			}
		}
	}
	// Fallback minimal: probe a small range around common ports
	for _, p := range []int{22, 80, 443, 3306, 6379} {
		if portListening(p) {
			used[p] = true
		}
	}
	return used
}

// parseProcNetPorts returns listening ports from /proc/net/{tcp,udp} style files.
func parseProcNetPorts(path string) map[int]bool {
	ports := map[int]bool{}
	f, err := os.Open(path)
	if err != nil {
		return ports
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// allow long lines in /proc/net
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		ln := sc.Text()
		lineNo++
		if lineNo == 1 || strings.TrimSpace(ln) == "" {
			continue
		}
		fields := strings.Fields(ln)
		if len(fields) < 4 {
			continue
		}
		local := fields[1]
		state := fields[3]
		// TCP LISTEN = 0A, UDP LISTEN = 07
		if state != "0A" && state != "07" {
			continue
		}
		if idx := strings.LastIndex(local, ":"); idx >= 0 && idx+1 < len(local) {
			if p, err := strconv.ParseInt(local[idx+1:], 16, 0); err == nil {
				if p > 0 && p <= 65535 {
					ports[int(p)] = true
				}
			}
		}
	}
	return ports
}

// suggestPorts returns up to count nearest higher free ports above base
func suggestPorts(base, count int) []int {
	if count <= 0 {
		count = 10
	}
	if base < 0 {
		base = 0
	}
	used := getUsedListeningPorts()
	out := make([]int, 0, count)
	p := base + 1
	scanned := 0
	for len(out) < count && p <= 65535 && scanned < 20000 {
		if !used[p] && !portListening(p) {
			out = append(out, p)
		}
		p++
		scanned++
	}
	return out
}

// addOrUpdateServices merges provided services into gost.json services array.
// If updateOnly is true, only update existing by name; otherwise upsert (add if missing).
func addOrUpdateServices(services []map[string]any, updateOnly bool) error {
	// 必须使用 Web API；若不可用，先尝试一次自动启用，再报错。
	if !isApiUsable() && !apiBootstrapOnce() {
		emitOpLog("gost_api_err", "web api unavailable", map[string]any{"message": "GOST Web API 未启用，请在节点上开启后重试"})
		return fmt.Errorf("gost web api unavailable: please enable on node")
	}
	if ports := loadAnyTLSRuntimePortSetForGostGuard(); len(ports) > 0 {
		filtered, skipped := filterAnyTLSGostLoopServices(services, ports)
		if len(skipped) > 0 {
			log.Printf("{\"event\":\"anytls_gost_loop_guard_skip\",\"services\":%q,\"updateOnly\":%v}", skipped, updateOnly)
			emitOpLog("anytls_gost_loop_guard", "skip AddService/UpdateService on AnyTLS runtime port", map[string]any{"services": skipped, "updateOnly": updateOnly})
			_ = deleteServices(skipped)
			services = filtered
			if len(services) == 0 {
				return nil
			}
		}
	}
	if isApiUsable() {
		// extract chains
		chains := make([]map[string]any, 0)
		for i := range services {
			if extra, ok := services[i]["_chains"]; ok {
				if arr, ok2 := extra.([]any); ok2 {
					for _, it := range arr {
						if m, ok3 := it.(map[string]any); ok3 {
							chains = append(chains, m)
						}
					}
				}
				delete(services[i], "_chains")
			}
		}
		if len(chains) > 0 {
			if err := apiConfigChains(chains, true); err != nil {
				return err
			}
		}
		// extract observers
		observers := make([]map[string]any, 0)
		for i := range services {
			if extra, ok := services[i]["_observers"]; ok {
				if arr, ok2 := extra.([]any); ok2 {
					for _, it := range arr {
						if m, ok3 := it.(map[string]any); ok3 {
							observers = append(observers, m)
						}
					}
				}
				delete(services[i], "_observers")
			}
		}
		if len(observers) > 0 {
			if err := apiConfigObservers(observers, true); err != nil {
				return err
			}
		}
		// no batch; single-object calls only per swagger
		okCount := 0
		firstErr := ""
		errSummary := func(name, action string, code int, body []byte, err error) string {
			if err != nil {
				return fmt.Sprintf("service %s %s error: %v", name, action, err)
			}
			s := strings.TrimSpace(string(maskJSONSecrets(body)))
			if len(s) > 220 {
				s = s[:220] + "..."
			}
			return fmt.Sprintf("service %s %s status=%d body=%s", name, action, code, s)
		}
		setFirstErr := func(msg string) {
			if firstErr == "" && strings.TrimSpace(msg) != "" {
				firstErr = msg
			}
		}
		for _, s := range services {
			name, _ := s["name"].(string)
			target := normalizeJSONAny(s)
			if cur, code, _ := apiGetByName("services", name); code == 200 && cur != nil {
				if equalBySubset(target, cur) {
					log.Printf(`{"event":"gost_api_skip_put","res":"services","name":%q}`, name)
					okCount++
					continue
				}
				body, _ := json.Marshal(s)
				code2, respBody, err := apiDo("PUT", "/config/services/"+url.PathEscape(name), body)
				if err == nil && code2/100 == 2 {
					okCount++
					continue
				}
				setFirstErr(errSummary(name, "PUT", code2, respBody, err))
			} else {
				body, _ := json.Marshal(s)
				code2, respBody, err := apiDo("POST", "/config/services", body)
				if err == nil && code2/100 == 2 {
					okCount++
					continue
				}
				setFirstErr(errSummary(name, "POST", code2, respBody, err))
			}
		}
		if okCount == len(services) {
			// ask GOST to persist its config server-side (best-effort)
			if err := persistGostConfigServer(); err != nil {
				log.Printf("{\"event\":\"gost_server_persist_err\",\"error\":%q}", err.Error())
			}
			return nil
		}
		if firstErr != "" {
			return fmt.Errorf("services api partial/failed: %d/%d (%s)", okCount, len(services), firstErr)
		}
		return fmt.Errorf("services api partial/failed: %d/%d", okCount, len(services))
	}
	emitOpLog("gost_api_err", "web api unavailable", map[string]any{"message": "GOST Web API 未启用，请在节点上开启后重试"})
	return fmt.Errorf("gost web api unavailable: please enable on node")
	// (不再回退写文件)
	/*cfg := readGostConfig()
		// merge optional chains injected per-service under _chains (upsert by name)
		// 1) Merge observers from _observers
		if arr, ok := cfg["observers"].([]any); ok {
			// keep existing
			_ = arr
		}
		observersAny, _ := cfg["observers"].([]any)
		obsIdx := map[string]int{}
		for i, it := range observersAny {
			if m, ok := it.(map[string]any); ok {
				if n, ok2 := m["name"].(string); ok2 && n != "" {
					obsIdx[n] = i
				}
			}
		}
		for _, svc := range services {
			if extra, ok := svc["_observers"]; ok {
				if arr, ok2 := extra.([]any); ok2 {
					for _, it := range arr {
						if m, ok3 := it.(map[string]any); ok3 {
							n, _ := m["name"].(string)
							if n == "" {
								continue
							}
							if i, ok4 := obsIdx[n]; ok4 {
								observersAny[i] = m
							} else {
								observersAny = append(observersAny, m)
								obsIdx[n] = len(observersAny) - 1
							}
						}
					}
				}
				delete(svc, "_observers")
			}
		}
		if len(observersAny) > 0 {
			cfg["observers"] = observersAny
		}

		// 2) Merge chains from _chains
		chainsAny, _ := cfg["chains"].([]any)
		chainIdx := map[string]int{}
		for i, it := range chainsAny {
			if m, ok := it.(map[string]any); ok {
				if n, ok2 := m["name"].(string); ok2 && n != "" {
					chainIdx[n] = i
				}
			}
		}
		for _, svc := range services {
			if extra, ok := svc["_chains"]; ok {
				if arr, ok2 := extra.([]any); ok2 {
					for _, it := range arr {
						if m, ok3 := it.(map[string]any); ok3 {
							n, _ := m["name"].(string)
							if n == "" {
								continue
							}
							if i, ok4 := chainIdx[n]; ok4 {
								chainsAny[i] = m
							} else {
								chainsAny = append(chainsAny, m)
								chainIdx[n] = len(chainsAny) - 1
							}
						}
					}
				}
				delete(svc, "_chains")
			}
			// Do not synthesize implicit chains in fallback path; only merge provided _chains
		}
		if len(chainsAny) > 0 {
			cfg["chains"] = chainsAny
		}

		// ensure services array exists
		arrAny, _ := cfg["services"].([]any)
		// build name -> index map
		idx := map[string]int{}
		for i, it := range arrAny {
			if m, ok := it.(map[string]any); ok {
				if n, ok2 := m["name"].(string); ok2 && n != "" {
					idx[n] = i
				}
			}
		}
		for _, svc := range services {
			name, _ := svc["name"].(string)
			if name == "" {
				continue
			}
			if i, ok := idx[name]; ok {
				if updateOnly {
					// merge into existing (handler-level merge)
					if existing, ok2 := arrAny[i].(map[string]any); ok2 {
						if hNew, okH := svc["handler"].(map[string]any); okH && hNew != nil {
							hOld, _ := existing["handler"].(map[string]any)
							if hOld == nil {
								hOld = map[string]any{}
							}
							for k, v := range hNew {
								hOld[k] = v
							}
							existing["handler"] = hOld
						}
						arrAny[i] = existing
					} else {
						arrAny[i] = svc
					}
				} else {
					// replace existing
					arrAny[i] = svc
				}
			} else {
				// missing service
				if updateOnly {
					// Add only if looks complete (has addr or listener)
					addr, _ := svc["addr"].(string)
					hasListener := false
					if lst, ok2 := svc["listener"].(map[string]any); ok2 && len(lst) > 0 {
						hasListener = true
					}
					if addr != "" || hasListener {
						arrAny = append(arrAny, svc)
						idx[name] = len(arrAny) - 1
					}
				} else {
					arrAny = append(arrAny, svc)
					idx[name] = len(arrAny) - 1
				}
			}
		}
		cfg["services"] = arrAny
	    return writeGostConfig(cfg)*/
}

func loadAnyTLSRuntimePortSetForGostGuard() map[int]struct{} {
	b, err := os.ReadFile(anytlsConfigPath)
	if err != nil || len(b) == 0 {
		return nil
	}
	var cfg anytlsConfigFile
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Printf("{\"event\":\"anytls_gost_loop_guard_config_err\",\"error\":%q}", err.Error())
		return nil
	}
	ports := map[int]struct{}{}
	if cfg.Port > 0 {
		ports[cfg.Port] = struct{}{}
	}
	for _, inst := range cfg.Instances {
		if inst.Port > 0 {
			ports[inst.Port] = struct{}{}
		}
	}
	return ports
}

func filterAnyTLSGostLoopServices(services []map[string]any, runtimePorts map[int]struct{}) ([]map[string]any, []string) {
	if len(services) == 0 || len(runtimePorts) == 0 {
		return services, nil
	}
	filtered := make([]map[string]any, 0, len(services))
	skippedSet := map[string]struct{}{}
	for _, svc := range services {
		if isAnyTLSGostLoopService(svc, runtimePorts) {
			for _, name := range expandAnyTLSGostGuardNames(mapStringValue(svc, "name")) {
				skippedSet[name] = struct{}{}
			}
			continue
		}
		filtered = append(filtered, svc)
	}
	if len(skippedSet) == 0 {
		return filtered, nil
	}
	skipped := make([]string, 0, len(skippedSet))
	for name := range skippedSet {
		if strings.TrimSpace(name) != "" {
			skipped = append(skipped, name)
		}
	}
	sort.Strings(skipped)
	return filtered, skipped
}

func isAnyTLSGostLoopService(svc map[string]any, runtimePorts map[int]struct{}) bool {
	if len(runtimePorts) == 0 || !isPanelManagedGostService(svc) {
		return false
	}
	port := parsePort(mapStringValue(svc, "addr"))
	if port <= 0 {
		return false
	}
	if _, ok := runtimePorts[port]; !ok {
		return false
	}
	if !strings.EqualFold(nestedTypeValue(svc, "handler"), "forward") {
		return false
	}
	lt := strings.ToLower(strings.TrimSpace(nestedTypeValue(svc, "listener")))
	return lt == "tcp" || lt == "rudp"
}

func isPanelManagedGostService(svc map[string]any) bool {
	meta, _ := svc["metadata"].(map[string]any)
	if meta == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(fmt.Sprint(meta["managedBy"])), "network-panel")
}

func nestedTypeValue(svc map[string]any, key string) string {
	raw := svc[key]
	if s, ok := raw.(string); ok {
		return s
	}
	if m, ok := raw.(map[string]any); ok {
		return strings.TrimSpace(fmt.Sprint(m["type"]))
	}
	return ""
}

func mapStringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(m[key]))
}

func expandAnyTLSGostGuardNames(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	base := strings.TrimSuffix(name, "_rudp")
	if base == "" || base == name && strings.HasSuffix(name, "_rudp") {
		return []string{name}
	}
	return []string{base, base + "_rudp"}
}

func deleteServices(names []string) error {
	if len(names) == 0 {
		return nil
	}
	if isApiUsable() {
		// batch delete
		payload := map[string]any{"services": names}
		b, _ := json.Marshal(payload)
		if code, _, err := apiDo("DELETE", "/config/services", b); err == nil && code/100 == 2 {
			if err := persistGostConfigServer(); err != nil {
				log.Printf("{\"event\":\"gost_server_persist_err\",\"error\":%q}", err.Error())
			}
			return nil
		}
		// per-name path delete (/config only)
		okCount := 0
		for _, n := range names {
			p := "/config/services/" + url.PathEscape(n)
			if code, _, err := apiDo("DELETE", p, nil); err == nil && code/100 == 2 {
				okCount++
				continue
			}
		}
		if okCount == len(names) {
			if err := persistGostConfigServer(); err != nil {
				log.Printf("{\"event\":\"gost_server_persist_err\",\"error\":%q}", err.Error())
			}
			return nil
		}
	}
	rm := map[string]struct{}{}
	for _, n := range names {
		if n != "" {
			rm[n] = struct{}{}
		}
	}
	cfg := readGostConfig()
	arrAny, _ := cfg["services"].([]any)
	out := make([]any, 0, len(arrAny))
	for _, it := range arrAny {
		keep := true
		if m, ok := it.(map[string]any); ok {
			if n, ok2 := m["name"].(string); ok2 {
				if _, bad := rm[n]; bad {
					keep = false
				}
			}
		}
		if keep {
			out = append(out, it)
		}
	}
	cfg["services"] = out
	return writeGostConfig(cfg)
}

func markServicesPaused(names []string, paused bool) error {
	if len(names) == 0 {
		return nil
	}
	want := map[string]struct{}{}
	for _, n := range names {
		if n != "" {
			want[n] = struct{}{}
		}
	}
	cfg := readGostConfig()
	arrAny, _ := cfg["services"].([]any)
	for i, it := range arrAny {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		n, _ := m["name"].(string)
		if _, hit := want[n]; !hit {
			continue
		}
		meta, _ := m["metadata"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		if paused {
			meta["paused"] = true
		} else {
			delete(meta, "paused")
		}
		if len(meta) == 0 {
			meta = nil
		}
		m["metadata"] = meta
		arrAny[i] = m
	}
	cfg["services"] = arrAny
	return writeGostConfig(cfg)
}

func runTCP(host string, port, count, timeoutMs int) (avg int, loss int) {
	if host == "" || port <= 0 {
		return 0, 100
	}
	succ := 0
	sum := 0
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	to := time.Duration(timeoutMs) * time.Millisecond
	for i := 0; i < count; i++ {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", addr, to)
		if err == nil {
			_ = conn.Close()
			ms := int(time.Since(start).Milliseconds())
			sum += ms
			succ++
		}
	}
	if succ == 0 {
		return 0, 100
	}
	return sum / succ, (count - succ) * 100 / count
}

func runICMP(host string, count, timeoutMs int) (avg int, loss int) {
	if host == "" {
		return 0, 100
	}
	timeoutS := fmt.Sprintf("%d", (timeoutMs+999)/1000)
	cmdName := "ping"
	args := []string{"-c", fmt.Sprintf("%d", count), "-W", timeoutS, host}
	if strings.Contains(host, ":") { // ipv6
		args = []string{"-6", "-c", fmt.Sprintf("%d", count), "-W", timeoutS, host}
	}
	cmd := exec.Command(cmdName, args...)
	out, err := runCommandCombinedLimited(cmd, 32*1024)
	if err != nil {
		return 0, 100
	}
	// parse loss
	pct := 100
	reLoss := regexp.MustCompile(`([0-9]+\.?[0-9]*)% packet loss`)
	if m := reLoss.FindStringSubmatch(string(out)); len(m) == 2 {
		if f, e := strconv.ParseFloat(m[1], 64); e == nil {
			pct = int(f + 0.5)
		}
	}
	// parse avg
	ag := 0
	reAvg := regexp.MustCompile(`= [0-9.]+/([0-9.]+)/[0-9.]+/[0-9.]+ ms`)
	if m := reAvg.FindStringSubmatch(string(out)); len(m) == 2 {
		if f, e := strconv.ParseFloat(m[1], 64); e == nil {
			ag = int(f + 0.5)
		}
	}
	return ag, pct
}

func pickPort() int {
	rand.Seed(time.Now().UnixNano())
	for i := 0; i < 100; i++ {
		p := 10000 + rand.Intn(20000)
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			_ = ln.Close()
			return p
		}
	}
	return 5201
}

func startIperf3Server(port int) bool {
	cmd := exec.Command("iperf3", "-s", "-D", "-p", fmt.Sprintf("%d", port))
	_, err := runCommandCombinedLimited(cmd, 32*1024)
	return err == nil
}

func runIperf3Client(host string, port, duration int, reverse bool) float64 {
	if host == "" || port <= 0 {
		return 0
	}
	args := []string{"-J", "-c", host, "-p", fmt.Sprintf("%d", port), "-t", fmt.Sprintf("%d", duration)}
	if reverse {
		args = append(args, "-R")
	}
	cmd := exec.Command("iperf3", args...)
	out, err := runCommandCombinedLimited(cmd, 256*1024)
	if err != nil {
		return 0
	}
	var m map[string]any
	if json.Unmarshal([]byte(out), &m) != nil {
		return 0
	}
	end, _ := m["end"].(map[string]any)
	rec, _ := end["sum_received"].(map[string]any)
	if rec == nil {
		rec, _ = end["sum_sent"].(map[string]any)
	}
	if rec == nil {
		return 0
	}
	bps, _ := rec["bits_per_second"].(float64)
	if bps <= 0 {
		return 0
	}
	return bps / 1e6
}

// runIperf3ClientVerbose returns bw and a compact message (error/output snippet) for logging
func runIperf3ClientVerbose(host string, port, duration int, reverse bool) (float64, string) {
	if host == "" || port <= 0 {
		return 0, "invalid host/port"
	}
	args := []string{"-J", "-c", host, "-p", fmt.Sprintf("%d", port), "-t", fmt.Sprintf("%d", duration)}
	if reverse {
		args = append(args, "-R")
	}
	cmd := exec.Command("iperf3", args...)
	out, err := runCommandCombinedLimited(cmd, 256*1024)
	if err != nil {
		msg := out
		if len(msg) > 240 {
			msg = msg[:240]
		}
		if msg == "" {
			msg = err.Error()
		}
		return 0, msg
	}
	var m map[string]any
	if json.Unmarshal([]byte(out), &m) != nil {
		msg := out
		if len(msg) > 240 {
			msg = msg[:240]
		}
		return 0, msg
	}
	end, _ := m["end"].(map[string]any)
	rec, _ := end["sum_received"].(map[string]any)
	if rec == nil {
		rec, _ = end["sum_sent"].(map[string]any)
	}
	if rec == nil {
		return 0, "no sum section"
	}
	bps, _ := rec["bits_per_second"].(float64)
	if bps <= 0 {
		return 0, "zero bps"
	}
	return bps / 1e6, "ok"
}

// ---- Probe targets poll & report ----

type probeTarget struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip"`
}

func httpPostJSON(url string, body any) (int, []byte, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := panelHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	return resp.StatusCode, out, nil
}

func apiURL(scheme, addr, path string) string {
	u := url.URL{Scheme: "http", Host: addr, Path: path}
	if scheme == "wss" {
		u.Scheme = "https"
	}
	return u.String()
}

func periodicProbe(addr, secret, scheme string, done <-chan struct{}) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		doProbeOnce(addr, secret, scheme)
		select {
		case <-done:
			return
		case <-ticker.C:
		}
	}
}

func doProbeOnce(addr, secret, scheme string) {
	// fetch targets
	url1 := apiURL(scheme, addr, "/api/v1/agent/probe-targets")
	type resp struct {
		Code int           `json:"code"`
		Data []probeTarget `json:"data"`
	}
	code, body, err := httpPostJSON(url1, map[string]any{"secret": secret})
	if err != nil || code != 200 {
		return
	}
	var r resp
	if json.Unmarshal(body, &r) != nil || r.Code != 0 || len(r.Data) == 0 {
		return
	}
	// ping each
	results := make([]map[string]any, 0, len(r.Data))
	for _, t := range r.Data {
		avg, loss := runICMP(t.IP, 1, 1000)
		ok := 0
		if loss < 100 && avg > 0 {
			ok = 1
		}
		results = append(results, map[string]any{"targetId": t.ID, "rttMs": avg, "ok": ok})
	}
	if len(results) == 0 {
		return
	}
	url2 := apiURL(scheme, addr, "/api/v1/agent/report-probe")
	_, _, _ = httpPostJSON(url2, map[string]any{"secret": secret, "results": results})
}

// selfUpgrade downloads latest agent binary from server and restarts service
func selfUpgrade(addr, scheme string) error {
	arch := detectArch()
	// pick binary and service by role
	binName := "flux-agent-linux-" + arch
	target := "/etc/gost/flux-agent"
	svc := "flux-agent"
	if isAgent2Binary() {
		binName = "flux-agent2-linux-" + arch
		target = "/etc/gost/flux-agent2"
		svc = "flux-agent2"
	}
	u := apiURL(scheme, addr, "/flux-agent/"+binName)
	tmp := target + ".new"
	log.Printf("{\"event\":\"agent_upgrade_begin\",\"url\":%q}", u)
	emitOpLog("agent_upgrade_start", "开始升级", map[string]any{"url": u})
	emitOpLog("agent_upgrade_download", "下载升级包", map[string]any{"url": u})
	if err := downloadRetry(u, tmp, 3); err != nil {
		log.Printf("upgrade download err: %v", err)
		emitOpLog("agent_upgrade_error", "下载失败: "+err.Error(), nil)
		return err
	}
	if err := validateBinary(tmp, arch); err != nil {
		log.Printf("upgrade validation err: %v", err)
		_ = os.Remove(tmp)
		emitOpLog("agent_upgrade_error", "校验失败: "+err.Error(), nil)
		return err
	}
	// Ensure downloaded temp binary is executable before smoke test.
	if err := os.Chmod(tmp, 0755); err != nil {
		log.Printf("upgrade chmod err: %v", err)
		_ = os.Remove(tmp)
		emitOpLog("agent_upgrade_error", "设置执行权限失败: "+err.Error(), nil)
		return err
	}
	if err := validateBinaryRunnable(tmp); err != nil {
		log.Printf("upgrade smoke test err: %v", err)
		_ = os.Remove(tmp)
		emitOpLog("agent_upgrade_error", "冒烟测试失败: "+err.Error(), nil)
		return err
	}
	emitOpLog("agent_upgrade_validate", "校验通过", nil)
	_ = safeReplace(target, tmp)
	_ = os.Chmod(target, 0755)
	// restart service or exec-replace
	emitOpLog("agent_upgrade_restart", "重启服务 "+svc, nil)
	if tryRestartService(svc) {
		log.Printf("{\"event\":\"agent_upgrade_done\",\"service\":%q}", svc)
		emitOpLog("agent_upgrade_done", "升级完成", map[string]any{"service": svc})
		return nil
	}
	// fallback: exec replace self
	args := append([]string{target}, os.Args[1:]...)
	_ = execReplace(target, args, os.Environ())
	// last resort: start child and exit
	_ = exec.Command(target, os.Args[1:]...).Start()
	os.Exit(0)
	return nil
}

func tryRestartService(name string) bool {
	if _, err := exec.LookPath("systemctl"); err == nil {
		if e := exec.Command("systemctl", "daemon-reload").Run(); e == nil { /*noop*/
		}
		if e := exec.Command("systemctl", "restart", name).Run(); e == nil {
			return true
		}
	}
	if _, err := exec.LookPath("service"); err == nil {
		if e := exec.Command("service", name, "restart").Run(); e == nil {
			return true
		}
	}
	return false
}

// upgradeAgent1 ensures flux-agent is installed and (re)started to expected version if provided.
func upgradeAgent1(addr, scheme, expected string) error {
	arch := detectArch()
	u := apiURL(scheme, addr, "/flux-agent/"+"flux-agent-linux-"+arch)
	target := "/etc/gost/flux-agent"
	verFile := target + ".version"
	emitOpLog("agent_upgrade_start", "开始升级", map[string]any{"url": u})
	emitOpLog("agent_upgrade_download", "下载升级包", map[string]any{"url": u})
	if expected != "" {
		if b, err := os.ReadFile(verFile); err == nil && strings.TrimSpace(string(b)) == expected {
			return nil
		}
	}
	tmp := target + ".new"
	if err := downloadRetry(u, tmp, 3); err != nil {
		emitOpLog("agent_upgrade_error", "下载失败: "+err.Error(), nil)
		return err
	}
	if err := validateBinary(tmp, arch); err != nil {
		_ = os.Remove(tmp)
		emitOpLog("agent_upgrade_error", "校验失败: "+err.Error(), nil)
		return err
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		_ = os.Remove(tmp)
		emitOpLog("agent_upgrade_error", "设置执行权限失败: "+err.Error(), nil)
		return err
	}
	if err := validateBinaryRunnable(tmp); err != nil {
		_ = os.Remove(tmp)
		emitOpLog("agent_upgrade_error", "冒烟测试失败: "+err.Error(), nil)
		return err
	}
	emitOpLog("agent_upgrade_validate", "校验通过", nil)
	_ = safeReplace(target, tmp)
	_ = os.Chmod(target, 0755)
	_ = os.WriteFile(verFile, []byte(expected), 0644)
	// ensure service exists and start
	ensureSystemdService("flux-agent", target)
	emitOpLog("agent_upgrade_restart", "重启服务 flux-agent", nil)
	if !tryRestartService("flux-agent") {
		// best-effort start detached
		_ = exec.Command(target).Start()
	}
	emitOpLog("agent_upgrade_done", "升级完成", map[string]any{"service": "flux-agent"})
	return nil
}

// upgradeAgent2 ensures flux-agent2 is installed and (re)started to expected version if provided.
func upgradeAgent2(addr, scheme, expected string) error {
	arch := detectArch()
	u := apiURL(scheme, addr, "/flux-agent/"+"flux-agent2-linux-"+arch)
	target := "/etc/gost/flux-agent2"
	verFile := target + ".version"
	emitOpLog("agent_upgrade_start", "开始升级", map[string]any{"url": u})
	emitOpLog("agent_upgrade_download", "下载升级包", map[string]any{"url": u})
	if expected != "" {
		if b, err := os.ReadFile(verFile); err == nil && strings.TrimSpace(string(b)) == expected {
			return nil
		}
	}
	tmp := target + ".new"
	if err := downloadRetry(u, tmp, 3); err != nil {
		emitOpLog("agent_upgrade_error", "下载失败: "+err.Error(), nil)
		return err
	}
	if err := validateBinary(tmp, arch); err != nil {
		_ = os.Remove(tmp)
		emitOpLog("agent_upgrade_error", "校验失败: "+err.Error(), nil)
		return err
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		_ = os.Remove(tmp)
		emitOpLog("agent_upgrade_error", "设置执行权限失败: "+err.Error(), nil)
		return err
	}
	if err := validateBinaryRunnable(tmp); err != nil {
		_ = os.Remove(tmp)
		emitOpLog("agent_upgrade_error", "冒烟测试失败: "+err.Error(), nil)
		return err
	}
	emitOpLog("agent_upgrade_validate", "校验通过", nil)
	_ = safeReplace(target, tmp)
	_ = os.Chmod(target, 0755)
	_ = os.WriteFile(verFile, []byte(expected), 0644)
	// In single-agent mode we only stage the binary and do NOT run service
	if singleAgentMode() {
		disableService("flux-agent2")
	} else {
		ensureSystemdService("flux-agent2", target)
		emitOpLog("agent_upgrade_restart", "重启服务 flux-agent2", nil)
		if !tryRestartService("flux-agent2") {
			_ = exec.Command(target).Start()
		}
	}
	emitOpLog("agent_upgrade_done", "升级完成", map[string]any{"service": "flux-agent2"})
	return nil
}

func ensureSystemdService(name, execPath string) {
	svc := "/etc/systemd/system/" + name + ".service"
	content := fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=-/etc/default/%s
ExecStart=%s
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
`, name, name, execPath)
	// write service file (best-effort)
	_ = os.WriteFile(svc, []byte(content), 0644)
	ensureServiceLogDropIn(name)
	_ = exec.Command("systemctl", "daemon-reload").Run()
	_ = exec.Command("systemctl", "enable", name).Run()
}

func ensureServiceLogDropIn(name string) {
	if _, err := exec.LookPath("systemctl"); err != nil || strings.TrimSpace(name) == "" {
		return
	}
	dir := "/etc/systemd/system/" + name + ".service.d"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	content := `[Service]
# network-panel: bound noisy node logs to avoid journald memory/disk spikes.
LogRateLimitIntervalSec=30s
LogRateLimitBurst=200
`
	_ = os.WriteFile(filepath.Join(dir, "90-network-panel-log-limit.conf"), []byte(content), 0644)
}

func ensureNodeJournalLimits() {
	if _, err := exec.LookPath("journalctl"); err == nil {
		_ = exec.Command("journalctl", "--vacuum-size=200M").Run()
		_ = exec.Command("journalctl", "--vacuum-time=7d").Run()
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return
	}
	ensureServiceLogDropIn("flux-agent")
	ensureServiceLogDropIn("flux-agent2")
	ensureServiceLogDropIn("gost")
	_ = exec.Command("systemctl", "daemon-reload").Run()
}

// ---- Generic Ops ----

func runScript(req map[string]any) map[string]any {
	// fields: content|string optional, url|string optional, timeoutSec|number
	content, _ := req["content"].(string)
	urlStr, _ := req["url"].(string)
	timeoutSec := 300
	if v, ok := req["timeoutSec"].(float64); ok && int(v) > 0 {
		timeoutSec = int(v)
	}
	var scriptPath string
	var err error
	if content != "" {
		f, e := os.CreateTemp("", "np_run_*.sh")
		if e != nil {
			return map[string]any{"success": false, "message": e.Error()}
		}
		defer os.Remove(f.Name())
		_, _ = f.WriteString(content)
		f.Close()
		_ = os.Chmod(f.Name(), 0755)
		scriptPath = f.Name()
		log.Printf("{\"event\":\"run_script_prepare\",\"mode\":\"content\",\"path\":%q,\"contentSample\":%q}", scriptPath, firstN(content, 120))
	} else if urlStr != "" {
		f, e := os.CreateTemp("", "np_run_*.sh")
		if e != nil {
			return map[string]any{"success": false, "message": e.Error()}
		}
		f.Close()
		scriptPath = f.Name()
		if err = download(urlStr, scriptPath); err != nil {
			return map[string]any{"success": false, "message": err.Error()}
		}
		_ = os.Chmod(scriptPath, 0755)
		log.Printf("{\"event\":\"run_script_prepare\",\"mode\":\"url\",\"url\":%q,\"path\":%q}", urlStr, scriptPath)
	} else {
		return map[string]any{"success": false, "message": "no script content or url"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	cmdPath := "/bin/sh"
	if hasShebang(scriptPath) {
		cmdPath = scriptPath
	}
	var cmd *exec.Cmd
	if cmdPath == scriptPath {
		cmd = exec.CommandContext(ctx, cmdPath)
	} else {
		cmd = exec.CommandContext(ctx, cmdPath, scriptPath)
	}
	log.Printf("{\"event\":\"run_script_exec\",\"cmd\":[%q,%q],\"timeoutSec\":%d}", cmdPath, scriptPath, timeoutSec)
	out, e := runCommandCombinedLimited(cmd, runScriptOutputMaxBytes())
	if ctx.Err() == context.DeadlineExceeded {
		return map[string]any{"success": false, "message": "timeout"}
	}
	if e != nil {
		return map[string]any{"success": false, "message": e.Error(), "stderr": out}
	}
	return map[string]any{"success": true, "message": "ok", "stdout": out}
}

// runStreamScript executes a script and streams stdout/stderr chunks to endpoint every ~3s
func runStreamScript(reqID, content, urlStr, endpoint, secret, kind string) {
	if endpoint == "" || secret == "" {
		log.Printf("{\"event\":\"run_stream_script_error\",\"msg\":\"missing endpoint/secret\"}")
		return
	}
	if content == "" && urlStr != "" {
		if b, err := fetchURL(urlStr); err == nil {
			content = string(b)
		}
	}
	if content == "" {
		log.Printf("{\"event\":\"run_stream_script_error\",\"msg\":\"empty content\"}")
		return
	}
	tmp, err := os.CreateTemp("", "np_run_stream_*.sh")
	if err != nil {
		return
	}
	_ = os.Chmod(tmp.Name(), 0o755)
	tmp.WriteString(content)
	tmp.Close()
	defer os.Remove(tmp.Name())

	cmdPath := "/bin/bash"
	if _, err := os.Stat(cmdPath); err != nil {
		cmdPath = "/bin/sh"
	}
	cmd := exec.Command(cmdPath, tmp.Name())
	streamCmd(cmd, endpoint, secret, reqID, kind)
}

func streamCmd(cmd *exec.Cmd, endpoint, secret, reqID, kind string) {
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		postStreamChunk(endpoint, secret, reqID, kind, "start failed: "+err.Error(), true, nil)
		return
	}

	type chunk struct{ s string }
	ch := make(chan chunk, 128)
	wg := sync.WaitGroup{}
	readPipe := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 2048)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				ch <- chunk{s: string(buf[:n])}
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go readPipe(stdout)
	go readPipe(stderr)

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var buf strings.Builder
	flush := func(done bool, exitCode *int) {
		if buf.Len() == 0 && !done {
			return
		}
		postStreamChunk(endpoint, secret, reqID, kind, buf.String(), done, exitCode)
		buf.Reset()
	}

	for {
		select {
		case ck, ok := <-ch:
			if !ok {
				ch = nil
			} else {
				appendBoundedBuilderTail(&buf, ck.s, streamScriptChunkMaxBytes())
			}
		case <-ticker.C:
			flush(false, nil)
		case <-doneCh:
			flush(false, nil)
			err := cmd.Wait()
			exitCode := 0
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					exitCode = ee.ExitCode()
				} else {
					exitCode = 1
				}
			}
			flush(true, &exitCode)
			return
		}
	}
}

// Source: https://github.com/zhanghanyun/backtrace (v1.0.8), derived logic for traceroute output formatting.
func runBacktraceStream(reqID, endpoint, secret, kind string) {
	if endpoint == "" || secret == "" {
		log.Printf("{\"event\":\"backtrace_error\",\"msg\":\"missing endpoint/secret\"}")
		return
	}
	emit := func(s string, done bool, exitCode *int) {
		postStreamChunk(endpoint, secret, reqID, kind, s, done, exitCode)
	}
	emit("正在测试三网回程路由...\n", false, nil)

	type ipInfo struct {
		IP      string `json:"ip"`
		City    string `json:"city"`
		Region  string `json:"region"`
		Country string `json:"country"`
		Org     string `json:"org"`
	}
	if info, err := fetchIPInfo(); err == nil {
		line := fmt.Sprintf("国家: %s 城市: %s 服务商: %s\n", info.Country, info.City, info.Org)
		emit(line, false, nil)
	}

	ch := make(chan struct {
		i int
		s string
	}, len(backtraceIPs))
	for i := range backtraceIPs {
		go func(idx int) {
			ch <- struct {
				i int
				s string
			}{i: idx, s: backtraceOne(idx)}
		}(i)
	}

	results := make([]string, len(backtraceIPs))
	doneCount := 0
	timeout := time.After(12 * time.Second)
	for doneCount < len(backtraceIPs) {
		select {
		case r := <-ch:
			if results[r.i] == "" {
				results[r.i] = r.s
				emit(r.s+"\n", false, nil)
				doneCount++
			}
		case <-timeout:
			doneCount = len(backtraceIPs)
		}
	}
	for i := range results {
		if results[i] == "" {
			results[i] = fmt.Sprintf("%s %-15s %s", backtraceNames[i], backtraceIPs[i], "测试超时")
			emit(results[i]+"\n", false, nil)
		}
	}
	emit("测试完成!\n", true, intPtr(0))
}

func intPtr(v int) *int { return &v }

// Source: https://github.com/zhanghanyun/backtrace (v1.0.8)
var backtraceIPs = []string{
	"219.141.140.10", "202.106.195.68", "221.179.155.161", "202.96.209.133",
	"210.22.97.1", "211.136.112.200", "58.60.188.222", "210.21.196.6",
	"120.196.165.24", "61.139.2.69", "119.6.6.6", "211.137.96.205",
}

// Source: https://github.com/zhanghanyun/backtrace (v1.0.8)
var backtraceNames = []string{
	"北京电信", "北京联通", "北京移动", "上海电信", "上海联通", "上海移动",
	"广州电信", "广州联通", "广州移动", "成都电信", "成都联通", "成都移动",
}

// Source: https://github.com/zhanghanyun/backtrace (v1.0.8)
var backtraceASNMap = map[string]string{
	"AS4134":  "电信163  [普通线路]",
	"AS4809":  "电信CN2  [优质线路]",
	"AS4837":  "联通4837 [普通线路]",
	"AS9929":  "联通9929 [优质线路]",
	"AS58807": "移动CMIN2[优质线路]",
	"AS9808":  "移动CMI  [普通线路]",
	"AS58453": "移动CMI  [普通线路]",
}

func backtraceOne(i int) string {
	ip := net.ParseIP(backtraceIPs[i])
	if ip == nil {
		return fmt.Sprintf("%s %-15s %s", backtraceNames[i], backtraceIPs[i], "IP无效")
	}
	hops, err := bt.Trace(ip)
	if err != nil {
		return fmt.Sprintf("%s %-15s %v", backtraceNames[i], backtraceIPs[i], err)
	}
	for _, h := range hops {
		for _, n := range h.Nodes {
			asn := backtraceASN(n.IP.String())
			if asn == "" {
				continue
			}
			as := backtraceASNMap[asn]
			if as == "" {
				as = asn
			}
			return fmt.Sprintf("%s %-15s %s", backtraceNames[i], backtraceIPs[i], as)
		}
	}
	return fmt.Sprintf("%s %-15s %s", backtraceNames[i], backtraceIPs[i], "测试超时")
}

func backtraceASN(ip string) string {
	switch {
	case strings.HasPrefix(ip, "59.43"):
		return "AS4809"
	case strings.HasPrefix(ip, "202.97"):
		return "AS4134"
	case strings.HasPrefix(ip, "218.105") || strings.HasPrefix(ip, "210.51"):
		return "AS9929"
	case strings.HasPrefix(ip, "219.158"):
		return "AS4837"
	case strings.HasPrefix(ip, "223.120.19") || strings.HasPrefix(ip, "223.120.17") || strings.HasPrefix(ip, "223.120.16"):
		return "AS58807"
	case strings.HasPrefix(ip, "223.118") || strings.HasPrefix(ip, "223.119") || strings.HasPrefix(ip, "223.120") || strings.HasPrefix(ip, "223.121"):
		return "AS58453"
	default:
		return ""
	}
}

func fetchIPInfo() (*struct {
	Country string `json:"country"`
	City    string `json:"city"`
	Org     string `json:"org"`
}, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://ipinfo.io")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var info struct {
		Country string `json:"country"`
		City    string `json:"city"`
		Org     string `json:"org"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

func postStreamChunk(endpoint, secret, reqID, kind, chunk string, done bool, exitCode *int) {
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	body := map[string]any{
		"secret":    secret,
		"requestId": reqID,
		"type":      kind,
		"chunk":     chunk,
		"done":      done,
		"timeMs":    time.Now().UnixMilli(),
	}
	if exitCode != nil {
		body["exitCode"] = *exitCode
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func writeFileOp(req map[string]any) map[string]any {
	path, _ := req["path"].(string)
	content, _ := req["content"].(string)
	if path == "" {
		return map[string]any{"success": false, "message": "empty path"}
	}
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0755)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return map[string]any{"success": false, "message": err.Error()}
	}
	log.Printf("{\"event\":\"write_file_done\",\"path\":%q,\"bytes\":%d,\"contentSample\":%q}", path, len(content), firstN(content, 120))
	return map[string]any{"success": true, "message": "ok"}
}

func tryStopService(name string) bool {
	if _, err := exec.LookPath("systemctl"); err == nil {
		if e := exec.Command("systemctl", "stop", name).Run(); e == nil {
			return true
		}
	}
	if _, err := exec.LookPath("service"); err == nil {
		if e := exec.Command("service", name, "stop").Run(); e == nil {
			return true
		}
	}
	return false
}

func firstN(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func runScriptOutputMaxBytes() int64 {
	if v := strings.TrimSpace(getenv("AGENT_RUN_SCRIPT_OUTPUT_MAX_BYTES", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return int64(n)
		}
	}
	return 128 * 1024
}

func runScriptURLMaxBytes() int64 {
	if v := strings.TrimSpace(getenv("AGENT_RUN_SCRIPT_URL_MAX_BYTES", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return int64(n)
		}
	}
	return 512 * 1024
}

func streamScriptChunkMaxBytes() int {
	if v := strings.TrimSpace(getenv("AGENT_STREAM_SCRIPT_CHUNK_MAX_BYTES", "")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 64 * 1024
}

func appendBoundedBuilderTail(b *strings.Builder, s string, max int) {
	if max <= 0 {
		return
	}
	if b.Len()+len(s) <= max {
		b.WriteString(s)
		return
	}
	marker := fmt.Sprintf("\n[truncated: stream chunk kept last %d bytes]\n", max)
	keep := max - len(marker)
	if keep <= 0 {
		b.Reset()
		b.WriteString(firstN(marker, max))
		return
	}
	combined := b.String() + s
	if len(combined) > keep {
		combined = combined[len(combined)-keep:]
	}
	b.Reset()
	b.WriteString(marker)
	b.WriteString(combined)
}

func hasShebang(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	r := bufio.NewReader(f)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return false
	}
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "#!")
}

// ---- interactive shell support ----

func startShellSession(sessionID string, rows, cols int, c *websocket.Conn) {
	if sessionID == "" {
		sessionID = "default"
	}
	if rows <= 0 {
		rows = defaultRows
	}
	if cols <= 0 {
		cols = defaultCols
	}
	shellMu.Lock()
	defer shellMu.Unlock()
	if activeShell != nil && !activeShell.closed {
		// already running; just send ready
		_ = wsWriteJSON(c, map[string]any{"type": "ShellReady", "sessionId": activeShell.id})
		return
	}
	cmd := exec.Command("/bin/bash", "--login")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "PS1=\\u@\\h:\\w$ ")
	ws := &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}
	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		_ = wsWriteJSON(c, map[string]any{"type": "ShellExit", "sessionId": sessionID, "code": -1, "message": err.Error()})
		return
	}
	sess := &shellSession{id: sessionID, cmd: cmd, ptmx: ptmx}
	activeShell = sess

	// reader goroutine
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				sess.append(chunk)
				_ = wsWriteJSON(c, map[string]any{"type": "ShellData", "sessionId": sessionID, "data": chunk, "timeMs": time.Now().UnixMilli()})
			}
			if err != nil {
				break
			}
		}
		waitErr := cmd.Wait()
		code := exitCodeFromError(waitErr)
		sess.markClosed()
		_ = wsWriteJSON(c, map[string]any{"type": "ShellExit", "sessionId": sessionID, "code": code})
		shellMu.Lock()
		if activeShell == sess {
			activeShell = nil
		}
		shellMu.Unlock()
	}()

	_ = wsWriteJSON(c, map[string]any{"type": "ShellReady", "sessionId": sessionID})
}

func shellInput(sessionID, data string, c *websocket.Conn) {
	shellMu.Lock()
	sess := activeShell
	shellMu.Unlock()
	if sess == nil || sess.closed || (sessionID != "" && sess.id != sessionID) {
		_ = wsWriteJSON(c, map[string]any{"type": "ShellExit", "sessionId": sessionID, "code": -1, "message": "session not running"})
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closed || sess.ptmx == nil {
		return
	}
	_, _ = sess.ptmx.Write([]byte(data))
}

func shellResize(sessionID string, rows, cols int, c *websocket.Conn) {
	shellMu.Lock()
	sess := activeShell
	shellMu.Unlock()
	if sess == nil || sess.closed || (sessionID != "" && sess.id != sessionID) {
		return
	}
	if rows <= 0 || cols <= 0 {
		return
	}
	if sess.ptmx != nil {
		_ = pty.Setsize(sess.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	}
}

func shellStop(sessionID string, c *websocket.Conn) {
	shellMu.Lock()
	sess := activeShell
	shellMu.Unlock()
	if sess == nil || sess.closed || (sessionID != "" && sess.id != sessionID) {
		return
	}
	sess.mu.Lock()
	if sess.closed {
		sess.mu.Unlock()
		return
	}
	sess.closed = true
	if sess.ptmx != nil {
		_ = sess.ptmx.Close()
	}
	_ = killProc(sess.cmd)
	sess.mu.Unlock()
	shellMu.Lock()
	if activeShell == sess {
		activeShell = nil
	}
	shellMu.Unlock()
	_ = wsWriteJSON(c, map[string]any{"type": "ShellExit", "sessionId": sess.id, "code": 0})
}

func killProc(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return terminateProcess(cmd.Process)
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func (s *shellSession) append(chunk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.buf.WriteString(chunk)
	// trim history if too large
	if s.buf.Len() > shellBufLimit {
		b := s.buf.Bytes()
		if len(b) > shellBufLimit {
			b = b[len(b)-shellBufLimit:]
		}
		s.buf.Reset()
		s.buf.Write(b)
	}
}

func (s *shellSession) markClosed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.ptmx != nil {
		_ = s.ptmx.Close()
	}
}

func getExpectedVersions(addr, scheme string) (agent1, agent2 string) {
	u := apiURL(scheme, addr, "/api/v1/version")
	req, _ := http.NewRequest("GET", u, nil)
	hc := &http.Client{Timeout: 6 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	var v struct {
		Code int               `json:"code"`
		Data map[string]string `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&v) != nil || v.Code != 0 {
		return "", ""
	}
	agent1 = v.Data["agent"]
	agent2 = v.Data["agent2"]
	return
}

func detectArch() string {
	out, _ := exec.Command("uname", "-m").Output()
	s := strings.TrimSpace(string(out))
	switch s {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "armv7l", "armv7":
		return "armv7"
	default:
		return "amd64"
	}
}

func download(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		// read small snippet for diagnostics
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("download failed: %s, body=%q", resp.Status, string(b))
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func downloadRetry(url, dest string, attempts int) error {
	var err error
	for i := 0; i < attempts; i++ {
		err = download(url, dest)
		if err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return err
}

func fetchURL(target string) ([]byte, error) {
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := client.Get(target)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %s body=%q", resp.Status, string(b))
	}
	maxBytes := runScriptURLMaxBytes()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("response too large: limit=%d bytes", maxBytes)
	}
	return b, nil
}

// validateBinary performs a minimal ELF validation and arch check to avoid
// accidentally writing HTML or truncated binaries during self-upgrade.
func validateBinary(path string, arch string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	// A typical Go static binary > 1MB; reject obviously small files.
	if fi.Size() < 1_000_000 {
		return fmt.Errorf("binary too small: %d bytes", fi.Size())
	}
	// Parse ELF and validate machine
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Use debug/elf
	ef, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("not an ELF executable: %v", err)
	}
	defer ef.Close()
	m := ef.FileHeader.Machine
	switch arch {
	case "amd64":
		if m != elf.EM_X86_64 {
			return fmt.Errorf("ELF machine mismatch: %v", m)
		}
	case "arm64":
		if m != elf.EM_AARCH64 {
			return fmt.Errorf("ELF machine mismatch: %v", m)
		}
	case "armv7":
		if m != elf.EM_ARM {
			return fmt.Errorf("ELF machine mismatch: %v", m)
		}
	}
	return nil
}

func validateBinaryRunnable(path string) error {
	runSmoke := func(bin string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "-v")
		out, err := runCommandCombinedLimited(cmd, 4*1024)
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("binary smoke test timeout")
		}
		if err != nil {
			msg := strings.TrimSpace(out)
			if len(msg) > 240 {
				msg = msg[:240]
			}
			return fmt.Errorf("binary smoke test failed: %v, output=%q", err, msg)
		}
		return nil
	}

	isExecPermissionErr := func(err error) bool {
		if err == nil {
			return false
		}
		s := strings.ToLower(err.Error())
		return strings.Contains(s, "permission denied") || strings.Contains(s, "operation not permitted")
	}

	// Some systems place /etc/gost on mount options that can block direct exec
	// for freshly written files. Retry smoke test from /tmp for compatibility.
	runFromTemp := func(src string) error {
		in, err := os.Open(src)
		if err != nil {
			return fmt.Errorf("open temp source failed: %w", err)
		}
		defer in.Close()

		f, err := os.CreateTemp("", "flux-agent-smoke-*")
		if err != nil {
			return fmt.Errorf("create temp binary failed: %w", err)
		}
		tmpPath := f.Name()
		defer func() {
			f.Close()
			_ = os.Remove(tmpPath)
		}()

		if _, err := io.Copy(f, in); err != nil {
			return fmt.Errorf("copy temp binary failed: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close temp binary failed: %w", err)
		}
		if err := os.Chmod(tmpPath, 0755); err != nil {
			return fmt.Errorf("chmod temp binary failed: %w", err)
		}
		if err := runSmoke(tmpPath); err != nil {
			return fmt.Errorf("temp smoke test failed: %w", err)
		}
		return nil
	}

	if err := runSmoke(path); err != nil {
		if !isExecPermissionErr(err) {
			return err
		}
		log.Printf("{\"event\":\"agent_upgrade_smoke_fallback\",\"reason\":\"permission\",\"path\":%q}", path)
		if err2 := runFromTemp(path); err2 != nil {
			return fmt.Errorf("%v; fallback-to-/tmp failed: %v", err, err2)
		}
	}
	return nil
}

func safeReplace(target, tmp string) error {
	// backup existing
	bak := target + ".bak"
	_ = os.Remove(bak)
	if _, err := os.Stat(target); err == nil {
		_ = os.Rename(target, bak)
	}
	if err := os.Rename(tmp, target); err != nil {
		// try restore
		_ = os.Rename(bak, target)
		return err
	}
	_ = os.Remove(bak)
	return nil
}

// --- self uninstall ---
func uninstallSelf() error {
	// Determine target binary and service name by role
	svc := "flux-agent"
	target := "/etc/gost/flux-agent"
	if isAgent2Binary() {
		svc = "flux-agent2"
		target = "/etc/gost/flux-agent2"
	}
	// stop and disable service if possible
	if _, err := exec.LookPath("systemctl"); err == nil {
		_ = exec.Command("systemctl", "stop", svc).Run()
		_ = exec.Command("systemctl", "disable", svc).Run()
		_ = os.Remove("/etc/systemd/system/" + svc + ".service")
		_ = exec.Command("systemctl", "daemon-reload").Run()
	} else if _, err := exec.LookPath("service"); err == nil {
		_ = exec.Command("service", svc, "stop").Run()
	}
	// remove service files even if managers unavailable
	_ = os.Remove("/etc/systemd/system/" + svc + ".service")
	_ = os.Remove("/etc/init.d/" + svc)
	// remove target binary and env file
	_ = os.Remove(target)
	if svc == "flux-agent" {
		_ = os.Remove("/etc/default/flux-agent")
	} else {
		_ = os.Remove("/etc/default/flux-agent2")
	}
	// attempt to remove current executable path as well
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe)
	}
	log.Printf("{\"event\":\"agent_uninstalled\",\"service\":%q}", svc)
	// exit process
	os.Exit(0)
	return nil
}

func restartGostService() error {
	if tryRestartService("gost") {
		log.Printf("{\"event\":\"gost_restarted\"}")
		return nil
	}
	log.Printf("{\"event\":\"gost_restart_failed\"}")
	return fmt.Errorf("restart gost failed")
}

// --- ensure gost.service stays running ---
// periodicEnsureGost removed as per requirement: no background restarts.

// isServiceActive checks if a service is active via systemctl/service.
// returns (active, known). known=false if neither manager exists or status unknown.
func isServiceActive(name string) (bool, bool) {
	if _, err := exec.LookPath("systemctl"); err == nil {
		// is-active --quiet exits 0 when active
		if err := exec.Command("systemctl", "is-active", "--quiet", name).Run(); err == nil {
			return true, true
		}
		// If systemctl can run, we consider it authoritative even if inactive
		return false, true
	}
	if _, err := exec.LookPath("service"); err == nil {
		// service <name> status returns 0 when running on many distros
		if err := exec.Command("service", name, "status").Run(); err == nil {
			return true, true
		}
		return false, true
	}
	return false, false
}
