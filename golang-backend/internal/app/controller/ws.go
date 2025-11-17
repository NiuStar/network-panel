package controller

import (
	"encoding/json"
	"log"
	"net/http"
	"reflect"
	"sync"
	"time"

	"fmt"
	"network-panel/golang-backend/internal/app/model"
	apputil "network-panel/golang-backend/internal/app/util"
	appver "network-panel/golang-backend/internal/app/version"
	dbpkg "network-panel/golang-backend/internal/db"
    "strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm/clause"
)

// jlog emits structured JSON logs for easier tracing
func jlog(m map[string]interface{}) {
	b, _ := json.Marshal(m)
	log.Print(string(b))
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// nodeConns stores active node websocket connections by node ID (support multiple conns per node)
type nodeConn struct {
    c   *websocket.Conn
    ver string
}

// adminClient wraps an admin websocket connection with a write mutex
// to ensure gorilla/websocket single-writer requirement.
type adminClient struct {
    c  *websocket.Conn
    mu sync.Mutex
}

// safeWriteMessage writes a text message to admin client with locking and deadline.
func (a *adminClient) safeWriteMessage(b []byte) error {
    a.mu.Lock()
    defer a.mu.Unlock()
    // Best-effort write deadline to avoid lingering stuck writes
    _ = a.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
    return a.c.WriteMessage(websocket.TextMessage, b)
}

// safePing sends a websocket Ping control frame with locking and deadline.
func (a *adminClient) safePing() error {
    a.mu.Lock()
    defer a.mu.Unlock()
    _ = a.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
    // per gorilla/websocket, WriteControl is safe with deadlines
    return a.c.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second))
}

var (
    nodeConnMu  sync.RWMutex
    nodeConns   = map[int64][]*nodeConn{}
    adminMu     sync.RWMutex
    // adminConns holds dashboard websocket clients with per-conn write lock
    adminConns  = map[*adminClient]struct{}{}
    diagMu      sync.Mutex
    diagWaiters = map[string]chan map[string]interface{}{}
    opMu       sync.Mutex
    opWaiters  = map[string]chan map[string]interface{}{}
    // latest health flags reported by agents
    healthMu   sync.RWMutex
    nodeHealth = map[int64]struct{GostAPI bool; GostRunning bool}{}
)

