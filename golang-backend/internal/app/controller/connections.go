package controller

import (
	"net/http"
	"network-panel/golang-backend/internal/app/response"

	"github.com/gin-gonic/gin"
)

// NodeConnections 获取节点连接信息
// @Summary 获取节点连接信息
// @Tags node
// @Produce json
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/connections [get]
// NodeConnections returns current WS connections per nodeId with versions
// GET /api/v1/node/connections
func NodeConnections(c *gin.Context) {
	if roleInf, ok := c.Get("role_id"); ok && roleInf != 0 {
		c.JSON(http.StatusOK, response.ErrMsg("无权限"))
		return
	}
	nodeConnMu.RLock()
	defer nodeConnMu.RUnlock()
	type connInfo struct {
		Version string `json:"version"`
	}
	type nodeInfo struct {
		NodeID int64      `json:"nodeId"`
		Conns  []connInfo `json:"conns"`
	}
	out := make([]nodeInfo, 0, len(nodeConns))
	for id, list := range nodeConns {
		item := nodeInfo{NodeID: id}
		for _, nc := range list {
			item.Conns = append(item.Conns, connInfo{Version: nc.ver})
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, response.Ok(out))
}
