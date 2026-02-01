package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/dto"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

// NodeSelfCheckRequest for quick node self-check.
type NodeSelfCheckRequest struct {
	NodeID int64 `json:"nodeId"`
}

// NodeCreate 创建节点
// @Summary 创建节点
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeCreateReq true "节点信息"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/create [post]
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
	var owner *int64
	if uidInf, ok := c.Get("user_id"); ok {
		uid := uidInf.(int64)
		owner = &uid
	}
	n := model.Node{BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now, Status: &status}, Name: req.Name, IP: req.IP, ServerIP: req.ServerIP, PortSta: req.PortSta, PortEnd: req.PortEnd, OwnerID: owner}
	n.PriceCents = req.PriceCents
	// prefer cycleMonths, fallback to cycleDays
	if req.CycleMonths != nil {
		if d := monthsToDays(*req.CycleMonths); d > 0 {
			tmp := d
			n.CycleDays = &tmp
		}
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

// NodeList 节点列表
// @Summary 节点列表
// @Tags node
// @Accept json
// @Produce json
// @Param offline_threshold_ms query int false "判定离线的阈值(毫秒)，默认30000"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/list [post]
// POST /api/v1/node/list
func NodeList(c *gin.Context) {
	var nodes []model.Node
	var userNodeMap map[int64]model.UserNode
	forwardNodes := map[int64]bool{}
	var uid int64
	if roleInf, ok := c.Get("role_id"); ok && roleInf != 0 {
		if uidInf, ok2 := c.Get("user_id"); ok2 {
			uid = uidInf.(int64)
			var owned []model.Node
			dbpkg.DB.Where("owner_id = ?", uid).Find(&owned)
			var shared []model.Node
			dbpkg.DB.Table("node n").
				Select("n.*").
				Joins("join user_node un on un.node_id = n.id").
				Where("un.user_id = ? AND un.status = 1", uid).
				Scan(&shared)
			seen := map[int64]bool{}
			for _, n := range owned {
				if !seen[n.ID] {
					nodes = append(nodes, n)
					seen[n.ID] = true
				}
			}
			for _, n := range shared {
				if !seen[n.ID] {
					nodes = append(nodes, n)
					seen[n.ID] = true
				}
			}
			var uns []model.UserNode
			dbpkg.DB.Where("user_id = ? AND status = 1", uid).Find(&uns)
			userNodeMap = map[int64]model.UserNode{}
			for _, un := range uns {
				userNodeMap[un.NodeID] = un
			}
			// include nodes referenced by user's forwards (entry/path/exit) to keep visibility consistent
			// even if user_node mapping is missing
			var forwards []model.Forward
			_ = dbpkg.DB.Where("user_id = ?", uid).Find(&forwards).Error
			if len(forwards) > 0 {
				tidSet := map[int64]struct{}{}
				for _, f := range forwards {
					if f.TunnelID > 0 {
						tidSet[f.TunnelID] = struct{}{}
					}
				}
				if len(tidSet) > 0 {
					ids := make([]int64, 0, len(tidSet))
					for id := range tidSet {
						ids = append(ids, id)
					}
					var tunnels []model.Tunnel
					_ = dbpkg.DB.Where("id in ?", ids).Find(&tunnels).Error
					needIDs := map[int64]bool{}
					for _, t := range tunnels {
						needIDs[t.InNodeID] = true
						if t.OutNodeID != nil {
							needIDs[*t.OutNodeID] = true
						}
						for _, pid := range getTunnelPathNodes(t.ID) {
							needIDs[pid] = true
						}
					}
					for nid := range needIDs {
						forwardNodes[nid] = true
					}
					if len(needIDs) > 0 {
						ids = ids[:0]
						for nid := range needIDs {
							if !seen[nid] {
								ids = append(ids, nid)
							}
						}
						if len(ids) > 0 {
							var extra []model.Node
							_ = dbpkg.DB.Where("id in ?", ids).Find(&extra).Error
							for _, n := range extra {
								if !seen[n.ID] {
									nodes = append(nodes, n)
									seen[n.ID] = true
								}
							}
						}
					}
				}
			}
		}
	} else {
		dbpkg.DB.Find(&nodes)
	}
	// websocket status already persisted in node.status; no sysinfo-based override
	// map to output adding cycleMonths for clarity; keep other fields
	// runtime snapshots (interfaces / used ports)
	idList := make([]int64, 0, len(nodes))
	for _, n := range nodes {
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

	outs := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		isShared := false
		assignedRanges := ""
		if uid > 0 {
			if n.OwnerID == nil || *n.OwnerID != uid {
				if un, ok := userNodeMap[n.ID]; ok {
					isShared = true
					assignedRanges = un.PortRanges
				} else if forwardNodes[n.ID] {
					isShared = true
				}
			}
		}
		// read last known health flags (in-memory)
		healthMu.RLock()
		hf, ok := nodeHealth[n.ID]
		healthMu.RUnlock()
		m := map[string]any{
			"id":                 n.ID,
			"name":               n.Name,
			"ip":                 n.IP,
			"serverIp":           n.ServerIP,
			"portSta":            n.PortSta,
			"portEnd":            n.PortEnd,
			"version":            n.Version,
			"status":             n.Status,
			"priceCents":         n.PriceCents,
			"startDateMs":        n.StartDateMs,
			"shared":             isShared,
			"assignedPortRanges": assignedRanges,
			// health flags
			"gostApi":     ifThen(ok && hf.GostAPI, 1, 0),
			"gostRunning": ifThen(ok && hf.GostRunning, 1, 0),
		}
		if rt, ok := runtimeMap[n.ID]; ok {
			if !isShared && rt.UsedPorts != nil && *rt.UsedPorts != "" {
				var list []int
				if json.Unmarshal([]byte(*rt.UsedPorts), &list) == nil {
					m["usedPorts"] = list
				}
			}
		}
		// derive cycleMonths from stored cycleDays
		if n.CycleDays != nil {
			cd := *n.CycleDays
			var cm *int
			switch cd {
			case 30:
				x := 1
				cm = &x
			case 90:
				x := 3
				cm = &x
			case 180:
				x := 6
				cm = &x
			case 365:
				x := 12
				cm = &x
			default:
				// leave nil
			}
			if cm != nil {
				m["cycleMonths"] = *cm
			} else {
				m["cycleDays"] = cd
			}
		}
		outs = append(outs, m)
	}
	c.JSON(http.StatusOK, response.Ok(outs))
}

// NodeUpdate 更新节点
// @Summary 更新节点
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeUpdateReq true "节点信息"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/update [post]
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
	if roleInf, ok := c.Get("role_id"); ok && roleInf != 0 {
		if uidInf, ok2 := c.Get("user_id"); ok2 {
			if n.OwnerID == nil || *n.OwnerID != uidInf.(int64) {
				c.JSON(http.StatusForbidden, response.ErrMsg("无权限"))
				return
			}
		}
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
		if d := monthsToDays(*req.CycleMonths); d > 0 {
			tmp := d
			n.CycleDays = &tmp
		}
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

// NodeSelfCheck runs a quick outbound connectivity check from the node.
// @Summary 节点自检
// @Tags node
// @Accept json
// @Produce json
// @Param data body NodeSelfCheckRequest true "节点ID"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/self-check [post]
func NodeSelfCheck(c *gin.Context) {
	var req NodeSelfCheckRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.NodeID <= 0 {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if _, _, _, _, _, errMsg, ok := nodeAccess(c, req.NodeID, false); !ok {
		c.JSON(http.StatusOK, response.ErrMsg(errMsg))
		return
	}
	avg, loss, ok, msg, rid := diagnosePingFromNodeCtx(
		req.NodeID,
		"1.1.1.1",
		3,
		1500,
		map[string]any{"src": "node", "step": "ping", "nodeId": req.NodeID},
	)
	avg2, loss2, ok2, msg2, rid2 := diagnoseFromNodeCtx(
		req.NodeID,
		"1.1.1.1",
		80,
		2,
		1500,
		map[string]any{"src": "node", "step": "tcp", "nodeId": req.NodeID},
	)
	c.JSON(http.StatusOK, response.Ok(map[string]any{
		"ping": map[string]any{
			"success":     ok,
			"averageTime": avg,
			"packetLoss":  loss,
			"message":     msg,
			"requestId":   rid,
			"target":      "1.1.1.1",
			"targetType":  "icmp",
		},
		"tcp": map[string]any{
			"success":     ok2,
			"averageTime": avg2,
			"packetLoss":  loss2,
			"message":     msg2,
			"requestId":   rid2,
			"target":      "1.1.1.1:80",
			"targetType":  "tcp",
		},
	}))
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
		if m <= 0 {
			return 0
		}
		return m * 30
	}
}

// NodeDelete 删除节点
// @Summary 删除节点
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeDeleteReq true "节点ID与是否卸载代理"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/delete [post]
// POST /api/v1/node/delete
func NodeDelete(c *gin.Context) {
	var p struct {
		ID        int64 `json:"id"`
		Uninstall bool  `json:"uninstall"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if roleInf, ok := c.Get("role_id"); ok && roleInf != 0 {
		if p.ID == 0 {
			c.JSON(http.StatusOK, response.ErrMsg("无权限"))
			return
		}
		if _, _, _, _, _, errMsg, ok := nodeAccess(c, p.ID, false); !ok {
			c.JSON(http.StatusOK, response.ErrMsg(errMsg))
			return
		}
	}
	// usage checks
	var cnt int64
	dbpkg.DB.Model(&model.Tunnel{}).Where("in_node_id = ?", p.ID).Or("out_node_id = ?", p.ID).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusOK, response.ErrMsg("该节点仍被隧道使用"))
		return
	}
	// permission
	if roleInf, ok := c.Get("role_id"); ok && roleInf != 0 {
		var node model.Node
		if dbpkg.DB.First(&node, p.ID).Error == nil {
			if uidInf, ok2 := c.Get("user_id"); ok2 {
				if node.OwnerID == nil || *node.OwnerID != uidInf.(int64) {
					c.JSON(http.StatusForbidden, response.ErrMsg("无权限"))
					return
				}
			}
		}
	}
	// best-effort notify agent to self-uninstall when node is removed
	_ = sendWSCommand(p.ID, "UninstallAgent", map[string]any{"reason": "node_deleted"})
	if err := dbpkg.DB.Delete(&model.Node{}, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点删除失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("节点删除成功"))
}

// NodeInstallCmd 获取节点安装命令
// @Summary 获取节点安装命令
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeInstallReq true "节点ID"
// @Success 200 {object} SwaggerNodeInstallResp
// @Router /api/v1/node/install [post]
// POST /api/v1/node/install
func NodeInstallCmd(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	n, _, _, _, _, errMsg, ok := nodeAccess(c, p.ID, false)
	if !ok {
		c.JSON(http.StatusOK, response.ErrMsg(errMsg))
		return
	}
	// read config ip from vite_config
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", "ip").First(&cfg).Error; err != nil || cfg.Value == "" {
		c.JSON(http.StatusOK, response.ErrMsg("请先前往网站配置中设置ip"))
		return
	}
	server := wrapIPv6(cfg.Value)
	staticURL := "https://panel-static.199028.xyz/network-panel/install.sh"
	ghURL := "https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/install.sh"
	localURL := "http://" + server + "/install.sh"
	buildCmd := func(url string) string {
		return "curl -fsSL " + url + " -o install.sh && chmod +x install.sh && sudo ./install.sh -a " + server + " -s " + n.Secret
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{
		"static": buildCmd(staticURL),
		"github": buildCmd(ghURL),
		"local":  buildCmd(localURL),
	}))
}

// NodeOps 查询节点操作日志
// @Summary 查询节点操作日志
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeOpsReq true "节点或请求ID，可指定limit"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/ops [post]
// POST /api/v1/node/ops {nodeId, limit}
func NodeOps(c *gin.Context) {
	var p struct {
		NodeID    int64  `json:"nodeId"`
		Limit     int    `json:"limit"`
		RequestID string `json:"requestId"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if p.Limit <= 0 || p.Limit > 1000 {
		p.Limit = 200
	}
	// If requestId provided, return all logs for this diagnosis across nodes (ignore nodeId), and include nodeName
	if strings.TrimSpace(p.RequestID) != "" {
		type item struct {
			model.NodeOpLog
			NodeName string `json:"nodeName"`
		}
		var list []model.NodeOpLog
		dbpkg.DB.Where("request_id = ?", p.RequestID).Order("time_ms asc").Limit(p.Limit).Find(&list)
		if extra := readBufferedOpLogsByReq(p.RequestID); len(extra) > 0 {
			// merge and sort asc
			list = append(list, extra...)
			sort.Slice(list, func(i, j int) bool { return list[i].TimeMs < list[j].TimeMs })
			if len(list) > p.Limit {
				list = list[:p.Limit]
			}
		}
		// build nodeId -> name map
		var nodes []model.Node
		dbpkg.DB.Find(&nodes)
		names := map[int64]string{}
		for _, n := range nodes {
			names[n.ID] = n.Name
		}
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
		if extra := readBufferedOpLogsByNode(p.NodeID, p.Limit); len(extra) > 0 {
			list = append(extra, list...)
			if len(list) > p.Limit {
				list = list[:p.Limit]
			}
		}
	} else {
		dbpkg.DB.Order("time_ms desc").Limit(p.Limit).Find(&list)
		if extra := readBufferedOpLogsByNode(0, p.Limit); len(extra) > 0 {
			list = append(extra, list...)
			if len(list) > p.Limit {
				list = list[:p.Limit]
			}
		}
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"ops": list}))
}

// NodeRestartGost 重启gost
// @Summary 重启节点上的gost
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeSimpleReq true "节点ID"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/restart-gost [post]
// POST /api/v1/node/restart-gost {nodeId}
// Ask agent to restart gost service and wait for result if supported.
func NodeRestartGost(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if p.NodeID <= 0 {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	// ensure node exists
	var n model.Node
	if err := dbpkg.DB.First(&n, p.NodeID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	// Prefer RestartService with name=gost to get explicit success/failure
	req := map[string]interface{}{"requestId": RandUUID(), "name": "gost"}
	if res, ok := RequestOp(p.NodeID, "RestartService", req, 8*time.Second); ok {
		// parse result
		data, _ := res["data"].(map[string]interface{})
		succ := false
		msg := ""
		if data != nil {
			if v, ok := data["success"].(bool); ok {
				succ = v
			}
			if v, ok := data["message"].(string); ok {
				msg = v
			}
		}
		c.JSON(http.StatusOK, response.Ok(map[string]any{"success": succ, "message": msg}))
		return
	}
	// Fallback: fire-and-forget old command; return timeout message
	_ = sendWSCommand(p.NodeID, "RestartGost", map[string]any{"reason": "manual_from_ui"})
	c.JSON(http.StatusOK, response.Ok(map[string]any{"success": false, "message": "agent未回执，已下发重启命令"}))
}

// NodeEnableGostAPI 启用gost API
// @Summary 启用节点的gost API
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeSimpleReq true "节点ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/enable-gost-api [post]
// POST /api/v1/node/enable-gost-api {nodeId}
// Ask agent to enable top-level GOST Web API (write api{} then restart gost)
func NodeEnableGostAPI(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId" binding:"required"`
	}
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

// agent stream log push
type nqStreamReq struct {
	Secret    string `json:"secret"`
	RequestID string `json:"requestId"`
	Chunk     string `json:"chunk"`
	Done      bool   `json:"done"`
	TimeMs    *int64 `json:"timeMs"`
	ExitCode  *int   `json:"exitCode"`
}

type diagStreamReq struct {
	Secret    string `json:"secret"`
	RequestID string `json:"requestId"`
	Chunk     string `json:"chunk"`
	Done      bool   `json:"done"`
	TimeMs    *int64 `json:"timeMs"`
	ExitCode  *int   `json:"exitCode"`
	Type      string `json:"type"`
}

// NodeNQStreamPush NodeQuality 流式回传
// @Summary NodeQuality 流式回传
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeNQStreamReq true "回传内容"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/nq/stream [post]
// POST /api/v1/nq/stream {secret, requestId, chunk, done?}
func NodeNQStreamPush(c *gin.Context) {
	var p nqStreamReq
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
	msg := "chunk"
	if p.Done {
		msg = "done"
	}
	_ = dbpkg.DB.Create(&model.NodeOpLog{
		TimeMs:    now,
		NodeID:    node.ID,
		Cmd:       "NQStream",
		RequestID: p.RequestID,
		Success:   1,
		Message:   msg,
		Stdout:    &p.Chunk,
	}).Error
	// append to nq_result
	var res model.NQResult
	if err := dbpkg.DB.Where("node_id = ? AND request_id = ?", node.ID, p.RequestID).First(&res).Error; err != nil || res.ID == 0 {
		res = model.NQResult{
			NodeID:      node.ID,
			RequestID:   p.RequestID,
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
		_ = dbpkg.DB.Model(&model.NQResult{}).Where("id = ?", res.ID).Updates(map[string]any{
			"content":      res.Content,
			"done":         res.Done,
			"time_ms":      res.TimeMs,
			"updated_time": res.UpdatedTime,
		})
	}
	c.JSON(http.StatusOK, response.OkNoData())
}

// NodeDiagStreamPush receives streaming logs from agent
// @Summary 节点诊断流式回传
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeNQStreamReq true "回传内容"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/diag/stream [post]
func NodeDiagStreamPush(c *gin.Context) {
	var p diagStreamReq
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
	kind := strings.TrimSpace(p.Type)
	if kind == "" {
		kind = "diag"
	}
	now := time.Now().UnixMilli()
	if p.TimeMs != nil && *p.TimeMs > 0 {
		now = *p.TimeMs
	}
	msg := "chunk"
	if p.Done {
		msg = "done"
	}
	_ = dbpkg.DB.Create(&model.NodeOpLog{
		TimeMs:    now,
		NodeID:    node.ID,
		Cmd:       "DiagStream:" + kind,
		RequestID: p.RequestID,
		Success:   1,
		Message:   msg,
		Stdout:    &p.Chunk,
	}).Error

	var res model.NodeDiagResult
	if err := dbpkg.DB.Where("node_id = ? AND request_id = ?", node.ID, p.RequestID).First(&res).Error; err != nil || res.ID == 0 {
		res = model.NodeDiagResult{
			NodeID:      node.ID,
			RequestID:   p.RequestID,
			Type:        kind,
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
		_ = dbpkg.DB.Model(&model.NodeDiagResult{}).Where("id = ?", res.ID).Updates(map[string]any{
			"content":      res.Content,
			"done":         res.Done,
			"time_ms":      res.TimeMs,
			"updated_time": res.UpdatedTime,
			"type":         kind,
		})
	}
	c.JSON(http.StatusOK, response.OkNoData())
}

// NodeGostConfig 获取gost配置
// @Summary 获取节点上的gost配置
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeSimpleReq true "节点ID"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/gost-config [post]
// POST /api/v1/node/gost-config {nodeId}
// Ask agent to read gost.json content and return
func NodeGostConfig(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	node, _, _, _, _, errMsg, ok := nodeAccess(c, p.NodeID, false)
	if !ok {
		c.JSON(http.StatusOK, response.ErrMsg(errMsg))
		return
	}
	script := "#!/bin/sh\nset +e\nfor p in /etc/gost/gost.json /usr/local/gost/gost.json ./gost.json; do if [ -f \"$p\" ]; then echo \"PATH:$p\"; cat \"$p\"; exit 0; fi; done; echo 'PATH:NOT_FOUND'; exit 0\n"
	req := map[string]any{"requestId": RandUUID(), "timeoutSec": 8, "content": script}
	if res, ok := RequestOp(node.ID, "RunScript", req, 10*time.Second); ok {
		msg := "ok"
		var so string
		if d, _ := res["data"].(map[string]any); d != nil {
			if m, _ := d["message"].(string); m != "" {
				msg = m
			}
			if s, _ := d["stdout"].(string); s != "" {
				so = s
			}
		}
		_ = dbpkg.DB.Create(&model.NodeOpLog{TimeMs: time.Now().UnixMilli(), NodeID: node.ID, Cmd: "GostConfigRead", RequestID: req["requestId"].(string), Success: 1, Message: msg, Stdout: &so}).Error
		c.JSON(http.StatusOK, response.Ok(map[string]any{
			"message": msg,
			"content": so,
		}))
		return
	}
	c.JSON(http.StatusOK, response.ErrMsg("未响应，请稍后重试"))
}

// NodeNQTest 触发节点质量测试
// @Summary 触发节点质量测试
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeSimpleReq true "节点ID"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/nq-test [post]
// POST /api/v1/node/nq-test {nodeId}
// Trigger NodeQuality test on agent via script
func NodeNQTest(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if _, _, _, _, _, errMsg, ok := nodeAccess(c, p.NodeID, false); !ok {
		c.JSON(http.StatusOK, response.ErrMsg(errMsg))
		return
	}
	var node model.Node
	if err := dbpkg.DB.First(&node, p.NodeID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	script := "#!/bin/bash\nset -e\nCMD=\"bash <(curl -fsSL https://run.NodeQuality.com)\"\nif command -v yes >/dev/null 2>&1; then\n  yes | eval \"$CMD\"\nelse\n  printf 'y\\n' | eval \"$CMD\"\nfi\n"
	reqID := RandUUID()
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	endpoint := fmt.Sprintf("%s://%s/api/v1/nq/stream", scheme, c.Request.Host)
	payload := map[string]any{
		"requestId": reqID,
		"content":   script,
		"endpoint":  endpoint,
		"secret":    node.Secret,
	}
	if err := sendWSCommand(node.ID, "RunStreamScript", payload); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("未响应，请稍后重试"))
		return
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"requestId": reqID}))
}

// NodeNQResult 查询节点质量测试结果
// @Summary 查询节点质量测试结果
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeSimpleReq true "节点ID"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/nq-result [post]
// POST /api/v1/node/nq-result {nodeId}
func NodeNQResult(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if _, _, _, _, _, errMsg, ok := nodeAccess(c, p.NodeID, false); !ok {
		c.JSON(http.StatusOK, response.ErrMsg(errMsg))
		return
	}
	// latest result
	var last model.NQResult
	if err := dbpkg.DB.Where("node_id = ?", p.NodeID).Order("time_ms desc").First(&last).Error; err != nil || last.ID == 0 {
		c.JSON(http.StatusOK, response.Ok(map[string]any{"content": "", "timeMs": nil, "done": false, "requestId": ""}))
		return
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{
		"content":   last.Content,
		"timeMs":    last.TimeMs,
		"done":      last.Done,
		"requestId": last.RequestID,
	}))
}

// NodeDiagStart triggers diagnostic scripts on node
// @Summary 触发节点诊断脚本
// @Tags node
// @Accept json
// @Produce json
// @Param data body object true "nodeId, kind"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/diag/start [post]
func NodeDiagStart(c *gin.Context) {
	var p struct {
		NodeID int64  `json:"nodeId" binding:"required"`
		Kind   string `json:"kind" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if _, _, _, _, _, errMsg, ok := nodeAccess(c, p.NodeID, false); !ok {
		c.JSON(http.StatusOK, response.ErrMsg(errMsg))
		return
	}
	kind := strings.TrimSpace(p.Kind)
	if kind == "" {
		c.JSON(http.StatusOK, response.ErrMsg("kind 不能为空"))
		return
	}
	var node model.Node
	if err := dbpkg.DB.First(&node, p.NodeID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	script := ""
	switch kind {
	case "backtrace":
		// backtrace handled by agent internally (no script download)
		script = ""
	case "iperf3-start":
		script = "#!/bin/sh\nset +e\nif ! command -v iperf3 >/dev/null 2>&1; then\n  echo \"iperf3 not found\"; exit 1\nfi\npick_port() {\n  i=5201\n  while [ $i -le 5299 ]; do\n    if command -v ss >/dev/null 2>&1; then\n      ss -lntu 2>/dev/null | awk '{print $4}' | grep -E \":$i$\" >/dev/null 2>&1 && { i=$((i+1)); continue; }\n    elif command -v netstat >/dev/null 2>&1; then\n      netstat -lntu 2>/dev/null | awk '{print $4}' | grep -E \":$i$\" >/dev/null 2>&1 && { i=$((i+1)); continue; }\n    fi\n    echo $i; return 0\n  done\n  echo 0\n}\nPORT=$(pick_port)\nif [ \"$PORT\" = \"0\" ]; then\n  echo \"no free port\"; exit 1\nfi\nLOG=/tmp/np_iperf3.log\nnohup iperf3 -s -p \"$PORT\" >>\"$LOG\" 2>&1 &\nPID=$!\necho \"$PID\" > /tmp/np_iperf3.pid\necho \"$PORT\" > /tmp/np_iperf3.port\necho \"iperf3 started on port $PORT (pid $PID)\"\n"
	case "iperf3-stop":
		script = "#!/bin/sh\nset +e\nif [ -f /tmp/np_iperf3.pid ]; then\n  PID=$(cat /tmp/np_iperf3.pid)\n  if [ -n \"$PID\" ]; then\n    kill \"$PID\" 2>/dev/null || true\n  fi\n  rm -f /tmp/np_iperf3.pid\nfi\npkill -f \"iperf3 -s\" 2>/dev/null || true\nif [ -f /tmp/np_iperf3.port ]; then\n  PORT=$(cat /tmp/np_iperf3.port)\n  rm -f /tmp/np_iperf3.port\n  echo \"iperf3 stopped (port $PORT)\"\nelse\n  echo \"iperf3 stopped\"\nfi\n"
	default:
		c.JSON(http.StatusOK, response.ErrMsg("未知诊断类型"))
		return
	}
	reqID := RandUUID()
	endpoint := fmt.Sprintf("%s://%s/api/v1/diag/stream", scheme, c.Request.Host)
	var payload map[string]any
	var cmd string
	if kind == "backtrace" {
		cmd = "BacktraceTest"
		payload = map[string]any{
			"requestId": reqID,
			"endpoint":  endpoint,
			"secret":    node.Secret,
			"type":      kind,
		}
	} else {
		cmd = "RunStreamScript"
		payload = map[string]any{
			"requestId": reqID,
			"content":   script,
			"endpoint":  endpoint,
			"secret":    node.Secret,
			"type":      kind,
		}
	}
	now := time.Now().UnixMilli()
	_ = dbpkg.DB.Create(&model.NodeDiagResult{
		NodeID:      node.ID,
		RequestID:   reqID,
		Type:        kind,
		Content:     "",
		Done:        false,
		TimeMs:      now,
		CreatedTime: now,
		UpdatedTime: now,
	}).Error
	if err := sendWSCommand(node.ID, cmd, payload); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("未响应，请稍后重试"))
		return
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"requestId": reqID}))
}

// NodeDiagResult fetches latest diagnostic output
// @Summary 获取节点诊断结果
// @Tags node
// @Accept json
// @Produce json
// @Param data body object true "nodeId, kind, requestId"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/diag/result [post]
func NodeDiagResult(c *gin.Context) {
	var p struct {
		NodeID    int64  `json:"nodeId" binding:"required"`
		Kind      string `json:"kind"`
		RequestID string `json:"requestId"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if _, _, _, _, _, errMsg, ok := nodeAccess(c, p.NodeID, false); !ok {
		c.JSON(http.StatusOK, response.ErrMsg(errMsg))
		return
	}
	kind := strings.TrimSpace(p.Kind)
	if kind == "" {
		kind = "diag"
	}
	var last model.NodeDiagResult
	q := dbpkg.DB.Where("node_id = ?", p.NodeID)
	if p.RequestID != "" {
		q = q.Where("request_id = ?", p.RequestID)
	} else {
		q = q.Where("type = ?", kind)
	}
	if err := q.Order("time_ms desc").First(&last).Error; err != nil || last.ID == 0 {
		c.JSON(http.StatusOK, response.Ok(map[string]any{"content": "", "timeMs": nil, "done": false, "requestId": ""}))
		return
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{
		"content":   last.Content,
		"timeMs":    last.TimeMs,
		"done":      last.Done,
		"requestId": last.RequestID,
	}))
}

// NodeIperf3Status returns iperf3 server status on node
// @Summary 获取 iperf3 状态
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeSimpleReq true "节点ID"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/diag/iperf3-status [post]
func NodeIperf3Status(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	node, _, _, _, _, errMsg, ok := nodeAccess(c, p.NodeID, false)
	if !ok {
		c.JSON(http.StatusOK, response.ErrMsg(errMsg))
		return
	}
	script := "#!/bin/sh\nset +e\nPID_FILE=/tmp/np_iperf3.pid\nPORT_FILE=/tmp/np_iperf3.port\nstatus=stopped\npid=\"\"\nport=\"\"\nif [ -f \"$PID_FILE\" ]; then pid=$(cat \"$PID_FILE\" 2>/dev/null); fi\nif [ -f \"$PORT_FILE\" ]; then port=$(cat \"$PORT_FILE\" 2>/dev/null); fi\nif [ -n \"$pid\" ] && kill -0 \"$pid\" 2>/dev/null; then status=running; fi\necho \"status=$status\"\n[ -n \"$pid\" ] && echo \"pid=$pid\"\n[ -n \"$port\" ] && echo \"port=$port\"\n"
	req := map[string]any{"requestId": RandUUID(), "timeoutSec": 6, "content": script}
	if res, ok := RequestOp(node.ID, "RunScript", req, 9*time.Second); ok {
		var so string
		if d, _ := res["data"].(map[string]any); d != nil {
			if s, _ := d["stdout"].(string); s != "" {
				so = s
			}
		}
		status := "unknown"
		pid := ""
		port := ""
		for _, line := range strings.Split(so, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "status=") {
				status = strings.TrimPrefix(line, "status=")
			} else if strings.HasPrefix(line, "pid=") {
				pid = strings.TrimPrefix(line, "pid=")
			} else if strings.HasPrefix(line, "port=") {
				port = strings.TrimPrefix(line, "port=")
			}
		}
		c.JSON(http.StatusOK, response.Ok(map[string]any{
			"status": status,
			"pid":    pid,
			"port":   port,
		}))
		return
	}
	c.JSON(http.StatusOK, response.ErrMsg("未响应，请稍后重试"))
}

