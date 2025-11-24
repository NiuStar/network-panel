package controller

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"time"

	"fmt"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/app/util"
	apputil "network-panel/golang-backend/internal/app/util"
	appver "network-panel/golang-backend/internal/app/version"
	dbpkg "network-panel/golang-backend/internal/db"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
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

// terminal client per node (admin-only)
type terminalClient struct {
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
	nodeConnMu sync.RWMutex
	nodeConns  = map[int64][]*nodeConn{}
	adminMu    sync.RWMutex
	// adminConns holds dashboard websocket clients with per-conn write lock
	adminConns = map[*adminClient]struct{}{}
	// terminal sessions and clients per node
	termMu       sync.RWMutex
	termSessions = map[int64]*terminalSession{}
	termClients  = map[int64]map[*terminalClient]struct{}{}
	diagMu       sync.Mutex
	diagWaiters  = map[string]chan map[string]interface{}{}
	opMu         sync.Mutex
	opWaiters    = map[string]chan map[string]interface{}{}
	// latest health flags reported by agents
	healthMu   sync.RWMutex
	nodeHealth = map[int64]struct {
		GostAPI     bool
		GostRunning bool
	}{}
)

type terminalSession struct {
	history strings.Builder
	running bool
}

func (ts *terminalSession) append(chunk string) {
	ts.history.WriteString(chunk)
	// trim history if too large
	const limit = 256 * 1024
	if ts.history.Len() > limit {
		b := ts.history.String()
		if len(b) > limit {
			b = b[len(b)-limit:]
		}
		ts.history.Reset()
		ts.history.WriteString(b)
	}
}

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
		go func(ac *adminClient) {
			// send current statuses
			var nodes []model.Node
			dbpkg.DB.Find(&nodes)
			for _, n := range nodes {
				b, _ := json.Marshal(map[string]interface{}{"id": n.ID, "type": "status", "data": ifThenBool(n.Status != nil && *n.Status == 1, 1, 0)})
				_ = ac.safeWriteMessage(b)
				// last sysinfo
				var s model.NodeSysInfo
				if err := dbpkg.DB.Where("node_id = ?", n.ID).Order("time_ms desc").First(&s).Error; err == nil && s.NodeID > 0 {
					payload := map[string]interface{}{
						"uptime":            s.Uptime,
						"bytes_received":    s.BytesRx,
						"bytes_transmitted": s.BytesTx,
						"cpu_usage":         s.CPU,
						"memory_usage":      s.Mem,
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
			enqueueAlert(model.Alert{TimeMs: now, Type: "online", NodeID: &nid, NodeName: &name, Message: "节点恢复上线，时长(s): " + fmt.Sprintf("%d", dur)})
		}

		nodeConnMu.Lock()
		nodeConns[node.ID] = append(nodeConns[node.ID], &nodeConn{c: conn, ver: version})
		nodeConnMu.Unlock()
		// broadcast online status
		broadcastToAdmins(map[string]interface{}{"id": node.ID, "type": "status", "data": 1})

		// auto-upgrade agent if version mismatch (expected strictly follows backend version)
		sv := appver.Get()
		if strings.HasPrefix(sv, "server-") {
			sv = strings.TrimPrefix(sv, "server-")
		}
		expected := "go-agent-" + sv
		if role == "agent2" {
			expected = "go-agent2-" + sv
		}
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
					enqueueDisconnect(rec)
					go notifyCallback("agent_offline", node, map[string]any{"downAtMs": now})
					// alert record
					name := node.Name
					nid := node.ID
					enqueueAlert(model.Alert{TimeMs: now, Type: "offline", NodeID: &nid, NodeName: &name, Message: "节点离线"})
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
				if t, ok := generic["type"].(string); ok && (t == "ShellData" || t == "ShellExit" || t == "ShellReady") {
					sid, _ := generic["sessionId"].(string)
					switch t {
					case "ShellData":
						if data, _ := generic["data"].(string); data != "" {
							appendTermData(node.ID, data)
						}
					case "ShellReady":
						setTermRunning(node.ID, true)
						broadcastTerm(node.ID, map[string]any{"type": "ready", "sessionId": sid})
					case "ShellExit":
						setTermRunning(node.ID, false)
						broadcastTerm(node.ID, generic)
					}
					continue
				} else if t, ok := generic["type"].(string); ok && (t == "DiagnoseResult" || t == "QueryServicesResult" || t == "SuggestPortsResult" || t == "ProbePortResult") {
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
							if s, _ := data["success"].(bool); s {
								okFlag = 1
							}
							msg, _ := data["message"].(string)
							so, _ := data["stdout"].(string)
							se, _ := data["stderr"].(string)
							var soPtr, sePtr *string
							if so != "" {
								soPtr = &so
							}
							if se != "" {
								sePtr = &se
							}
							enqueueOpLog(model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nid, Cmd: t, RequestID: reqID, Success: okFlag, Message: msg, Stdout: soPtr, Stderr: sePtr})
						} else {
							enqueueOpLog(model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nid, Cmd: t, RequestID: reqID, Success: okFlag, Message: "no data"})
						}
						if ch != nil {
							select {
							case ch <- generic:
							default:
							}
							close(ch)
							continue
						}
					}
				} else if ok && t == "OpLog" {
					// Generic operation progress log from agent; persist for UI visibility
					var nid int64 = node.ID
					step, _ := generic["step"].(string)
					msg := ""
					if m, _ := generic["message"].(string); m != "" {
						msg = m
					}
					if data, _ := generic["data"].(map[string]interface{}); data != nil {
						if s, _ := data["message"].(string); s != "" {
							msg = s
						}
					}
					enqueueOpLog(model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nid, Cmd: "OpLog:" + step, RequestID: "", Success: 1, Message: msg})
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
					b := false
					if bb, ok2 := v.(bool); ok2 {
						b = bb
					}
					healthMu.Lock()
					h := nodeHealth[node.ID]
					h.GostAPI = b
					nodeHealth[node.ID] = h
					healthMu.Unlock()
				}
				if v, ok := payload["gost_running"]; ok {
					b := false
					if bb, ok2 := v.(bool); ok2 {
						b = bb
					}
					healthMu.Lock()
					h := nodeHealth[node.ID]
					h.GostRunning = b
					nodeHealth[node.ID] = h
					healthMu.Unlock()
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

// ----- terminal (interactive shell) support -----

func getOrCreateTermSession(nodeID int64) *terminalSession {
	termMu.Lock()
	defer termMu.Unlock()
	ts := termSessions[nodeID]
	if ts == nil {
		ts = &terminalSession{}
		termSessions[nodeID] = ts
	}
	return ts
}

func appendTermData(nodeID int64, chunk string) {
	ts := getOrCreateTermSession(nodeID)
	ts.append(chunk)
	broadcastTerm(nodeID, map[string]any{"type": "data", "data": chunk})
}

func broadcastTerm(nodeID int64, payload map[string]any) {
	b, _ := json.Marshal(payload)
	termMu.RLock()
	clients := termClients[nodeID]
	termMu.RUnlock()
	for cli := range clients {
		if cli == nil || cli.c == nil {
			continue
		}
		cli.mu.Lock()
		_ = cli.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err := cli.c.WriteMessage(websocket.TextMessage, b)
		cli.mu.Unlock()
		if err != nil {
			termMu.Lock()
			delete(clients, cli)
			termMu.Unlock()
			_ = cli.c.Close()
		}
	}
}

func setTermRunning(nodeID int64, running bool) {
	termMu.Lock()
	ts := termSessions[nodeID]
	if ts != nil {
		ts.running = running
	}
	termMu.Unlock()
}

func resetTermSession(nodeID int64) {
	termMu.Lock()
	delete(termSessions, nodeID)
	termMu.Unlock()
}

// NodeTerminalWS handles admin websocket to node shell.
// Messages from frontend:
//
//	{type:"start", rows, cols}
//	{type:"input", data}
//	{type:"resize", rows, cols}
//	{type:"stop"}
//
// Backend relays to agent via sendWSCommand Shell* commands.
func NodeTerminalWS(c *gin.Context) {
	token := strings.TrimSpace(c.GetHeader("Authorization"))
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	jlog(map[string]interface{}{"event": "NodeTerminalWS 1", "token": token})
	if token == "" {
		token = strings.TrimSpace(c.Query("token"))
	}
	jlog(map[string]interface{}{"event": "NodeTerminalWS 2", "token": token})

	if token == "" {
		token = strings.TrimSpace(c.GetHeader("token"))
	}
	jlog(map[string]interface{}{"event": "NodeTerminalWS 3", "token": token})

	valid := util.ValidateToken(token)
	roleID := util.GetRoleID(token)
	userID := util.GetUserID(token)
	jlog(map[string]interface{}{"event": "terminal_auth", "token_len": len(token), "valid": valid, "role": roleID, "user": userID})
	if !valid {
		jlog(map[string]interface{}{"event": "NodeTerminalWS not valid", "token": token})

		// 容忍部分代理无法携带 Authorization 的情况：仅校验 exp 和角色
		if !tokenAdminStillValid(token) || roleID != 0 {
			jlog(map[string]interface{}{"event": "terminal_auth_fail", "reason": "invalid_token_or_role", "role": roleID})
			c.JSON(http.StatusUnauthorized, response.ErrMsg("未登录或token无效"))
			return
		}
	} else if roleID != 0 {
		jlog(map[string]interface{}{"event": "terminal_auth_fail", "reason": "not_admin", "role": roleID})
		c.JSON(http.StatusForbidden, response.ErrMsg("仅管理员可用"))
		return
	}
	c.Set("user_id", userID)
	c.Set("role_id", roleID)
	idStr := c.Param("id")
	nid, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || nid <= 0 {
		c.JSON(http.StatusBadRequest, response.ErrMsg("无效节点"))
		return
	}
	// ensure node exists
	var node model.Node
	if dbpkg.DB.First(&node, nid).Error != nil {
		c.JSON(http.StatusBadRequest, response.ErrMsg("节点不存在"))
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	cli := &terminalClient{c: conn}
	termMu.Lock()
	if termClients[nid] == nil {
		termClients[nid] = map[*terminalClient]struct{}{}
	}
	termClients[nid][cli] = struct{}{}
	ts := termSessions[nid]
	termMu.Unlock()

	// send history if exists
	if ts != nil && ts.history.Len() > 0 {
		_ = cli.c.WriteJSON(map[string]any{"type": "history", "data": ts.history.String(), "running": ts.running})
	}

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var m map[string]any
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		t, _ := m["type"].(string)
		switch t {
		case "start":
			rows := intFrom(m["rows"], 24)
			cols := intFrom(m["cols"], 80)
			if err := sendWSCommand(nid, "ShellStart", map[string]any{"sessionId": "default", "rows": rows, "cols": cols}); err != nil {
				_ = conn.WriteJSON(map[string]any{"type": "error", "message": err.Error()})
			}
		case "input":
			data, _ := m["data"].(string)
			if data != "" {
				if err := sendWSCommand(nid, "ShellInput", map[string]any{"sessionId": "default", "data": data}); err != nil {
					_ = conn.WriteJSON(map[string]any{"type": "error", "message": err.Error()})
				}
			}
		case "resize":
			rows := intFrom(m["rows"], 0)
			cols := intFrom(m["cols"], 0)
			if rows > 0 && cols > 0 {
				_ = sendWSCommand(nid, "ShellResize", map[string]any{"sessionId": "default", "rows": rows, "cols": cols})
			}
		case "stop":
			_ = sendWSCommand(nid, "ShellStop", map[string]any{"sessionId": "default"})
			resetTermSession(nid)
			// notify client that history cleared
			_ = conn.WriteJSON(map[string]any{"type": "cleared"})
		}
	}
	// unregister
	termMu.Lock()
	if set, ok := termClients[nid]; ok {
		delete(set, cli)
		if len(set) == 0 {
			delete(termClients, nid)
		}
	}
	termMu.Unlock()
	_ = conn.Close()
}

func intFrom(v interface{}, def int) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		if i, err := strconv.Atoi(string(t)); err == nil {
			return i
		}
	}
	return def
}

