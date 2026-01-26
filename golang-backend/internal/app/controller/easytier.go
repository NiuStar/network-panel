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
	etAutoKey    = "easytier_auto_join"
	panelHostCacheKey = "panel_host_cache"
	etStatusNotInstalled = "not_installed"
	etStatusDownloading  = "downloading"
	etStatusInstalling   = "installing"
	etStatusInstalled    = "installed"
	etStatusFailed       = "failed"
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
	etReqMu      sync.Mutex
	etReqOps     = map[string]string{}
)

type ipInfo struct {
	v4 []string
	v6 []string
}

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

func setEtRequestOp(reqID string, op string) {
	if reqID == "" {
		return
	}
	etReqMu.Lock()
	etReqOps[reqID] = op
	etReqMu.Unlock()
}

func getEtRequestOp(reqID string) string {
	if reqID == "" {
		return ""
	}
	etReqMu.Lock()
	op := etReqOps[reqID]
	etReqMu.Unlock()
	return op
}

func clearEtRequestOp(reqID string) {
	if reqID == "" {
		return
	}
	etReqMu.Lock()
	delete(etReqOps, reqID)
	etReqMu.Unlock()
}

func updateEasyTierRuntime(nodeID int64, status string, op string, errMsg string, requestID string, updatedAt int64) {
	base, _ := getRuntimeCached(nodeID)
	rt := base
	rt.NodeID = nodeID
	if status != "" {
		rt.EasyTierStatus = &status
	}
	if op != "" {
		rt.EasyTierOp = &op
	}
	if errMsg != "" {
		rt.EasyTierError = &errMsg
	} else if status == etStatusInstalled || status == etStatusNotInstalled {
		rt.EasyTierError = nil
	}
	if requestID != "" {
		rt.EasyTierRequestID = &requestID
	}
	if updatedAt > 0 {
		rt.EasyTierUpdatedTime = &updatedAt
		rt.UpdatedTime = updatedAt
	} else {
		rt.UpdatedTime = time.Now().UnixMilli()
		t := rt.UpdatedTime
		rt.EasyTierUpdatedTime = &t
	}
	setRuntime(rt)
}

func shouldMarkDownloading(chunk string) bool {
	l := strings.ToLower(chunk)
	return strings.Contains(l, "fetch") || strings.Contains(l, "download") || strings.Contains(l, "wget")
}

func tailText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

func EasyTierStatus(c *gin.Context) {
	enabled := getCfg(etEnabledKey) == "1"
	secret := getCfg(etSecretKey)
	autoJoin := getCfg(etAutoKey) == "1"
	var master etMaster
	_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &master)
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"enabled": enabled, "secret": secret, "autoJoin": autoJoin, "master": master, "nodes": nodes}))
}

// EasyTierVersion returns current(master) and latest version info.
// @Summary EasyTier 版本信息
// @Tags easytier
// @Produce json
// @Success 200 {object} SwaggerResp
// @Router /api/v1/easytier/version [get]
func EasyTierVersion(c *gin.Context) {
	latest := fetchEasyTierLatestVersion()
	current := ""
	var master etMaster
	_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &master)
	if master.NodeID != 0 {
		current = fetchEasyTierNodeVersion(master.NodeID)
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{
		"latest":  latest,
		"current": current,
	}))
}

// EasyTierUpdateAll updates EasyTier on all joined nodes.
// @Summary EasyTier 全部更新
// @Tags easytier
// @Accept json
// @Produce json
// @Success 200 {object} SwaggerResp
// @Router /api/v1/easytier/update-all [post]
func EasyTierUpdateAll(c *gin.Context) {
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	ids := make([]int64, 0, len(nodes))
	seen := map[int64]bool{}
	for _, n := range nodes {
		if n.NodeID > 0 && !seen[n.NodeID] {
			ids = append(ids, n.NodeID)
			seen[n.NodeID] = true
		}
	}
	if len(ids) == 0 {
		var list []model.Node
		_ = dbpkg.DB.Find(&list).Error
		for _, n := range list {
			if n.ID > 0 && !seen[n.ID] {
				ids = append(ids, n.ID)
				seen[n.ID] = true
			}
		}
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, response.OkMsg("无可更新节点"))
		return
	}
	go deployEasyTierNodes(ids)
	c.JSON(http.StatusOK, response.Ok(map[string]any{"count": len(ids)}))
}

// EasyTierReapply re-sends EasyTier config to selected nodes.
// @Summary EasyTier 重新下发配置(批量)
// @Tags easytier
// @Accept json
// @Produce json
// @Success 200 {object} SwaggerResp
// @Router /api/v1/easytier/reapply [post]
func EasyTierReapply(c *gin.Context) {
	var p struct {
		NodeIDs []int64 `json:"nodeIds" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil || len(p.NodeIDs) == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if getCfg(etEnabledKey) != "1" {
		c.JSON(http.StatusOK, response.ErrMsg("请先启用组网"))
		return
	}
	var master etMaster
	_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &master)
	if master.NodeID == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("主控节点未配置"))
		return
	}
	var masterNode model.Node
	if err := dbpkg.DB.First(&masterNode, master.NodeID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("主控节点不存在"))
		return
	}
	masterChanged := false
	if master.IP == "" {
		master.IP = masterNode.ServerIP
		masterChanged = true
	}
	if master.Port == 0 {
		master.Port = pickNodePort(masterNode)
		masterChanged = true
	}
	if masterChanged {
		if b, err := json.Marshal(master); err == nil {
			setCfg(etMasterKey, string(b))
		}
	}
	// load configured nodes list
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	configured := map[int64]etNode{}
	idx := map[int64]int{}
	for i, n := range nodes {
		configured[n.NodeID] = n
		idx[n.NodeID] = i
	}
	changed := false
	if midx, ok := idx[master.NodeID]; ok {
		m := nodes[midx]
		if m.IP == "" && master.IP != "" {
			m.IP = master.IP
			changed = true
		}
		if m.Port == 0 && master.Port != 0 {
			m.Port = master.Port
			changed = true
		}
		if m.IPv4 == "" {
			m.IPv4 = fmt.Sprintf("%d", master.NodeID)
			changed = true
		}
		if changed {
			nodes[midx] = m
			configured[m.NodeID] = m
		}
	} else {
		nodes = append(nodes, etNode{NodeID: master.NodeID, IP: master.IP, Port: master.Port, IPv4: fmt.Sprintf("%d", master.NodeID)})
		idx[master.NodeID] = len(nodes) - 1
		configured[master.NodeID] = nodes[idx[master.NodeID]]
		changed = true
	}
	// load node records
	ids := make([]int64, 0, len(p.NodeIDs))
	seen := map[int64]bool{}
	for _, id := range p.NodeIDs {
		if id > 0 && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	var list []model.Node
	if len(ids) > 0 {
		_ = dbpkg.DB.Where("id in ?", ids).Find(&list).Error
	}
	nodeMap := map[int64]model.Node{}
	for _, n := range list {
		nodeMap[n.ID] = n
	}
	if _, ok := nodeMap[master.NodeID]; !ok {
		nodeMap[master.NodeID] = masterNode
	}
	ifaceList := make([]model.Node, 0, len(nodeMap))
	for _, n := range nodeMap {
		ifaceList = append(ifaceList, n)
	}
	ifaceMap := loadNodeInterfaces(ifaceList)
	masterInfo := splitNodeIPs(masterNode, ifaceMap[masterNode.ID])
	results := make([]map[string]any, 0, len(ids))
	resultMap := map[int64]map[string]any{}
	eligible := make([]int64, 0, len(ids))
	for _, id := range ids {
		item := map[string]any{"nodeId": id}
		resultMap[id] = item
		results = append(results, item)
		node, ok := nodeMap[id]
		if !ok {
			item["error"] = "节点不存在"
			continue
		}
		if node.Status == nil || *node.Status != 1 {
			item["error"] = "节点离线"
			continue
		}
		if _, ok := configured[id]; !ok {
			info := splitNodeIPs(node, ifaceMap[node.ID])
			selfIP := pickIP(info, false)
			if selfIP == "" {
				selfIP = node.ServerIP
			}
			if selfIP == "" {
				item["error"] = "无可用IP"
				continue
			}
			port := pickNodePort(node)
			ipv4 := fmt.Sprintf("%d", node.ID)
			var peerNodeID *int64
			var peerIP *string
			if node.ID != master.NodeID {
				nid := master.NodeID
				peerNodeID = &nid
				if len(info.v6) > 0 && len(masterInfo.v6) > 0 {
					v6 := masterInfo.v6[0]
					peerIP = &v6
				}
			}
			nodes = append(nodes, etNode{
				NodeID:     node.ID,
				IP:         selfIP,
				Port:       port,
				PeerNodeID: peerNodeID,
				PeerIP:     peerIP,
				IPv4:       ipv4,
			})
			idx[node.ID] = len(nodes) - 1
			configured[node.ID] = nodes[idx[node.ID]]
			changed = true
		}
		eligible = append(eligible, id)
	}
	if changed {
		if b, err := json.Marshal(nodes); err == nil {
			setCfg(etNodesKey, string(b))
		}
	}
	success := 0
	for _, id := range eligible {
		item := resultMap[id]
		if !beginEtInstall(id) {
			item["error"] = "该节点已有任务进行中"
			continue
		}
		reqID := RandUUID()
		now := time.Now().UnixMilli()
		updateEasyTierRuntime(id, "", "reapply", "", reqID, now)
		conf := renderEasyTierConf(id)
		ok, msg := writeEasyTierConfig(id, conf, reqID)
		if ok {
			item["requestId"] = reqID
			success++
			_ = requestWithRetry(id, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier@default"}, 15*time.Second, 1)
			_ = requestWithRetry(id, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier"}, 20*time.Second, 1)
			go func(nid int64, rid string) {
				time.Sleep(2 * time.Second)
				verifyEasyTierNode(nid, rid, "reapply")
			}(id, reqID)
		} else {
			if msg == "" {
				msg = "重发失败"
			}
			item["error"] = msg
		}
		endEtInstall(id)
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{
		"success": success,
		"failed":  len(results) - success,
		"results": results,
	}))
}

func EasyTierEnable(c *gin.Context) {
	var p struct {
		Enable       bool   `json:"enable"`
		MasterNodeID int64  `json:"masterNodeId"`
		IP           string `json:"ip"`
		Port         int    `json:"port"`
		AutoJoin     *bool  `json:"autoJoin"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	setCfg(etEnabledKey, ifThen(p.Enable, "1", "0"))
	if p.AutoJoin != nil {
		setCfg(etAutoKey, ifThen(*p.AutoJoin, "1", "0"))
	}
	if p.Enable {
		if getCfg(etSecretKey) == "" {
			setCfg(etSecretKey, RandUUID32())
		}
		ip := p.IP
		port := p.Port
		// Fill default IP/Port or auto select master if missing
		masterID := p.MasterNodeID
		if masterID == 0 {
			var cur etMaster
			_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &cur)
			if cur.NodeID != 0 {
				masterID = cur.NodeID
				ip = cur.IP
				port = cur.Port
			}
		}
		if masterID == 0 {
			if m, ok := pickBestMaster(); ok {
				masterID = m.NodeID
				ip = m.IP
				port = m.Port
			}
		}
		if masterID != 0 && (ip == "" || port == 0) {
			var n model.Node
			_ = dbpkg.DB.First(&n, masterID).Error
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
				port = findFreePortOnNode(masterID, 0, minP, maxP)
				if port == 0 {
					port = minP
				}
			}
		}
		if masterID == 0 {
			c.JSON(http.StatusOK, response.ErrMsg("无可用节点作为中心"))
			return
		}
		b, _ := json.Marshal(etMaster{NodeID: masterID, IP: ip, Port: port})
		setCfg(etMasterKey, string(b))
		// ensure master exists in nodes and deploy config
		ensureMasterJoined(masterID, ip, port, resolvePanelHost(c))
		// auto join all nodes (async)
		go ensureEasyTierAutoJoin()
	}
	c.JSON(http.StatusOK, response.OkMsg("ok"))
}

