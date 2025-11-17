package controller

import (
    "net/http"
    "time"
    "fmt"
    "strings"

    "github.com/gin-gonic/gin"
    "network-panel/golang-backend/internal/app/dto"
    "network-panel/golang-backend/internal/app/model"
    "network-panel/golang-backend/internal/app/response"
    dbpkg "network-panel/golang-backend/internal/db"
    "strconv"
)

// POST /api/v1/node/create
func NodeCreate(c *gin.Context) {
	var req dto.NodeDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if req.PortSta < 1 || req.PortSta > 65535 || req.PortEnd < 1 || req.PortEnd > 65535 || req.PortEnd < req.PortSta {
		c.JSON(http.StatusOK, response.ErrMsg("端口范围无效"))
		return
	}
	now := time.Now().UnixMilli()
	status := 0
    n := model.Node{BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now, Status: &status}, Name: req.Name, IP: req.IP, ServerIP: req.ServerIP, PortSta: req.PortSta, PortEnd: req.PortEnd}
    n.PriceCents = req.PriceCents
    // prefer cycleMonths, fallback to cycleDays
    if req.CycleMonths != nil {
        if d := monthsToDays(*req.CycleMonths); d > 0 { tmp := d; n.CycleDays = &tmp }
    } else {
        n.CycleDays = req.CycleDays
    }
    n.StartDateMs = req.StartDateMs
	// simple secret
	n.Secret = RandUUID()
	if err := dbpkg.DB.Create(&n).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点创建失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("节点创建成功"))
}

// POST /api/v1/node/list
func NodeList(c *gin.Context) {
    var nodes []model.Node
    dbpkg.DB.Find(&nodes)
    // build last-seen map from node_sysinfo (latest sample per node)
    type rec struct{ NodeID int64; TimeMs int64 }
    var recs []rec
    _ = dbpkg.DB.Raw("select node_id, max(time_ms) as time_ms from node_sys_info group by node_id").Scan(&recs)
    lastSeen := map[int64]int64{}
    for _, r := range recs { lastSeen[r.NodeID] = r.TimeMs }
    now := time.Now().UnixMilli()
    // consider offline if no sysinfo within threshold
    thresholdMs := int64(30 * 1000) // 30s
    if v := c.Request.URL.Query().Get("offline_threshold_ms"); v != "" {
        if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 { thresholdMs = n }
    }
    // map to output adding cycleMonths for clarity; keep other fields
    outs := make([]map[string]any, 0, len(nodes))
    for _, n := range nodes {
        // read last known health flags (in-memory)
        healthMu.RLock()
        hf, ok := nodeHealth[n.ID]
        healthMu.RUnlock()
        m := map[string]any{
            "id": n.ID,
            "name": n.Name,
            "ip": n.IP,
            "serverIp": n.ServerIP,
            "portSta": n.PortSta,
            "portEnd": n.PortEnd,
            "version": n.Version,
            "status": n.Status,
            "priceCents": n.PriceCents,
            "startDateMs": n.StartDateMs,
            // health flags
            "gostApi":     ifThen(ok && hf.GostAPI, 1, 0),
            "gostRunning": ifThen(ok && hf.GostRunning, 1, 0),
        }
        // override status by last-seen if stale
        if ts, ok2 := lastSeen[n.ID]; ok2 {
            if now-ts > thresholdMs {
                m["status"] = 0
            }
        }
        // derive cycleMonths from stored cycleDays
        if n.CycleDays != nil {
            cd := *n.CycleDays
            var cm *int
            switch cd {
            case 30:
                x := 1; cm = &x
            case 90:
                x := 3; cm = &x
            case 180:
                x := 6; cm = &x
            case 365:
                x := 12; cm = &x
            default:
                // leave nil
            }
            if cm != nil { m["cycleMonths"] = *cm } else { m["cycleDays"] = cd }
        }
        outs = append(outs, m)
    }
    c.JSON(http.StatusOK, response.Ok(outs))
}

