package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

// NodeSetExit 配置出口节点服务
// @Summary 配置出口节点服务
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeExitReq true "出口服务配置"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/node/set-exit [post]
// POST /api/v1/node/set-exit {nodeId, port, password, method?}
// Creates/updates an SS server service on the selected node with given port/password.
func NodeSetExit(c *gin.Context) {
	var p struct {
		NodeID        int64   `json:"nodeId" binding:"required"`
		Type          string  `json:"type"`
		Port          int     `json:"port" binding:"required"`
		Password      string  `json:"password" binding:"required"`
		Method        string  `json:"method"`
		ExitIP        *string `json:"exitIp"`
		AllowFallback *bool   `json:"allowFallback"`
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
	if _, _, _, _, _, errMsg, ok := nodeAccess(c, p.NodeID, true); !ok {
		c.JSON(http.StatusOK, response.ErrMsg(errMsg))
		return
	}
	exitType := strings.ToLower(strings.TrimSpace(p.Type))
	if exitType == "" {
		exitType = "ss"
	}
	if exitType != "ss" && exitType != "anytls" {
		c.JSON(http.StatusOK, response.ErrMsg("无效的出口类型"))
		return
	}
	_, un, _, _, isShared, errMsg, ok := nodeAccess(c, p.NodeID, true)
	if !ok {
		c.JSON(http.StatusOK, response.ErrMsg(errMsg))
		return
	}
	if isShared {
		if !portAllowedForShared(un, p.Port) {
			c.JSON(http.StatusOK, response.ErrMsg("端口不在授权范围"))
			return
		}
	}
	if p.Port <= 0 || p.Port > 65535 || p.Password == "" {
		c.JSON(http.StatusOK, response.ErrMsg("无效的端口或密码"))
		return
	}
	if p.Method == "" && exitType == "ss" {
		p.Method = "AEAD_CHACHA20_POLY1305"
	}

	if exitType == "anytls" {
		allowFallback := getAnyTLSExitFallback(p.NodeID)
		if p.AllowFallback != nil {
			allowFallback = *p.AllowFallback
		}
		exitIP := getAnyTLSExitIP(p.NodeID)
		if p.ExitIP != nil {
			exitIP = strings.TrimSpace(*p.ExitIP)
		}
		var baseUserID int64
		if v, ok := c.Get("user_id"); ok {
			if id, ok2 := v.(int64); ok2 {
				baseUserID = id
			}
		}
		if baseUserID == 0 {
			var u model.User
			if err := dbpkg.DB.Where("role_id = 0").Order("id asc").First(&u).Error; err == nil {
				baseUserID = u.ID
			}
		}
		req := map[string]interface{}{
			"requestId": RandUUID(),
			"port":      p.Port,
			"password":  p.Password,
			"users":     buildAnyTLSUsersForNode(p.NodeID, p.Password),
		}
		if baseUserID > 0 {
			req["baseUserId"] = baseUserID
		}
		if exitIP != "" {
			req["exitIp"] = exitIP
		}
		req["allowFallback"] = allowFallback
		if res, ok := RequestOp(p.NodeID, "SetAnyTLS", req, 12*time.Second); ok {
			msg := "AnyTLS 出口已创建/更新"
			success := true
			if data, _ := res["data"].(map[string]interface{}); data != nil {
				if v, _ := data["message"].(string); v != "" {
					msg = v
				}
				if v, _ := data["success"].(bool); !v {
					success = false
				}
			}
			if !success {
				c.JSON(http.StatusOK, response.ErrMsg(msg))
				return
			}
			now := time.Now().UnixMilli()
			var existing model.AnyTLSSetting
			tx := dbpkg.DB.Where("node_id = ?", p.NodeID).First(&existing)
			if tx.Error == nil && existing.ID > 0 {
				existing.Port = p.Port
				existing.Password = p.Password
				if baseUserID > 0 {
					existing.BaseUserID = &baseUserID
				}
				existing.UpdatedTime = now
				_ = dbpkg.DB.Save(&existing).Error
			} else {
				status := 1
				rec := model.AnyTLSSetting{
					BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now, Status: &status},
					NodeID:     p.NodeID,
					Port:       p.Port,
					Password:   p.Password,
					BaseUserID: func() *int64 {
						if baseUserID > 0 {
							return &baseUserID
						}
						return nil
					}(),
				}
				_ = dbpkg.DB.Create(&rec).Error
			}
			if p.ExitIP != nil {
				_ = setAnyTLSExitIP(p.NodeID, exitIP)
			}
			if p.AllowFallback != nil {
				_ = setAnyTLSExitFallback(p.NodeID, allowFallback)
			}
			c.JSON(http.StatusOK, response.OkMsg(msg))
			return
		}
		c.JSON(http.StatusOK, response.ErrMsg("节点未响应，请稍后重试"))
		return
	}

	// Build service config (SS)
	name := fmt.Sprintf("exit_ss_%d", p.Port)
	var baseUserID int64
	if v, ok := c.Get("user_id"); ok {
		if id, ok2 := v.(int64); ok2 {
			baseUserID = id
		}
	}
	if baseUserID == 0 {
		var u model.User
		if err := dbpkg.DB.Where("role_id = 0").Order("id asc").First(&u).Error; err == nil {
			baseUserID = u.ID
		}
	}
	exitObserverName := ""
	var exitObserverSpec map[string]any
	if baseUserID > 0 {
		exitObserverName, exitObserverSpec = buildExitObserverPluginSpec(p.NodeID, baseUserID, p.Port)
	}
	obsName := exitObserverName
	if obsName == "" {
		obsName = strings.TrimSpace(p.Observer)
	}
	svc := buildSSService(name, p.Port, p.Password, p.Method, map[string]any{
		"observer": obsName,
		"limiter":  p.Limiter,
		"rlimiter": p.RLimiter,
		"metadata": p.Metadata,
	})
	if exitObserverSpec != nil {
		svc["_observers"] = []any{exitObserverSpec}
	}
	// 同步等待 agent 回执，便于捕获 GOST 配置错误
	req := map[string]interface{}{
		"requestId": RandUUID(),
		"services":  expandRUDP([]map[string]any{svc}),
	}
	if res, ok := RequestOp(p.NodeID, "AddService", req, 10*time.Second); ok {
		// Parse agent result
		msg := "出口节点服务已创建/更新"
		success := true
		if data, _ := res["data"].(map[string]interface{}); data != nil {
			if v, _ := data["message"].(string); v != "" {
				msg = v
			}
			if v, _ := data["success"].(bool); !v {
				success = false
			}
		}
		if !success {
			c.JSON(http.StatusOK, response.ErrMsg(msg))
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
			if baseUserID > 0 {
				existing.BaseUserID = &baseUserID
			}
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
				BaseUserID: func() *int64 {
					if baseUserID > 0 {
						return &baseUserID
					}
					return nil
				}(),
				Metadata:   metaStr,
			}
			_ = dbpkg.DB.Create(&rec).Error
		}
		c.JSON(http.StatusOK, response.OkMsg(msg))
		return
	}

	c.JSON(http.StatusOK, response.ErrMsg("节点未响应，请稍后重试"))
	return

}