func EasyTierListNodes(c *gin.Context) {
	if getCfg(etEnabledKey) == "1" && getCfg(etAutoKey) == "1" {
		_ = resolvePanelHost(c)
		go ensureEasyTierAutoJoin()
	}
	// join info persisted under etNodesKey; augment with Node names and public ServerIP
	var list []model.Node
	dbpkg.DB.Find(&list)
	idList := make([]int64, 0, len(list))
	for _, n := range list {
		idList = append(idList, n.ID)
	}
	runtimeMap := map[int64]model.NodeRuntime{}
	if len(idList) > 0 {
		var runs []model.NodeRuntime
		_ = dbpkg.DB.Where("node_id in ?", idList).Find(&runs).Error
		for _, r := range runs {
			runtimeMap[r.NodeID] = r
		}
	}
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	// normalize IPv4 segment to node ID to avoid duplicates
	changed := false
	for i := range nodes {
		if nodes[i].NodeID == 0 {
			continue
		}
		ipv4Seg := fmt.Sprintf("%d", nodes[i].NodeID)
		if nodes[i].IPv4 != ipv4Seg {
			nodes[i].IPv4 = ipv4Seg
			changed = true
		}
	}
	if changed {
		if b, err := json.Marshal(nodes); err == nil {
			setCfg(etNodesKey, string(b))
		}
	}
	joined := map[int64]etNode{}
	for _, n := range nodes {
		joined[n.NodeID] = n
	}
	out := make([]map[string]any, 0, len(list))
	for _, n := range list {
		online := n.Status != nil && *n.Status == 1
		ifaces := parseInterfaceList(runtimeMap[n.ID].Interfaces)
		it := map[string]any{"nodeId": n.ID, "nodeName": n.Name, "serverIp": n.ServerIP, "online": online}
		if rt, ok := runtimeMap[n.ID]; ok {
			if rt.EasyTierStatus != nil {
				it["etStatus"] = *rt.EasyTierStatus
			}
			if rt.EasyTierOp != nil {
				it["etOp"] = *rt.EasyTierOp
			}
			if rt.EasyTierError != nil {
				it["etError"] = *rt.EasyTierError
			}
			if rt.EasyTierUpdatedTime != nil {
				it["etUpdatedTime"] = *rt.EasyTierUpdatedTime
			}
			if rt.EasyTierRequestID != nil {
				it["etRequestId"] = *rt.EasyTierRequestID
			}
			if rt.EasyTierVersion != nil {
				it["etVersion"] = *rt.EasyTierVersion
			}
		}
		if j, ok := joined[n.ID]; ok {
			it["configured"] = true
			it["ip"] = j.IP
			it["port"] = j.Port
			it["peerNodeId"] = j.PeerNodeID
			it["ipv4"] = j.IPv4
			if j.PeerIP != nil {
				it["peerIp"] = *j.PeerIP
			}
			expectedIP := ""
			ipv4Seg := ipv4Tail(j.IPv4, n.ID)
			if ipv4Seg != "" {
				expectedIP = "10.126.126." + ipv4Seg
				it["expectedIp"] = expectedIP
			}
			joinedOk := false
			if online && expectedIP != "" && hasIP(ifaces, expectedIP) {
				joinedOk = true
			}
			it["joined"] = joinedOk
		} else {
			it["configured"] = false
			it["joined"] = false
		}
		if _, ok := it["etStatus"]; !ok {
			it["etStatus"] = etStatusNotInstalled
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
	host := resolvePanelHost(c)
	reqID := ""
	if beginEtInstall(p.NodeID) {
		defer endEtInstall(p.NodeID)
		var ok bool
		var errMsg string
		reqID, ok, errMsg = runEasyTierInstallOp(p.NodeID, "install", host, installTO)
		if !ok {
			c.JSON(http.StatusOK, response.ErrMsg(errMsg))
			return
		}
	} else {
		// Another install is in progress; wait for it to finish to avoid upstream overwriting our config
		waitEtInstallFinish(p.NodeID, time.Duration(installTO)*time.Second)
		reqID = lastEasyTierRequestID(p.NodeID)
	}
	// render and send default.conf (write to both common paths)
	conf := renderEasyTierConf(p.NodeID)
	if ok, msg := writeEasyTierConfig(p.NodeID, conf, reqID); !ok {
		if msg == "" {
			msg = "写配置失败"
		}
		c.JSON(http.StatusOK, response.ErrMsg(msg))
		return
	}
	// restart instance service first, then generic
	_ = requestWithRetry(p.NodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier@default"}, 15*time.Second, 1)
	if !requestWithRetry(p.NodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier"}, 20*time.Second, 2) {
		// ignore error; instance service may be the active one
	}
	c.JSON(http.StatusOK, response.OkMsg("加入已下发"))
}

// ensureMasterJoined upserts master into nodes list and deploys config/service
func ensureMasterJoined(nodeID int64, ip string, port int, host string) {
	if nodeID == 0 {
		return
	}
	ipv4 := fmt.Sprintf("%d", nodeID)
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
			nodes[i].IPv4 = ipv4
			break
		}
	}
	if !present {
		nodes = append(nodes, etNode{NodeID: nodeID, IP: ip, Port: port, IPv4: ipv4})
	}
	b, _ := json.Marshal(nodes)
	setCfg(etNodesKey, string(b))
	// deploy on agent best-effort
	if host == "" {
		host = resolvePanelHost(nil)
	}
	// avoid duplicate installs for master as well
	installTO := getCfgInt("easytier_install_timeout_sec", 420)
	reqID := ""
	if beginEtInstall(nodeID) {
		defer endEtInstall(nodeID)
		reqID, _, _ = runEasyTierInstallOp(nodeID, "install", host, installTO)
	} else {
		waitEtInstallFinish(nodeID, time.Duration(installTO)*time.Second)
		reqID = lastEasyTierRequestID(nodeID)
	}
	conf := renderEasyTierConf(nodeID)
	_, _ = writeEasyTierConfig(nodeID, conf, reqID)
	_ = requestWithRetry(nodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier@default"}, 15*time.Second, 1)
	_ = requestWithRetry(nodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier"}, 20*time.Second, 1)
}

func ensureEasyTierAutoJoin() {
	if getCfg(etEnabledKey) != "1" || getCfg(etAutoKey) != "1" {
		return
	}
	host := resolvePanelHost(nil)
	var list []model.Node
	if err := dbpkg.DB.Find(&list).Error; err != nil || len(list) == 0 {
		return
	}
	ifaceMap := loadNodeInterfaces(list)
	// ensure master exists
	var master etMaster
	_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &master)
	masterIPInfo := ipInfo{}
	if master.NodeID == 0 || !nodeExists(list, master.NodeID) {
		if m, ok := pickBestMaster(); ok {
			master = m
			b, _ := json.Marshal(master)
			setCfg(etMasterKey, string(b))
			ensureMasterJoined(master.NodeID, master.IP, master.Port, host)
		} else {
			return
		}
	}
	// master ip choices
	if n, ok := findNode(list, master.NodeID); ok {
		masterIPInfo = splitNodeIPs(n, ifaceMap[n.ID])
	}
	// load current nodes list
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	nodeIdx := map[int64]int{}
	for i := range nodes {
		nodeIdx[nodes[i].NodeID] = i
	}
	added := make([]int64, 0)
	changed := false
	// ensure master exists in nodes list
	if _, ok := nodeIdx[master.NodeID]; !ok {
		ensureMasterJoined(master.NodeID, master.IP, master.Port, host)
		if v := getCfg(etNodesKey); v != "" {
			nodes = nil
			_ = json.Unmarshal([]byte(v), &nodes)
			nodeIdx = map[int64]int{}
			for i := range nodes {
				nodeIdx[nodes[i].NodeID] = i
			}
		}
	}
	for _, n := range list {
		if n.ID == master.NodeID {
			continue
		}
		if idx, ok := nodeIdx[n.ID]; ok {
			if nodes[idx].PeerNodeID == nil && nodes[idx].PeerIP == nil {
				nid := master.NodeID
				nodes[idx].PeerNodeID = &nid
				changed = true
			}
			continue
		}
		info := splitNodeIPs(n, ifaceMap[n.ID])
		selfIP := pickIP(info, false)
		if selfIP == "" {
			selfIP = n.ServerIP
		}
		port := pickNodePort(n)
		nid := master.NodeID
		var peerIP *string
		if len(info.v6) > 0 && len(masterIPInfo.v6) > 0 {
			v6 := masterIPInfo.v6[0]
			peerIP = &v6
		}
		nodes = append(nodes, etNode{
			NodeID:     n.ID,
			IP:         selfIP,
			Port:       port,
			PeerNodeID: &nid,
			IPv4:       fmt.Sprintf("%d", n.ID),
			PeerIP:     peerIP,
		})
		added = append(added, n.ID)
		changed = true
	}
	if changed {
		b, _ := json.Marshal(nodes)
		setCfg(etNodesKey, string(b))
	}
	if len(added) > 0 {
		go deployEasyTierNodes(added)
	}
}

func ensureEasyTierAutoJoinFor(nodeID int64) {
	if nodeID == 0 {
		return
	}
	if getCfg(etEnabledKey) != "1" || getCfg(etAutoKey) != "1" {
		return
	}
	host := resolvePanelHost(nil)
	var list []model.Node
	if err := dbpkg.DB.Find(&list).Error; err != nil || len(list) == 0 {
		return
	}
	node, ok := findNode(list, nodeID)
	if !ok {
		return
	}
	// ensure master exists
	var master etMaster
	_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &master)
	if master.NodeID == 0 || !nodeExists(list, master.NodeID) {
		if m, ok := pickBestMaster(); ok {
			master = m
			b, _ := json.Marshal(master)
			setCfg(etMasterKey, string(b))
			ensureMasterJoined(master.NodeID, master.IP, master.Port, host)
		} else {
			return
		}
	}
	if node.ID == master.NodeID {
		return
	}
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	for _, n := range nodes {
		if n.NodeID == node.ID {
			return
		}
	}
	ifaceMap := loadNodeInterfaces([]model.Node{node})
	var masterNode model.Node
	_ = dbpkg.DB.First(&masterNode, master.NodeID).Error
	masterIfaces := loadNodeInterfaces([]model.Node{masterNode})
	info := splitNodeIPs(node, ifaceMap[node.ID])
	selfIP := pickIP(info, false)
	if selfIP == "" {
		selfIP = node.ServerIP
	}
	masterInfo := splitNodeIPs(masterNode, masterIfaces[master.NodeID])
	nid := master.NodeID
	var peerIP *string
	if len(info.v6) > 0 && len(masterInfo.v6) > 0 {
		v6 := masterInfo.v6[0]
		peerIP = &v6
	}
	port := pickNodePort(node)
	nodes = append(nodes, etNode{
		NodeID:     node.ID,
		IP:         selfIP,
		Port:       port,
		PeerNodeID: &nid,
		IPv4:       fmt.Sprintf("%d", node.ID),
		PeerIP:     peerIP,
	})
	b, _ := json.Marshal(nodes)
	setCfg(etNodesKey, string(b))
	go deployEasyTierNodes([]int64{node.ID})
}

func deployEasyTierNodes(ids []int64) {
	installTO := getCfgInt("easytier_install_timeout_sec", 420)
	host := resolvePanelHost(nil)
	for _, nodeID := range ids {
		reqID := ""
		if beginEtInstall(nodeID) {
			reqID, _, _ = runEasyTierInstallOp(nodeID, "install", host, installTO)
			endEtInstall(nodeID)
		} else {
			waitEtInstallFinish(nodeID, time.Duration(installTO)*time.Second)
			reqID = lastEasyTierRequestID(nodeID)
		}
		conf := renderEasyTierConf(nodeID)
		_, _ = writeEasyTierConfig(nodeID, conf, reqID)
		_ = requestWithRetry(nodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier@default"}, 15*time.Second, 1)
		_ = requestWithRetry(nodeID, "RestartService", map[string]any{"requestId": RandUUID(), "name": "easytier"}, 20*time.Second, 1)
	}
}

func pickBestMaster() (etMaster, bool) {
	var list []model.Node
	if err := dbpkg.DB.Find(&list).Error; err != nil || len(list) == 0 {
		return etMaster{}, false
	}
	ifaceMap := loadNodeInterfaces(list)
	bestScore := -1
	var best model.Node
	bestInfo := ipInfo{}
	for _, n := range list {
		info := splitNodeIPs(n, ifaceMap[n.ID])
		score := 0
		if n.Status != nil && *n.Status == 1 {
			score += 2
		}
		if len(info.v4) > 0 {
			score += 2
		}
		if len(info.v6) > 0 {
			score += 2
		}
		if score > bestScore {
			bestScore = score
			best = n
			bestInfo = info
		}
	}
	if best.ID == 0 {
		return etMaster{}, false
	}
	ip := pickIP(bestInfo, false)
	if ip == "" {
		ip = best.ServerIP
	}
	port := pickNodePort(best)
	return etMaster{NodeID: best.ID, IP: ip, Port: port}, true
}

func fetchEasyTierLatestVersion() string {
	type respBody struct {
		Tag string `json:"tag_name"`
	}
	urls := []string{
		"https://api.github.com/repos/EasyTier/EasyTier/releases/latest",
		"https://proxy.529851.xyz/https://api.github.com/repos/EasyTier/EasyTier/releases/latest",
	}
	client := &http.Client{Timeout: 6 * time.Second}
	for _, u := range urls {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "network-panel-easytier")
		res, err := client.Do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode/100 != 2 {
			continue
		}
		var r respBody
		if json.Unmarshal(b, &r) == nil {
			return strings.TrimSpace(r.Tag)
		}
	}
	return ""
}

func fetchEasyTierNodeVersion(nodeID int64) string {
	script := `#!/bin/sh
set -e
easytier --version 2>/dev/null || easytier -V 2>/dev/null || /opt/easytier/easytier --version 2>/dev/null || /opt/easytier/easytier -V 2>/dev/null || true
`
	payload := map[string]any{"requestId": RandUUID(), "timeoutSec": 10, "content": script}
	res, ok := RequestOp(nodeID, "RunScript", payload, 15*time.Second)
	if !ok || res == nil {
		return ""
	}
	data, _ := res["data"].(map[string]any)
	if data == nil {
		return ""
	}
	stdout, _ := data["stdout"].(string)
	stderr, _ := data["stderr"].(string)
	out := strings.TrimSpace(stdout)
	if out == "" {
		out = strings.TrimSpace(stderr)
	}
	if out == "" {
		return ""
	}
	if idx := strings.Index(out, "\n"); idx >= 0 {
		out = strings.TrimSpace(out[:idx])
	}
	return out
}

func pickNodePort(n model.Node) int {
	minP, maxP := 10000, 65535
	if n.PortSta > 0 {
		minP = n.PortSta
	}
	if n.PortEnd > 0 {
		maxP = n.PortEnd
	}
	port := findFreePortOnNode(n.ID, 0, minP, maxP)
	if port == 0 {
		port = minP
	}
	return port
}

func nodeExists(list []model.Node, nodeID int64) bool {
	for _, n := range list {
		if n.ID == nodeID {
			return true
		}
	}
	return false
}

func findNode(list []model.Node, nodeID int64) (model.Node, bool) {
	for _, n := range list {
		if n.ID == nodeID {
			return n, true
		}
	}
	return model.Node{}, false
}

func loadNodeInterfaces(list []model.Node) map[int64][]string {
	idList := make([]int64, 0, len(list))
	for _, n := range list {
		idList = append(idList, n.ID)
	}
	out := map[int64][]string{}
	if len(idList) == 0 {
		return out
	}
	var runs []model.NodeRuntime
	_ = dbpkg.DB.Where("node_id in ?", idList).Find(&runs).Error
	for _, r := range runs {
		if r.Interfaces == nil || *r.Interfaces == "" {
			continue
		}
		var arr []string
		if json.Unmarshal([]byte(*r.Interfaces), &arr) == nil {
			out[r.NodeID] = arr
		}
	}
	return out
}

func splitNodeIPs(n model.Node, ifaces []string) ipInfo {
	info := ipInfo{}
	seen := map[string]bool{}
	add := func(ip string) {
		ip = strings.TrimSpace(ip)
		if ip == "" || seen[ip] {
			return
		}
		seen[ip] = true
		if strings.Contains(ip, ":") {
			info.v6 = append(info.v6, ip)
		} else if strings.Contains(ip, ".") {
			info.v4 = append(info.v4, ip)
		}
	}
	add(n.ServerIP)
	add(n.IP)
	for _, ip := range ifaces {
		add(ip)
	}
	return info
}

func pickIP(info ipInfo, preferV6 bool) string {
	if preferV6 {
		if len(info.v6) > 0 {
			return info.v6[0]
		}
		if len(info.v4) > 0 {
			return info.v4[0]
		}
		return ""
	}
	if len(info.v4) > 0 {
		return info.v4[0]
	}
	if len(info.v6) > 0 {
		return info.v6[0]
	}
	return ""
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

func requestWithRetrySuccess(nodeID int64, cmd string, data map[string]any, timeout time.Duration, retries int) (bool, string) {
	if retries < 0 {
		retries = 0
	}
	lastMsg := ""
	for i := 0; i <= retries; i++ {
		res, ok := RequestOp(nodeID, cmd, data, timeout)
		if !ok {
			lastMsg = "请求失败"
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if data, _ := res["data"].(map[string]interface{}); data != nil {
			if s, ok := data["success"].(bool); ok {
				if s {
					return true, ""
				}
				if msg, _ := data["message"].(string); msg != "" {
					lastMsg = msg
				} else if stderr, _ := data["stderr"].(string); stderr != "" {
					lastMsg = stderr
				} else {
					lastMsg = "操作失败"
				}
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}
		return true, ""
	}
	if lastMsg == "" {
		lastMsg = "操作失败"
	}
	return false, lastMsg
}

func buildEasyTierConfigScript(conf string) string {
	delimiter := "NP_EASYTIER_CONF_" + RandUUID32()
	lines := []string{
		"#!/usr/bin/env sh",
		"set -e",
		"tmp=$(mktemp /tmp/np_et_conf.XXXX)",
		"cat >\"$tmp\" <<'" + delimiter + "'",
		conf,
		delimiter,
		"SUDO=\"\"",
		"if [ \"$(id -u)\" -ne 0 ] && command -v sudo >/dev/null 2>&1; then",
		"  SUDO=\"sudo\"",
		"fi",
		"$SUDO mkdir -p /opt/easytier/config /opt/easytier/config/default",
		"$SUDO cp -f \"$tmp\" /opt/easytier/config/default.conf",
		"$SUDO cp -f \"$tmp\" /opt/easytier/config/default/default.conf",
		"rm -f \"$tmp\"",
	}
	return strings.Join(lines, "\n") + "\n"
}

func lastEasyTierRequestID(nodeID int64) string {
	if base, ok := getRuntimeCached(nodeID); ok && base.EasyTierRequestID != nil {
		return *base.EasyTierRequestID
	}
	return ""
}

func logEasyTierConfig(nodeID int64, reqID string, conf string) {
	reqID = strings.TrimSpace(reqID)
	if reqID == "" {
		return
	}
	if strings.TrimSpace(conf) == "" {
		return
	}
	now := time.Now().UnixMilli()
	chunk := "[config] final easytier config\n" + conf
	var res model.EasyTierResult
	if err := dbpkg.DB.Where("node_id = ? AND request_id = ?", nodeID, reqID).First(&res).Error; err == nil && res.ID != 0 {
		content := res.Content
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += chunk
		_ = dbpkg.DB.Model(&res).Updates(map[string]any{"content": content, "updated_time": now, "time_ms": now}).Error
	} else {
		_ = dbpkg.DB.Create(&model.EasyTierResult{
			NodeID:      nodeID,
			RequestID:   reqID,
			Op:          "config",
			Content:     chunk,
			Done:        true,
			TimeMs:      now,
			CreatedTime: now,
			UpdatedTime: now,
		}).Error
	}
	publishEtStream(reqID, etStreamEvent{Chunk: chunk, Done: false, TimeMs: now})
}

func appendEasyTierLog(nodeID int64, reqID string, chunk string) {
	reqID = strings.TrimSpace(reqID)
	if reqID == "" {
		return
	}
	chunk = strings.TrimSpace(chunk)
	if chunk == "" {
		return
	}
	now := time.Now().UnixMilli()
	var res model.EasyTierResult
	if err := dbpkg.DB.Where("node_id = ? AND request_id = ?", nodeID, reqID).First(&res).Error; err == nil && res.ID != 0 {
		content := res.Content
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += chunk
		_ = dbpkg.DB.Model(&res).Updates(map[string]any{"content": content, "updated_time": now, "time_ms": now}).Error
	} else {
		_ = dbpkg.DB.Create(&model.EasyTierResult{
			NodeID:      nodeID,
			RequestID:   reqID,
			Op:          "verify",
			Content:     chunk,
			Done:        false,
			TimeMs:      now,
			CreatedTime: now,
			UpdatedTime: now,
		}).Error
	}
	publishEtStream(reqID, etStreamEvent{Chunk: chunk, Done: false, TimeMs: now})
}

func writeEasyTierConfig(nodeID int64, conf string, reqID string) (bool, string) {
	if strings.TrimSpace(conf) == "" {
		return false, "配置内容为空"
	}
	if reqID == "" {
		reqID = lastEasyTierRequestID(nodeID)
	}
	logOp := func(success bool, msg string) {
		if msg == "" {
			if success {
				msg = "写配置成功（详情见安装日志）"
			} else {
				msg = "写配置失败"
			}
		}
		enqueueOpLog(model.NodeOpLog{
			TimeMs:    time.Now().UnixMilli(),
			NodeID:    nodeID,
			Cmd:       "EasyTierWriteConfig",
			RequestID: reqID,
			Success:   ifThen(success, 1, 0),
			Message:   msg,
		})
	}
	ok1, msg1 := requestWithRetrySuccess(nodeID, "WriteFile", map[string]any{
		"requestId": RandUUID(),
		"path":      "/opt/easytier/config/default.conf",
		"content":   conf,
	}, 15*time.Second, 2)
	ok2, msg2 := requestWithRetrySuccess(nodeID, "WriteFile", map[string]any{
		"requestId": RandUUID(),
		"path":      "/opt/easytier/config/default/default.conf",
		"content":   conf,
	}, 10*time.Second, 1)
	if ok1 && ok2 {
		logEasyTierConfig(nodeID, reqID, conf)
		logOp(true, "")
		return true, ""
	}
	if ok, msg := requestWithRetrySuccess(nodeID, "RunScript", map[string]any{
		"requestId": RandUUID(),
		"content":   buildEasyTierConfigScript(conf),
		"timeoutSec": 20,
	}, 30*time.Second, 1); ok {
		logEasyTierConfig(nodeID, reqID, conf)
		logOp(true, "写配置成功（脚本兜底，详情见安装日志）")
		return true, ""
	} else if msg != "" {
		logOp(false, msg)
		return false, msg
	}
	if !ok1 && msg1 != "" {
		logOp(false, msg1)
		return false, msg1
	}
	if !ok2 && msg2 != "" {
		logOp(false, msg2)
		return false, msg2
	}
	logOp(false, "")
	return false, "写配置失败"
}

func verifyEasyTierNode(nodeID int64, reqID string, op string) {
	reqID = strings.TrimSpace(reqID)
	if nodeID == 0 {
		return
	}
	script := "#!/bin/sh\n" +
		"set +e\n" +
		"ERR=\"\"\n" +
		"ACTIVE=1\n" +
		"if command -v systemctl >/dev/null 2>&1; then\n" +
		"  systemctl is-active --quiet easytier@default || systemctl is-active --quiet easytier || ACTIVE=0\n" +
		"elif command -v pgrep >/dev/null 2>&1; then\n" +
		"  pgrep -x easytier-core >/dev/null 2>&1 || ACTIVE=0\n" +
		"elif command -v pidof >/dev/null 2>&1; then\n" +
		"  pidof easytier-core >/dev/null 2>&1 || ACTIVE=0\n" +
		"fi\n" +
		"if [ \"$ACTIVE\" = \"0\" ]; then\n" +
		"  ERR=\"easytier-core not running\"\n" +
		"fi\n" +
		"PORT_OK=\"\"\n" +
		"if command -v ss >/dev/null 2>&1; then\n" +
		"  ss -lnt 2>/dev/null | awk '{print $4}' | grep -q ':15888$' && PORT_OK=1\n" +
		"elif command -v netstat >/dev/null 2>&1; then\n" +
		"  netstat -lnt 2>/dev/null | awk '{print $4}' | grep -q ':15888$' && PORT_OK=1\n" +
		"fi\n" +
		"if [ -z \"$PORT_OK\" ]; then\n" +
		"  if [ -n \"$ERR\" ]; then ERR=\"$ERR; \"; fi\n" +
		"  ERR=\"${ERR}rpc 127.0.0.1:15888 not listening\"\n" +
		"fi\n" +
		"if [ -n \"$ERR\" ]; then\n" +
		"  echo \"$ERR\"\n" +
		"  exit 2\n" +
		"fi\n" +
		"echo \"verify ok\"\n" +
		"exit 0\n"
	req := map[string]any{
		"requestId":  RandUUID(),
		"timeoutSec": 15,
		"content":    script,
	}
	res, ok := RequestOp(nodeID, "RunScript", req, 18*time.Second)
	now := time.Now().UnixMilli()
	if !ok {
		errText := "校验失败：节点未响应"
		appendEasyTierLog(nodeID, reqID, "[verify] "+errText)
		updateEasyTierRuntime(nodeID, etStatusFailed, op, errText, reqID, now)
		return
	}
	data, _ := res["data"].(map[string]interface{})
	success, _ := data["success"].(bool)
	msg, _ := data["message"].(string)
	stdout, _ := data["stdout"].(string)
	stderr, _ := data["stderr"].(string)
	out := strings.TrimSpace(strings.Join([]string{stdout, stderr, msg}, "\n"))
	if !success {
		if out == "" {
			out = "校验失败"
		}
		appendEasyTierLog(nodeID, reqID, "[verify] "+out)
		updateEasyTierRuntime(nodeID, etStatusFailed, op, tailText(out, 800), reqID, now)
	}
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
	if secret == "" {
		secret = RandUUID32()
		setCfg(etSecretKey, secret)
	}
	var n model.Node
	_ = dbpkg.DB.First(&n, nodeID).Error
	var master etMaster
	_ = json.Unmarshal([]byte(getCfg(etMasterKey)), &master)
	// lookup node config
	var nodes []etNode
	if v := getCfg(etNodesKey); v != "" {
		_ = json.Unmarshal([]byte(v), &nodes)
	}
	var self etNode
	selfIdx := -1
	for i, x := range nodes {
		if x.NodeID == nodeID {
			self = x
			selfIdx = i
			break
		}
	}
	hostName := orString(n.Name, fmt.Sprintf("node-%d", nodeID))
	devName := safeDevName(hostName)
	changed := false
	if selfIdx == -1 {
		self = etNode{NodeID: nodeID}
		changed = true
	}
	if self.IP == "" && n.ServerIP != "" {
		self.IP = n.ServerIP
		changed = true
	}
	if self.Port == 0 && n.ID != 0 {
		self.Port = pickNodePort(n)
		changed = true
	}
	ipv4Seg := fmt.Sprintf("%d", nodeID)
	if self.IPv4 != ipv4Seg {
		self.IPv4 = ipv4Seg
		changed = true
	}
	if self.PeerNodeID == nil && self.PeerIP == nil && master.NodeID != 0 && master.NodeID != nodeID {
		nid := master.NodeID
		self.PeerNodeID = &nid
		changed = true
	}
	if changed {
		if selfIdx >= 0 {
			nodes[selfIdx] = self
		} else {
			nodes = append(nodes, self)
		}
		if b, err := json.Marshal(nodes); err == nil {
			setCfg(etNodesKey, string(b))
		}
	}
	// peer lookup: 默认使用自身对外 IP+端口；若配置了对端则覆盖；若指定了 PeerIP 则优先生效
	peerIP := self.IP
	peerPort := self.Port
	if self.PeerNodeID != nil {
		found := false
		for _, x := range nodes {
			if x.NodeID == *self.PeerNodeID {
				peerIP = x.IP
				peerPort = x.Port
				found = true
				break
			}
		}
		if !found && master.NodeID == *self.PeerNodeID {
			peerIP = master.IP
			peerPort = master.Port
		}
	}
	if self.PeerIP != nil && *self.PeerIP != "" {
		peerIP = *self.PeerIP
	}
	if (self.PeerNodeID == nil && (self.PeerIP == nil || *self.PeerIP == "")) && master.NodeID != 0 && master.NodeID != nodeID {
		if master.IP != "" {
			peerIP = master.IP
		}
		if master.Port != 0 {
			peerPort = master.Port
		}
	}
	if peerIP == "" && master.NodeID != 0 && master.NodeID != nodeID {
		peerIP = master.IP
	}
	if peerPort == 0 && master.NodeID != 0 && master.NodeID != nodeID && master.Port != 0 {
		peerPort = master.Port
	}
	// bracket IPv6 for URL safety
	if strings.Contains(peerIP, ":") && !(strings.HasPrefix(peerIP, "[") && strings.HasSuffix(peerIP, "]")) {
		peerIP = "[" + peerIP + "]"
	}
	listenHost := "0.0.0.0"
	if n.ID != 0 {
		ifaceMap := loadNodeInterfaces([]model.Node{n})
		info := splitNodeIPs(n, ifaceMap[n.ID])
		if len(info.v4) == 0 && len(info.v6) > 0 {
			listenHost = "[::]"
		}
	}
	tpl := readFileDefault("easytier/default.conf")
	if strings.TrimSpace(tpl) == "" {
		tpl = `hostname = "{hostname}"
instance_name = "network-panel"
dhcp = false
ipv4 = "10.126.126.{ipv4}"
listeners = [
    "tcp://{listen}:{port}",
]
exit_nodes = []
rpc_portal = "127.0.0.1:15888"

[[peer]]
uri = "tcp://{ip}:{peer_port}"

[network_identity]
network_name = "network-panel"
network_secret = "{secret}"

[flags]
default_protocol = "tcp"
dev_name = "{dev_name}"
enable_encryption = false
enable_ipv6 = true
mtu = 1500
latency_first = false
enable_exit_node = false
no_tun = false
use_smoltcp = false
foreign_network_whitelist = "*"
disable_p2p = false
relay_all_peer_rpc = false
disable_udp_hole_punching = false
private_mode = true
enable-quic-proxy=true
`
	}
	out := tpl
	out = strings.ReplaceAll(out, "{hostname}", hostName)
	out = strings.ReplaceAll(out, "{ipv4}", ipv4Tail(self.IPv4, nodeID))
	out = strings.ReplaceAll(out, "{listen}", listenHost)
	out = strings.ReplaceAll(out, "{port}", fmt.Sprintf("%d", self.Port))
	out = strings.ReplaceAll(out, "{ip}", peerIP)
	out = strings.ReplaceAll(out, "{peer_port}", fmt.Sprintf("%d", peerPort))
	out = strings.ReplaceAll(out, "{secret}", secret)
	out = strings.ReplaceAll(out, "{dev_name}", devName)
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}

func readFileDefault(p string) string { b, _ := os.ReadFile(p); return string(b) }

type etStreamReq struct {
	Secret    string `json:"secret"`
	RequestID string `json:"requestId"`
	Chunk     string `json:"chunk"`
	Done      bool   `json:"done"`
	TimeMs    *int64 `json:"timeMs"`
	ExitCode  *int   `json:"exitCode"`
}

type etStreamEvent struct {
	Chunk    string `json:"chunk"`
	Done     bool   `json:"done"`
	TimeMs   int64  `json:"timeMs"`
	ExitCode *int   `json:"exitCode,omitempty"`
}

var (
	etStreamMu   sync.Mutex
	etStreamSubs = map[string]map[chan etStreamEvent]struct{}{}
)

func subscribeEtStream(reqID string) chan etStreamEvent {
	ch := make(chan etStreamEvent, 32)
	etStreamMu.Lock()
	if etStreamSubs[reqID] == nil {
		etStreamSubs[reqID] = map[chan etStreamEvent]struct{}{}
	}
	etStreamSubs[reqID][ch] = struct{}{}
	etStreamMu.Unlock()
	return ch
}

func unsubscribeEtStream(reqID string, ch chan etStreamEvent) {
	etStreamMu.Lock()
	if subs, ok := etStreamSubs[reqID]; ok {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(etStreamSubs, reqID)
		}
	}
	etStreamMu.Unlock()
	close(ch)
}

func publishEtStream(reqID string, evt etStreamEvent) {
	etStreamMu.Lock()
	subs := etStreamSubs[reqID]
	for ch := range subs {
		select {
		case ch <- evt:
		default:
		}
	}
	etStreamMu.Unlock()
}

func buildEasyTierInstallPayload(host string) map[string]any {
	script := readFileDefault("easytier/install.sh")
	payload := map[string]any{}
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
	return payload
}

func sanitizeForwardedProto(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, ","); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	if raw == "http" || raw == "https" {
		return raw
	}
	return ""
}

func sanitizeForwardedPort(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, ","); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	p, err := strconv.Atoi(raw)
	if err != nil || p < 1 || p > 65535 {
		return ""
	}
	return strconv.Itoa(p)
}

func sanitizeHostHeader(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	if i := strings.IndexAny(raw, "/?"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "@") {
		return ""
	}
	for _, ch := range raw {
		if ch > 127 {
			return ""
		}
		if (ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '.' || ch == '_' || ch == ':' || ch == '[' || ch == ']' {
			continue
		}
		return ""
	}
	host := raw
	port := ""
	if strings.HasPrefix(raw, "[") {
		idx := strings.Index(raw, "]")
		if idx <= 0 {
			return ""
		}
		host = raw[:idx+1]
		rest := strings.TrimSpace(raw[idx+1:])
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return ""
			}
			port = strings.TrimPrefix(rest, ":")
		}
	} else if strings.Count(raw, ":") == 1 {
		parts := strings.SplitN(raw, ":", 2)
		host = parts[0]
		port = parts[1]
	} else if strings.Count(raw, ":") > 1 {
		host = "[" + raw + "]"
	}
	if host == "" {
		return ""
	}
	if port != "" {
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return ""
		}
		return host + ":" + port
	}
	return host
}