// POST /api/v1/node/update
func NodeUpdate(c *gin.Context) {
	var req dto.NodeUpdateDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var n model.Node
	if err := dbpkg.DB.First(&n, req.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	if req.PortSta < 1 || req.PortSta > 65535 || req.PortEnd < 1 || req.PortEnd > 65535 || req.PortEnd < req.PortSta {
		c.JSON(http.StatusOK, response.ErrMsg("端口范围无效"))
		return
	}
	n.Name, n.IP, n.ServerIP, n.PortSta, n.PortEnd = req.Name, req.IP, req.ServerIP, req.PortSta, req.PortEnd
    if req.PriceCents != nil {
        n.PriceCents = req.PriceCents
    }
    if req.CycleMonths != nil {
        if d := monthsToDays(*req.CycleMonths); d > 0 { tmp := d; n.CycleDays = &tmp }
    } else if req.CycleDays != nil {
        n.CycleDays = req.CycleDays
    }
	if req.StartDateMs != nil {
		n.StartDateMs = req.StartDateMs
	}
	n.UpdatedTime = time.Now().UnixMilli()
    if err := dbpkg.DB.Save(&n).Error; err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("节点更新失败"))
        return
    }
	// update tunnels referencing IPs
	dbpkg.DB.Model(&model.Tunnel{}).Where("in_node_id = ?", n.ID).Update("in_ip", n.IP)
	dbpkg.DB.Model(&model.Tunnel{}).Where("out_node_id = ?", n.ID).Update("out_ip", n.ServerIP)
	c.JSON(http.StatusOK, response.OkMsg("节点更新成功"))
}

func monthsToDays(m int) int {
    switch m {
    case 1:
        return 30
    case 3:
        return 90
    case 6:
        return 180
    case 12:
        return 365
    default:
        if m <= 0 { return 0 }
        return m * 30
    }
}

// POST /api/v1/node/delete
func NodeDelete(c *gin.Context) {
    var p struct {
        ID         int64 `json:"id"`
        Uninstall  bool  `json:"uninstall"`
    }
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
    // usage checks
    var cnt int64
    dbpkg.DB.Model(&model.Tunnel{}).Where("in_node_id = ?", p.ID).Or("out_node_id = ?", p.ID).Count(&cnt)
    if cnt > 0 {
        c.JSON(http.StatusOK, response.ErrMsg("该节点仍被隧道使用"))
        return
    }
    // best-effort uninstall agent on node if requested
    if p.Uninstall {
        _ = sendWSCommand(p.ID, "UninstallAgent", map[string]any{"reason": "node_deleted"})
    }
    if err := dbpkg.DB.Delete(&model.Node{}, p.ID).Error; err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("节点删除失败"))
        return
    }
    c.JSON(http.StatusOK, response.OkMsg("节点删除成功"))
}

// POST /api/v1/node/install
func NodeInstallCmd(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var n model.Node
	if err := dbpkg.DB.First(&n, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	// read config ip from vite_config
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", "ip").First(&cfg).Error; err != nil || cfg.Value == "" {
		c.JSON(http.StatusOK, response.ErrMsg("请先前往网站配置中设置ip"))
		return
	}
	server := wrapIPv6(cfg.Value)
	// Pull install.sh from the deployed service instead of GitHub raw
	// Assumes the service exposes GET /install.sh on the same address stored in vite_config.ip
	// Example: ip = 1.2.3.4:6365 or [2001:db8::1]:6365
	cmd := "curl -fsSL http://" + server + "/install.sh -o ./install.sh && chmod +x ./install.sh && ./install.sh -a " + server + " -s " + n.Secret
	c.JSON(http.StatusOK, response.Ok(cmd))
}

