package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

// Captcha endpoints are stubbed to keep compatibility.

func CaptchaCheck(c *gin.Context) {
	// read vite_config captcha_enabled; if true return 1 else 0
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", "captcha_enabled").First(&cfg).Error; err != nil || cfg.Value != "true" {
		c.JSON(http.StatusOK, response.Ok(0))
		return
	}
	c.JSON(http.StatusOK, response.Ok(1))
}

func CaptchaGenerate(c *gin.Context) {
	// stub response
	c.JSON(http.StatusOK, gin.H{"id": "stub", "type": "SLIDER", "data": gin.H{"url": ""}})
}

func CaptchaVerify(c *gin.Context) {
	// always success, return {validToken: id}
	var p struct {
		ID string `json:"id"`
	}
	_ = c.ShouldBindJSON(&p)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"validToken": p.ID}})
}
