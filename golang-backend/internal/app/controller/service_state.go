package controller

import (
    "sync"
)

// In-memory node services snapshot reported by agents
type nodeSvcSnapshot struct {
    names  map[string]struct{}
    timeMs int64
    hashes map[string]string // name -> hash(subset)
}

var (
    nodeSvcMu  sync.RWMutex
    nodeSvcs   = map[int64]*nodeSvcSnapshot{} // nodeId -> snapshot
)

func updateNodeServices(nodeID int64, names []string, hashes map[string]string, timeMs int64) {
    m := make(map[string]struct{}, len(names))
    for _, n := range names { if n != "" { m[n] = struct{}{} } }
    nodeSvcMu.Lock()
    nodeSvcs[nodeID] = &nodeSvcSnapshot{names: m, timeMs: timeMs, hashes: hashes}
    nodeSvcMu.Unlock()
}

func getNodeServiceSnapshot(nodeID int64) (names map[string]struct{}, hashes map[string]string, timeMs int64, ok bool) {
    nodeSvcMu.RLock()
    s, exists := nodeSvcs[nodeID]
    nodeSvcMu.RUnlock()
    if !exists || s == nil { return nil, nil, 0, false }
    return s.names, s.hashes, s.timeMs, true
}
