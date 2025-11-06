package app

import (
	"net/http"
	"strings"

	"network-panel/golang-backend/internal/app/controller"
	"network-panel/golang-backend/internal/app/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(r *gin.Engine) {
	// enable CORS and preflight handling globally
	r.Use(middleware.CORS())
	// health
	r.GET("/health", func(c *gin.Context) { c.String(200, "ok") })
	// serve install script for nodes
	r.GET("/install.sh", controller.InstallScript)
	// serve flux-agent binaries
	r.GET("/flux-agent/:file", func(c *gin.Context) {
		f := c.Param("file")
		if f == "" {
			c.JSON(404, gin.H{"code": 404})
			return
		}
		c.File("public/flux-agent/" + f)
	})
	// websocket for node status
	r.GET("/system-info", controller.SystemInfoWS)

	api := r.Group("/api/v1")

	// captcha (stubbed)
	captcha := api.Group("/captcha")
	{
		captcha.POST("/check", controller.CaptchaCheck)
		captcha.POST("/generate", controller.CaptchaGenerate)
		captcha.POST("/verify", controller.CaptchaVerify)
	}

	// public config
	conf := api.Group("/config")
	{
		conf.POST("/list", controller.ConfigList)
		conf.POST("/get", controller.ConfigGet)
		conf.POST("/update", middleware.RequireRole(), controller.ConfigUpdate)
		conf.POST("/update-single", middleware.RequireRole(), controller.ConfigUpdateSingle)
	}

	// user
	user := api.Group("/user")
	{
		user.POST("/login", controller.UserLogin)
		user.POST("/package", middleware.AuthOptional(), controller.UserPackage)
		user.POST("/updatePassword", middleware.Auth(), controller.UserUpdatePassword)

		userAdmin := user.Group("")
		userAdmin.Use(middleware.RequireRole())
		{
			userAdmin.POST("/create", controller.UserCreate)
			userAdmin.POST("/list", controller.UserList)
			userAdmin.POST("/update", controller.UserUpdate)
			userAdmin.POST("/delete", controller.UserDelete)
			userAdmin.POST("/reset", controller.UserReset)
		}
	}

	// node
	node := api.Group("/node")
	node.Use(middleware.RequireRole())
	{
		node.POST("/create", controller.NodeCreate)
		node.POST("/list", controller.NodeList)
		node.POST("/update", controller.NodeUpdate)
		node.POST("/delete", controller.NodeDelete)
		node.POST("/install", controller.NodeInstallCmd)
		node.GET("/connections", controller.NodeConnections)
		// create/update exit node SS service
		node.POST("/set-exit", controller.NodeSetExit)
		// get last saved exit settings for node
		node.POST("/get-exit", controller.NodeGetExit)
		// query services on node
		node.POST("/query-services", controller.NodeQueryServices)
		// network stats for node
		node.POST("/network-stats", controller.NodeNetworkStats)
		node.POST("/network-stats-batch", controller.NodeNetworkStatsBatch)
		node.POST("/sysinfo", controller.NodeSysinfo)
		node.POST("/interfaces", controller.NodeInterfaces)
	}

	// tunnel
	tunnel := api.Group("/tunnel")
	{
		tunnel.POST("/user/tunnel", middleware.AuthOptional(), controller.TunnelUserTunnel)

		adm := tunnel.Group("")
		adm.Use(middleware.RequireRole())
		{
			adm.POST("/create", controller.TunnelCreate)
			adm.POST("/list", controller.TunnelList)
			adm.POST("/update", controller.TunnelUpdate)
			adm.POST("/delete", controller.TunnelDelete)
			adm.POST("/user/assign", controller.TunnelUserAssign)
			adm.POST("/user/list", controller.TunnelUserList)
			adm.POST("/user/remove", controller.TunnelUserRemove)
			adm.POST("/user/update", controller.TunnelUserUpdate)
			adm.POST("/diagnose", controller.TunnelDiagnose)
			adm.POST("/diagnose-step", controller.TunnelDiagnoseStep)
		}
	}

	// forward
	forward := api.Group("/forward")
	{
		forward.POST("/create", middleware.Auth(), controller.ForwardCreate)
		forward.POST("/list", middleware.Auth(), controller.ForwardList)
		forward.POST("/update", middleware.Auth(), controller.ForwardUpdate)
		forward.POST("/delete", middleware.Auth(), controller.ForwardDelete)
		forward.POST("/force-delete", middleware.Auth(), controller.ForwardForceDelete)
		forward.POST("/pause", middleware.Auth(), controller.ForwardPause)
		forward.POST("/resume", middleware.Auth(), controller.ForwardResume)
		forward.POST("/diagnose", middleware.RequireRole(), controller.ForwardDiagnose)
		forward.POST("/diagnose-step", middleware.RequireRole(), controller.ForwardDiagnoseStep)
		forward.POST("/update-order", middleware.Auth(), controller.ForwardUpdateOrder)
	}

	// speed-limit
	sl := api.Group("/speed-limit")
	sl.Use(middleware.RequireRole())
	{
		sl.POST("/create", controller.SpeedLimitCreate)
		sl.POST("/list", controller.SpeedLimitList)
		sl.POST("/update", controller.SpeedLimitUpdate)
		sl.POST("/delete", controller.SpeedLimitDelete)
		sl.POST("/tunnels", controller.SpeedLimitTunnels)
	}

	// open api
	openAPI := api.Group("/open_api")
	{
		openAPI.GET("/sub_store", controller.OpenAPISubStore)
	}

	// version
	api.GET("/version", controller.Version)

	// migrate (admin only)
	api.POST("/migrate", middleware.RequireRole(), controller.MigrateFrom)
	api.POST("/migrate/test", middleware.RequireRole(), controller.MigrateTest)
	api.POST("/migrate/start", middleware.RequireRole(), controller.MigrateStart)
	api.GET("/migrate/status", middleware.RequireRole(), controller.MigrateStatus)

	// flow
	r.POST("/flow/config", controller.FlowConfig)
	r.Any("/flow/test", controller.FlowTest)
	r.Any("/flow/upload", controller.FlowUpload)
	// alerts
	api.POST("/alerts/recent", middleware.RequireRole(), controller.AlertsRecent)

	// probe targets (admin)
	probe := api.Group("/probe")
	probe.Use(middleware.RequireRole())
	{
		probe.POST("/list", controller.ProbeList)
		probe.POST("/create", controller.ProbeCreate)
		probe.POST("/update", controller.ProbeUpdate)
		probe.POST("/delete", controller.ProbeDelete)
	}

	// serve static frontend under /app to avoid root conflicts
	r.Static("/app", "./public")

	// SPA fallback for /app paths; return JSON 404 for others
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/flow/") || p == "/health" {
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "msg": "not found"})
			return
		}
		if strings.HasPrefix(p, "/app") || p == "/app" {
			c.File("public/index.html")
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "msg": "not found"})
	})

	// agent endpoints (authenticated by node secret in payload)
	agent := api.Group("/agent")
	{
		agent.POST("/desired-services", controller.AgentDesiredServices)
		agent.POST("/push-services", controller.AgentPushServices)
		agent.POST("/reconcile", controller.AgentReconcile)
		agent.POST("/remove-services", controller.AgentRemoveServices)
		agent.POST("/reconcile-node", controller.AgentReconcileNode)
		agent.POST("/probe-targets", controller.AgentProbeTargets)
		agent.POST("/report-probe", controller.AgentReportProbe)
	}
}
