package controller

import (
	"sync"
	"time"

	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

var offlineOnce sync.Once

// StartNodeOfflineMonitor starts a background checker that marks nodes offline
// if no sysinfo is received within the threshold.
func StartNodeOfflineMonitor() {
	offlineOnce.Do(func() {
		go nodeOfflineMonitor()
	})
}

func nodeOfflineMonitor() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		markOfflineOnce()
	}
}

func markOfflineOnce() {
	var nodes []model.Node
	if err := dbpkg.DB.Find(&nodes).Error; err != nil {
		return
	}
	if len(nodes) == 0 {
		return
	}
	connected := map[int64]bool{}
	nodeConnMu.RLock()
	for id, list := range nodeConns {
		if len(list) > 0 {
			connected[id] = true
		}
	}
	nodeConnMu.RUnlock()
	for _, n := range nodes {
		isConnected := connected[n.ID]
		if isConnected {
			if n.Status == nil || *n.Status != 1 {
				s := 1
				_ = dbpkg.DB.Model(&model.Node{}).Where("id = ?", n.ID).Update("status", s).Error
				broadcastToAdmins(map[string]interface{}{"id": n.ID, "type": "status", "data": 1})
			}
			continue
		}
		if n.Status != nil && *n.Status == 1 {
			// mark offline
			s := 0
			_ = dbpkg.DB.Model(&model.Node{}).Where("id = ?", n.ID).Update("status", s).Error
			broadcastToAdmins(map[string]interface{}{"id": n.ID, "type": "status", "data": 0})
			// create disconnect log and alert
			nowMs := time.Now().UnixMilli()
			rec := model.NodeDisconnectLog{NodeID: n.ID, DownAtMs: nowMs}
			enqueueDisconnect(rec)
			name := n.Name
			nid := n.ID
			enqueueAlert(model.Alert{TimeMs: nowMs, Type: "offline", NodeID: &nid, NodeName: &name, Message: "节点离线"})
			go notifyCallback("agent_offline", n, map[string]any{"downAtMs": nowMs})
		}
	}
}
