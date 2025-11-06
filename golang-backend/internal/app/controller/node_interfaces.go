package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

// POST /api/v1/node/interfaces {nodeId}
func NodeInterfaces(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var r model.NodeRuntime
	if err := dbpkg.DB.First(&r, "node_id = ?", p.NodeID).Error; err != nil || r.Interfaces == nil {
		c.JSON(http.StatusOK, response.Ok(map[string]any{"ips": []string{}}))
		return
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"ips": r.Interfaces}))
}
