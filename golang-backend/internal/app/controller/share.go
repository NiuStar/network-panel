package controller

import (
    "net/http"
    "time"

    "github.com/gin-gonic/gin"
    dbpkg "network-panel/golang-backend/internal/db"
    "network-panel/golang-backend/internal/app/model"
    "network-panel/golang-backend/internal/app/response"
)

// POST /api/v1/share/network-list {range}
// Public, read-only: returns sanitized nodes + batch RTT stats + latest sysinfo snapshot
func ShareNetworkList(c *gin.Context) {
    var p struct{ Range string `json:"range"` }
    _ = c.ShouldBindJSON(&p)
    // nodes (sanitized)
    var nodes []model.Node
    dbpkg.DB.Find(&nodes)
    type nodeOut struct {
        ID          int64   `json:"id"`
        Name        string  `json:"name"`
        Status      *int    `json:"status"`
        Version     string  `json:"version"`
        PriceCents  *int64  `json:"priceCents,omitempty"`
        CycleMonths *int    `json:"cycleMonths,omitempty"`
        StartDateMs *int64  `json:"startDateMs,omitempty"`
    }
    outs := make([]nodeOut, 0, len(nodes))
    for _, n := range nodes {
        var cm *int
        if n.CycleDays != nil {
            switch *n.CycleDays { case 30: x:=1; cm=&x; case 90: x:=3; cm=&x; case 180: x:=6; cm=&x; case 365: x:=12; cm=&x }
        }
        outs = append(outs, nodeOut{ID: n.ID, Name: n.Name, Status: n.Status, Version: n.Version, PriceCents: n.PriceCents, CycleMonths: cm, StartDateMs: n.StartDateMs})
    }

    // batch RTT stats in window (reuse logic from NodeNetworkStatsBatch)
    now := time.Now().UnixMilli()
    var windowMs int64
    switch p.Range {
    case "12h":
        windowMs = 12 * 3600 * 1000
    case "1d":
        windowMs = 24 * 3600 * 1000
    default:
        windowMs = 3600 * 1000
    }
    from := now - windowMs
    var rows []model.NodeProbeResult
    dbpkg.DB.Where("time_ms >= ?", from).Order("time_ms asc").Find(&rows)
    type stat struct{ Sum, Cnt int; Latest *int; LatestTarget int64 }
    agg := map[int64]*stat{}
    for _, r := range rows {
        s := agg[r.NodeID]
        if s == nil { s = &stat{}; agg[r.NodeID] = s }
        if r.OK == 1 && r.RTTMs > 0 { s.Sum += r.RTTMs; s.Cnt++ }
        v := r.RTTMs; s.Latest = &v; s.LatestTarget = r.TargetID
    }
    // target meta for latest
    tset := map[int64]struct{}{}
    for _, s := range agg { if s.LatestTarget > 0 { tset[s.LatestTarget] = struct{}{} } }
    tmeta := map[int64]map[string]string{}
    if len(tset) > 0 {
        ids := make([]int64, 0, len(tset)); for id := range tset { ids = append(ids, id) }
        var tgts []model.ProbeTarget
        dbpkg.DB.Where("id IN ?", ids).Find(&tgts)
        for _, t := range tgts { tmeta[t.ID] = map[string]string{"name": t.Name, "ip": t.IP} }
    }
    stats := map[int64]map[string]any{}
    for nid, s := range agg {
        var avg *int
        if s.Cnt > 0 { v := s.Sum / s.Cnt; avg = &v }
        stats[nid] = map[string]any{"avg": avg, "latest": s.Latest}
        if m, ok := tmeta[s.LatestTarget]; ok { stats[nid]["latestTarget"] = map[string]any{"id": s.LatestTarget, "name": m["name"], "ip": m["ip"]} }
    }

    // latest sysinfo per node (snapshot)
    sys := map[int64]model.NodeSysInfo{}
    for _, n := range nodes {
        var s model.NodeSysInfo
        dbpkg.DB.Where("node_id = ?", n.ID).Order("time_ms desc").Limit(1).Find(&s)
        if s.NodeID != 0 { sys[n.ID] = s }
    }
    c.JSON(http.StatusOK, response.Ok(map[string]any{"nodes": outs, "stats": stats, "sys": sys}))
}

// POST /api/v1/share/network-stats {nodeId, range}
// Public, read-only: mirror of NodeNetworkStats
func ShareNetworkStats(c *gin.Context) {
    var p struct{ NodeID int64 `json:"nodeId"`; Range string `json:"range"` }
    if err := c.ShouldBindJSON(&p); err != nil { c.JSON(http.StatusOK, response.ErrMsg("参数错误")); return }
    now := time.Now().UnixMilli()
    var windowMs int64
    switch p.Range {
    case "12h": windowMs = 12 * 3600 * 1000
    case "1d": windowMs = 24 * 3600 * 1000
    case "7d": windowMs = 7 * 24 * 3600 * 1000
    case "30d": windowMs = 30 * 24 * 3600 * 1000
    default: windowMs = 3600 * 1000
    }
    from := now - windowMs
    var results []model.NodeProbeResult
    dbpkg.DB.Where("node_id = ? AND time_ms >= ?", p.NodeID, from).Order("time_ms asc").Find(&results)
    // targets
    targetIDs := make([]int64, 0); seen := map[int64]struct{}{}
    for _, r := range results { if _, ok := seen[r.TargetID]; !ok { seen[r.TargetID] = struct{}{}; targetIDs = append(targetIDs, r.TargetID) } }
    m := map[int64]map[string]string{}
    if len(targetIDs) > 0 {
        var tgts []model.ProbeTarget
        dbpkg.DB.Where("id IN ?", targetIDs).Find(&tgts)
        for _, t := range tgts { m[t.ID] = map[string]string{"name": t.Name, "ip": t.IP} }
    }
    // disconnect logs
    var logs []model.NodeDisconnectLog
    dbpkg.DB.Where("node_id = ? AND (down_at_ms >= ? OR (up_at_ms IS NOT NULL AND up_at_ms >= ?))", p.NodeID, from, from).Order("down_at_ms asc").Find(&logs)
    var downMs int64 = 0
    for _, l := range logs {
        start := l.DownAtMs; end := now
        if l.UpAtMs != nil { end = *l.UpAtMs }
        s := max64(start, from); e := min64(end, now)
        if e > s { downMs += (e - s) }
    }
    sla := 0.0
    if windowMs > 0 { sla = float64(windowMs-downMs) / float64(windowMs) }
    c.JSON(http.StatusOK, response.Ok(map[string]any{
        "results": results, "targets": m, "disconnects": logs, "sla": sla, "from": from, "to": now,
    }))
}

// use min64/max64 helpers defined in probe.go within the same package
