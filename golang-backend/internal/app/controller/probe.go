package controller

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

// ---- Admin CRUD for probe targets ----

// POST /api/v1/probe/list
func ProbeList(c *gin.Context) {
	var list []model.ProbeTarget
	dbpkg.DB.Order("id desc").Find(&list)
	c.JSON(http.StatusOK, response.Ok(list))
}

// POST /api/v1/probe/create {name, ip}
func ProbeCreate(c *gin.Context) {
	var p struct {
		Name string `json:"name" binding:"required"`
		IP   string `json:"ip" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	now := time.Now().UnixMilli()
	rec := model.ProbeTarget{CreatedTime: now, UpdatedTime: now, Status: 1, Name: p.Name, IP: p.IP}
	if err := dbpkg.DB.Create(&rec).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("保存失败"))
		return
	}
	c.JSON(http.StatusOK, response.Ok(rec))
}

// POST /api/v1/probe/update {id, name, ip, status?}
func ProbeUpdate(c *gin.Context) {
	var p struct {
		ID     int64  `json:"id" binding:"required"`
		Name   string `json:"name"`
		IP     string `json:"ip"`
		Status *int   `json:"status"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var rec model.ProbeTarget
	if err := dbpkg.DB.First(&rec, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("不存在"))
		return
	}
	if p.Name != "" {
		rec.Name = p.Name
	}
	if p.IP != "" {
		rec.IP = p.IP
	}
	if p.Status != nil {
		rec.Status = *p.Status
	}
	rec.UpdatedTime = time.Now().UnixMilli()
	if err := dbpkg.DB.Save(&rec).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("保存失败"))
		return
	}
	c.JSON(http.StatusOK, response.Ok(rec))
}

