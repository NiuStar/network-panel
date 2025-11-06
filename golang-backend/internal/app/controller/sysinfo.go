package controller

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

// POST /api/v1/node/sysinfo {nodeId, range}
// range: 1h,12h,1d,7d,30d
func NodeSysinfo(c *gin.Context) {
	var p struct {
		NodeID int64  `json:"nodeId" binding:"required"`
		Range  string `json:"range"`
		Limit  int    `json:"limit"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	now := time.Now().UnixMilli()
	var windowMs int64
	switch p.Range {
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
	q := dbpkg.DB.Model(&model.NodeSysInfo{}).Where("node_id = ? AND time_ms >= ?", p.NodeID, from).Order("time_ms asc")
	if p.Limit > 0 {
		q = q.Limit(p.Limit)
	}
	var list []model.NodeSysInfo
	q.Find(&list)
	c.JSON(http.StatusOK, response.Ok(list))
}
