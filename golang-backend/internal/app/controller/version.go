package controller

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/response"
	appver "network-panel/golang-backend/internal/app/version"
)

// Version 获取后端版本信息
// @Summary 版本信息
// @Tags version
// @Produce json
// @Success 200 {object} SwaggerVersionResp
// @Router /api/v1/version [get]
func Version(c *gin.Context) {
	// Backend version
	serverVer := appver.Get() // e.g. "1.0.1"
	base := serverVer
	// tolerate legacy values like "server-1.0.1"
	if strings.HasPrefix(base, "server-") {
		base = strings.TrimPrefix(base, "server-")
	}
	// Expected agent versions strictly follow backend version
	agentVer := "go-agent-" + base
	agent2Ver := "go-agent2-" + base
	c.JSON(http.StatusOK, response.Ok(map[string]any{
		"server":   serverVer,
		"agent":    agentVer,
		"agent2":   agent2Ver,
		"center":   os.Getenv("network-panel-center"),
		"centerOn": centerEnabled(),
	}))
}

// centerEnabled checks env for network-panel-center/NETWORK_PANEL_CENTER toggles.
func centerEnabled() bool {
	v := os.Getenv("network-panel-center")
	if v == "" {
		v = os.Getenv("NETWORK_PANEL_CENTER")
	}
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
