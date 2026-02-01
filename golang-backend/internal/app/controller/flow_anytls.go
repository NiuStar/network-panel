package controller

import (
	"net/http"
	"strings"
	"time"

	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// POST /flow/anytls?secret=...
// Updates user/user_node flow counters for AnyTLS traffic.
func FlowAnyTLSUpload(c *gin.Context) {
	secret := strings.TrimSpace(c.Query("secret"))
	if secret == "" {
		c.String(http.StatusOK, "ok")
		return
	}
	var node model.Node
	if err := dbpkg.DB.Select("id").Where("secret = ?", secret).First(&node).Error; err != nil || node.ID == 0 {
		c.String(http.StatusOK, "ok")
		return
	}
	var req struct {
		UserID   int64 `json:"userId"`
		InBytes  int64 `json:"inBytes"`
		OutBytes int64 `json:"outBytes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.String(http.StatusOK, "ok")
		return
	}
	if req.UserID <= 0 {
		c.String(http.StatusOK, "ok")
		return
	}
	inInc := req.InBytes
	outInc := req.OutBytes
	if inInc < 0 {
		inInc = 0
	}
	if outInc < 0 {
		outInc = 0
	}
	if inInc == 0 && outInc == 0 {
		c.String(http.StatusOK, "ok")
		return
	}

	now := time.Now()
	nowMs := now.UnixMilli()
	nowCST := now.In(time.FixedZone("UTC+8", 8*3600))
	calc := inInc + outInc

	dbpkg.DB.Model(&model.User{}).Where("id = ?", req.UserID).
		Updates(map[string]any{"in_flow": gorm.Expr("in_flow + ?", inInc), "out_flow": gorm.Expr("out_flow + ?", outInc), "updated_time": nowMs})
	ur := dbpkg.DB.Model(&model.UserNode{}).Where("user_id = ? AND node_id = ?", req.UserID, node.ID).
		Updates(map[string]any{"in_flow": gorm.Expr("in_flow + ?", inInc), "out_flow": gorm.Expr("out_flow + ?", outInc)})
	if ur.RowsAffected == 0 {
		status := 1
		_ = dbpkg.DB.Create(&model.UserNode{
			UserID:  req.UserID,
			NodeID:  node.ID,
			InFlow:  inInc,
			OutFlow: outInc,
			Status:  status,
		}).Error
	}

	// statistics_flow (hourly bucket)
	hourKey := nowCST.Format("01-02 15:00")
	var rec model.StatisticsFlow
	if err := dbpkg.DB.Where("user_id = ? AND time = ?", req.UserID, hourKey).First(&rec).Error; err == nil && rec.ID > 0 {
		dbpkg.DB.Model(&model.StatisticsFlow{}).Where("id = ?", rec.ID).
			Updates(map[string]any{"flow": gorm.Expr("flow + ?", calc), "total_flow": gorm.Expr("total_flow + ?", calc)})
	} else {
		rec = model.StatisticsFlow{UserID: req.UserID, Flow: calc, TotalFlow: calc, Time: hourKey, CreatedTime: nowMs}
		_ = dbpkg.DB.Create(&rec).Error
	}
	_ = dbpkg.DB.Create(&model.FlowTimeseries{
		UserID:      req.UserID,
		InBytes:     inInc,
		OutBytes:    outInc,
		BilledBytes: calc,
		Source:      "anytls",
		TimeMs:      nowMs,
		CreatedTime: nowMs,
	}).Error

	// enforce limits: user and per-node assignment
	var user model.User
	if err := dbpkg.DB.First(&user, req.UserID).Error; err == nil {
		if overUserLimit(user) || expired(user.ExpTime) || (user.Status != nil && *user.Status != 1) {
			pauseAllUserForwards(user.ID)
			s := 0
			user.Status = &s
			_ = dbpkg.DB.Save(&user).Error
			// disable anytls for this node as well
			dbpkg.DB.Model(&model.UserNode{}).Where("user_id = ? AND node_id = ?", req.UserID, node.ID).
				Update("status", 0)
			go pushAnyTLSConfigToNode(node.ID)
		}
	}
	var un model.UserNode
	if err := dbpkg.DB.Where("user_id = ? AND node_id = ?", req.UserID, node.ID).First(&un).Error; err == nil && un.ID > 0 {
		if overUserNodeLimit(un) || expired(un.ExpTime) || un.Status != 1 {
			un.Status = 0
			_ = dbpkg.DB.Save(&un).Error
			pauseUserNodeForwards(un.UserID, un.NodeID)
			go pushAnyTLSConfigToNode(node.ID)
		}
	}

	c.String(http.StatusOK, "ok")
}