// POST /api/v1/probe/delete {id}
func ProbeDelete(c *gin.Context) {
	var p struct {
		ID int64 `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	_ = dbpkg.DB.Delete(&model.ProbeTarget{}, p.ID).Error
	c.JSON(http.StatusOK, response.OkNoData())
}

// ---- Agent endpoints ----

// POST /api/v1/agent/probe-targets {secret}
func AgentProbeTargets(c *gin.Context) {
	var p struct {
		Secret string `json:"secret" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var node model.Node
	if err := dbpkg.DB.Where("secret = ?", p.Secret).First(&node).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	var list []model.ProbeTarget
	dbpkg.DB.Where("status = 1").Order("id asc").Find(&list)
	c.JSON(http.StatusOK, response.Ok(list))
}

// POST /api/v1/agent/report-probe {secret, results:[{targetId, rttMs, ok, timeMs?}]}
func AgentReportProbe(c *gin.Context) {
	var p struct {
		Secret  string `json:"secret" binding:"required"`
		Results []struct {
			TargetID int64  `json:"targetId"`
			RTTMs    int    `json:"rttMs"`
			OK       int    `json:"ok"`
			TimeMs   *int64 `json:"timeMs"`
		} `json:"results"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var node model.Node
	if err := dbpkg.DB.Where("secret = ?", p.Secret).First(&node).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	now := time.Now().UnixMilli()
	if len(p.Results) == 0 {
		c.JSON(http.StatusOK, response.OkNoData())
		return
	}
    rows := make([]model.NodeProbeResult, 0, len(p.Results))
	for _, r := range p.Results {
		t := now
		if r.TimeMs != nil && *r.TimeMs > 0 {
			t = *r.TimeMs
		}
		rows = append(rows, model.NodeProbeResult{NodeID: node.ID, TargetID: r.TargetID, RTTMs: r.RTTMs, OK: r.OK, TimeMs: t})
	}
    enqueueProbes(rows)
    c.JSON(http.StatusOK, response.OkNoData())
}

// ---- Query stats for frontend ----

// POST /api/v1/node/network-stats {nodeId, range}
func NodeNetworkStats(c *gin.Context) {
	var p struct {
		NodeID int64  `json:"nodeId" binding:"required"`
		Range  string `json:"range"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	// time window
	now := time.Now().UnixMilli()
	var windowMs int64
	switch p.Range {
	case "1h":
		windowMs = 3600 * 1000
	case "12h":
		windowMs = 12 * 3600 * 1000
	case "1d":
		windowMs = 24 * 3600 * 1000
	case "7d":
		windowMs = 7 * 24 * 3600 * 1000
	case "30d":
		windowMs = 30 * 24 * 3600 * 1000
	default:
		windowMs = 3600 * 1000
	}
	from := now - windowMs

	// results
    var results []model.NodeProbeResult
    dbpkg.DB.Where("node_id = ? AND time_ms >= ?", p.NodeID, from).Order("time_ms asc").Find(&results)
    // merge buffered (unsaved) in-memory probe results
    if extra := readBufferedProbes(p.NodeID, from); len(extra) > 0 {
        results = append(results, extra...)
    }

	// collect target meta
	targetIDs := make([]int64, 0)
	seen := map[int64]struct{}{}
	for _, r := range results {
		if _, ok := seen[r.TargetID]; !ok {
			seen[r.TargetID] = struct{}{}
			targetIDs = append(targetIDs, r.TargetID)
		}
	}
	m := map[int64]map[string]string{}
	if len(targetIDs) > 0 {
		var tgts []model.ProbeTarget
		dbpkg.DB.Where("id IN ?", targetIDs).Find(&tgts)
		for _, t := range tgts {
			m[t.ID] = map[string]string{"name": t.Name, "ip": t.IP}
		}
	}

	// disconnect logs
    var logs []model.NodeDisconnectLog
    dbpkg.DB.Where("node_id = ? AND (down_at_ms >= ? OR (up_at_ms IS NOT NULL AND up_at_ms >= ?))", p.NodeID, from, from).Order("down_at_ms asc").Find(&logs)
    if extra := readBufferedDisconnects(p.NodeID, from); len(extra) > 0 { logs = append(logs, extra...) }

	// compute SLA in window: uptime / window
	// approximate: subtract summed downtime intersecting window
	var downMs int64 = 0
	for _, l := range logs {
		start := l.DownAtMs
		end := now
		if l.UpAtMs != nil {
			end = *l.UpAtMs
		}
		// intersect [start,end] with [from,now]
		s := max64(start, from)
		e := min64(end, now)
		if e > s {
			downMs += (e - s)
		}
	}
	sla := 0.0
	if windowMs > 0 {
		sla = float64(windowMs-downMs) / float64(windowMs)
	}

	c.JSON(http.StatusOK, response.Ok(map[string]interface{}{
		"results":     results,
		"targets":     m,
		"disconnects": logs,
		"sla":         sla,
		"from":        from,
		"to":          now,
	}))
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Batch network stats across nodes (latest+avg rtt in window)
// POST /api/v1/node/network-stats-batch {range}
func NodeNetworkStatsBatch(c *gin.Context) {
	var p struct {
		Range string `json:"range"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	now := time.Now().UnixMilli()
	var windowMs int64
	switch p.Range {
	case "1h":
		windowMs = 3600 * 1000
	case "12h":
		windowMs = 12 * 3600 * 1000
	case "1d":
		windowMs = 24 * 3600 * 1000
	default:
		windowMs = 3600 * 1000
	}
	from := now - windowMs
	// fetch in window
    var rows []model.NodeProbeResult
    dbpkg.DB.Where("time_ms >= ?", from).Order("time_ms asc").Find(&rows)
    // include buffered
    if extra := readBufferedProbes(0, from); len(extra) > 0 {
        rows = append(rows, extra...)
    }
	// aggregate per node
	type stat struct {
		Sum          int
		Cnt          int
		Latest       *int
		LatestTarget int64
	}
	agg := map[int64]*stat{}
	for _, r := range rows {
		s := agg[r.NodeID]
		if s == nil {
			s = &stat{}
			agg[r.NodeID] = s
		}
		if r.OK == 1 && r.RTTMs > 0 {
			s.Sum += r.RTTMs
			s.Cnt++
		}
		v := r.RTTMs
		s.Latest = &v
		s.LatestTarget = r.TargetID
	}
	// fetch target metas for latest target per node
	tset := map[int64]struct{}{}
	for _, s := range agg {
		if s.LatestTarget > 0 {
			tset[s.LatestTarget] = struct{}{}
		}
	}
	tmeta := map[int64]map[string]string{}
	if len(tset) > 0 {
		ids := make([]int64, 0, len(tset))
		for id := range tset {
			ids = append(ids, id)
		}
		var tgts []model.ProbeTarget
		dbpkg.DB.Where("id IN ?", ids).Find(&tgts)
		for _, t := range tgts {
			tmeta[t.ID] = map[string]string{"name": t.Name, "ip": t.IP}
		}
	}
	out := map[int64]map[string]any{}
	for nid, s := range agg {
		var avg *int
		if s.Cnt > 0 {
			v := s.Sum / s.Cnt
			avg = &v
		}
		out[nid] = map[string]any{"avg": avg, "latest": s.Latest}
		if m, ok := tmeta[s.LatestTarget]; ok {
			out[nid]["latestTarget"] = map[string]any{"id": s.LatestTarget, "name": m["name"], "ip": m["ip"]}
		}
	}
	c.JSON(http.StatusOK, response.Ok(out))
}
