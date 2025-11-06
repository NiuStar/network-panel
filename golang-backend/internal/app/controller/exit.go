package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

// POST /api/v1/node/set-exit {nodeId, port, password, method?}
// Creates/updates an SS server service on the selected node with given port/password.
func NodeSetExit(c *gin.Context) {
	var p struct {
		NodeID   int64  `json:"nodeId" binding:"required"`
		Port     int    `json:"port" binding:"required"`
		Password string `json:"password" binding:"required"`
		Method   string `json:"method"`
		// optional extras
		Observer string                 `json:"observer"`
		Limiter  string                 `json:"limiter"`
		RLimiter string                 `json:"rlimiter"`
		Metadata map[string]interface{} `json:"metadata"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	if p.Port <= 0 || p.Port > 65535 || p.Password == "" {
		c.JSON(http.StatusOK, response.ErrMsg("无效的端口或密码"))
		return
	}
	if p.Method == "" {
		p.Method = "AEAD_CHACHA20_POLY1305"
	}

	// Ensure node exists
	var cnt int64
	dbpkg.DB.Table("node").Where("id = ?", p.NodeID).Count(&cnt)
	if cnt == 0 {
		c.JSON(http.StatusOK, response.ErrMsg("节点不存在"))
		return
	}

	// Build service config and push to node
	name := fmt.Sprintf("exit_ss_%d", p.Port)
	svc := buildSSService(name, p.Port, p.Password, p.Method, map[string]any{
		"observer": p.Observer,
		"limiter":  p.Limiter,
		"rlimiter": p.RLimiter,
		"metadata": p.Metadata,
	})
	if err := sendWSCommand(p.NodeID, "AddService", []map[string]any{svc}); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("发送到节点失败: "+err.Error()))
		return
	}

	// persist settings for this node (upsert by node_id)
	now := time.Now().UnixMilli()
	var metaStr *string
	if p.Metadata != nil {
		if b, err := json.Marshal(p.Metadata); err == nil {
			s := string(b)
			metaStr = &s
		}
	}
	var existing model.ExitSetting
	tx := dbpkg.DB.Where("node_id = ?", p.NodeID).First(&existing)
	if tx.Error == nil && existing.ID > 0 {
		existing.Port = p.Port
		existing.Password = p.Password
		existing.Method = p.Method
		existing.Observer = strPtrOrNil(p.Observer)
		existing.Limiter = strPtrOrNil(p.Limiter)
		existing.RLimiter = strPtrOrNil(p.RLimiter)
		existing.Metadata = metaStr
		existing.UpdatedTime = now
		_ = dbpkg.DB.Save(&existing).Error
	} else {
		status := 1
		rec := model.ExitSetting{
			BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now, Status: &status},
			NodeID:     p.NodeID,
			Port:       p.Port,
			Password:   p.Password,
			Method:     p.Method,
			Observer:   strPtrOrNil(p.Observer),
			Limiter:    strPtrOrNil(p.Limiter),
			RLimiter:   strPtrOrNil(p.RLimiter),
			Metadata:   metaStr,
		}
		_ = dbpkg.DB.Create(&rec).Error
	}
	c.JSON(http.StatusOK, response.OkMsg("出口节点服务已创建/更新"))
}

// POST /api/v1/node/get-exit {nodeId}
// Returns last saved SS exit settings for node if any
func NodeGetExit(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var item model.ExitSetting
	if err := dbpkg.DB.Where("node_id = ?", p.NodeID).First(&item).Error; err != nil || item.ID == 0 {
		c.JSON(http.StatusOK, response.OkNoData())
		return
	}
	// unpack metadata JSON string into map for frontend convenience
	var meta map[string]interface{}
	if item.Metadata != nil && *item.Metadata != "" {
		_ = json.Unmarshal([]byte(*item.Metadata), &meta)
	}
	out := gin.H{
		"nodeId":   item.NodeID,
		"port":     item.Port,
		"password": item.Password,
		"method":   item.Method,
		"observer": deref(item.Observer),
		"limiter":  deref(item.Limiter),
		"rlimiter": deref(item.RLimiter),
		"metadata": meta,
	}
	c.JSON(http.StatusOK, response.Ok(out))
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
