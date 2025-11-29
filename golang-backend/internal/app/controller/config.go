package controller

import (
	"net/http"
	"time"

	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
)

// ConfigList 配置列表
// @Summary 获取所有配置
// @Tags config
// @Produce json
// @Success 200 {object} SwaggerConfigListResp
// @Router /api/v1/config/list [post]
func ConfigList(c *gin.Context) {
	var items []model.ViteConfig
	dbpkg.DB.Find(&items)
	m := map[string]string{}
	for _, it := range items {
		m[it.Name] = it.Value
	}
	c.JSON(http.StatusOK, response.Ok(m))
}

// ConfigGet 获取单个配置
// @Summary 根据 name 获取配置
// @Tags config
// @Accept json
// @Produce json
// @Param data body SwaggerConfigGetReq true "配置名"
// @Success 200 {object} SwaggerConfigGetResp
// @Router /api/v1/config/get [post]
func ConfigGet(c *gin.Context) {
	var p struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&p); err != nil || p.Name == "" {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var it model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", p.Name).First(&it).Error; err != nil {
		c.JSON(http.StatusOK, response.Ok(""))
		return
	}
	c.JSON(http.StatusOK, response.Ok(it.Value))
}

// ConfigUpdate 批量更新配置
// @Summary 批量更新配置
// @Tags config
// @Accept json
// @Produce json
// @Param data body SwaggerConfigUpdateMap true "键值对"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/config/update [post]
func ConfigUpdate(c *gin.Context) {
	var m map[string]string
	if err := c.ShouldBindJSON(&m); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	for k, v := range m {
		var it model.ViteConfig
		if err := dbpkg.DB.Where("name = ?", k).First(&it).Error; err != nil {
			it.Name, it.Value, it.Time = k, v, timeNow()
			dbpkg.DB.Create(&it)
		} else {
			dbpkg.DB.Model(&it).Updates(map[string]any{"value": v, "time": timeNow()})
		}
	}
	c.JSON(http.StatusOK, response.OkNoData())
}

// ConfigUpdateSingle 单项配置更新
// @Summary 单项配置更新
// @Tags config
// @Accept json
// @Produce json
// @Param data body SwaggerConfigUpdateSingle true "name/value"
// @Success 200 {object} BaseSwaggerResp
// @Router /api/v1/config/update-single [post]
func ConfigUpdateSingle(c *gin.Context) {
	var p struct{ Name, Value string }
	if err := c.ShouldBindJSON(&p); err != nil || p.Name == "" {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	var it model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", p.Name).First(&it).Error; err != nil {
		it.Name, it.Value, it.Time = p.Name, p.Value, timeNow()
		dbpkg.DB.Create(&it)
	} else {
		dbpkg.DB.Model(&it).Updates(map[string]any{"value": p.Value, "time": timeNow()})
	}
	c.JSON(http.StatusOK, response.OkNoData())
}

func timeNow() int64 { return time.Now().UnixMilli() }
