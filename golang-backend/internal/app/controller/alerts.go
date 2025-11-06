package controller

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

// POST /api/v1/alerts/recent {limit?}
func AlertsRecent(c *gin.Context) {
	var p struct {
		Limit int `json:"limit"`
	}
	_ = c.ShouldBindJSON(&p)
	if p.Limit <= 0 || p.Limit > 200 {
		p.Limit = 50
	}
	var list []model.Alert
	dbpkg.DB.Order("time_ms desc").Limit(p.Limit).Find(&list)
	c.JSON(http.StatusOK, response.Ok(list))
}
