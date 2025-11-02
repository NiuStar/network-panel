package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

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

	version := "go-agent-1.0.1"
	u := url.URL{Scheme: scheme, Host: addr, Path: "/system-info"}
	q := u.Query()
	q.Set("type", "1")
	q.Set("secret", secret)
	q.Set("version", version)
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
        default:
            // ignore unknown
        }
    }
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
                                if v, ok2 := meta["managedBy"].(string); ok2 && v == "flux-panel" {
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
			bw := runIperf3Client(d.Host, d.Port, d.Duration)
			ok := bw > 0
			resp = map[string]any{"success": ok, "bandwidthMbps": bw, "ctx": d.Ctx}
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
    if dir := strings.TrimSuffix(path, "/gost.json"); dir != "" { _ = os.MkdirAll(dir, 0755) }
    return os.WriteFile(path, b, 0600)
}

// queryServices returns a summary list of services, optionally filtered by handler type.
func queryServices(filter string) []map[string]any {
    cfg := readGostConfig()
    arrAny, _ := cfg["services"].([]any)
    out := make([]map[string]any, 0, len(arrAny))
    for _, it := range arrAny {
        m, ok := it.(map[string]any)
        if !ok { continue }
        name, _ := m["name"].(string)
        addr, _ := m["addr"].(string)
        handler, _ := m["handler"].(map[string]any)
        htype := ""
        if handler != nil { if v, ok := handler["type"].(string); ok { htype = v } }
        if filter != "" && strings.ToLower(htype) != strings.ToLower(filter) { continue }
        limiter, _ := m["limiter"].(string)
        rlimiter, _ := m["rlimiter"].(string)
        meta, _ := m["metadata"].(map[string]any)
        port := parsePort(addr)
        listening := false
        if port > 0 {
            listening = portListening(port)
        }
        out = append(out, map[string]any{
            "name": name,
            "addr": addr,
            "handler": htype,
            "port": port,
            "listening": listening,
            "limiter": limiter,
            "rlimiter": rlimiter,
            "metadata": meta,
        })
    }
    return out
}

func parsePort(addr string) int {
    if addr == "" { return 0 }
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
    if port <= 0 { return false }
    to := 200 * time.Millisecond
    // try ipv4 loopback
    c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), to)
    if err == nil { c.Close(); return true }
    // try ipv6 loopback
    c2, err2 := net.DialTimeout("tcp", fmt.Sprintf("[::1]:%d", port), to)
    if err2 == nil { c2.Close(); return true }
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
            if n, ok2 := m["name"].(string); ok2 && n != "" { chainIdx[n] = i }
        }
    }
    for _, svc := range services {
        if extra, ok := svc["_chains"]; ok {
            if arr, ok2 := extra.([]any); ok2 {
                for _, it := range arr {
                    if m, ok3 := it.(map[string]any); ok3 {
                        n, _ := m["name"].(string)
                        if n == "" { continue }
                        if i, ok4 := chainIdx[n]; ok4 { chainsAny[i] = m } else { chainsAny = append(chainsAny, m); chainIdx[n] = len(chainsAny)-1 }
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
                                if a, ok6 := n0["addr"].(string); ok6 { addr = a }
                            }
                        }
                    }
                    if addr != "" {
                        c := map[string]any{
                            "name": cn,
                            "hops": []any{ map[string]any{ "name": cn + "_hop", "nodes": []any{ map[string]any{"name":"auto","addr": addr} } } },
                        }
                        chainsAny = append(chainsAny, c)
                        chainIdx[cn] = len(chainsAny)-1
                    }
                }
            }
        }
    }
    if len(chainsAny) > 0 { cfg["chains"] = chainsAny }

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
	for i := 0; i < 20; i++ {
		p := 20000 + rand.Intn(20000)
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

func runIperf3Client(host string, port, duration int) float64 {
	if host == "" || port <= 0 {
		return 0
	}
	args := []string{"-J", "-R", "-c", host, "-p", fmt.Sprintf("%d", port), "-t", fmt.Sprintf("%d", duration)}
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