// POST /api/v1/node/ops {nodeId, limit}
func NodeOps(c *gin.Context) {
    var p struct{ NodeID int64 `json:"nodeId"`; Limit int `json:"limit"`; RequestID string `json:"requestId"` }
    if err := c.ShouldBindJSON(&p); err != nil { c.JSON(http.StatusOK, response.ErrMsg("参数错误")); return }
    if p.Limit <= 0 || p.Limit > 1000 { p.Limit = 200 }
    // If requestId provided, return all logs for this diagnosis across nodes (ignore nodeId), and include nodeName
    if strings.TrimSpace(p.RequestID) != "" {
        type item struct {
            model.NodeOpLog
            NodeName string `json:"nodeName"`
        }
        var list []model.NodeOpLog
        dbpkg.DB.Where("request_id = ?", p.RequestID).Order("time_ms asc").Limit(p.Limit).Find(&list)
        // build nodeId -> name map
        var nodes []model.Node
        dbpkg.DB.Find(&nodes)
        names := map[int64]string{}
        for _, n := range nodes { names[n.ID] = n.Name }
        out := make([]item, 0, len(list))
        for _, it := range list {
            out = append(out, item{NodeOpLog: it, NodeName: names[it.NodeID]})
        }
        c.JSON(http.StatusOK, response.Ok(map[string]any{"ops": out}))
        return
    }
    // else fallback: by node or recent
    var list []model.NodeOpLog
    if p.NodeID > 0 {
        dbpkg.DB.Where("node_id = ?", p.NodeID).Order("time_ms desc").Limit(p.Limit).Find(&list)
    } else {
        dbpkg.DB.Order("time_ms desc").Limit(p.Limit).Find(&list)
    }
    c.JSON(http.StatusOK, response.Ok(map[string]any{"ops": list}))
}

// POST /api/v1/node/restart-gost {nodeId}
// Ask agent to restart gost service and wait for result if supported.
func NodeRestartGost(c *gin.Context) {
    var p struct{ NodeID int64 `json:"nodeId"` }
    if err := c.ShouldBindJSON(&p); err != nil { c.JSON(http.StatusOK, response.ErrMsg("参数错误")); return }
    if p.NodeID <= 0 { c.JSON(http.StatusOK, response.ErrMsg("参数错误")); return }
    // ensure node exists
    var n model.Node
    if err := dbpkg.DB.First(&n, p.NodeID).Error; err != nil { c.JSON(http.StatusOK, response.ErrMsg("节点不存在")); return }
    // Prefer RestartService with name=gost to get explicit success/failure
    req := map[string]interface{}{"requestId": RandUUID(), "name": "gost"}
    if res, ok := RequestOp(p.NodeID, "RestartService", req, 8*time.Second); ok {
        // parse result
        data, _ := res["data"].(map[string]interface{})
        succ := false
        msg := ""
        if data != nil {
            if v, ok := data["success"].(bool); ok { succ = v }
            if v, ok := data["message"].(string); ok { msg = v }
        }
        c.JSON(http.StatusOK, response.Ok(map[string]any{"success": succ, "message": msg}))
        return
    }
    // Fallback: fire-and-forget old command; return timeout message
    _ = sendWSCommand(p.NodeID, "RestartGost", map[string]any{"reason": "manual_from_ui"})
    c.JSON(http.StatusOK, response.Ok(map[string]any{"success": false, "message": "agent未回执，已下发重启命令"}))
}

// POST /api/v1/node/enable-gost-api {nodeId}
// Ask agent to enable top-level GOST Web API (write api{} then restart gost)
func NodeEnableGostAPI(c *gin.Context) {
    var p struct{ NodeID int64 `json:"nodeId" binding:"required"` }
    if err := c.ShouldBindJSON(&p); err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
        return
    }
    var node model.Node
    if err := dbpkg.DB.First(&node, p.NodeID).Error; err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
        return
    }
    _ = sendWSCommand(node.ID, "EnableGostAPI", map[string]any{"from": "manual"})
    c.JSON(http.StatusOK, response.OkNoData())
}

// utils (local)
func wrapIPv6(hostport string) string {
	// naive: if value contains ':' more than once and not wrapped, wrap host
	if len(hostport) > 0 && hostport[0] == '[' {
		return hostport
	}
	colon := 0
	for _, ch := range hostport {
		if ch == ':' {
			colon++
		}
	}
	if colon < 2 {
		return hostport
	}
	// split last ':'
	last := -1
	for i := len(hostport) - 1; i >= 0; i-- {
		if hostport[i] == ':' {
			last = i
			break
		}
	}
	if last == -1 {
		return "[" + hostport + "]"
	}
	return "[" + hostport[:last] + "]" + hostport[last:]
}

func RandUUID() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }
