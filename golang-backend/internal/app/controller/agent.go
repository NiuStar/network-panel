package controller

import (
    "fmt"
    "net/http"
    "time"

    "github.com/gin-gonic/gin"
    "network-panel/golang-backend/internal/app/model"
    "network-panel/golang-backend/internal/app/response"
    dbpkg "network-panel/golang-backend/internal/db"
)

// POST /api/v1/agent/desired-services {secret}
// Returns desired gost services for the node resolved by secret
func AgentDesiredServices(c *gin.Context) {
	var p struct {
		Secret string `json:"secret" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var node model.Node
	if err := dbpkg.DB.Where("secret = ?", p.Secret).First(&node).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	services := desiredServices(node.ID)
	c.JSON(http.StatusOK, response.Ok(services))
}

// POST /api/v1/agent/push-services {secret, services: []}
// Server will send AddService to gost connection for that node.
func AgentPushServices(c *gin.Context) {
	var p struct {
		Secret   string           `json:"secret" binding:"required"`
		Services []map[string]any `json:"services"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var node model.Node
	if err := dbpkg.DB.Where("secret = ?", p.Secret).First(&node).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	if len(p.Services) == 0 {
		c.JSON(http.StatusOK, response.OkNoData())
		return
	}
	_ = sendWSCommand(node.ID, "AddService", expandRUDP(p.Services))
	c.JSON(http.StatusOK, response.OkNoData())
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }

// POST /api/v1/agent/reconcile {secret}
// Server computes missing services vs gost.json-reported not available (agent does local read). Here we only push desired set unconditionally.
func AgentReconcile(c *gin.Context) {
	var p struct {
		Secret string `json:"secret" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var node model.Node
	if err := dbpkg.DB.Where("secret = ?", p.Secret).First(&node).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	services := desiredServices(node.ID)
	if len(services) > 0 {
		_ = sendWSCommand(node.ID, "AddService", expandRUDP(services))
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"pushed": len(services)}))
}

// compute desired services for a node id from forwards+tunnels
func desiredServices(nodeID int64) []map[string]any {
	var rows []struct {
		model.Forward
		TType      int     `gorm:"column:t_type"`
		InNodeID   int64   `gorm:"column:in_node_id"`
		OutNodeID  *int64  `gorm:"column:out_node_id"`
		OutIP      *string `gorm:"column:out_ip"`
		TInterface *string `gorm:"column:t_interface"`
	}
	dbpkg.DB.Table("forward f").
		Select("f.*, t.type as t_type, t.in_node_id, t.out_node_id, t.out_ip, t.interface_name as t_interface").
		Joins("left join tunnel t on t.id = f.tunnel_id").Scan(&rows)
	services := make([]map[string]any, 0)
	for _, r := range rows {
		name := buildServiceName(r.ID, r.UserID, r.TunnelID)
		iface := preferIface(r.InterfaceName, r.TInterface)
		if r.TType == 2 {
			// Do not compute desired services for tunnel-forward; these are managed at create/update time
			continue
		} else {
            if r.InNodeID == nodeID {
                svc := buildServiceConfig(name, r.InPort, r.RemoteAddr, iface)
                if obsName, spec := buildObserverPluginSpec(nodeID, name); obsName != "" && spec != nil {
                    svc["observer"] = obsName
                    svc["_observers"] = []any{spec}
                }
                services = append(services, svc)
            }
		}
	}
	return services
}

// POST /api/v1/agent/remove-services {secret, services:[name...]}
func AgentRemoveServices(c *gin.Context) {
	var p struct {
		Secret   string   `json:"secret" binding:"required"`
		Services []string `json:"services"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var node model.Node
	if err := dbpkg.DB.Where("secret = ?", p.Secret).First(&node).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	if len(p.Services) == 0 {
		c.JSON(http.StatusOK, response.OkNoData())
		return
	}
    _ = sendWSCommand(node.ID, "DeleteService", map[string]any{"services": expandNamesWithRUDP(p.Services)})
	c.JSON(http.StatusOK, response.OkNoData())
}

// POST /api/v1/agent/reconcile-node {nodeId}
// Admin-triggered reconcile by nodeId
func AgentReconcileNode(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var node model.Node
	if err := dbpkg.DB.First(&node, p.NodeID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}
	services := desiredServices(node.ID)
	if len(services) > 0 {
		_ = sendWSCommand(node.ID, "AddService", expandRUDP(services))
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"pushed": len(services)}))
}

// POST /api/v1/agent/report-services {secret, services: [name...], timeMs?}
// Update in-memory snapshot of services present on a node, reported by agent every few seconds.
func AgentReportServices(c *gin.Context) {
    var p struct {
        Secret   string   `json:"secret" binding:"required"`
        Services []string `json:"services"`
        Hashes   map[string]string `json:"hashes"`
        TimeMs   int64    `json:"timeMs"`
    }
    if err := c.ShouldBindJSON(&p); err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
        return
    }
    var node model.Node
    if err := dbpkg.DB.Where("secret = ?", p.Secret).First(&node).Error; err != nil {
        c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
        return
    }
    ts := p.TimeMs
    if ts <= 0 { ts = time.Now().UnixMilli() }
    updateNodeServices(node.ID, p.Services, p.Hashes, ts)
    c.JSON(http.StatusOK, response.OkNoData())
}