// GET /system-info?type=1&secret=...&version=...
// Minimal websocket endpoint to mark node online/offline and keep a connection for commands.
func SystemInfoWS(c *gin.Context) {
	secret := c.Query("secret")
	nodeType := c.Query("type")
    version := c.Query("version")
    role := c.Query("role") // agent1 or agent2 (optional)

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("websocket upgrade error: %v", err)
		return
	}

    // Admin monitor channel
    if nodeType == "0" {
        cli := &adminClient{c: conn}
        adminMu.Lock()
        adminConns[cli] = struct{}{}
        adminMu.Unlock()
        // Tighten idle detection with read deadline extended by Pong
        deadlineSec := 120
        _ = conn.SetReadDeadline(time.Now().Add(time.Duration(deadlineSec) * time.Second))
        conn.SetPongHandler(func(string) error {
            _ = conn.SetReadDeadline(time.Now().Add(time.Duration(deadlineSec) * time.Second))
            return nil
        })
        // Periodic server-initiated Ping to keep intermediaries alive
        go func(ac *adminClient) {
            ticker := time.NewTicker(30 * time.Second)
            defer ticker.Stop()
            for range ticker.C {
                if err := ac.safePing(); err != nil {
                    // drop and close; read-loop will exit as well
                    adminMu.Lock()
                    delete(adminConns, ac)
                    adminMu.Unlock()
                    _ = ac.c.Close()
                    return
                }
            }
        }(cli)
        // send initial snapshot: node online statuses + last sysinfo samples
        go func(ac *adminClient){
            // send current statuses
            var nodes []model.Node
            dbpkg.DB.Find(&nodes)
            for _, n := range nodes {
                b, _ := json.Marshal(map[string]interface{}{"id": n.ID, "type": "status", "data": ifThenBool(n.Status!=nil && *n.Status==1, 1, 0)})
                _ = ac.safeWriteMessage(b)
                // last sysinfo
                var s model.NodeSysInfo
                if err := dbpkg.DB.Where("node_id = ?", n.ID).Order("time_ms desc").First(&s).Error; err == nil && s.NodeID > 0 {
                    payload := map[string]interface{}{
                        "uptime": s.Uptime,
                        "bytes_received": s.BytesRx,
                        "bytes_transmitted": s.BytesTx,
                        "cpu_usage": s.CPU,
                        "memory_usage": s.Mem,
                    }
                    b2, _ := json.Marshal(map[string]interface{}{"id": n.ID, "type": "info", "data": payload})
                    _ = ac.safeWriteMessage(b2)
                }
            }
        }(cli)
        // keep read loop to detect close
        for {
            if _, _, err := conn.ReadMessage(); err != nil {
                adminMu.Lock()
                delete(adminConns, cli)
                adminMu.Unlock()
                conn.Close()
                return
            }
        }
    }

	// Node agent channel
	var node model.Node
	if err := dbpkg.DB.Where("secret = ?", secret).First(&node).Error; err == nil && nodeType == "1" {
		jlog(map[string]interface{}{"event": "node_connected", "nodeId": node.ID, "name": node.Name, "remote": c.Request.RemoteAddr, "version": version})
		s := 1
		node.Status = &s
		if version != "" {
			node.Version = version
		}
		_ = dbpkg.DB.Save(&node).Error
		// close an open disconnect log if any
		var lastLog model.NodeDisconnectLog
		if err := dbpkg.DB.Where("node_id = ? AND up_at_ms IS NULL", node.ID).Order("down_at_ms desc").First(&lastLog).Error; err == nil && lastLog.ID > 0 {
			now := time.Now().UnixMilli()
			dur := (now - lastLog.DownAtMs) / 1000
			lastLog.UpAtMs = &now
			lastLog.DurationS = &dur
			_ = dbpkg.DB.Save(&lastLog).Error
			// alert online with downtime info
			name := node.Name
			nid := node.ID
			_ = dbpkg.DB.Create(&model.Alert{TimeMs: now, Type: "online", NodeID: &nid, NodeName: &name, Message: "节点恢复上线，时长(s): " + fmt.Sprintf("%d", dur)}).Error
		}

		nodeConnMu.Lock()
		nodeConns[node.ID] = append(nodeConns[node.ID], &nodeConn{c: conn, ver: version})
		nodeConnMu.Unlock()
		// broadcast online status
		broadcastToAdmins(map[string]interface{}{"id": node.ID, "type": "status", "data": 1})

        // auto-upgrade agent if version mismatch (expected strictly follows backend version)
        sv := appver.Get()
        if strings.HasPrefix(sv, "server-") { sv = strings.TrimPrefix(sv, "server-") }
        expected := "go-agent-" + sv
        if role == "agent2" { expected = "go-agent2-" + sv }
        if version != "" && expected != "" && version != expected {
            jlog(map[string]interface{}{"event": "agent_upgrade_trigger", "nodeId": node.ID, "from": version, "to": expected, "role": role})
            _ = sendWSCommand(node.ID, "UpgradeAgent", map[string]any{"to": expected})
        }

        // Note: do not push or reapply services on reconnect.
        // Services are only applied when user saves a forward.
        // No restart here; Web API updates are live

		// read messages and forward system info
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				// connection closed; update connection set
				jlog(map[string]interface{}{"event": "node_disconnected", "nodeId": node.ID, "name": node.Name})
				nodeConnMu.Lock()
				// remove this specific connection
				list := nodeConns[node.ID]
				for i := range list {
					if list[i].c == conn {
						nodeConns[node.ID] = append(list[:i], list[i+1:]...)
						break
					}
				}
				if len(nodeConns[node.ID]) == 0 {
					delete(nodeConns, node.ID)
					s := 0
					node.Status = &s
					_ = dbpkg.DB.Save(&node).Error
				}
				offline := (len(nodeConns[node.ID]) == 0)
				nodeConnMu.Unlock()
				if offline {
					broadcastToAdmins(map[string]interface{}{"id": node.ID, "type": "status", "data": 0})
					// create disconnect log
					now := time.Now().UnixMilli()
					rec := model.NodeDisconnectLog{NodeID: node.ID, DownAtMs: now}
					_ = dbpkg.DB.Create(&rec).Error
					go notifyCallback("agent_offline", node, map[string]any{"downAtMs": now})
					// alert record
					name := node.Name
					nid := node.ID
					_ = dbpkg.DB.Create(&model.Alert{TimeMs: now, Type: "offline", NodeID: &nid, NodeName: &name, Message: "节点离线"}).Error
				}
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					log.Printf("ws closed: %v", err)
				}
				conn.Close()
				return
			}
			if mt != websocket.TextMessage && mt != websocket.BinaryMessage {
				continue
			}
			// Try to parse as command reply first
			var generic map[string]interface{}
            if err := json.Unmarshal(msg, &generic); err == nil {
                if t, ok := generic["type"].(string); ok && (t == "DiagnoseResult" || t == "QueryServicesResult" || t == "SuggestPortsResult" || t == "ProbePortResult") {
                    if reqID, ok := generic["requestId"].(string); ok {
                        diagMu.Lock()
                        ch := diagWaiters[reqID]
                        delete(diagWaiters, reqID)
                        diagMu.Unlock()
                        if ch != nil {
                            // pass full payload data back
                            select {
                            case ch <- generic:
                            default:
                            }
                            close(ch)
                            continue
                        }
                    }
                } else if ok && (t == "RunScriptResult" || t == "WriteFileResult" || t == "RestartServiceResult" || t == "StopServiceResult") {
                    if reqID, ok := generic["requestId"].(string); ok {
                        opMu.Lock()
                        ch := opWaiters[reqID]
                        delete(opWaiters, reqID)
                        opMu.Unlock()
                        // persist to node_op_log
                        var nid int64 = node.ID
                        okFlag := 0
                        if data, _ := generic["data"].(map[string]interface{}); data != nil {
                            if s, _ := data["success"].(bool); s { okFlag = 1 }
                            msg, _ := data["message"].(string)
                            so, _ := data["stdout"].(string)
                            se, _ := data["stderr"].(string)
                            var soPtr, sePtr *string
                            if so != "" { soPtr = &so }
                            if se != "" { sePtr = &se }
                            _ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nid, Cmd: t, RequestID: reqID, Success: okFlag, Message: msg, Stdout: soPtr, Stderr: sePtr}).Error
                        } else {
                            _ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nid, Cmd: t, RequestID: reqID, Success: okFlag, Message: "no data"}).Error
                        }
                        if ch != nil {
                            select { case ch <- generic: default: }
                            close(ch)
                            continue
                        }
                    }
                } else if ok && t == "OpLog" {
                    // Generic operation progress log from agent; persist for UI visibility
                    var nid int64 = node.ID
                    step, _ := generic["step"].(string)
                    msg := ""
                    if m, _ := generic["message"].(string); m != "" { msg = m }
                    if data, _ := generic["data"].(map[string]interface{}); data != nil {
                        if s, _ := data["message"].(string); s != "" { msg = s }
                    }
                    _ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nid, Cmd: "OpLog:" + step, RequestID: "", Success: 1, Message: msg}).Error
                } else {
                    // Other JSON payload received (debug)
                    jlog(map[string]interface{}{"event": "node_unknown_json", "nodeId": node.ID, "payload": string(msg)})
                }
            }
			// Else treat as system info payload
			payload := parseNodeSystemInfo(node.Secret, msg)
            if payload != nil {
                // store into DB for long-term charts
                storeSysInfoSample(node.ID, payload)
                // update in-memory health flags for NodeList aggregation
                if v, ok := payload["gost_api"]; ok {
                    b := false; if bb, ok2 := v.(bool); ok2 { b = bb }
                    healthMu.Lock(); h := nodeHealth[node.ID]; h.GostAPI = b; nodeHealth[node.ID] = h; healthMu.Unlock()
                }
                if v, ok := payload["gost_running"]; ok {
                    b := false; if bb, ok2 := v.(bool); ok2 { b = bb }
                    healthMu.Lock(); h := nodeHealth[node.ID]; h.GostRunning = b; nodeHealth[node.ID] = h; healthMu.Unlock()
                }
                broadcastToAdmins(map[string]interface{}{"id": node.ID, "type": "info", "data": payload})
            } else {
                jlog(map[string]interface{}{"event": "node_non_json", "nodeId": node.ID, "len": len(msg)})
            }
		}
	} else {
		// unknown node; just close
		jlog(map[string]interface{}{"event": "node_rejected", "remote": c.Request.RemoteAddr, "secret": maskSecret(secret)})
		conn.Close()
	}
}

