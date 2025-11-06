package controller

import (
	"net/http"
	"time"

	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
)

// POST /api/v1/config/list
func ConfigList(c *gin.Context) {
	var items []model.ViteConfig
	dbpkg.DB.Find(&items)
	m := map[string]string{}
	for _, it := range items {
		m[it.Name] = it.Value
	}
	c.JSON(http.StatusOK, response.Ok(m))
}

// POST /api/v1/config/get {"name":"..."}
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

// POST /api/v1/config/update {"k":"v"...}
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

// POST /api/v1/config/update-single {name, value}
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
