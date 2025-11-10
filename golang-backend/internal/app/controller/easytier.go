package controller

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Keys in ViteConfig
const (
	etEnabledKey = "easytier_enabled"
	etSecretKey  = "easytier_secret"
	etMasterKey  = "easytier_master"
	etNodesKey   = "easytier_nodes"
)

type etMaster struct {
	NodeID int64  `json:"nodeId"`
	IP     string `json:"ip"`
	Port   int    `json:"port"`
}
type etNode struct {
	NodeID     int64   `json:"nodeId"`
	IP         string  `json:"ip"`
	Port       int     `json:"port"`
	PeerNodeID *int64  `json:"peerNodeId,omitempty"`
	IPv4       string  `json:"ipv4"`
	PeerIP     *string `json:"peerIp,omitempty"`
}

// guard to avoid duplicate EasyTier install scripts being sent concurrently
// key: nodeID; value true while an install is in-flight
var (
	etInstallMu  sync.Mutex
	etInstalling = map[int64]bool{}
)

func beginEtInstall(nodeID int64) bool {
	etInstallMu.Lock()
	defer etInstallMu.Unlock()
	if etInstalling[nodeID] {
		return false
	}
	etInstalling[nodeID] = true
	return true
}

func endEtInstall(nodeID int64) {
	etInstallMu.Lock()
	delete(etInstalling, nodeID)
	etInstallMu.Unlock()
}

func waitEtInstallFinish(nodeID int64, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		etInstallMu.Lock()
		busy := etInstalling[nodeID]
		etInstallMu.Unlock()
		if !busy {
			return
		}
		time.Sleep(1 * time.Second)
	}
}

func EasyTierStatus(c *gin.Context) {
	enabled := getCfg(etEnabledKey) == "1"
	secret := getCfg(etSecretKey)
	var master etMaster
	_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &master)
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"enabled": enabled, "secret": secret, "master": master, "nodes": nodes}))
}