// sendWSCommand sends a command to a node by ID: {type: ..., data: ...}
func sendWSCommand(nodeID int64, cmdType string, data interface{}) error {
	nodeConnMu.RLock()
	list := append([]*nodeConn(nil), nodeConns[nodeID]...)
	nodeConnMu.RUnlock()
	if len(list) == 0 {
		return fmt.Errorf("node %d not connected", nodeID)
	}
	msg := make(map[string]interface{})
	kind := reflect.TypeOf(data).Kind()
	if kind != reflect.String && kind != reflect.Slice {
		dataBytes, _ := json.Marshal(data)
		msg = map[string]interface{}{"type": cmdType, "data": json.RawMessage(dataBytes)}
		jlog(map[string]interface{}{"event": "测试输出", "msg": msg})
	} else {
		msg = map[string]interface{}{"type": cmdType, "data": data}
	}
	b, _ := json.Marshal(msg)

	// Diagnose: target only agent (or any single fallback)
	if cmdType == "Diagnose" {
		var target *nodeConn
		for i := range list {
			if list[i].ver != "" && strings.Contains(list[i].ver, "agent") {
				target = list[i]
				break
			}
		}
		if target == nil {
			target = list[len(list)-1]
		}
		jlog(map[string]interface{}{"event": "ws_send", "cmd": cmdType, "nodeId": nodeID, "version": target.ver, "payload": string(b)})
		return target.c.WriteMessage(websocket.TextMessage, b)
	}

	// Service mutations: broadcast to all connections for reliability
	var writeErr error
	okCount := 0
	for _, nc := range list {
		if nc == nil || nc.c == nil {
			continue
		}
		if err := nc.c.WriteMessage(websocket.TextMessage, b); err != nil {
			writeErr = err
			jlog(map[string]interface{}{"event": "ws_send_err", "cmd": cmdType, "nodeId": nodeID, "version": nc.ver, "error": err.Error()})
			continue
		}
		okCount++
		// include payload for debugging unknown_msg at agent side
		jlog(map[string]interface{}{"event": "ws_send", "cmd": cmdType, "nodeId": nodeID, "version": nc.ver, "payload": string(b)})
	}
	if okCount == 0 && writeErr != nil {
		return writeErr
	}
	return nil
}