func hostHasPort(host string) bool {
	if strings.HasPrefix(host, "[") {
		return strings.Contains(host, "]:")
	}
	return strings.Contains(host, ":")
}

func normalizePanelHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	scheme := ""
	if strings.HasPrefix(raw, "http://") {
		scheme = "http"
		raw = strings.TrimPrefix(raw, "http://")
	} else if strings.HasPrefix(raw, "https://") {
		scheme = "https"
		raw = strings.TrimPrefix(raw, "https://")
	}
	host := sanitizeHostHeader(raw)
	if host == "" {
		return ""
	}
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + host
}

func requestBaseURL(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	host := sanitizeHostHeader(c.Request.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = sanitizeHostHeader(c.Request.Host)
	}
	if host == "" {
		host = sanitizeHostHeader(c.Request.Header.Get("Host"))
	}
	if host == "" {
		return ""
	}
	scheme := sanitizeForwardedProto(c.Request.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	if !hostHasPort(host) {
		if xfPort := sanitizeForwardedPort(c.Request.Header.Get("X-Forwarded-Port")); xfPort != "" {
			if (scheme == "http" && xfPort != "80") || (scheme == "https" && xfPort != "443") {
				host = host + ":" + xfPort
			}
		}
	}
	return scheme + "://" + host
}

func resolvePanelHost(c *gin.Context) string {
	host := normalizePanelHost(getCfg("ip"))
	if host == "" {
		host = normalizePanelHost(requestBaseURL(c))
		if host != "" {
			setCfg(panelHostCacheKey, host)
		}
	}
	if host == "" {
		host = normalizePanelHost(getCfg(panelHostCacheKey))
	}
	return host
}

func buildEasyTierUninstallScript(host string) string {
	if host == "" {
		host = "/"
	}
	if strings.HasSuffix(host, "/") {
		host = strings.TrimSuffix(host, "/")
	}
	return "#!/usr/bin/env bash\nset -euo pipefail\n" +
		"SERVER=\"" + host + "\"\n" +
		"if [ \"$SERVER\" = \"/\" ]; then\n" +
		"  SERVER=\"\"\n" +
		"fi\n" +
		"INSTALL_URL=\"\"\n" +
		"STATIC_URL=\"https://panel-static.199028.xyz/network-panel/easytier/install_easytier.sh\"\n" +
		"ok=0\n" +
		"if [ -n \"$SERVER\" ]; then\n" +
		"  INSTALL_URL=\"${SERVER%/}/easytier/install_easytier.sh\"\n" +
		"  echo \"[uninstall] fetching easytier install.sh from ${INSTALL_URL}\"\n" +
		"  if wget -T 10 --tries=1 -O /tmp/easytier.sh \"$INSTALL_URL\"; then\n" +
		"    ok=1\n" +
		"  else\n" +
		"    echo \"[uninstall] panel host unavailable, trying static host\"\n" +
		"    if wget -T 10 --tries=1 -O /tmp/easytier.sh \"$STATIC_URL\"; then\n" +
		"      ok=1\n" +
		"    fi\n" +
		"  fi\n" +
		"else\n" +
		"  echo \"[uninstall] panel host empty, skipping static host\"\n" +
		"fi\n" +
		"if [ $ok -ne 1 ]; then\n" +
		"  echo \"[uninstall] install script unavailable, trying fallbacks...\"\n" +
		"  FALLBACKS=(\n" +
		"    \"https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/easytier/install_easytier.sh\"\n" +
		"    \"https://ghfast.top/https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/easytier/install_easytier.sh\"\n" +
		"    \"https://proxy.529851.xyz/https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/easytier/install_easytier.sh\"\n" +
		"  )\n" +
		"  for u in \"${FALLBACKS[@]}\"; do\n" +
		"    if wget -T 10 --tries=1 -O /tmp/easytier.sh \"$u\"; then\n" +
		"      ok=1\n" +
		"      break\n" +
		"    fi\n" +
		"  done\n" +
		"  if [ $ok -ne 1 ]; then\n" +
		"    echo \"[uninstall] failed to fetch install script from all sources\"\n" +
		"    exit 1\n" +
		"  fi\n" +
		"fi\n" +
		"chmod +x /tmp/easytier.sh\n" +
		"sudo bash /tmp/easytier.sh uninstall || true\n" +
		"sudo rm -rf /opt/easytier\n" +
		"echo \"[uninstall] done\"\n"
}

func composeOpOutput(data map[string]interface{}) string {
	if data == nil {
		return ""
	}
	stdout, _ := data["stdout"].(string)
	stderr, _ := data["stderr"].(string)
	msg, _ := data["message"].(string)
	lines := make([]string, 0, 3)
	if strings.TrimSpace(stdout) != "" {
		lines = append(lines, strings.TrimSpace(stdout))
	}
	if strings.TrimSpace(stderr) != "" {
		lines = append(lines, strings.TrimSpace(stderr))
	}
	if strings.TrimSpace(msg) != "" {
		lines = append(lines, strings.TrimSpace(msg))
	}
	return strings.Join(lines, "\n")
}

func runEasyTierInstallOp(nodeID int64, op string, host string, installTO int) (string, bool, string) {
	reqID := RandUUID()
	now := time.Now().UnixMilli()
	updateEasyTierRuntime(nodeID, etStatusInstalling, op, "", reqID, now)
	_ = dbpkg.DB.Create(&model.EasyTierResult{
		NodeID:      nodeID,
		RequestID:   reqID,
		Op:          op,
		Content:     "",
		Done:        false,
		TimeMs:      now,
		CreatedTime: now,
		UpdatedTime: now,
	}).Error
	payload := buildEasyTierInstallPayload(host)
	payload["requestId"] = reqID
	payload["timeoutSec"] = installTO
	res, ok := RequestOp(nodeID, "RunScript", payload, time.Duration(installTO)*time.Second)
	if !ok {
		errText := "安装脚本未响应"
		updateEasyTierRuntime(nodeID, etStatusFailed, op, errText, reqID, time.Now().UnixMilli())
		_ = dbpkg.DB.Model(&model.EasyTierResult{}).
			Where("node_id = ? AND request_id = ?", nodeID, reqID).
			Updates(map[string]any{"content": errText, "done": true, "updated_time": time.Now().UnixMilli()})
		return reqID, false, errText
	}
	data, _ := res["data"].(map[string]interface{})
	success := false
	if data != nil {
		if s, _ := data["success"].(bool); s {
			success = true
		}
	}
	content := composeOpOutput(data)
	if !success {
		errText := content
		if errText == "" {
			errText = "安装失败"
		}
		updateEasyTierRuntime(nodeID, etStatusFailed, op, tailText(errText, 800), reqID, time.Now().UnixMilli())
		_ = dbpkg.DB.Model(&model.EasyTierResult{}).
			Where("node_id = ? AND request_id = ?", nodeID, reqID).
			Updates(map[string]any{"content": errText, "done": true, "updated_time": time.Now().UnixMilli()})
		return reqID, false, errText
	}
	updateEasyTierRuntime(nodeID, etStatusInstalled, op, "", reqID, time.Now().UnixMilli())
	_ = dbpkg.DB.Model(&model.EasyTierResult{}).
		Where("node_id = ? AND request_id = ?", nodeID, reqID).
		Updates(map[string]any{"content": content, "done": true, "updated_time": time.Now().UnixMilli()})
	return reqID, true, ""
}

func triggerEasyTierAction(c *gin.Context, nodeID int64, action string) (string, string) {
	act := strings.ToLower(strings.TrimSpace(action))
	if act == "" {
		return "", "操作不能为空"
	}
	var node model.Node
	if err := dbpkg.DB.First(&node, nodeID).Error; err != nil {
		return "", "节点不存在"
	}
	if node.Status == nil || *node.Status != 1 {
		return "", "节点离线，无法触发"
	}
	if act == "check" {
		reqID := RandUUID()
		script := "set +e\n" +
			"ET=\"\"\n" +
			"if command -v easytier >/dev/null 2>&1; then ET=\"easytier\"; fi\n" +
			"if [ -z \"$ET\" ] && [ -x /opt/easytier/easytier ]; then ET=\"/opt/easytier/easytier\"; fi\n" +
			"if [ -z \"$ET\" ]; then echo \"STATUS:not_installed\"; exit 0; fi\n" +
			"VER=$($ET --version 2>/dev/null || $ET -V 2>/dev/null || true)\n" +
			"echo \"STATUS:installed\"\n" +
			"if [ -n \"$VER\" ]; then echo \"VERSION:$VER\"; fi\n"
		now := time.Now().UnixMilli()
		updateEasyTierRuntime(nodeID, "", "check", "", reqID, now)
		_ = dbpkg.DB.Create(&model.EasyTierResult{
			NodeID:      nodeID,
			RequestID:   reqID,
			Op:          "check",
			Content:     "",
			Done:        false,
			TimeMs:      now,
			CreatedTime: now,
			UpdatedTime: now,
		}).Error
		res, ok := RequestOp(nodeID, "RunScript", map[string]any{"requestId": reqID, "content": script}, 30*time.Second)
		if !ok {
			errText := "未响应，请稍后重试"
			updateEasyTierRuntime(nodeID, etStatusFailed, "check", errText, reqID, time.Now().UnixMilli())
			_ = dbpkg.DB.Model(&model.EasyTierResult{}).
				Where("node_id = ? AND request_id = ?", nodeID, reqID).
				Updates(map[string]any{"content": errText, "done": true, "updated_time": time.Now().UnixMilli()})
			return "", errText
		}
		data, _ := res["data"].(map[string]interface{})
		success := false
		if data != nil {
			if s, _ := data["success"].(bool); s {
				success = true
			}
		}
		content := composeOpOutput(data)
		status := etStatusNotInstalled
		version := ""
		stdout, _ := data["stdout"].(string)
		for _, line := range strings.Split(stdout, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "STATUS:") {
				v := strings.TrimPrefix(line, "STATUS:")
				if v == "installed" {
					status = etStatusInstalled
				} else if v == "not_installed" {
					status = etStatusNotInstalled
				}
			}
			if strings.HasPrefix(line, "VERSION:") {
				version = strings.TrimSpace(strings.TrimPrefix(line, "VERSION:"))
			}
		}
		if !success {
			errText := content
			if errText == "" {
				errText = "检查失败"
			}
			updateEasyTierRuntime(nodeID, etStatusFailed, "check", tailText(errText, 800), reqID, time.Now().UnixMilli())
			_ = dbpkg.DB.Model(&model.EasyTierResult{}).
				Where("node_id = ? AND request_id = ?", nodeID, reqID).
				Updates(map[string]any{"content": errText, "done": true, "updated_time": time.Now().UnixMilli()})
			return reqID, ""
		}
		updateEasyTierRuntime(nodeID, status, "check", "", reqID, time.Now().UnixMilli())
		if version != "" {
			base, _ := getRuntimeCached(nodeID)
			rt := base
			rt.NodeID = nodeID
			rt.EasyTierVersion = &version
			rt.UpdatedTime = time.Now().UnixMilli()
			setRuntime(rt)
		}
		_ = dbpkg.DB.Model(&model.EasyTierResult{}).
			Where("node_id = ? AND request_id = ?", nodeID, reqID).
			Updates(map[string]any{"content": content, "done": true, "updated_time": time.Now().UnixMilli()})
		return reqID, ""
	}
	if act != "install" && act != "reinstall" && act != "upgrade" && act != "uninstall" {
		return "", "不支持的操作"
	}
	if !beginEtInstall(nodeID) {
		return "", "该节点已有安装任务进行中"
	}
	reqID := RandUUID()
	setEtRequestOp(reqID, act)
	now := time.Now().UnixMilli()
	updateEasyTierRuntime(nodeID, etStatusInstalling, act, "", reqID, now)
	_ = dbpkg.DB.Create(&model.EasyTierResult{
		NodeID:      nodeID,
		RequestID:   reqID,
		Op:          act,
		Content:     "",
		Done:        false,
		TimeMs:      now,
		CreatedTime: now,
		UpdatedTime: now,
	}).Error
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	endpoint := fmt.Sprintf("%s://%s/api/v1/easytier/stream", scheme, c.Request.Host)
	host := resolvePanelHost(c)
	payload := map[string]any{
		"requestId": reqID,
		"endpoint":  endpoint,
		"secret":    node.Secret,
	}
	if act == "uninstall" {
		payload["content"] = buildEasyTierUninstallScript(host)
	} else {
		for k, v := range buildEasyTierInstallPayload(host) {
			payload[k] = v
		}
	}
	if err := sendWSCommand(node.ID, "RunStreamScript", payload); err != nil {
		endEtInstall(node.ID)
		clearEtRequestOp(reqID)
		updateEasyTierRuntime(node.ID, etStatusFailed, act, "未响应，请稍后重试", reqID, time.Now().UnixMilli())
		return "", "未响应，请稍后重试"
	}
	return reqID, ""
}

