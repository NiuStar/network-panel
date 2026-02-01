package controller

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
)

const (
	singboxConnectURLDefault = "http://www.google.com/generate_204"
	singboxSpeedURLDefault   = "https://speed.cloudflare.com/__down?bytes=20000000"
)

type singboxTestReq struct {
	ForwardID int64  `json:"forwardId" binding:"required"`
	NodeID    int64  `json:"nodeId"`
	Mode      string `json:"mode"` // connect | speed
}

func ForwardSingboxTest(c *gin.Context) {
	var p singboxTestReq
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	if mode == "" {
		mode = "connect"
	}
	if mode != "connect" && mode != "speed" {
		c.JSON(http.StatusOK, response.ErrMsg("mode仅支持connect/speed"))
		return
	}

	var f model.Forward
	if err := dbpkg.DB.First(&f, p.ForwardID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("转发不存在"))
		return
	}
	// auth: admin or owner or user has tunnel permission
	if roleInf, ok := c.Get("role_id"); ok {
		if role, _ := roleInf.(int); role != 0 {
			if uidInf, ok2 := c.Get("user_id"); ok2 {
				uid := uidInf.(int64)
				if f.UserID != uid {
					var utCnt int64
					dbpkg.DB.Model(&model.UserTunnel{}).Where("user_id=? and tunnel_id=?", uid, f.TunnelID).Count(&utCnt)
					if utCnt == 0 {
						c.JSON(http.StatusForbidden, response.ErrMsg("权限不足"))
						return
					}
				}
			}
		}
	}

	var t model.Tunnel
	if err := dbpkg.DB.First(&t, f.TunnelID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("隧道不存在"))
		return
	}

	// choose node for test
	nodeID := p.NodeID
	if nodeID <= 0 {
		nodeID = t.InNodeID
	}
	var testNode model.Node
	if err := dbpkg.DB.First(&testNode, nodeID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("测试节点不存在"))
		return
	}
	if testNode.Status != nil && *testNode.Status != 1 {
		c.JSON(http.StatusOK, response.ErrMsg("测试节点离线"))
		return
	}
	if !IsNodeWSOnline(nodeID) {
		c.JSON(http.StatusOK, response.ErrMsg("测试节点未连接WS"))
		return
	}

    outbound, err := buildSingboxOutboundForForward(f, t, nodeID)
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg(err.Error()))
		return
	}

	// prepare log capture for all involved nodes (gost only)
	captureNodes := collectForwardNodesForLog(t)
	reqID := RandUUID()
	captureIDs := map[int64]string{}
	for _, nid := range captureNodes {
		if nid <= 0 || !IsNodeWSOnline(nid) {
			continue
		}
		cid := fmt.Sprintf("%s-%d", reqID, nid)
		captureIDs[nid] = cid
		_ = sendWSCommand(nid, "LogCaptureStart", map[string]interface{}{
			"requestId": cid,
			"target":    "agent",
		})
	}

	url := singboxConnectURLDefault
	if mode == "speed" {
		url = singboxSpeedURLDefault
		if v := getConfigString("singbox_speed_url"); v != "" {
			url = v
		}
	} else {
		if v := getConfigString("singbox_connect_url"); v != "" {
			url = v
		}
	}

	payload := map[string]interface{}{
		"requestId":  reqID,
		"mode":       mode,
		"url":        url,
		"duration":   3,
		"outbound":   outbound,
		"forwardId":  f.ID,
		"forwardTag": f.Name,
	}

	timeout := 10 * time.Second
	if mode == "speed" {
		timeout = 18 * time.Second
	}
	if err := sendWSCommand(nodeID, "SingboxTest", payload); err != nil {
		stopLogCaptureForNodes(captureIDs, false)
		c.JSON(http.StatusOK, response.ErrMsg("发送测试指令失败:"+err.Error()))
		return
	}
	res, ok := RequestSingboxTest(nodeID, payload, timeout)
	if !ok || res == nil {
		logs := stopLogCaptureForNodes(captureIDs, true)
		c.JSON(http.StatusOK, response.Ok(map[string]any{
			"success": false,
			"message": "测试超时或无响应",
			"logs":    logs,
		}))
		return
	}
	data, _ := res["data"].(map[string]interface{})
	if data == nil {
		data = map[string]interface{}{}
	}
	// if test failed, collect logs
	if okFlag, _ := data["success"].(bool); !okFlag {
		data["logs"] = stopLogCaptureForNodes(captureIDs, true)
	} else {
		stopLogCaptureForNodes(captureIDs, false)
	}
	c.JSON(http.StatusOK, response.Ok(data))
}

