package controller

import (
    "encoding/json"
    "net/http"
    "time"
    "strconv"
    "strings"

    "github.com/gin-gonic/gin"
    "network-panel/golang-backend/internal/app/model"
    "network-panel/golang-backend/internal/app/response"
    dbpkg "network-panel/golang-backend/internal/db"
)

// TunnelPathGet 获取隧道多级路径
// @Summary 获取隧道多级路径
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerTunnelIDReq true "隧道ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/path/get [post]
func TunnelPathGet(c *gin.Context) {
    var p struct{ TunnelID int64 `json:"tunnelId" binding:"required"` }
    if err := c.ShouldBindJSON(&p); err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
        return
    }
    key := tunnelPathKey(p.TunnelID)
    var cfg model.ViteConfig
    _ = dbpkg.DB.Where("name = ?", key).First(&cfg).Error
    var ids []int64
    if cfg.Value != "" {
        _ = json.Unmarshal([]byte(cfg.Value), &ids)
    }
    c.JSON(http.StatusOK, response.Ok(map[string]any{"path": ids}))
}

// TunnelPathSet 设置隧道多级路径
// @Summary 设置隧道多级路径
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerTunnelPathSetReq true "隧道ID与路径节点"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/path/set [post]
func TunnelPathSet(c *gin.Context) {
    var p struct{ TunnelID int64 `json:"tunnelId" binding:"required"`; Path []int64 `json:"path"` }
    if err := c.ShouldBindJSON(&p); err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
        return
    }
    // de-dup and validate nodes exist
    uniq := make([]int64, 0, len(p.Path))
    seen := map[int64]struct{}{}
    for _, id := range p.Path {
        if id <= 0 { continue }
        if _, ok := seen[id]; ok { continue }
        var n model.Node
        if dbpkg.DB.First(&n, id).Error == nil {
            uniq = append(uniq, id)
            seen[id] = struct{}{}
        }
    }
    // persist to ViteConfig
    b, _ := json.Marshal(uniq)
    key := tunnelPathKey(p.TunnelID)
    now := time.Now().UnixMilli()
    var cfg model.ViteConfig
    if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err == nil {
        cfg.Value = string(b)
        cfg.Time = now
        _ = dbpkg.DB.Save(&cfg).Error
    } else {
        _ = dbpkg.DB.Create(&model.ViteConfig{Name: key, Value: string(b), Time: now}).Error
    }
    // 使用 Web API 动态配置，无需重启；重连或编辑保存时会按路径自动下发服务
    var t model.Tunnel
    _ = dbpkg.DB.First(&t, p.TunnelID).Error
    nodes := make([]int64, 0, 2+len(uniq))
    nodes = append(nodes, t.InNodeID)
    nodes = append(nodes, uniq...)
    if t.OutNodeID != nil { nodes = append(nodes, *t.OutNodeID) }
    c.JSON(http.StatusOK, response.Ok(map[string]any{"saved": len(uniq)}))
}

func tunnelPathKey(tid int64) string { return "tunnel_path_" + strconv.FormatInt(tid, 10) }