// notifyCallback sends a simple callback to configured URL on events (GET or POST)
func notifyCallback(event string, node model.Node, extra map[string]any) {
	// read from vite_config
	var urlC, methodC, hdrC, bodyTpl model.ViteConfig
	dbpkg.DB.Where("name = ?", "callback_url").First(&urlC)
	if urlC.Value == "" {
		return
	}
	dbpkg.DB.Where("name = ?", "callback_method").First(&methodC)
	dbpkg.DB.Where("name = ?", "callback_headers").First(&hdrC)
	dbpkg.DB.Where("name = ?", "callback_template").First(&bodyTpl)

	method := strings.ToUpper(methodC.Value)
	if method != "GET" && method != "POST" {
		method = "POST"
	}
	headers := map[string]string{}
	if hdrC.Value != "" {
		var m map[string]string
		if json.Unmarshal([]byte(hdrC.Value), &m) == nil {
			headers = m
		}
	}
	payload := map[string]any{"event": event, "nodeId": node.ID, "name": node.Name, "time": time.Now().UnixMilli()}
	for k, v := range extra {
		payload[k] = v
	}
	b, _ := json.Marshal(payload)

	// apply template helpers
	apply := func(s string) string {
		if s == "" {
			return s
		}
		out := s
		repl := map[string]string{
			"{event}":  event,
			"{nodeId}": fmt.Sprintf("%d", node.ID),
			"{name}":   node.Name,
			"{time}":   fmt.Sprintf("%d", payload["time"]),
		}
		if v, ok := extra["downAtMs"]; ok {
			repl["{downAt}"] = fmt.Sprintf("%v", v)
		}
		if v, ok := extra["upAtMs"]; ok {
			repl["{upAt}"] = fmt.Sprintf("%v", v)
		}
		if v, ok := extra["durationS"]; ok {
			repl["{duration}"] = fmt.Sprintf("%v", v)
		}
		for k, v := range repl {
			out = strings.ReplaceAll(out, k, v)
		}
		return out
	}

	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		u := urlC.Value
		// If template provided, apply to query/body
		if bodyTpl.Value != "" {
			t := apply(bodyTpl.Value)
			if method == "GET" {
				if strings.Contains(u, "?") {
					u = u + "&" + t
				} else {
					u = u + "?" + t
				}
			} else {
				b = []byte(t)
			}
		}
		if method == "GET" {
			req, _ := http.NewRequest("GET", u, nil)
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			_, _ = client.Do(req)
			return
		}
		req, _ := http.NewRequest("POST", u, strings.NewReader(string(b)))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
		_, _ = client.Do(req)
	}()
}

