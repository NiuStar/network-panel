package controller

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

type exitNodeItem struct {
	Source      string  `json:"source"`
	NodeID      *int64  `json:"nodeId,omitempty"`
	ExitID      *int64  `json:"exitId,omitempty"`
	Name        string  `json:"name"`
	Host        string  `json:"host"`
	Online      bool    `json:"online"`
	SSPort      *int    `json:"ssPort,omitempty"`
	AnyTLSPort  *int    `json:"anytlsPort,omitempty"`
	AnyTLSExitIP *string `json:"anytlsExitIp,omitempty"`
	Protocol    *string `json:"protocol,omitempty"`
	Port        *int    `json:"port,omitempty"`
	Config      json.RawMessage `json:"config,omitempty"`
}

// ExitNodeList lists internal exit nodes + external exit nodes
// @Summary 出口节点列表
// @Tags exit
// @Accept json
// @Produce json
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/exit/list [post]
func ExitNodeList(c *gin.Context) {
	exitMap := map[int64]*exitNodeItem{}

	var ss []model.ExitSetting
	_ = dbpkg.DB.Find(&ss).Error
	for _, s := range ss {
		item := exitMap[s.NodeID]
		if item == nil {
			id := s.NodeID
			item = &exitNodeItem{Source: "node", NodeID: &id, Online: false}
			exitMap[s.NodeID] = item
		}
		item.SSPort = intPtr(s.Port)
	}

	var at []model.AnyTLSSetting
	_ = dbpkg.DB.Find(&at).Error
	for _, a := range at {
		item := exitMap[a.NodeID]
		if item == nil {
			id := a.NodeID
			item = &exitNodeItem{Source: "node", NodeID: &id, Online: false}
			exitMap[a.NodeID] = item
		}
		item.AnyTLSPort = intPtr(a.Port)
		if v := getAnyTLSExitIP(a.NodeID); v != "" {
			item.AnyTLSExitIP = strPtr(v)
		}
	}

	// attach node info
	nodeExists := map[int64]struct{}{}
	if len(exitMap) > 0 {
		ids := make([]int64, 0, len(exitMap))
		for id := range exitMap {
			ids = append(ids, id)
		}
		var nodes []model.Node
		dbpkg.DB.Where("id IN ?", ids).Find(&nodes)
		for _, n := range nodes {
			nodeExists[n.ID] = struct{}{}
			if item, ok := exitMap[n.ID]; ok {
				item.Name = n.Name
				item.Host = n.ServerIP
				item.Online = n.Status != nil && *n.Status == 1
			}
		}
		staleIDs := make([]int64, 0)
		for id := range exitMap {
			if _, ok := nodeExists[id]; !ok {
				staleIDs = append(staleIDs, id)
			}
		}
		if len(staleIDs) > 0 {
			dbpkg.DB.Where("node_id IN ?", staleIDs).Delete(&model.ExitSetting{})
			dbpkg.DB.Where("node_id IN ?", staleIDs).Delete(&model.AnyTLSSetting{})
		}
	}

	items := make([]exitNodeItem, 0, len(exitMap))
	for _, v := range exitMap {
		if v.NodeID != nil {
			if _, ok := nodeExists[*v.NodeID]; !ok {
				continue
			}
		}
		if v.Name == "" {
			v.Name = "节点"
		}
		items = append(items, *v)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	// external exit nodes
	var externals []model.ExitNodeExternal
	dbpkg.DB.Find(&externals)
	for _, ext := range externals {
		id := ext.ID
		p := ext.Protocol
		port := ext.Port
		var cfg json.RawMessage
		if ext.Config != nil && strings.TrimSpace(*ext.Config) != "" {
			cfg = json.RawMessage(*ext.Config)
		}
		items = append(items, exitNodeItem{
			Source:   "external",
			ExitID:   &id,
			Name:     ext.Name,
			Host:     ext.Host,
			Online:   true,
			Protocol: p,
			Port:     &port,
			Config:   cfg,
		})
	}

	c.JSON(http.StatusOK, response.Ok(items))
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ExitCleanup removes stale exit settings for deleted nodes
// @Summary 清理已删除节点的出口配置
// @Tags exit
// @Accept json
// @Produce json
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/exit/cleanup [post]
func ExitCleanup(c *gin.Context) {
	var nodeIDs []int64
	dbpkg.DB.Model(&model.Node{}).Select("id").Pluck("id", &nodeIDs)
	nodeSet := map[int64]struct{}{}
	for _, id := range nodeIDs {
		nodeSet[id] = struct{}{}
	}

	staleSet := map[int64]struct{}{}
	var exitIDs []int64
	dbpkg.DB.Model(&model.ExitSetting{}).Select("distinct node_id").Pluck("node_id", &exitIDs)
	for _, id := range exitIDs {
		if _, ok := nodeSet[id]; !ok {
			staleSet[id] = struct{}{}
		}
	}
	var anyTLSIDs []int64
	dbpkg.DB.Model(&model.AnyTLSSetting{}).Select("distinct node_id").Pluck("node_id", &anyTLSIDs)
	for _, id := range anyTLSIDs {
		if _, ok := nodeSet[id]; !ok {
			staleSet[id] = struct{}{}
		}
	}

	if len(staleSet) == 0 {
		c.JSON(http.StatusOK, response.Ok(map[string]interface{}{
			"deletedExit":   0,
			"deletedAnyTLS": 0,
		}))
		return
	}
	staleIDs := make([]int64, 0, len(staleSet))
	for id := range staleSet {
		staleIDs = append(staleIDs, id)
	}
	resExit := dbpkg.DB.Where("node_id IN ?", staleIDs).Delete(&model.ExitSetting{})
	resAny := dbpkg.DB.Where("node_id IN ?", staleIDs).Delete(&model.AnyTLSSetting{})
	c.JSON(http.StatusOK, response.Ok(map[string]interface{}{
		"deletedExit":   resExit.RowsAffected,
		"deletedAnyTLS": resAny.RowsAffected,
	}))
}

// ExitExternalCreate creates external exit node
// @Summary 新增外部出口节点
// @Tags exit
// @Accept json
// @Produce json
// @Param data body object true "name, host, port, protocol"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/exit/external/create [post]
func ExitExternalCreate(c *gin.Context) {
	var p struct {
		Name     string  `json:"name"`
		Host     string  `json:"host"`
		Port     int     `json:"port"`
		Protocol *string `json:"protocol"`
		Config   json.RawMessage `json:"config"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Host = strings.TrimSpace(p.Host)
	if p.Name == "" || p.Host == "" || p.Port <= 0 || p.Port > 65535 {
		c.JSON(http.StatusOK, response.ErrMsg("名称/地址/端口不合法"))
		return
	}
	now := time.Now().UnixMilli()
	item := model.ExitNodeExternal{
		BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now},
		Name:       p.Name,
		Host:       p.Host,
		Port:       p.Port,
		Protocol:   p.Protocol,
		Config:     nil,
	}
	if cfg := strings.TrimSpace(string(p.Config)); cfg != "" && cfg != "null" && cfg != "{}" {
		item.Config = &cfg
	}
	if err := dbpkg.DB.Create(&item).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("创建失败"))
		return
	}
	c.JSON(http.StatusOK, response.Ok(item))
}

// ExitExternalUpdate updates external exit node
// @Summary 更新外部出口节点
// @Tags exit
// @Accept json
// @Produce json
// @Param data body object true "id, name, host, port, protocol"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/exit/external/update [post]
func ExitExternalUpdate(c *gin.Context) {
	var p struct {
		ID       int64   `json:"id"`
		Name     string  `json:"name"`
		Host     string  `json:"host"`
		Port     int     `json:"port"`
		Protocol *string `json:"protocol"`
		Config   json.RawMessage `json:"config"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var item model.ExitNodeExternal
	if err := dbpkg.DB.First(&item, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("记录不存在"))
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Host = strings.TrimSpace(p.Host)
	if p.Name == "" || p.Host == "" || p.Port <= 0 || p.Port > 65535 {
		c.JSON(http.StatusOK, response.ErrMsg("名称/地址/端口不合法"))
		return
	}
	item.Name = p.Name
	item.Host = p.Host
	item.Port = p.Port
	item.Protocol = p.Protocol
	item.Config = nil
	if cfg := strings.TrimSpace(string(p.Config)); cfg != "" && cfg != "null" && cfg != "{}" {
		item.Config = &cfg
	}
	item.UpdatedTime = time.Now().UnixMilli()
	if err := dbpkg.DB.Save(&item).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("更新失败"))
		return
	}
	c.JSON(http.StatusOK, response.Ok(item))
}

// ExitExternalDelete deletes external exit node
// @Summary 删除外部出口节点
// @Tags exit
// @Accept json
// @Produce json
// @Param data body object true "id"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/exit/external/delete [post]
func ExitExternalDelete(c *gin.Context) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if err := dbpkg.DB.Delete(&model.ExitNodeExternal{}, p.ID).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("删除失败"))
		return
	}
	c.JSON(http.StatusOK, response.OkMsg("删除成功"))
}

func intPtr(v int) *int { return &v }
