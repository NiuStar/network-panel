package controller

import (
    "encoding/json"
    "net/http"
    "strconv"

    "github.com/gin-gonic/gin"
    "network-panel/golang-backend/internal/app/model"
    "network-panel/golang-backend/internal/app/response"
    dbpkg "network-panel/golang-backend/internal/db"
)

// in-bind IP map: per-node listener bind IP for tunnel-forward (exclude entry by convention)

func tunnelBindKey(tid int64) string { return "tunnel_bindip_" + strconv.FormatInt(tid, 10) }

// TunnelBindGet 获取绑定IP
// @Summary 获取隧道节点绑定IP
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerTunnelIDReq true "隧道ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/bind/get [post]
func TunnelBindGet(c *gin.Context) {
    var p struct{ TunnelID int64 `json:"tunnelId" binding:"required"` }
    if err := c.ShouldBindJSON(&p); err != nil { c.JSON(http.StatusOK, response.ErrMsg("参数错误")); return }
    m := getTunnelBindMap(p.TunnelID)
    list := make([]map[string]any, 0, len(m))
    for k, v := range m { list = append(list, map[string]any{"nodeId": k, "ip": v}) }
    c.JSON(http.StatusOK, response.Ok(map[string]any{"binds": list}))
}

// TunnelBindSet 设置绑定IP
// @Summary 设置隧道节点绑定IP
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerTunnelBindReq true "隧道ID与绑定IP"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/bind/set [post]
func TunnelBindSet(c *gin.Context) {
    var p struct{ TunnelID int64 `json:"tunnelId" binding:"required"`; Binds []struct{ NodeID int64 `json:"nodeId"`; IP string `json:"ip"` } `json:"binds"` }
    if err := c.ShouldBindJSON(&p); err != nil { c.JSON(http.StatusOK, response.ErrMsg("参数错误")); return }
    m := map[int64]string{}
    for _, it := range p.Binds { if it.NodeID > 0 { m[it.NodeID] = it.IP } }
    b, _ := json.Marshal(m)
    key := tunnelBindKey(p.TunnelID)
    var cfg model.ViteConfig
    if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err == nil {
        cfg.Value = string(b)
        _ = dbpkg.DB.Save(&cfg).Error
    } else {
        _ = dbpkg.DB.Create(&model.ViteConfig{Name: key, Value: string(b)}).Error
    }
    c.JSON(http.StatusOK, response.OkMsg("已保存"))
}

func getTunnelBindMap(tunnelID int64) map[int64]string {
    key := tunnelBindKey(tunnelID)
    var cfg model.ViteConfig
    m := map[int64]string{}
    if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err != nil || cfg.Value == "" { return m }
    _ = json.Unmarshal([]byte(cfg.Value), &m)
    return m
}