// tokenAdminStillValid decodes payload without verifying signature; checks exp>now and role==admin.
func tokenAdminStillValid(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var p struct {
		Exp   int64 `json:"exp"`
		Role  int   `json:"role_id"`
		Role2 int   `json:"roleId"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return false
	}
	role := p.Role
	if role == 0 {
		// ok
	} else if p.Role2 == 0 {
		role = 0
	}
	if role != 0 {
		return false
	}
	if p.Exp <= time.Now().Unix() {
		return false
	}
	return true
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
	if reqID == "" {
		reqID = RandUUID()
		data["requestId"] = reqID
	}
	// log sending summary (truncate large fields)
	sum := func(k string) string {
		if v, ok := data[k].(string); ok {
			if len(v) > 200 {
				return v[:200]
			}
			return v
		}
		return ""
	}
	jlog(map[string]interface{}{"event": "op_send", "nodeId": nodeID, "cmd": cmd, "requestId": reqID, "contentSample": sum("content"), "path": data["path"], "name": data["name"], "url": data["url"]})
	if err := sendWSCommand(nodeID, cmd, data); err != nil {
		return nil, false
	}
	// persist op_send to NodeOpLog for front-end visibility (buffered)
	var msg string
	if v, ok := data["path"].(string); ok && v != "" {
		msg += " path=" + v
	}
	if v, ok := data["name"].(string); ok && v != "" {
		msg += " name=" + v
	}
	if v, ok := data["url"].(string); ok && v != "" {
		msg += " url=" + v
	}
	if v, ok := data["content"].(string); ok && v != "" {
		msg += " content=" + v
	}
	enqueueOpLog(model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nodeID, Cmd: cmd + "-send", RequestID: reqID, Success: 1, Message: strings.TrimSpace(msg)})
	ch := make(chan map[string]interface{}, 1)
	opMu.Lock()
	opWaiters[reqID] = ch
	opMu.Unlock()
	defer func() { opMu.Lock(); delete(opWaiters, reqID); opMu.Unlock() }()
	select {
	case res := <-ch:
		jlog(map[string]interface{}{"event": "op_recv", "nodeId": nodeID, "cmd": cmd, "requestId": reqID, "data": res["data"]})
		// persist op_recv summary (buffered)
		var okFlag int
		var msg string
		if data, _ := res["data"].(map[string]interface{}); data != nil {
			if s, _ := data["success"].(bool); s {
				okFlag = 1
			}
			msg, _ = data["message"].(string)
		}
		enqueueOpLog(model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: nodeID, Cmd: cmd + "-recv", RequestID: reqID, Success: okFlag, Message: msg})
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
	for c := range adminConns {
		clients = append(clients, c)
	}
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
func ifThenBool(cond bool, a int, b int) int {
	if cond {
		return a
	}
	return b
}

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
	if v, ok := in["GostAPI"]; ok {
		out["gost_api"] = v
	} else if v, ok := in["gost_api"]; ok {
		out["gost_api"] = v
	}
	if v, ok := in["GostRunning"]; ok {
		out["gost_running"] = v
	} else if v, ok := in["gost_running"]; ok {
		out["gost_running"] = v
	}
	if v, ok := in["GostAPIConfigured"]; ok {
		out["gost_api_configured"] = v
	} else if v, ok := in["gost_api_configured"]; ok {
		out["gost_api_configured"] = v
	}
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
	enqueueSysInfo(s)
	// persist interfaces snapshot if provided
	if ifs, ok := m["interfaces"]; ok && ifs != nil {
		if b, err := json.Marshal(ifs); err == nil {
			s := string(b)
			rec := model.NodeRuntime{NodeID: nodeID, Interfaces: &s, UpdatedTime: now}
			setRuntime(rec)
		}
	}
}

func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}
