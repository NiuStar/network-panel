package controller

import (
    "os"
    "strconv"
    "sync"
    "time"
    "sort"

    dbpkg "network-panel/golang-backend/internal/db"
    "network-panel/golang-backend/internal/app/model"
    "gorm.io/gorm/clause"
)

// Simple in-memory buffers with periodic batch flush to reduce DB write pressure.

var (
    // sysinfo samples buffer
    bufSysMu sync.Mutex
    bufSys   []model.NodeSysInfo

    // probe results buffer
    bufProbeMu sync.Mutex
    bufProbe   []model.NodeProbeResult

    // latest node runtime (interfaces) by node; only latest value is kept
    bufRuntimeMu sync.Mutex
    bufRuntime   = map[int64]*model.NodeRuntime{}

    // op logs buffer
    bufOpMu sync.Mutex
    bufOp   []model.NodeOpLog

    // alerts buffer
    bufAlertMu sync.Mutex
    bufAlert   []model.Alert

    // disconnect logs buffer
    bufDiscMu sync.Mutex
    bufDisc   []model.NodeDisconnectLog
)

func init() {
    go batchFlusher()
}

func batchFlusher() {
    sec := 60
    if v := os.Getenv("BATCH_FLUSH_SEC"); v != "" {
        if n, err := strconv.Atoi(v); err == nil && n > 0 { sec = n }
    }
    ticker := time.NewTicker(time.Duration(sec) * time.Second)
    defer ticker.Stop()
    for range ticker.C {
        flushOnce()
    }
}

func flushOnce() {
    // swap buffers
    bufSysMu.Lock(); sys := bufSys; bufSys = nil; bufSysMu.Unlock()
    bufProbeMu.Lock(); probes := bufProbe; bufProbe = nil; bufProbeMu.Unlock()
    bufRuntimeMu.Lock(); runMap := bufRuntime; bufRuntime = map[int64]*model.NodeRuntime{}; bufRuntimeMu.Unlock()

    if len(sys) > 0 {
        _ = dbpkg.DB.Create(&sys).Error
    }
    if len(probes) > 0 {
        _ = dbpkg.DB.Create(&probes).Error
    }
    if len(runMap) > 0 {
        list := make([]model.NodeRuntime, 0, len(runMap))
        for _, v := range runMap { if v != nil { list = append(list, *v) } }
        if len(list) > 0 {
            _ = dbpkg.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name:"node_id"}}, DoUpdates: clause.AssignmentColumns([]string{"interfaces","updated_time"})}).Create(&list).Error
        }
    }
    if len(bufOp) > 0 {
        bufOpMu.Lock(); ops := bufOp; bufOp = nil; bufOpMu.Unlock()
        if len(ops) > 0 { _ = dbpkg.DB.Create(&ops).Error }
    }
    if len(bufAlert) > 0 {
        bufAlertMu.Lock(); als := bufAlert; bufAlert = nil; bufAlertMu.Unlock()
        if len(als) > 0 { _ = dbpkg.DB.Create(&als).Error }
    }
    if len(bufDisc) > 0 {
        bufDiscMu.Lock(); ds := bufDisc; bufDisc = nil; bufDiscMu.Unlock()
        if len(ds) > 0 { _ = dbpkg.DB.Create(&ds).Error }
    }
}

// Enqueue sysinfo sample
func enqueueSysInfo(s model.NodeSysInfo) {
    bufSysMu.Lock(); bufSys = append(bufSys, s); bufSysMu.Unlock()
}

// Enqueue probe results
func enqueueProbes(rows []model.NodeProbeResult) {
    if len(rows) == 0 { return }
    bufProbeMu.Lock(); bufProbe = append(bufProbe, rows...); bufProbeMu.Unlock()
}

// Set latest runtime snapshot for node (overwrites previous)
func setRuntime(rec model.NodeRuntime) {
    bufRuntimeMu.Lock(); bufRuntime[rec.NodeID] = &rec; bufRuntimeMu.Unlock()
}

// Read buffered sysinfo samples for a node since timestamp (inclusive)
func readBufferedSysInfo(nodeID int64, from int64) []model.NodeSysInfo {
    bufSysMu.Lock(); defer bufSysMu.Unlock()
    out := make([]model.NodeSysInfo, 0)
    for _, s := range bufSys {
        if s.NodeID == nodeID && s.TimeMs >= from { out = append(out, s) }
    }
    return out
}

// Read buffered probe results filtered by node and from time
func readBufferedProbes(nodeID int64, from int64) []model.NodeProbeResult {
    bufProbeMu.Lock(); defer bufProbeMu.Unlock()
    out := make([]model.NodeProbeResult, 0)
    for _, r := range bufProbe {
        if (nodeID <= 0 || r.NodeID == nodeID) && r.TimeMs >= from { out = append(out, r) }
    }
    return out
}

// Enqueue op log
func enqueueOpLog(rec model.NodeOpLog) { bufOpMu.Lock(); bufOp = append(bufOp, rec); bufOpMu.Unlock() }

// Read buffered op logs by node (latest first), limit N
func readBufferedOpLogsByNode(nodeID int64, limit int) []model.NodeOpLog {
    bufOpMu.Lock(); defer bufOpMu.Unlock()
    tmp := make([]model.NodeOpLog, 0)
    for _, it := range bufOp { if nodeID <= 0 || it.NodeID == nodeID { tmp = append(tmp, it) } }
    sort.Slice(tmp, func(i,j int) bool { return tmp[i].TimeMs > tmp[j].TimeMs })
    if limit > 0 && len(tmp) > limit { tmp = tmp[:limit] }
    return tmp
}

// Read buffered op logs by requestId (asc time)
func readBufferedOpLogsByReq(reqID string) []model.NodeOpLog {
    bufOpMu.Lock(); defer bufOpMu.Unlock()
    tmp := make([]model.NodeOpLog, 0)
    for _, it := range bufOp { if it.RequestID == reqID { tmp = append(tmp, it) } }
    sort.Slice(tmp, func(i,j int) bool { return tmp[i].TimeMs < tmp[j].TimeMs })
    return tmp
}

// Enqueue alert
func enqueueAlert(a model.Alert) { bufAlertMu.Lock(); bufAlert = append(bufAlert, a); bufAlertMu.Unlock() }

// Read buffered alerts latest first, limit N
func readBufferedAlerts(limit int) []model.Alert {
    bufAlertMu.Lock(); defer bufAlertMu.Unlock()
    tmp := make([]model.Alert, len(bufAlert))
    copy(tmp, bufAlert)
    sort.Slice(tmp, func(i,j int) bool { return tmp[i].TimeMs > tmp[j].TimeMs })
    if limit > 0 && len(tmp) > limit { tmp = tmp[:limit] }
    return tmp
}

// Enqueue disconnect
func enqueueDisconnect(d model.NodeDisconnectLog) { bufDiscMu.Lock(); bufDisc = append(bufDisc, d); bufDiscMu.Unlock() }

// Read buffered disconnect logs filtered by node and from time
func readBufferedDisconnects(nodeID int64, from int64) []model.NodeDisconnectLog {
    bufDiscMu.Lock(); defer bufDiscMu.Unlock()
    out := make([]model.NodeDisconnectLog, 0)
    for _, it := range bufDisc {
        if (nodeID <= 0 || it.NodeID == nodeID) && (it.DownAtMs >= from || (it.UpAtMs != nil && *it.UpAtMs >= from)) {
            out = append(out, it)
        }
    }
    sort.Slice(out, func(i,j int) bool { return out[i].DownAtMs < out[j].DownAtMs })
    return out
}