// TriggerCallback exposes the callback hook to other packages (e.g., scheduler)
func TriggerCallback(event string, node model.Node, extra map[string]any) {
	notifyCallback(event, node, extra)
}

// (no-op helpers removed)

// RequestDiagnose sends a Diagnose command to a node and waits for a reply with the same requestId.
// Returns the parsed result map and a boolean indicating if it was received in time.
func RequestDiagnose(nodeID int64, payload map[string]interface{}, timeout time.Duration) (map[string]interface{}, bool) {
	reqID := payload["requestId"].(string)
	ch := make(chan map[string]interface{}, 1)
	diagMu.Lock()
	diagWaiters[reqID] = ch
	diagMu.Unlock()
	// wait
	select {
	case res := <-ch:
		b, _ := json.Marshal(res)
		jlog(map[string]interface{}{"event": "diagnose_recv", "nodeId": nodeID, "payload": string(b)})
		return res, true
	case <-time.After(timeout):
		diagMu.Lock()
		delete(diagWaiters, reqID)
		diagMu.Unlock()
		jlog(map[string]interface{}{"event": "diagnose_timeout", "nodeId": nodeID, "reqId": reqID, "timeoutMs": timeout.Milliseconds()})
		return nil, false
	}
}

// RequestOp waits for a generic operation result from node (RunScript/WriteFile/RestartService/StopService)
func RequestOp(nodeID int64, cmd string, data map[string]interface{}, timeout time.Duration) (map[string]interface{}, bool) {
    reqID, _ := data["requestId"].(string)
    if reqID == "" { reqID = RandUUID(); data["requestId"] = reqID }
    // log sending summary (truncate large fields)
    sum := func(k string) string { if v, ok := data[k].(string); ok { if len(v) > 200 { return v[:200] } ; return v } ; return "" }
    jlog(map[string]interface{}{"event":"op_send","nodeId":nodeID,"cmd":cmd,"requestId":reqID,"contentSample":sum("content"),"path":data["path"],"name":data["name"],"url":data["url"]})
    if err := sendWSCommand(nodeID, cmd, data); err != nil { return nil, false }
    // persist op_send to NodeOpLog for front-end visibility
    var msg string
    if v, ok := data["path"].(string); ok && v != "" { msg += " path=" + v }
    if v, ok := data["name"].(string); ok && v != "" { msg += " name=" + v }
    if v, ok := data["url"].(string); ok && v != "" { msg += " url=" + v }
    if v, ok := data["content"].(string); ok && v != "" { msg += " content=" + v }
    _ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nodeID, Cmd: cmd + "-send", RequestID: reqID, Success: 1, Message: strings.TrimSpace(msg)}).Error
    ch := make(chan map[string]interface{}, 1)
    opMu.Lock(); opWaiters[reqID] = ch; opMu.Unlock()
    defer func(){ opMu.Lock(); delete(opWaiters, reqID); opMu.Unlock() }()
    select {
    case res := <-ch:
        jlog(map[string]interface{}{"event":"op_recv","nodeId":nodeID,"cmd":cmd,"requestId":reqID,"data":res["data"]})
        // persist op_recv summary
        var okFlag int
        var msg string
        if data, _ := res["data"].(map[string]interface{}); data != nil {
            if s, _ := data["success"].(bool); s { okFlag = 1 }
            msg, _ = data["message"].(string)
        }
        _ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nodeID, Cmd: cmd + "-recv", RequestID: reqID, Success: okFlag, Message: msg}).Error
        return res, true
    case <-time.After(timeout):
        return nil, false
    }
}

