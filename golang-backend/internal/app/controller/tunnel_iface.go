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

// TunnelIfaceGet 获取出口IP映射
// @Summary 获取隧道节点出口IP
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerTunnelIDReq true "隧道ID"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/iface/get [post]
func TunnelIfaceGet(c *gin.Context) {
    var p struct{ TunnelID int64 `json:"tunnelId" binding:"required"` }
    if err := c.ShouldBindJSON(&p); err != nil { c.JSON(http.StatusOK, response.ErrMsg("参数错误")); return }
    m := getTunnelIfaceMap(p.TunnelID)
    // pack into list form for frontend
    list := make([]map[string]any, 0, len(m))
    for k, v := range m { list = append(list, map[string]any{"nodeId": k, "ip": v}) }
    c.JSON(http.StatusOK, response.Ok(map[string]any{"ifaces": list}))
}

// TunnelIfaceSet 设置出口IP映射
// @Summary 设置隧道节点出口IP
// @Tags tunnel
// @Accept json
// @Produce json
// @Param data body SwaggerTunnelIfaceReq true "隧道ID与出口IP"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/tunnel/iface/set [post]
func TunnelIfaceSet(c *gin.Context) {
    var p struct{ TunnelID int64 `json:"tunnelId" binding:"required"`; Ifaces []struct{ NodeID int64 `json:"nodeId"`; IP string `json:"ip"` } `json:"ifaces"` }
    if err := c.ShouldBindJSON(&p); err != nil { c.JSON(http.StatusOK, response.ErrMsg("参数错误")); return }
    m := map[int64]string{}
    for _, it := range p.Ifaces {
        if it.NodeID <= 0 { continue }
        m[it.NodeID] = it.IP // empty allowed (means unset)
    }
    b, _ := json.Marshal(m)
    key := tunnelIfaceKey(p.TunnelID)
    var cfg model.ViteConfig
    if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err == nil {
        cfg.Value = string(b)
        _ = dbpkg.DB.Save(&cfg).Error
    } else {
        _ = dbpkg.DB.Create(&model.ViteConfig{Name: key, Value: string(b)}).Error
    }
    c.JSON(http.StatusOK, response.OkMsg("已保存"))
}

func tunnelIfaceKey(tid int64) string { return "tunnel_iface_" + strconv.FormatInt(tid, 10) }

// getTunnelIfaceMap reads map[nodeId]ip from ViteConfig
func getTunnelIfaceMap(tunnelID int64) map[int64]string {
    key := tunnelIfaceKey(tunnelID)
    var cfg model.ViteConfig
    m := map[int64]string{}
    if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err != nil || cfg.Value == "" {
        return m
    }
    _ = json.Unmarshal([]byte(cfg.Value), &m)
    return m
}