func EasyTierEnable(c *gin.Context) {
	var p struct {
		Enable       bool   `json:"enable"`
		MasterNodeID int64  `json:"masterNodeId"`
		IP           string `json:"ip"`
		Port         int    `json:"port"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	setCfg(etEnabledKey, ifThen(p.Enable, "1", "0"))
	if p.Enable {
		if getCfg(etSecretKey) == "" {
			setCfg(etSecretKey, RandUUID32())
		}
		// Fill default IP/Port if missing
		ip := p.IP
		port := p.Port
		if ip == "" || port == 0 {
			var n model.Node
			_ = dbpkg.DB.First(&n, p.MasterNodeID).Error
			if ip == "" {
				ip = n.ServerIP
			}
			if port == 0 {
				minP, maxP := 10000, 65535
				if n.PortSta > 0 {
					minP = n.PortSta
				}
				if n.PortEnd > 0 {
					maxP = n.PortEnd
				}
				port = findFreePortOnNode(p.MasterNodeID, 0, minP, maxP)
				if port == 0 {
					port = minP
				}
			}
		}
		b, _ := json.Marshal(etMaster{NodeID: p.MasterNodeID, IP: ip, Port: port})
		setCfg(etMasterKey, string(b))
		// ensure master exists in nodes and deploy config
		ensureMasterJoined(p.MasterNodeID, ip, port)
	}
	c.JSON(http.StatusOK, response.OkMsg("ok"))
}

func EasyTierListNodes(c *gin.Context) {
	// join info persisted under etNodesKey; augment with Node names and public ServerIP
	var list []model.Node
	dbpkg.DB.Find(&list)
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	joined := map[int64]etNode{}
	for _, n := range nodes {
		joined[n.NodeID] = n
	}
	out := make([]map[string]any, 0, len(list))
	for _, n := range list {
		it := map[string]any{"nodeId": n.ID, "nodeName": n.Name, "serverIp": n.ServerIP}
		if j, ok := joined[n.ID]; ok {
			it["joined"] = true
			it["ip"] = j.IP
			it["port"] = j.Port
			it["peerNodeId"] = j.PeerNodeID
			it["ipv4"] = j.IPv4
			if j.PeerIP != nil {
				it["peerIp"] = *j.PeerIP
			}
		}
		out = append(out, it)
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"nodes": out}))
}

func EasyTierJoin(c *gin.Context) {
	var p struct {
		NodeID     int64   `json:"nodeId"`
		IP         string  `json:"ip"`
		Port       int     `json:"port"`
		PeerNodeID *int64  `json:"peerNodeId"`
		PeerIP     *string `json:"peerIp"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if getCfg(etEnabledKey) != "1" {
		c.JSON(http.StatusOK, response.ErrMsg("请先启用组网并设置主控节点"))
		return
	}
	var master etMaster
	_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &master)
	if master.NodeID == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("主控节点未配置"))
		return
	}
	// Validate IP exists in node interfaces
	if !ipInNodeInterfaces(p.NodeID, p.IP) {
		c.JSON(http.StatusOK, response.ErrMsg("所选IP不在节点接口列表中"))
		return
	}
	// load and update nodes list
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	// use node id as the last segment (template carries prefix)
	ipv4 := fmt.Sprintf("%d", p.NodeID)
	// normalize/validate port
	var n model.Node
	_ = dbpkg.DB.First(&n, p.NodeID).Error
	port := p.Port
	if port <= 0 || (n.PortSta > 0 && port < n.PortSta) || (n.PortEnd > 0 && port > n.PortEnd) {
		minP, maxP := 10000, 65535
		if n.PortSta > 0 {
			minP = n.PortSta
		}
		if n.PortEnd > 0 {
			maxP = n.PortEnd
		}
		picked := findFreePortOnNode(p.NodeID, 0, minP, maxP)
		if picked == 0 {
			c.JSON(http.StatusOK, response.ErrMsg("端口不可用，请调整端口范围"))
			return
		}
		port = picked
	}
	// upsert
	found := false
	for i := range nodes {
		if nodes[i].NodeID == p.NodeID {
			nodes[i].IP = p.IP
			nodes[i].Port = port
			nodes[i].PeerNodeID = p.PeerNodeID
			if p.PeerIP != nil && *p.PeerIP != "" {
				nodes[i].PeerIP = p.PeerIP
			}
			found = true
			break
		}
	}
	if !found {
		nodes = append(nodes, etNode{NodeID: p.NodeID, IP: p.IP, Port: port, PeerNodeID: p.PeerNodeID, IPv4: ipv4, PeerIP: p.PeerIP})
	}
	b, _ := json.Marshal(nodes)
	setCfg(etNodesKey, string(b))
	// trigger agent install & config write (idempotent)
	// prevent duplicate installs and use a longer server-side timeout
	// a) Install script
	installTO := getCfgInt("easytier_install_timeout_sec", 420) // default 7min
	if beginEtInstall(p.NodeID) {
		defer endEtInstall(p.NodeID)
		script := readFileDefault("easytier/install.sh")
		var payload = map[string]any{"requestId": RandUUID(), "timeoutSec": installTO}
		// compute server base url
		host := getCfg("ip")
		if host != "" && !strings.HasPrefix(host, "http") {
			host = "http://" + host
		}
		if host == "" {
			host = "/"
		}
		if strings.HasSuffix(host, "/") {
			host = strings.TrimSuffix(host, "/")
		}
		if script == "" {
			payload["url"] = host + "/easytier/install.sh"
		} else {
			payload["content"] = strings.ReplaceAll(script, "{SERVER}", host)
		}
		if !requestWithRetry(p.NodeID, "RunScript", payload, time.Duration(installTO)*time.Second, 0) {
			c.JSON(http.StatusOK, response.ErrMsg("安装脚本未响应"))
			return
		}
	} else {
		// Another install is in progress; wait for it to finish to avoid upstream overwriting our config
		waitEtInstallFinish(p.NodeID, time.Duration(installTO)*time.Second)
	}
	// render and send default.conf (write to both common paths)
	conf := renderEasyTierConf(p.NodeID)
	if !requestWithRetry(p.NodeID, "WriteFile", map[string]any{"requestId": RandUUID(), "path": "/opt/easytier/config/default.conf", "content": conf}, 15*time.Second, 2) {
		c.JSON(http.StatusOK, response.ErrMsg("写配置失败"))
		return
	}
	_ = requestWithRetry(p.NodeID, "WriteFile", map[string]any{"requestId": RandUUID(), "path": "/opt/easytier/config/default/default.conf", "content": conf}, 10*time.Second, 1)
	// restart instance service first, then generic
	_ = requestWithRetry(p.NodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier@default"}, 15*time.Second, 1)
	if !requestWithRetry(p.NodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier"}, 20*time.Second, 2) {
		// ignore error; instance service may be the active one
	}
	c.JSON(http.StatusOK, response.OkMsg("加入已下发"))
}