// EasyTierOperate triggers install/upgrade/uninstall per node with streaming logs
func EasyTierOperate(c *gin.Context) {
	var p struct {
		NodeID int64  `json:"nodeId" binding:"required"`
		Action string `json:"action" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	reqID, msg := triggerEasyTierAction(c, p.NodeID, p.Action)
	if msg != "" {
		c.JSON(http.StatusOK, response.ErrMsg(msg))
		return
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"requestId": reqID}))
}

// EasyTierOperateBatch batch trigger install/upgrade/uninstall
func EasyTierOperateBatch(c *gin.Context) {
	var p struct {
		NodeIDs []int64 `json:"nodeIds" binding:"required"`
		Action  string  `json:"action" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil || len(p.NodeIDs) == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	results := make([]map[string]any, 0, len(p.NodeIDs))
	for _, nid := range p.NodeIDs {
		reqID, msg := triggerEasyTierAction(c, nid, p.Action)
		item := map[string]any{"nodeId": nid}
		if msg != "" {
			item["error"] = msg
		} else {
			item["requestId"] = reqID
		}
		results = append(results, item)
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"results": results}))
}

// EasyTierStreamPush receives streaming logs from agents
func EasyTierStreamPush(c *gin.Context) {
	var p etStreamReq
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if strings.TrimSpace(p.Secret) == "" {
		c.JSON(http.StatusOK, response.ErrMsg("secret 不能为空"))
		return
	}
	var node model.Node
	if err := dbpkg.DB.Where("secret = ?", p.Secret).First(&node).Error; err != nil {
		c.JSON(http.StatusForbidden, response.ErrMsg("节点未授权"))
		return
	}
	now := time.Now().UnixMilli()
	if p.TimeMs != nil && *p.TimeMs > 0 {
		now = *p.TimeMs
	}
	op := getEtRequestOp(p.RequestID)
	var res model.EasyTierResult
	if err := dbpkg.DB.Where("node_id = ? AND request_id = ?", node.ID, p.RequestID).First(&res).Error; err != nil || res.ID == 0 {
		res = model.EasyTierResult{
			NodeID:      node.ID,
			RequestID:   p.RequestID,
			Op:          op,
			Content:     p.Chunk,
			Done:        p.Done,
			TimeMs:      now,
			CreatedTime: now,
			UpdatedTime: now,
		}
		_ = dbpkg.DB.Create(&res).Error
	} else {
		content := res.Content
		if p.Chunk != "" {
			if content != "" && !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			content += p.Chunk
		}
		res.Content = content
		res.Done = p.Done
		res.TimeMs = now
		res.UpdatedTime = now
		if op != "" && res.Op == "" {
			res.Op = op
		}
		_ = dbpkg.DB.Model(&model.EasyTierResult{}).Where("id = ?", res.ID).Updates(map[string]any{
			"content":      res.Content,
			"done":         res.Done,
			"time_ms":      res.TimeMs,
			"updated_time": res.UpdatedTime,
			"op":           res.Op,
		})
	}
	if op == "" {
		op = res.Op
	}
	if !p.Done {
		status := etStatusInstalling
		if shouldMarkDownloading(p.Chunk) {
			status = etStatusDownloading
		}
		updateEasyTierRuntime(node.ID, status, op, "", p.RequestID, now)
	} else {
		fail := false
		if p.ExitCode != nil && *p.ExitCode != 0 {
			fail = true
		}
		if fail {
			errText := strings.TrimSpace(p.Chunk)
			if errText == "" {
				errText = strings.TrimSpace(res.Content)
			}
			if errText == "" {
				errText = "安装失败"
			}
			updateEasyTierRuntime(node.ID, etStatusFailed, op, tailText(errText, 800), p.RequestID, now)
		} else {
			status := etStatusInstalled
			if op == "uninstall" {
				status = etStatusNotInstalled
			}
			updateEasyTierRuntime(node.ID, status, op, "", p.RequestID, now)
			if op != "uninstall" {
				go verifyEasyTierNode(node.ID, p.RequestID, op)
			}
		}
		endEtInstall(node.ID)
		clearEtRequestOp(p.RequestID)
	}
	publishEtStream(p.RequestID, etStreamEvent{Chunk: p.Chunk, Done: p.Done, TimeMs: now, ExitCode: p.ExitCode})
	c.JSON(http.StatusOK, response.OkNoData())
}

