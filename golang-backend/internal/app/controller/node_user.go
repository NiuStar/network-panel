package controller

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/dto"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/db"
)

// NodeUserNode 获取用户可用节点
// @Summary 用户可用节点列表
// @Tags node
// @Produce json
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/user/node [post]
func NodeUserNode(c *gin.Context) {
	roleID, _ := c.Get("role_id")
	userID, _ := c.Get("user_id")
	var nodes []model.Node
	if roleID == 0 || roleID == nil { // admin or no token
		db.DB.Where("status = ?", 1).Find(&nodes)
	} else {
		db.DB.Raw(`select n.* from node n join user_node un on un.node_id=n.id where un.user_id=? and un.status=1`, userID).Scan(&nodes)
	}
	c.JSON(http.StatusOK, response.Ok(nodes))
}

// NodeUserAssign 分配节点权限给用户
// @Summary 分配节点权限
// @Tags node
// @Accept json
// @Produce json
// @Param data body dto.UserNodeDto true "用户节点权限"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/user/assign [post]
func NodeUserAssign(c *gin.Context) {
	var req dto.UserNodeDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var cnt int64
	db.DB.Model(&model.UserNode{}).Where("user_id=? and node_id=?", req.UserID, req.NodeID).Count(&cnt)
	if cnt > 0 {
		c.JSON(http.StatusOK, response.ErrMsg("该用户已拥有此节点权限"))
		return
	}
	speedMbps := val(req.SpeedMbps, 0)
	if speedMbps < 0 {
		speedMbps = 0
	}
	un := model.UserNode{
		UserID:        req.UserID,
		NodeID:        req.NodeID,
		Flow:          req.Flow,
		Num:           req.Num,
		PortRanges:    strings.TrimSpace(req.PortRanges),
		FlowResetTime: req.FlowResetTime,
		ExpTime:       req.ExpTime,
		SpeedMbps:     speedMbps,
		Status:        val(req.Status, 1),
	}
	if err := db.DB.Create(&un).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户节点权限分配失败"))
		return
	}
	go pushAnyTLSConfigToNode(un.NodeID)
	c.JSON(http.StatusOK, response.OkMsg("用户节点权限分配成功"))
}

// NodeUserList 用户节点权限列表
// @Summary 用户节点权限列表
// @Tags node
// @Accept json
// @Produce json
// @Param data body dto.UserNodeQueryDto true "用户ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/user/list [post]
func NodeUserList(c *gin.Context) {
	var req dto.UserNodeQueryDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var items []struct {
		model.UserNode
		NodeName string `json:"nodeName"`
		// StatusReason is computed in handler
		StatusReason string `json:"statusReason"`
	}
	db.DB.Table("user_node un").
		Select("un.*, n.name as node_name").
		Joins("left join node n on n.id = un.node_id").
		Where("un.user_id = ?", req.UserID).
		Scan(&items)
	nowMs := time.Now().UnixMilli()
	for i := range items {
		if items[i].Status == 1 {
			continue
		}
		items[i].StatusReason = nodeDisableReason(items[i].UserNode, nowMs)
	}
	c.JSON(http.StatusOK, response.Ok(items))
}

func nodeDisableReason(un model.UserNode, nowMs int64) string {
	if un.Flow > 0 && (un.InFlow+un.OutFlow) >= un.Flow*1024*1024*1024 {
		return "流量超额"
	}
	if un.ExpTime != nil && *un.ExpTime > 0 && *un.ExpTime <= nowMs {
		return "已过期"
	}
	return "已禁用"
}

// NodeUserRemove 移除用户节点权限
// @Summary 移除用户节点权限
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerIDReq true "用户节点记录ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/user/remove [post]
func NodeUserRemove(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var un model.UserNode
	if err := db.DB.First(&un, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("未找到对应的用户节点权限记录"))
		return
	}
	db.DB.Delete(&un)
	go pushAnyTLSConfigToNode(un.NodeID)
	c.JSON(http.StatusOK, response.OkMsg("用户节点权限删除成功"))
}

// NodeUserUpdate 更新用户节点权限
// @Summary 更新用户节点权限
// @Tags node
// @Accept json
// @Produce json
// @Param data body dto.UserNodeUpdateDto true "更新内容"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/user/update [post]
func NodeUserUpdate(c *gin.Context) {
	var req dto.UserNodeUpdateDto
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var un model.UserNode
	if err := db.DB.First(&un, req.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户节点权限不存在"))
		return
	}
	un.Flow, un.Num = req.Flow, req.Num
	if req.PortRanges != nil {
		un.PortRanges = strings.TrimSpace(*req.PortRanges)
	}
	if req.FlowResetTime != nil {
		un.FlowResetTime = req.FlowResetTime
	}
	if req.ExpTime != nil {
		un.ExpTime = req.ExpTime
	}
	if req.Status != nil {
		un.Status = *req.Status
	}
	if req.SpeedMbps != nil {
		if *req.SpeedMbps < 0 {
			un.SpeedMbps = 0
		} else {
			un.SpeedMbps = *req.SpeedMbps
		}
	}
	if err := db.DB.Save(&un).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("用户节点权限更新失败"))
		return
	}
	go pushAnyTLSConfigToNode(un.NodeID)
	c.JSON(http.StatusOK, response.OkMsg("用户节点权限更新成功"))
}

// NodeUserUsageByNode 管理员查看指定节点的用户用量
// @Summary 管理员查看指定节点的用户用量
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerIDReq true "nodeId"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/user/usage [post]
func NodeUserUsageByNode(c *gin.Context) {
	roleID, _ := c.Get("role_id")
	if roleID != nil && roleID != 0 {
		c.JSON(http.StatusOK, response.ErrMsg("无权限"))
		return
	}
	var req struct {
		NodeID int64 `json:"nodeId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.NodeID == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var items []struct {
		model.UserNode
		UserName string `json:"userName"`
	}
	db.DB.Table("user_node un").
		Select("un.*, u.user as user_name").
		Joins("left join user u on u.id = un.user_id").
		Where("un.node_id = ?", req.NodeID).
		Order("un.user_id asc").
		Scan(&items)
	c.JSON(http.StatusOK, response.Ok(items))
}