// NodeGetExit 获取出口节点配置
// @Summary 获取出口节点配置
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeSimpleReq true "节点ID"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/get-exit [post]
// POST /api/v1/node/get-exit {nodeId}
// Returns last saved SS exit settings for node if any
func NodeGetExit(c *gin.Context) {
	var p struct {
		NodeID int64  `json:"nodeId" binding:"required"`
		Type   string `json:"type"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	exitType := strings.ToLower(strings.TrimSpace(p.Type))
	if exitType == "" {
		exitType = "ss"
	}
	if exitType == "anytls" {
		var item model.AnyTLSSetting
		if err := dbpkg.DB.Where("node_id = ?", p.NodeID).First(&item).Error; err != nil || item.ID == 0 {
			c.JSON(http.StatusOK, response.OkNoData())
			return
		}
		exitIP := getAnyTLSExitIP(p.NodeID)
		allowFallback := getAnyTLSExitFallback(p.NodeID)
		out := gin.H{
			"nodeId":        item.NodeID,
			"port":          item.Port,
			"password":      item.Password,
			"type":          "anytls",
			"exitIp":        exitIP,
			"allowFallback": allowFallback,
		}
		c.JSON(http.StatusOK, response.Ok(out))
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

func anytlsExitIPKey(nodeID int64) string { return fmt.Sprintf("anytls_exit_ip_%d", nodeID) }
func anytlsExitFallbackKey(nodeID int64) string {
	return fmt.Sprintf("anytls_exit_fallback_%d", nodeID)
}

func setAnyTLSExitIP(nodeID int64, ip string) error {
	key := anytlsExitIPKey(nodeID)
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return dbpkg.DB.Where("name = ?", key).Delete(&model.ViteConfig{}).Error
	}
	now := time.Now().UnixMilli()
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err == nil {
		cfg.Value = ip
		cfg.Time = now
		return dbpkg.DB.Save(&cfg).Error
	}
	return dbpkg.DB.Create(&model.ViteConfig{Name: key, Value: ip, Time: now}).Error
}

func getAnyTLSExitIP(nodeID int64) string {
	key := anytlsExitIPKey(nodeID)
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Value)
}

func setAnyTLSExitFallback(nodeID int64, allow bool) error {
	key := anytlsExitFallbackKey(nodeID)
	val := "false"
	if allow {
		val = "true"
	}
	now := time.Now().UnixMilli()
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err == nil {
		cfg.Value = val
		cfg.Time = now
		return dbpkg.DB.Save(&cfg).Error
	}
	return dbpkg.DB.Create(&model.ViteConfig{Name: key, Value: val, Time: now}).Error
}

func getAnyTLSExitFallback(nodeID int64) bool {
	key := anytlsExitFallbackKey(nodeID)
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cfg.Value), "true")
}