// collectForwardNodesForLog returns all nodes involved in a tunnel path (entry, mids, exit).
func collectForwardNodesForLog(t model.Tunnel) []int64 {
	seen := map[int64]struct{}{}
	add := func(id int64) {
		if id > 0 {
			seen[id] = struct{}{}
		}
	}
	add(t.InNodeID)
	for _, id := range getTunnelPathNodes(t.ID) {
		add(id)
	}
	if t.OutNodeID != nil {
		add(*t.OutNodeID)
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

// stopLogCaptureForNodes sends LogCaptureStop and optionally collects logs.
func stopLogCaptureForNodes(captureIDs map[int64]string, collect bool) []map[string]any {
	out := make([]map[string]any, 0, len(captureIDs))
	if len(captureIDs) == 0 {
		return out
	}
	names := map[int64]string{}
	if collect {
		var ids []int64
		for nid := range captureIDs {
			ids = append(ids, nid)
		}
		if len(ids) > 0 {
			var nodes []model.Node
			dbpkg.DB.Where("id IN ?", ids).Find(&nodes)
			for _, n := range nodes {
				names[n.ID] = n.Name
			}
		}
	}
	for nid, cid := range captureIDs {
		if !collect {
			_ = sendWSCommand(nid, "LogCaptureStop", map[string]interface{}{
				"requestId": cid,
				"target":    "agent",
			})
			continue
		}
		res, ok := RequestLogCaptureStop(nid, map[string]interface{}{
			"requestId": cid,
			"target":    "agent",
		}, 8*time.Second)
		entry := map[string]any{
			"nodeId":   nid,
			"nodeName": names[nid],
		}
		if ok && res != nil {
			if data, _ := res["data"].(map[string]interface{}); data != nil {
				if msg, _ := data["message"].(string); msg != "" {
					entry["message"] = msg
				}
				if src, _ := data["source"].(string); src != "" {
					entry["source"] = src
				}
				if logText, _ := data["log"].(string); logText != "" {
					entry["log"] = filterTestLog(logText)
				}
				if okFlag, _ := data["success"].(bool); !okFlag {
					entry["error"] = true
				}
			}
		} else {
			entry["error"] = true
			entry["message"] = "log capture timeout"
		}
		out = append(out, entry)
	}
	return out
}

func buildSingboxOutboundForForward(f model.Forward, t model.Tunnel, testNodeID int64) (map[string]interface{}, error) {
	baseName := strings.TrimSpace(f.Name)
	if baseName == "" {
		baseName = "forward"
	}
	baseGroup := strings.TrimSpace(f.Group)
	if baseGroup == "" {
		baseGroup = "默认"
	}
	var entryNode model.Node
	if err := dbpkg.DB.First(&entryNode, t.InNodeID).Error; err != nil {
		return nil, fmt.Errorf("入口节点不存在")
	}
    entryHost := subscriptionEntryHost(t, entryNode)
	if entryHost == "" {
		return nil, fmt.Errorf("入口IP为空")
	}
	if f.InPort <= 0 {
		return nil, fmt.Errorf("入口端口无效")
	}
	testPort := f.InPort
	if dp := directExitPortForTunnel(t); dp > 0 {
		testPort = dp
	}
	var ext *model.ExitNodeExternal
	if t.OutExitID != nil {
		var e model.ExitNodeExternal
		if err := dbpkg.DB.First(&e, *t.OutExitID).Error; err == nil {
			ext = &e
		}
	}
	outIDs := map[int64]struct{}{}
	if t.OutNodeID != nil {
		outIDs[*t.OutNodeID] = struct{}{}
	}
	outIDs[t.InNodeID] = struct{}{}
	idList := make([]int64, 0, len(outIDs))
	for id := range outIDs {
		idList = append(idList, id)
	}
	ssMap := map[int64]model.ExitSetting{}
	anyTLSMap := map[int64]model.AnyTLSSetting{}
	if len(idList) > 0 {
		var ss []model.ExitSetting
		dbpkg.DB.Where("node_id IN ?", idList).Find(&ss)
		for _, s := range ss {
			ssMap[s.NodeID] = s
		}
		var ats []model.AnyTLSSetting
		dbpkg.DB.Where("node_id IN ?", idList).Find(&ats)
		for _, a := range ats {
			anyTLSMap[a.NodeID] = a
		}
	}

	proto, cipher, password, params := resolveExitProtocol(t, ext, ssMap, anyTLSMap)
	if strings.TrimSpace(proto) == "" {
		return nil, fmt.Errorf("出口协议为空")
	}
	if !isSupportedProtocol(proto) {
		if strings.EqualFold(strings.TrimSpace(proto), "tls") {
			return nil, fmt.Errorf("协议为 tls，未映射，请在出口节点里维护真实协议")
		}
		return nil, fmt.Errorf("不支持协议:%s", proto)
	}
	if proto == "ss" && (cipher == "" || password == "") {
		return nil, fmt.Errorf("SS缺少加密或密码")
	}
	if proto == "anytls" && password == "" {
		return nil, fmt.Errorf("AnyTLS缺少密码")
	}
	if !requiredParamsReady(proto, subProxy{Type: proto, Cipher: cipher, Password: password, Params: params}, params) {
		return nil, fmt.Errorf("协议参数不完整")
	}
	if testPort == f.InPort && t.Type == 1 && len(getTunnelPathNodes(t.ID)) == 0 && t.OutExitID == nil {
		if (t.OutNodeID == nil || (t.OutNodeID != nil && *t.OutNodeID == t.InNodeID)) && (proto == "anytls" || proto == "ss") {
			if proto == "anytls" {
				if at, ok := anyTLSMap[t.InNodeID]; ok && at.Port > 0 {
					testPort = at.Port
				}
			} else if proto == "ss" {
				if es, ok := ssMap[t.InNodeID]; ok && es.Port > 0 {
					testPort = es.Port
				}
			}
		}
	}
    // avoid hairpin/NAT loop when testing on the same node as entry
    if testNodeID > 0 && testNodeID == t.InNodeID {
        if isLikelyEntryHost(entryHost, entryNode, t.InIP) {
            if strings.Contains(entryHost, ":") {
                entryHost = "::1"
            } else {
                entryHost = "127.0.0.1"
            }
        }
    }

    item := subProxy{
		ID:       f.ID,
		Name:     baseName,
		Group:    baseGroup,
		Type:     proto,
		Server:   entryHost,
		Port:     testPort,
		Cipher:   cipher,
		Password: password,
		Params:   params,
	}
	ob := buildSingboxOutbound(proto, item, params)
	if len(ob) == 0 {
		return nil, fmt.Errorf("协议参数不完整")
	}
	ob["tag"] = "proxy"
	return ob, nil
}

func isLikelyEntryHost(host string, entryNode model.Node, inIP string) bool {
	if host == "" {
		return false
	}
	if host == inIP || host == entryNode.ServerIP || host == entryNode.IP {
		return true
	}
	return false
}

func filterTestLog(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, "singbox_test") {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
