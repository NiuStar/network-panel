package controller

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// POST /flow/exit?secret=...&userId=...&nodeId=...&port=...
// Updates user/user_node flow counters for exit (gost) traffic.
func FlowExitUpload(c *gin.Context) {
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
	uid, _ := strconv.ParseInt(strings.TrimSpace(c.Query("userId")), 10, 64)
	if uid <= 0 {
		c.String(http.StatusOK, "ok")
		return
	}
	// read raw body once; support observer events format
	body, _ := io.ReadAll(c.Request.Body)
	type obsStats struct {
		TotalConns   int   `json:"totalConns"`
		CurrentConns int   `json:"currentConns"`
		InputBytes   int64 `json:"inputBytes"`
		OutputBytes  int64 `json:"outputBytes"`
		TotalErrs    int   `json:"totalErrs"`
	}
	type obsEvent struct {
		Kind    string   `json:"kind"`
		Service string   `json:"service"`
		Type    string   `json:"type"`
		Stats   obsStats `json:"stats"`
	}
	var obsPayload struct {
		Events []obsEvent `json:"events"`
	}
	var inBytes, outBytes int64
	if err := json.Unmarshal(body, &obsPayload); err == nil && len(obsPayload.Events) > 0 {
		for _, e := range obsPayload.Events {
			if strings.ToLower(e.Type) != "stats" {
				continue
			}
			if e.Stats.InputBytes > 0 {
				inBytes += e.Stats.InputBytes
			}
			if e.Stats.OutputBytes > 0 {
				outBytes += e.Stats.OutputBytes
			}
		}
	}
	if inBytes == 0 && outBytes == 0 {
		c.String(http.StatusOK, "ok")
		return
	}
	now := time.Now()
	nowMs := now.UnixMilli()
	nowCST := now.In(time.FixedZone("UTC+8", 8*3600))
	calc := inBytes + outBytes

	dbpkg.DB.Model(&model.User{}).Where("id = ?", uid).
		Updates(map[string]any{"in_flow": gorm.Expr("in_flow + ?", inBytes), "out_flow": gorm.Expr("out_flow + ?", outBytes), "updated_time": nowMs})
	// user_node upsert
	ur := dbpkg.DB.Model(&model.UserNode{}).Where("user_id = ? AND node_id = ?", uid, node.ID).
		Updates(map[string]any{"in_flow": gorm.Expr("in_flow + ?", inBytes), "out_flow": gorm.Expr("out_flow + ?", outBytes)})
	if ur.RowsAffected == 0 {
		status := 1
		_ = dbpkg.DB.Create(&model.UserNode{
			UserID:        uid,
			NodeID:        node.ID,
			InFlow:        inBytes,
			OutFlow:       outBytes,
			Status:        status,
		}).Error
	}
	// statistics_flow (hourly bucket)
	hourKey := nowCST.Format("01-02 15:00")
	var rec model.StatisticsFlow
	if err := dbpkg.DB.Where("user_id = ? AND time = ?", uid, hourKey).First(&rec).Error; err == nil && rec.ID > 0 {
		dbpkg.DB.Model(&model.StatisticsFlow{}).Where("id = ?", rec.ID).
			Updates(map[string]any{"flow": gorm.Expr("flow + ?", calc), "total_flow": gorm.Expr("total_flow + ?", calc)})
	} else {
		rec = model.StatisticsFlow{UserID: uid, Flow: calc, TotalFlow: calc, Time: hourKey, CreatedTime: nowMs}
		_ = dbpkg.DB.Create(&rec).Error
	}
	_ = dbpkg.DB.Create(&model.FlowTimeseries{
		UserID:      uid,
		InBytes:     inBytes,
		OutBytes:    outBytes,
		BilledBytes: calc,
		Source:      "gost",
		TimeMs:      nowMs,
		CreatedTime: nowMs,
	}).Error

	c.String(http.StatusOK, "ok")
}
