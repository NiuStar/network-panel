package main

import (
	"bytes"
	"context"
	"crypto/tls"
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
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

// versionBase is the agent semantic version (without role prefix).
// final reported version is: go-agent-<versionBase> or go-agent2-<versionBase>
var versionBase = "1.0.6.3"
var version = "" // computed in main()

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

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func readPanelConfig() (addr, secret string) {
	// fallback to /etc/gost/config.json {addr, secret}
	f, err := os.ReadFile("/etc/gost/config.json")
	if err != nil {
		return "", ""
	}
	var m map[string]any
	if json.Unmarshal(f, &m) == nil {
		if v, ok := m["addr"].(string); ok {
			addr = v
		}
		if v, ok := m["secret"].(string); ok {
			secret = v
		}
	}
	return
}

func main() {
	var (
		flagAddr   = flag.String("a", "", "panel addr:port")
		flagSecret = flag.String("s", "", "node secret")
		flagScheme = flag.String("S", "", "ws or wss")
	)
	flag.Parse()

	addr := getenv("ADDR", *flagAddr)
	secret := getenv("SECRET", *flagSecret)
	scheme := getenv("SCHEME", *flagScheme)
	if scheme == "" {
		scheme = "ws"
	}
	if addr == "" || secret == "" {
		a2, s2 := readPanelConfig()
		if addr == "" {
			addr = a2
		}
		if secret == "" {
			secret = s2
		}
	}
	if addr == "" || secret == "" {
		log.Fatalf("missing ADDR/SECRET (env or flags) and /etc/gost/config.json fallback")
	}

	// compute version and role by binary name
	if isAgent2Binary() {
		version = "go-agent2-" + versionBase
	} else {
		version = "go-agent-" + versionBase
	}

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

	for {
		if err := runOnce(u.String(), addr, secret, scheme); err != nil {
			log.Printf("{\"event\":\"agent_error\",\"error\":%q}", err.Error())
		}
		time.Sleep(3 * time.Second)
	}
}

func runOnce(wsURL, addr, secret, scheme string) error {
	log.Printf("{\"event\":\"connecting\",\"url\":%q}", wsURL)
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	if strings.HasPrefix(wsURL, "wss://") {
		d.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	c, _, err := d.Dial(wsURL, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	log.Printf("{\"event\":\"connected\"}")

	// on connect reconcile & periodic reconcile
	go reconcile(addr, secret, scheme)
	go periodicReconcile(addr, secret, scheme)
	go periodicProbe(addr, secret, scheme)
	go periodicSystemInfo(c)
	go periodicEnsureGost()
	// after connect, cross-check counterpart agent
	go func() {
		// fetch expected versions
		a1, a2 := getExpectedVersions(addr, scheme)
		if isAgent2Binary() {
			// agent2 ensures agent1 up-to-date
			if a1 != "" {
				_ = upgradeAgent1(addr, scheme, a1)
			}
		} else {
			// agent1 ensures agent2 up-to-date
			if a2 != "" {
				_ = upgradeAgent2(addr, scheme, a2)
			}
		}
	}()

	// read loop
	c.SetReadLimit(1 << 20)
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.SetPongHandler(func(string) error { c.SetReadDeadline(time.Now().Add(60 * time.Second)); return nil })

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			_ = c.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second))
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
									log.Printf("{\"event\":\"unknown_msg\",\"error\":%q,\"payload\":%q}", e4.Error(), string(msg))
									continue
								}
							} else {
								log.Printf("{\"event\":\"unknown_msg\",\"error\":%q,\"payload\":%q}", e3.Error(), string(msg))
								continue
							}
						} else {
							log.Printf("{\"event\":\"unknown_msg\",\"error\":%q,\"payload\":%q}", e3.Error(), string(msg))
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
								log.Printf("{\"event\":\"unknown_msg\",\"error\":%q,\"payload\":%q}", err.Error(), string(msg))
								continue
							}
						} else {
							log.Printf("{\"event\":\"unknown_msg\",\"error\":%q,\"payload\":%q}", err.Error(), string(msg))
							continue
						}
					} else {
						log.Printf("{\"event\":\"unknown_msg\",\"error\":%q,\"payload\":%q}", err.Error(), string(msg))
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
			if err := json.Unmarshal(m.Data, &services); err != nil {
				log.Printf("{\"event\":\"svc_cmd_parse_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
				continue
			}
			if err := addOrUpdateServices(services, false); err != nil {
				log.Printf("{\"event\":\"svc_cmd_apply_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
			} else {
				log.Printf("{\"event\":\"svc_cmd_applied\",\"type\":%q,\"count\":%d}", m.Type, len(services))
			}
		case "UpdateService":
			var services []map[string]any
			if err := json.Unmarshal(m.Data, &services); err != nil {
				log.Printf("{\"event\":\"svc_cmd_parse_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
				continue
			}
			if err := addOrUpdateServices(services, true); err != nil {
				log.Printf("{\"event\":\"svc_cmd_apply_err\",\"type\":%q,\"error\":%q}", m.Type, err.Error())
			} else {
				log.Printf("{\"event\":\"svc_cmd_applied\",\"type\":%q,\"count\":%d}", m.Type, len(services))
			}
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
			_ = c.WriteJSON(out)
			log.Printf("{\"event\":\"send_qs_result\",\"count\":%d}", len(list))
		case "UpgradeAgent":
			// optional payload: {to: "go-agent-1.x.y"}
			go func() { _ = selfUpgrade(addr, scheme) }()
		case "UpgradeAgent1":
			go func() { _ = upgradeAgent1(addr, scheme, "") }()
		case "UpgradeAgent2":
			go func() { _ = upgradeAgent2(addr, scheme, "") }()
		case "RestartGost":
			go func() { _ = restartGostService() }()
		case "UninstallAgent":
			go func() {
				_ = uninstallSelf()
			}()
		case "RunScript":
			var req map[string]any
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				reqID, _ := req["requestId"].(string)
				content, _ := req["content"].(string)
				urlStr, _ := req["url"].(string)
				log.Printf("{\"event\":\"run_script_recv\",\"hasContent\":%t,\"contentLen\":%d,\"url\":%q}", content != "", len(content), urlStr)
				_ = c.WriteJSON(map[string]any{"type": "OpLog", "step": "run_script_recv", "message": fmt.Sprintf("RunScript recv hasContent=%t contentLen=%d url=%s content=%s", content != "", len(content), urlStr, content)})
				res := map[string]any{"type": "RunScriptResult", "requestId": reqID, "data": runScript(req)}
				_ = c.WriteJSON(res)
				if d, ok := res["data"].(map[string]any); ok {
					_ = c.WriteJSON(map[string]any{"type": "OpLog", "step": "run_script_done", "message": fmt.Sprintf("RunScript done success=%v message=%v stdout=%v stderr=%v", d["success"], d["message"], d["stdout"], d["stderr"])})
				}
			}()
		case "WriteFile":
			var req map[string]any
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				reqID, _ := req["requestId"].(string)
				path, _ := req["path"].(string)
				content, _ := req["content"].(string)
				log.Printf("{\"event\":\"write_file_recv\",\"path\":%q,\"contentLen\":%d}", path, len(content))
				_ = c.WriteJSON(map[string]any{"type": "OpLog", "step": "write_file_recv", "message": fmt.Sprintf("WriteFile recv path=%s bytes=%d content=%s", path, len(content), content)})
				res := map[string]any{"type": "WriteFileResult", "requestId": reqID, "data": writeFileOp(req)}
				_ = c.WriteJSON(res)
				if d, ok := res["data"].(map[string]any); ok {
					_ = c.WriteJSON(map[string]any{"type": "OpLog", "step": "write_file_done", "message": fmt.Sprintf("WriteFile done path=%s success=%v message=%v", path, d["success"], d["message"])})
				}
			}()
		case "RestartService":
			var req map[string]any
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				reqID, _ := req["requestId"].(string)
				name, _ := req["name"].(string)
				log.Printf("{\"event\":\"restart_service_recv\",\"name\":%q}", name)
				_ = c.WriteJSON(map[string]any{"type": "OpLog", "step": "restart_service_recv", "message": fmt.Sprintf("RestartService recv name=%s", name)})
				ok := tryRestartService(name)
				res := map[string]any{"type": "RestartServiceResult", "requestId": reqID, "data": map[string]any{"success": ok}}
				_ = c.WriteJSON(res)
				_ = c.WriteJSON(map[string]any{"type": "OpLog", "step": "restart_service_done", "message": fmt.Sprintf("RestartService done name=%s success=%v", name, ok)})
			}()
		case "StopService":
			var req map[string]any
			_ = json.Unmarshal(m.Data, &req)
			go func() {
				reqID, _ := req["requestId"].(string)
				name, _ := req["name"].(string)
				ok := tryStopService(name)
				res := map[string]any{"type": "StopServiceResult", "requestId": reqID, "data": map[string]any{"success": ok}}
				_ = c.WriteJSON(res)
			}()
		default:
			// ignore unknown
		}
	}
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