// TunnelPathCheck 检查多级路径节点
// @Summary 检查多级路径节点状态
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerTunnelIDReq true "隧道ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/path/check [post]
func TunnelPathCheck(c *gin.Context) {
    var p struct{ TunnelID int64 `json:"tunnelId" binding:"required"` }
    if err := c.ShouldBindJSON(&p); err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
        return
    }
    var t model.Tunnel
    if err := dbpkg.DB.First(&t, p.TunnelID).Error; err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("隧道不存在"))
        return
    }
    // build hop list
    path := getTunnelPathNodes(t.ID)
    hops := make([]int64, 0, 2+len(path))
    hops = append(hops, t.InNodeID)
    hops = append(hops, path...)
    // Only tunnel-forward (type=2) has exit node hop
    if t.Type == 2 && t.OutNodeID != nil { hops = append(hops, *t.OutNodeID) }

    out := make([]map[string]any, 0, len(hops))
    allOK := true
    // propose ports for middle relays using agent query
    // use tunnel TCPListenAddr port as baseline if available, else 0
    preferPort := 0
    // resolve entry node port range for baseline
    for idx, nid := range hops {
        var n model.Node
        _ = dbpkg.DB.First(&n, nid).Error
        role := "mid"
        if idx == 0 { role = "entry" }
        if t.Type == 2 && idx == len(hops)-1 { role = "exit" }
        online := (n.Status != nil && *n.Status == 1)
        if !online { allOK = false }
        // collect relay presence via QueryServices
        services := queryNodeServicesRaw(nid)
        relayGrpc := false
        usedPorts := map[int]bool{}
        for _, s := range services {
            if v, ok := s["addr"].(string); ok {
                if p := parsePort(v); p > 0 { usedPorts[p] = true }
            }
            if h, ok := s["handler"].(map[string]any); ok {
                if typ, _ := h["type"].(string); typ == "relay" {
                    if lst, ok2 := s["listener"].(map[string]any); ok2 {
                        if lt, _ := lst["type"].(string); lt == "grpc" { relayGrpc = true }
                    }
                }
            }
        }
        // propose free port for middle nodes only; include suggestions list from agent
        proposed := 0
        suggestions := []int{}
        if role == "mid" {
            minP := 10000; maxP := 65535
            if n.PortSta > 0 { minP = n.PortSta }
            if n.PortEnd > 0 { maxP = n.PortEnd }
            base := preferPort
            if base <= 0 { base = minP - 1 }
            if base < 0 { base = 0 }
            if arr := suggestPortsViaAgent(nid, base, 10); len(arr) > 0 {
                // filter to range and capture
                for _, p2 := range arr { if p2 >= minP && p2 <= maxP { suggestions = append(suggestions, p2) } }
                if len(suggestions) > 0 { proposed = suggestions[0] }
            }
            if proposed == 0 {
                proposed = findFreePortOnNode(nid, preferPort, minP, maxP)
            }
            if proposed == 0 { allOK = false }
        }
        out = append(out, map[string]any{
            "nodeId": nid,
            "nodeName": n.Name,
            "role": role,
            "online": online,
            "relayGrpc": relayGrpc,
            "proposedPort": proposed,
            "suggestedPorts": suggestions,
        })
    }
    c.JSON(http.StatusOK, response.Ok(map[string]any{"hops": out, "ok": allOK}))
}

// TunnelCleanupTemp 清理路径临时服务
// @Summary 清理路径临时服务（iperf3）
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerTunnelIDReq true "隧道ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/cleanup-temp [post]
func TunnelCleanupTemp(c *gin.Context) {
    var p struct{ TunnelID int64 `json:"tunnelId" binding:"required"` }
    if err := c.ShouldBindJSON(&p); err != nil { c.JSON(http.StatusOK, response.ErrMsg("参数错误")); return }
    var t model.Tunnel
    if err := dbpkg.DB.First(&t, p.TunnelID).Error; err != nil { c.JSON(http.StatusOK, response.ErrMsg("隧道不存在")); return }
    // collect nodes: entry + mids + exit (safe)
    path := getTunnelPathNodes(t.ID)
    nodes := make([]int64, 0, 2+len(path))
    nodes = append(nodes, t.InNodeID)
    nodes = append(nodes, path...)
    if t.OutNodeID != nil { nodes = append(nodes, *t.OutNodeID) }
    prefix := "tmp_iperf3_" + strconv.FormatInt(t.ID, 10) + "_"
    total := 0
    cleaned := 0
    for _, nid := range nodes {
        svcs := queryNodeServicesRaw(nid)
        names := make([]string, 0)
        for _, s := range svcs {
            if n, ok := s["name"].(string); ok && strings.HasPrefix(n, prefix) {
                names = append(names, n)
            }
        }
        total += len(names)
        if len(names) > 0 {
            _ = sendWSCommand(nid, "DeleteService", map[string]any{"services": names})
            cleaned += len(names)
        }
    }
    c.JSON(http.StatusOK, response.Ok(map[string]any{"found": total, "deleted": cleaned}))
}