// broadcastToAdmins sends a JSON message to all admin monitor connections.
func broadcastToAdmins(v interface{}) {
    b, _ := json.Marshal(v)
    // copy snapshot of clients under read lock
    adminMu.RLock()
    clients := make([]*adminClient, 0, len(adminConns))
    for c := range adminConns { clients = append(clients, c) }
    adminMu.RUnlock()
    // write sequentially; drop broken ones
    var toDrop []*adminClient
    for _, ac := range clients {
        if err := ac.safeWriteMessage(b); err != nil {
            toDrop = append(toDrop, ac)
        }
    }
    if len(toDrop) > 0 {
        adminMu.Lock()
        for _, ac := range toDrop {
            delete(adminConns, ac)
            _ = ac.c.Close()
        }
        adminMu.Unlock()
    }
}

// parseNodeSystemInfo handles plain or AES-wrapped system info from node and converts keys.
func parseNodeSystemInfo(secret string, msg []byte) map[string]interface{} {
	// Try to detect wrapper {encrypted:true, data:"..."}
	var wrapper struct {
		Encrypted bool   `json:"encrypted"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(msg, &wrapper); err == nil && wrapper.Encrypted && wrapper.Data != "" {
		// decrypt
		if plain, err := apputil.AESDecrypt(secret, wrapper.Data); err == nil {
			return convertSysInfoJSON(plain)
		}
		return nil
	}
	// else assume msg is JSON object with camelCase keys
    return convertSysInfoJSON(msg)
}

// small helper: ternary for ints used in initial snapshot
func ifThenBool(cond bool, a int, b int) int { if cond { return a } ; return b }

func convertSysInfoJSON(b []byte) map[string]interface{} {
    var in map[string]interface{}
    if err := json.Unmarshal(b, &in); err != nil {
        return nil
    }
    // unwrap common wrappers: {event:"sysinfo_report", payload:{...}}
    if v, ok := in["payload"]; ok {
        if m, ok2 := v.(map[string]interface{}); ok2 {
            in = m
        }
    }
    // map known fields to snake_case expected by frontend
    out := map[string]interface{}{}
    if v, ok := in["Uptime"]; ok {
        out["uptime"] = v
    } else if v, ok := in["uptime"]; ok {
		out["uptime"] = v
	}
	if v, ok := in["BytesReceived"]; ok {
		out["bytes_received"] = v
	} else if v, ok := in["bytes_received"]; ok {
		out["bytes_received"] = v
	}
	if v, ok := in["BytesTransmitted"]; ok {
		out["bytes_transmitted"] = v
	} else if v, ok := in["bytes_transmitted"]; ok {
		out["bytes_transmitted"] = v
	}
	if v, ok := in["CPUUsage"]; ok {
		out["cpu_usage"] = v
	} else if v, ok := in["cpu_usage"]; ok {
		out["cpu_usage"] = v
	}
    if v, ok := in["MemoryUsage"]; ok {
        out["memory_usage"] = v
    } else if v, ok := in["memory_usage"]; ok {
        out["memory_usage"] = v
    }
    // interfaces list (array of IP strings)
    if v, ok := in["Interfaces"]; ok {
        out["interfaces"] = v
    } else if v, ok := in["interfaces"]; ok {
        out["interfaces"] = v
    }
    // passthrough health flags: gost api & service status
    if v, ok := in["GostAPI"]; ok { out["gost_api"] = v } else if v, ok := in["gost_api"]; ok { out["gost_api"] = v }
    if v, ok := in["GostRunning"]; ok { out["gost_running"] = v } else if v, ok := in["gost_running"]; ok { out["gost_running"] = v }
    if v, ok := in["GostAPIConfigured"]; ok { out["gost_api_configured"] = v } else if v, ok := in["gost_api_configured"]; ok { out["gost_api_configured"] = v }
    return out
}

// storeSysInfoSample persists a sysinfo payload into node_sysinfo table
func storeSysInfoSample(nodeID int64, m map[string]interface{}) {
	// parse numbers safely
	toInt64 := func(v any) int64 {
		switch x := v.(type) {
		case float64:
			return int64(x)
		case float32:
			return int64(x)
		case int64:
			return x
		case int:
			return int64(x)
		case json.Number:
			if i, err := x.Int64(); err == nil {
				return i
			}
			if f, err := x.Float64(); err == nil {
				return int64(f)
			}
		}
		return 0
	}
	toFloat := func(v any) float64 {
		switch x := v.(type) {
		case float64:
			return x
		case float32:
			return float64(x)
		case int64:
			return float64(x)
		case int:
			return float64(x)
		case json.Number:
			if f, err := x.Float64(); err == nil {
				return f
			}
		}
		return 0
	}
	now := time.Now().UnixMilli()
	s := model.NodeSysInfo{
		NodeID:  nodeID,
		TimeMs:  now,
		Uptime:  toInt64(m["uptime"]),
		BytesRx: toInt64(m["bytes_received"]),
		BytesTx: toInt64(m["bytes_transmitted"]),
		CPU:     toFloat(m["cpu_usage"]),
		Mem:     toFloat(m["memory_usage"]),
	}
	_ = dbpkg.DB.Create(&s).Error
	// persist interfaces snapshot if provided
	if ifs, ok := m["interfaces"]; ok && ifs != nil {
		if b, err := json.Marshal(ifs); err == nil {
			s := string(b)
			rec := model.NodeRuntime{NodeID: nodeID, Interfaces: &s, UpdatedTime: now}
			// upsert by node_id
			_ = dbpkg.DB.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "node_id"}},
				DoUpdates: clause.Assignments(map[string]interface{}{"interfaces": s, "updated_time": now}),
			}).Create(&rec).Error
		}
	}
}

func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}