func periodicSystemInfo(c *websocket.Conn) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		rx, tx := netBytes()
		// gather interface list (best-effort)
		ifaces := getInterfaces()
		payload := map[string]any{
			"Uptime": uptimeSeconds(),
		}
		payload["BytesReceived"] = int64(rx)
		payload["BytesTransmitted"] = int64(tx)
		payload["CPUUsage"] = cpuUsagePercent()
		payload["MemoryUsage"] = memUsagePercent()
		if len(ifaces) > 0 {
			payload["Interfaces"] = ifaces
		}
		b, _ := json.Marshal(payload)
		log.Printf("{\"event\":\"sysinfo_report\",\"payload\":%s}", string(b))
		if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
			return
		}
		<-ticker.C
	}
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

func periodicReconcile(addr, secret, scheme string) {
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
	for range t.C {
		reconcile(addr, secret, scheme)
	}
}

func reconcile(addr, secret, scheme string) {
	// read local gost.json service names and panel-managed flag
	present := map[string]struct{}{}
	managed := map[string]bool{}
	if b, err := os.ReadFile(resolveGostConfigPathForRead()); err == nil {
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
	_ = c.WriteJSON(out)
	log.Printf("{\"event\":\"send_result\",\"requestId\":%q,\"data\":%s}", d.RequestID, string(mustJSON(resp)))
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// --- gost.json helpers ---
// prefer installed gost.json under /usr/local/gost, fallback to /etc/gost/gost.json
var gostConfigPathCandidates = []string{
	"/etc/gost/gost.json",
	"/usr/local/gost/gost.json",
	"./gost.json",
}

func resolveGostConfigPathForRead() string {
	for _, p := range gostConfigPathCandidates {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return p
		}
	}
	// default
	return "/etc/gost/gost.json"
}

func resolveGostConfigPathForWrite() string { return resolveGostConfigPathForRead() }

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

// queryServices returns a summary list of services, optionally filtered by handler type.
func queryServices(filter string) []map[string]any {
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

// addOrUpdateServices merges provided services into gost.json services array.
// If updateOnly is true, only update existing by name; otherwise upsert (add if missing).
func addOrUpdateServices(services []map[string]any, updateOnly bool) error {
	cfg := readGostConfig()
	// merge optional chains injected per-service under _chains (upsert by name)
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
		// fallback: if service references handler.chain but chain not present and no _chains provided, synthesize a simple chain
		if h, ok := svc["handler"].(map[string]any); ok {
			if cn, ok2 := h["chain"].(string); ok2 && cn != "" {
				if _, exists := chainIdx[cn]; !exists {
					// try to extract a node addr from forwarder.nodes[0]
					addr := ""
					if fwd, ok3 := svc["forwarder"].(map[string]any); ok3 {
						if nodes, ok4 := fwd["nodes"].([]any); ok4 && len(nodes) > 0 {
							if n0, ok5 := nodes[0].(map[string]any); ok5 {
								if a, ok6 := n0["addr"].(string); ok6 {
									addr = a
								}
							}
						}
					}
					if addr != "" {
						c := map[string]any{
							"name": cn,
							"hops": []any{map[string]any{"name": cn + "_hop", "nodes": []any{map[string]any{"name": "auto", "addr": addr}}}},
						}
						chainsAny = append(chainsAny, c)
						chainIdx[cn] = len(chainsAny) - 1
					}
				}
			}
		}
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
			// replace existing
			arrAny[i] = svc
		} else if !updateOnly {
			arrAny = append(arrAny, svc)
			idx[name] = len(arrAny) - 1
		}
	}
	cfg["services"] = arrAny
	return writeGostConfig(cfg)
}

