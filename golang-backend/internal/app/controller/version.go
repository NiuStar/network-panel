package controller

import (
	"net/http"
	"os"

	"flux-panel/golang-backend/internal/app/response"
	appver "flux-panel/golang-backend/internal/app/version"
	"github.com/gin-gonic/gin"
)

// GET /api/v1/version
func Version(c *gin.Context) {
	// server.version from main package
	serverVer := appver.Get()
	// agent version (expected agent binary baseline)
	agentVer := os.Getenv("AGENT_VERSION")
	if agentVer == "" {
		agentVer = "go-agent-1.0.7"
	}
	agent2Ver := os.Getenv("AGENT2_VERSION")
	if agent2Ver == "" {
		agent2Ver = "go-agent2-1.0.7"
	}
	c.JSON(http.StatusOK, response.Ok(map[string]string{
		"server": serverVer,
		"agent":  agentVer,
		"agent2": agent2Ver,
	}))
}