// DiagBacktraceScript proxies backtrace script from upstream
// @Summary 获取 backtrace 脚本
// @Tags node
// @Produce text/plain
// @Router /api/v1/diag/backtrace.sh [get]
func DiagBacktraceScript(c *gin.Context) {
	urls := []string{
		"https://raw.githubusercontent.com/zhanghanyun/backtrace/main/install.sh",
	}
	client := &http.Client{Timeout: 20 * time.Second}
	var lastErr string
	for _, u := range urls {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "network-panel-backtrace-proxy")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Sprintf("status=%d url=%s", resp.StatusCode, u)
			resp.Body.Close()
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || len(body) == 0 {
			lastErr = "empty body"
			continue
		}
		// normalize line endings and strip BOM if present
		if len(body) >= 3 && body[0] == 0xEF && body[1] == 0xBB && body[2] == 0xBF {
			body = body[3:]
		}
		body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
		body = bytes.ReplaceAll(body, []byte("\r"), []byte("\n"))
		if bytes.HasPrefix(bytes.TrimSpace(body), []byte("<!DOCTYPE")) || bytes.HasPrefix(bytes.TrimSpace(body), []byte("<html")) {
			lastErr = "html response"
			continue
		}
		if !bytes.HasPrefix(body, []byte("#!")) {
			lastErr = "missing shebang"
			continue
		}
		c.Header("Content-Type", "text/x-shellscript; charset=utf-8")
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/x-shellscript; charset=utf-8", body)
		return
	}
	if lastErr == "" {
		lastErr = "fetch failed"
	}
	c.Data(http.StatusBadGateway, "text/plain; charset=utf-8", []byte("backtrace fetch failed: "+lastErr))
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