// ensureMasterJoined upserts master into nodes list and deploys config/service
func ensureMasterJoined(nodeID int64, ip string, port int) {
	if nodeID == 0 {
		return
	}
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	present := false
	for i := range nodes {
		if nodes[i].NodeID == nodeID {
			present = true
			nodes[i].IP = ip
			nodes[i].Port = port
			break
		}
	}
	if !present {
		ipv4 := fmt.Sprintf("%d", nodeID+1)
		nodes = append(nodes, etNode{NodeID: nodeID, IP: ip, Port: port, IPv4: ipv4})
	}
	b, _ := json.Marshal(nodes)
	setCfg(etNodesKey, string(b))
	// deploy on agent best-effort
	script := readFileDefault("easytier/install.sh")
	payload := map[string]any{"requestId": RandUUID(), "timeoutSec": 300}
	host := getCfg("ip")
	if host != "" && !strings.HasPrefix(host, "http") {
		host = "http://" + host
	}
	if host == "" {
		host = "/"
	}
	if strings.HasSuffix(host, "/") {
		host = strings.TrimSuffix(host, "/")
	}
	if script == "" {
		payload["url"] = host + "/easytier/install.sh"
	} else {
		payload["content"] = strings.ReplaceAll(script, "{SERVER}", host)
	}
	// avoid duplicate installs for master as well
	installTO := getCfgInt("easytier_install_timeout_sec", 420)
	if beginEtInstall(nodeID) {
		defer endEtInstall(nodeID)
		_ = requestWithRetry(nodeID, "RunScript", payload, time.Duration(installTO)*time.Second, 0)
	} else {
		waitEtInstallFinish(nodeID, time.Duration(installTO)*time.Second)
	}
	conf := renderEasyTierConf(nodeID)
	_ = requestWithRetry(nodeID, "WriteFile", map[string]any{"requestId": RandUUID(), "path": "/opt/easytier/config/default.conf", "content": conf}, 15*time.Second, 2)
	_ = requestWithRetry(nodeID, "WriteFile", map[string]any{"requestId": RandUUID(), "path": "/opt/easytier/config/default/default.conf", "content": conf}, 10*time.Second, 1)
	_ = requestWithRetry(nodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier@default"}, 15*time.Second, 1)
	_ = requestWithRetry(nodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier"}, 20*time.Second, 1)
}

// ipInNodeInterfaces checks if given ip belongs to node interfaces snapshot
func ipInNodeInterfaces(nodeID int64, ip string) bool {
	if ip == "" {
		return false
	}
	// Accept when matches node's configured ServerIP or IP
	var n model.Node
	_ = dbpkg.DB.First(&n, nodeID).Error
	if ip == n.ServerIP || ip == n.IP {
		return true
	}
	var r model.NodeRuntime
	if err := dbpkg.DB.First(&r, "node_id = ?", nodeID).Error; err != nil || r.Interfaces == nil {
		return false
	}
	var arr []string
	if err := json.Unmarshal([]byte(*r.Interfaces), &arr); err != nil {
		return false
	}
	for _, v := range arr {
		if v == ip {
			return true
		}
	}
	return false
}

// requestWithRetry wraps RequestOp with simple retries
func requestWithRetry(nodeID int64, cmd string, data map[string]any, timeout time.Duration, retries int) bool {
	if retries < 0 {
		retries = 0
	}
	for i := 0; i <= retries; i++ {
		if _, ok := RequestOp(nodeID, cmd, data, timeout); ok {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// POST /api/v1/easytier/remove {nodeId}
// Remove a node from easytier list (backend guard: master node cannot be removed)
func EasyTierRemove(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	// load master
	var master etMaster
	_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &master)
	if master.NodeID != 0 && p.NodeID == master.NodeID {
		c.JSON(http.StatusOK, response.ErrMsg("主控节点不可移除"))
		return
	}
	// load nodes list
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	out := make([]etNode, 0, len(nodes))
	for _, n := range nodes {
		if n.NodeID != p.NodeID {
			out = append(out, n)
		}
	}
	b, _ := json.Marshal(out)
	setCfg(etNodesKey, string(b))
	// best-effort stop easytier service on that node
	_ = sendWSCommand(p.NodeID, "StopService", map[string]any{"name": "easytier"})
	c.JSON(http.StatusOK, response.OkMsg("已移除"))
}

func renderEasyTierConf(nodeID int64) string {
	// simple template: load from easytier/default.conf and replace placeholders
	// placeholders: {hostname}, {ipv4}, {port}, {ip}, {peer_port}, {secret}
	secret := getCfg(etSecretKey)
	var n model.Node
	_ = dbpkg.DB.First(&n, nodeID).Error
	// lookup node config
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	var self etNode
	for _, x := range nodes {
		if x.NodeID == nodeID {
			self = x
			break
		}
	}
	// peer lookup: 默认使用自身对外 IP+端口；若配置了对端则覆盖；若指定了 PeerIP 则优先生效
	peerIP := self.IP
	peerPort := self.Port
	if self.PeerNodeID != nil {
		for _, x := range nodes {
			if x.NodeID == *self.PeerNodeID {
				peerIP = x.IP
				peerPort = x.Port
				break
			}
		}
	}
	if self.PeerIP != nil && *self.PeerIP != "" {
		peerIP = *self.PeerIP
	}
	// bracket IPv6 for URL safety
	if strings.Contains(peerIP, ":") && !(strings.HasPrefix(peerIP, "[") && strings.HasSuffix(peerIP, "]")) {
		peerIP = "[" + peerIP + "]"
	}
	tpl := readFileDefault("easytier/default.conf")
	out := tpl
	out = strings.ReplaceAll(out, "{hostname}", orString(n.Name, fmt.Sprintf("node-%d", nodeID)))
	out = strings.ReplaceAll(out, "{ipv4}", ipv4Tail(self.IPv4, nodeID))
	out = strings.ReplaceAll(out, "{port}", fmt.Sprintf("%d", self.Port))
	out = strings.ReplaceAll(out, "{ip}", peerIP)
	out = strings.ReplaceAll(out, "{peer_port}", fmt.Sprintf("%d", peerPort))
	out = strings.ReplaceAll(out, "{secret}", secret)
	// generate dev_name as network_ + 5 random alnum chars
	out = strings.ReplaceAll(out, "{dev_name}", "network_"+randDevSuffix(5))
	return out
}

func readFileDefault(p string) string { b, _ := os.ReadFile(p); return string(b) }
func RandUUID32() string              { v := RandUUID(); sum := md5.Sum([]byte(v)); return fmt.Sprintf("%x", sum) }

// ipv4Tail returns the last numeric segment for template placeholder.
func ipv4Tail(v string, nodeID int64) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Sprintf("%d", nodeID)
	}
	// digits only
	if allDigits(v) {
		return v
	}
	// dotted ipv4
	if strings.Contains(v, ".") {
		parts := strings.Split(v, ".")
		last := strings.TrimSpace(parts[len(parts)-1])
		if allDigits(last) {
			return last
		}
	}
	return fmt.Sprintf("%d", nodeID)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n >= 0 && n <= 255
	}
	return false
}

func randDevSuffix(n int) string {
	if n <= 0 {
		n = 5
	}
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, n)
	// rand seeded elsewhere or default, reseed here to be safe
	rand.Seed(time.Now().UnixNano())
	for i := 0; i < n; i++ {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// ===== Extra operations =====

// POST /api/v1/easytier/suggest-port {nodeId}
func EasyTierSuggestPort(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var n model.Node
	if err := dbpkg.DB.First(&n, p.NodeID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	minP, maxP := 10000, 65535
	if n.PortSta > 0 {
		minP = n.PortSta
	}
	if n.PortEnd > 0 {
		maxP = n.PortEnd
	}
	port := findFreePortOnNode(p.NodeID, 0, minP, maxP)
	if port == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("端口已满"))
		return
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"port": port}))
}

// (removed duplicate EasyTierRemove; guarded version defined above)

// GET /api/v1/easytier/ghproxy/*path
// Simple passthrough proxy so agents can fetch upstream scripts/binaries via server.
func EasyTierProxy(c *gin.Context) {
	raw := strings.TrimPrefix(c.Param("path"), "/")
	if raw == "" || !(strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")) {
		c.JSON(http.StatusBadRequest, response.ErrMsg("bad url"))
		return
	}
	req, err := http.NewRequest("GET", raw, nil)
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("build req failed"))
		return
	}
	req.Header.Set("User-Agent", "network-panel-easytier-proxy")
	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("fetch failed"))
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			c.Writer.Header().Set(k, vs[0])
		}
	}
	c.Status(resp.StatusCode)

	_, _ = io.Copy(c.Writer, resp.Body)
}

// POST /api/v1/easytier/change-peer {nodeId, peerNodeId}
func EasyTierChangePeer(c *gin.Context) {
	var p struct {
		NodeID     int64   `json:"nodeId" binding:"required"`
		PeerNodeID int64   `json:"peerNodeId" binding:"required"`
		PeerIP     *string `json:"peerIp"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	for i := range nodes {
		if nodes[i].NodeID == p.NodeID {
			nodes[i].PeerNodeID = &p.PeerNodeID
			if p.PeerIP != nil && *p.PeerIP != "" {
				nodes[i].PeerIP = p.PeerIP
			}
		}
	}
	b, _ := json.Marshal(nodes)
	setCfg(etNodesKey, string(b))
	// rewrite config on target node and restart
	conf := renderEasyTierConf(p.NodeID)
	RequestOp(p.NodeID, "WriteFile", map[string]any{"requestId": RandUUID(), "path": "/opt/easytier/config/default.conf", "content": conf}, 15*time.Second)
	RequestOp(p.NodeID, "WriteFile", map[string]any{"requestId": RandUUID(), "path": "/opt/easytier/config/default/default.conf", "content": conf}, 10*time.Second)
	RequestOp(p.NodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier@default"}, 15*time.Second)
	RequestOp(p.NodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier"}, 15*time.Second)
	c.JSON(http.StatusOK, response.OkMsg("已变更"))
}

// POST /api/v1/easytier/auto-assign {mode:"chain"}
func EasyTierAutoAssign(c *gin.Context) {
	var p struct {
		Mode string `json:"mode"`
	}
	_ = c.ShouldBindJSON(&p)
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	if len(nodes) < 2 {
		c.JSON(http.StatusOK, response.OkMsg("无需分配"))
		return
	}
	// support chain (default), star (all -> master), ring (i->i+1, last->first)
	switch p.Mode {
	case "star":
		var master etMaster
		_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &master)
		if master.NodeID == 0 {
			c.JSON(http.StatusOK, response.ErrMsg("主控未配置"))
			return
		}
		for i := range nodes {
			if nodes[i].NodeID != master.NodeID {
				nid := master.NodeID
				nodes[i].PeerNodeID = &nid
			}
		}
	case "ring":
		for i := range nodes {
			next := nodes[(i+1)%len(nodes)].NodeID
			nodes[i].PeerNodeID = &next
		}
	default: // chain
		for i := 1; i < len(nodes); i++ {
			prev := nodes[i-1].NodeID
			nodes[i].PeerNodeID = &prev
		}
	}
	b, _ := json.Marshal(nodes)
	setCfg(etNodesKey, string(b))
	// rewrite all configs and restart
	for _, n := range nodes {
		conf := renderEasyTierConf(n.NodeID)
		_ = requestWithRetry(n.NodeID, "WriteFile", map[string]any{"requestId": RandUUID(), "path": "/opt/easytier/config/default.conf", "content": conf}, 15*time.Second, 2)
		_ = requestWithRetry(n.NodeID, "WriteFile", map[string]any{"requestId": RandUUID(), "path": "/opt/easytier/config/default/default.conf", "content": conf}, 10*time.Second, 1)
		_ = requestWithRetry(n.NodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier@default"}, 15*time.Second, 1)
		_ = requestWithRetry(n.NodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier"}, 20*time.Second, 1)
	}
	c.JSON(http.StatusOK, response.OkMsg("已分配"))
}

// POST /api/v1/easytier/redeploy-master
func EasyTierRedeployMaster(c *gin.Context) {
	var m etMaster
	if err := json.Unmarshal([]byte(getCfg(etMasterKey)), &m); err != nil || m.NodeID == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("主控未配置"))
		return
	}
	ensureMasterJoined(m.NodeID, m.IP, m.Port)
	c.JSON(http.StatusOK, response.OkMsg("已重新部署主控"))
}

// helpers to get/set ViteConfig
func getCfg(name string) string {
	var v model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", name).First(&v).Error; err == nil {
		return v.Value
	}
	return ""
}
func setCfg(name, value string) {
	now := time.Now().UnixMilli()
	var v model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", name).First(&v).Error; err == nil {
		v.Value = value
		v.Time = now
		_ = dbpkg.DB.Save(&v).Error
	} else {
		_ = dbpkg.DB.Create(&model.ViteConfig{Name: name, Value: value, Time: now}).Error
	}
}

// helper: read numeric vite_config with default fallback
func getCfgInt(name string, def int) int {
	v := strings.TrimSpace(getCfg(name))
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	return def
}
