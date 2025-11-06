package controller

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"network-panel/golang-backend/internal/app/response"
	"time"
)

// POST /api/v1/node/query-services {nodeId, filter?, requestId?}
func NodeQueryServices(c *gin.Context) {
	var p struct {
		NodeID int64  `json:"nodeId" binding:"required"`
		Filter string `json:"filter"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}

	req := map[string]interface{}{
		"requestId": RandUUID(),
		"filter":    p.Filter,
	}
	// send explicit QueryServices command
	if err := sendWSCommand(p.NodeID, "QueryServices", req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点未连接: "+err.Error()))
		return
	}
	// wait for result using the same waiter map
	ch := make(chan map[string]interface{}, 1)
	diagMu.Lock()
	diagWaiters[req["requestId"].(string)] = ch
	diagMu.Unlock()
	select {
	case res := <-ch:
		// expect {type: QueryServicesResult, requestId, data: [...]}
		if data, _ := res["data"].([]interface{}); data != nil {
			c.JSON(http.StatusOK, response.Ok(data))
			return
		}
		c.JSON(http.StatusOK, response.Ok(res["data"]))
	case <-time.After(5 * time.Second):
		diagMu.Lock()
		delete(diagWaiters, req["requestId"].(string))
		diagMu.Unlock()
		c.JSON(http.StatusOK, response.ErrMsg("查询超时"))
	}
}
