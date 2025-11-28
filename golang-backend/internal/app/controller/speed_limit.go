package controller

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/dto"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

// POST /api/v1/speed-limit/create
func SpeedLimitCreate(c *gin.Context) {
	var req dto.SpeedLimitDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var t model.Tunnel
	if err := dbpkg.DB.First(&t, req.TunnelID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("隧道不存在"))
		return
	}
	// create
	now := time.Now().UnixMilli()
	sl := model.SpeedLimit{CreatedTime: now, UpdatedTime: now, Status: 1, Name: req.Name, Speed: req.Speed, TunnelID: req.TunnelID, TunnelName: req.TunnelName}
	if err := dbpkg.DB.Create(&sl).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("限速规则创建失败"))
		return
	}
	applyLimiterForTunnel(sl.TunnelID)
	// Gost limiter add is stubbed
	c.JSON(http.StatusOK, response.OkNoData())
}

// POST /api/v1/speed-limit/list
func SpeedLimitList(c *gin.Context) {
	var list []model.SpeedLimit
	dbpkg.DB.Find(&list)
	c.JSON(http.StatusOK, response.Ok(list))
}

// POST /api/v1/speed-limit/update
func SpeedLimitUpdate(c *gin.Context) {
	var req dto.SpeedLimitUpdateDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var sl model.SpeedLimit
	if err := dbpkg.DB.First(&sl, req.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("限速规则不存在"))
		return
	}
	var t model.Tunnel
	if err := dbpkg.DB.First(&t, req.TunnelID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("隧道不存在"))
		return
	}
	sl.Name, sl.Speed, sl.TunnelID, sl.TunnelName = req.Name, req.Speed, req.TunnelID, req.TunnelName
	sl.UpdatedTime = time.Now().UnixMilli()
	if err := dbpkg.DB.Save(&sl).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("限速规则更新失败"))
		return
	}
	applyLimiterForTunnel(sl.TunnelID)
	c.JSON(http.StatusOK, response.OkMsg("限速规则更新成功"))
}

// POST /api/v1/speed-limit/delete
func SpeedLimitDelete(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var cnt int64
	dbpkg.DB.Model(&model.UserTunnel{}).Where("speed_id = ?", p.ID).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusOK, response.ErrMsg("该限速规则还有用户在使用 请先取消分配"))
		return
	}
	if err := dbpkg.DB.Delete(&model.SpeedLimit{}, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("限速规则删除失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("限速规则删除成功"))
}

// POST /api/v1/speed-limit/tunnels
func SpeedLimitTunnels(c *gin.Context) {
	var list []model.Tunnel
	dbpkg.DB.Find(&list)
	c.JSON(http.StatusOK, response.Ok(list))
}

// applyLimiterForTunnel pushes limiter config to all entry services on the tunnel's entry node(s).
// It updates existing services via UpdateService, safe to call repeatedly.
func applyLimiterForTunnel(tunnelID int64) {
	type row struct {
		ID       int64
		UserID   int64
		TunnelID int64
		InNodeID int64
	}
	var rows []row
	dbpkg.DB.Table("forward f").
		Select("f.id, f.user_id, f.tunnel_id, t.in_node_id").
		Joins("left join tunnel t on t.id = f.tunnel_id").
		Where("f.tunnel_id = ?", tunnelID).
		Scan(&rows)
	if len(rows) == 0 {
		return
	}
	byNode := map[int64][]map[string]any{}
	for _, r := range rows {
		if r.InNodeID == 0 {
			continue
		}
		svc := fetchServiceByName(r.InNodeID, buildServiceName(r.ID, r.UserID, r.TunnelID))
		if svc == nil {
			continue
		}
		if mergeLimiterIntoService(svc, r.InNodeID) {
			byNode[r.InNodeID] = append(byNode[r.InNodeID], svc)
		}
	}
	for nodeID, patches := range byNode {
		_ = sendWSCommand(nodeID, "UpdateService", expandRUDP(patches))
	}
}