func deleteServices(names []string) error {
	if len(names) == 0 {
		return nil
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
	out, err := exec.Command(cmdName, args...).CombinedOutput()
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
	_, err := exec.Command("iperf3", "-s", "-D", "-p", fmt.Sprintf("%d", port)).CombinedOutput()
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
	out, err := exec.Command("iperf3", args...).CombinedOutput()
	if err != nil {
		return 0
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
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
	out, err := exec.Command("iperf3", args...).CombinedOutput()
	if err != nil {
		msg := string(out)
		if len(msg) > 240 {
			msg = msg[:240]
		}
		if msg == "" {
			msg = err.Error()
		}
		return 0, msg
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		msg := string(out)
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
	hc := &http.Client{Timeout: 6 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out, nil
}

func apiURL(scheme, addr, path string) string {
	u := url.URL{Scheme: "http", Host: addr, Path: path}
	if scheme == "wss" {
		u.Scheme = "https"
	}
	return u.String()
}

func periodicProbe(addr, secret, scheme string) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		doProbeOnce(addr, secret, scheme)
		<-ticker.C
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
	if err := download(u, tmp); err != nil {
		log.Printf("upgrade download err: %v", err)
		return err
	}
	_ = os.Rename(tmp, target)
	_ = os.Chmod(target, 0755)
	// restart service or exec-replace
	if tryRestartService(svc) {
		log.Printf("{\"event\":\"agent_upgrade_done\",\"service\":%q}", svc)
		return nil
	}
	// fallback: exec replace self
	args := append([]string{target}, os.Args[1:]...)
	_ = syscall.Exec(target, args, os.Environ())
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
	if expected != "" {
		if b, err := os.ReadFile(verFile); err == nil && strings.TrimSpace(string(b)) == expected {
			return nil
		}
	}
	tmp := target + ".new"
	if err := download(u, tmp); err != nil {
		return err
	}
	_ = os.Rename(tmp, target)
	_ = os.Chmod(target, 0755)
	_ = os.WriteFile(verFile, []byte(expected), 0644)
	// ensure service exists and start
	ensureSystemdService("flux-agent", target)
	if !tryRestartService("flux-agent") {
		// best-effort start detached
		_ = exec.Command(target).Start()
	}
	return nil
}

// upgradeAgent2 ensures flux-agent2 is installed and (re)started to expected version if provided.
func upgradeAgent2(addr, scheme, expected string) error {
	arch := detectArch()
	u := apiURL(scheme, addr, "/flux-agent/"+"flux-agent2-linux-"+arch)
	target := "/etc/gost/flux-agent2"
	verFile := target + ".version"
	if expected != "" {
		if b, err := os.ReadFile(verFile); err == nil && strings.TrimSpace(string(b)) == expected {
			return nil
		}
	}
	tmp := target + ".new"
	if err := download(u, tmp); err != nil {
		return err
	}
	_ = os.Rename(tmp, target)
	_ = os.Chmod(target, 0755)
	_ = os.WriteFile(verFile, []byte(expected), 0644)
	ensureSystemdService("flux-agent2", target)
	if !tryRestartService("flux-agent2") {
		_ = exec.Command(target).Start()
	}
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
	_ = exec.Command("systemctl", "daemon-reload").Run()
	_ = exec.Command("systemctl", "enable", name).Run()
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
	cmd := exec.CommandContext(ctx, "/bin/sh", scriptPath)
	log.Printf("{\"event\":\"run_script_exec\",\"cmd\":[%q,%q],\"timeoutSec\":%d}", "/bin/sh", scriptPath, timeoutSec)
	out, e := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return map[string]any{"success": false, "message": "timeout"}
	}
	if e != nil {
		return map[string]any{"success": false, "message": e.Error(), "stderr": string(out)}
	}
	return map[string]any{"success": true, "message": "ok", "stdout": string(out)}
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
		// remove service file
		_ = os.Remove("/etc/systemd/system/" + svc + ".service")
		_ = exec.Command("systemctl", "daemon-reload").Run()
	} else if _, err := exec.LookPath("service"); err == nil {
		_ = exec.Command("service", svc, "stop").Run()
	}
	// remove target binary and env file
	_ = os.Remove(target)
	if svc == "flux-agent" {
		_ = os.Remove("/etc/default/flux-agent")
	} else {
		_ = os.Remove("/etc/default/flux-agent2")
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
func periodicEnsureGost() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		active, known := isServiceActive("gost")
		if !known {
			// service manager unavailable or service not installed; skip
			continue
		}
		if active {
			continue
		}
		// try restart when inactive
		if tryRestartService("gost") {
			log.Printf("{\"event\":\"gost_autorestart\",\"status\":\"restarted\"}")
		} else {
			log.Printf("{\"event\":\"gost_autorestart\",\"status\":\"failed\"}")
		}
	}
}

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
