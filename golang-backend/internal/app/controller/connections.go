package controller

import (
	"net/http"
	"network-panel/golang-backend/internal/app/response"

	"github.com/gin-gonic/gin"
)

// NodeConnections returns current WS connections per nodeId with versions
// GET /api/v1/node/connections
func NodeConnections(c *gin.Context) {
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
