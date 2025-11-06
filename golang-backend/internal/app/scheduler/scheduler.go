package scheduler

import (
	"fmt"
	"time"

	"network-panel/golang-backend/internal/app/controller"
	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

func Start() {
	go billingChecker()
}

func billingChecker() {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		checkOnce()
		<-ticker.C
	}
}

func checkOnce() {
	var nodes []model.Node
	dbpkg.DB.Find(&nodes)
	now := time.Now().UnixMilli()
	dayMs := int64(24 * 3600 * 1000)
	for _, n := range nodes {
		if n.CycleDays == nil || *n.CycleDays <= 0 || n.StartDateMs == nil || *n.StartDateMs <= 0 {
			continue
		}
		cycleMs := int64(*n.CycleDays) * dayMs
		if cycleMs <= 0 {
			continue
		}
		if now < *n.StartDateMs {
			continue
		}
		elapsed := now - *n.StartDateMs
		// remaining in current period
		rem := cycleMs - (elapsed % cycleMs)
		// if <= 1 day, trigger reminder
		if rem <= dayMs {
			msg := fmt.Sprintf("节点即将到期，剩余 %d 天", (rem+dayMs-1)/dayMs)
			// alert record
			name := n.Name
			nid := n.ID
			a := model.Alert{TimeMs: now, Type: "due", NodeID: &nid, NodeName: &name, Message: msg}
			_ = dbpkg.DB.Create(&a).Error
			// callback
			controller.TriggerCallback("node_due", n, map[string]any{"remainMs": rem})
		}
	}
}