// EasyTierLog returns current log snapshot
func EasyTierLog(c *gin.Context) {
	var p struct {
		RequestID string `json:"requestId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var res model.EasyTierResult
	if err := dbpkg.DB.Where("request_id = ?", p.RequestID).Order("time_ms desc").First(&res).Error; err != nil || res.ID == 0 {
		c.JSON(http.StatusOK, response.Ok(map[string]any{"content": "", "timeMs": nil, "done": false, "requestId": p.RequestID}))
		return
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{
		"content":   res.Content,
		"timeMs":    res.TimeMs,
		"done":      res.Done,
		"requestId": res.RequestID,
		"op":        res.Op,
	}))
}

// EasyTierLogStream streams logs to clients (SSE)
func EasyTierLogStream(c *gin.Context) {
	reqID := strings.TrimSpace(c.Query("requestId"))
	if reqID == "" {
		c.JSON(http.StatusOK, response.ErrMsg("requestId 不能为空"))
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Writer.Flush()
	ch := subscribeEtStream(reqID)
	defer unsubscribeEtStream(reqID, ch)
	notify := c.Request.Context().Done()
	for {
		select {
		case <-notify:
			return
		case evt := <-ch:
			b, _ := json.Marshal(map[string]any{
				"event":    "log",
				"data":     evt.Chunk,
				"done":     evt.Done,
				"timeMs":   evt.TimeMs,
				"exitCode": evt.ExitCode,
			})
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
			c.Writer.Flush()
			if evt.Done {
				return
			}
		}
	}
}
func RandUUID32() string              { v := RandUUID(); sum := md5.Sum([]byte(v)); return fmt.Sprintf("%x", sum) }

// ipv4Tail returns the last numeric segment for template placeholder.
func ipv4Tail(v string, nodeID int64) string {
	v = strings.TrimSpace(v)
	target := fmt.Sprintf("%d", nodeID)
	if v == "" {
		return target
	}
	// digits only
	if allDigits(v) {
		if v == target {
			return v
		}
		return target
	}
	// dotted ipv4
	if strings.Contains(v, ".") {
		parts := strings.Split(v, ".")
		last := strings.TrimSpace(parts[len(parts)-1])
		if allDigits(last) && last == target {
			return last
		}
	}
	return target
}

func parseInterfaceList(raw *string) []string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil
	}
	return out
}

func safeDevName(name string) string {
	name = strings.TrimSpace(name)
	if name != "" && isSafeDevName(name) {
		return name
	}
	return RandUUID32()[:8]
}

func isSafeDevName(name string) bool {
	if name == "" || len(name) > 15 {
		return false
	}
	for _, ch := range name {
		if ch > 127 {
			return false
		}
		if (ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '_' {
			continue
		}
		return false
	}
	return true
}

func hasIP(list []string, ip string) bool {
	if ip == "" || len(list) == 0 {
		return false
	}
	for _, v := range list {
		if v == ip {
			return true
		}
	}
	return false
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
	_, _ = writeEasyTierConfig(p.NodeID, conf, "")
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
		_, _ = writeEasyTierConfig(n.NodeID, conf, "")
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
	ensureMasterJoined(m.NodeID, m.IP, m.Port, resolvePanelHost(c))
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
