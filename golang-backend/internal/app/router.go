package app

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"network-panel/golang-backend/docs"
	"network-panel/golang-backend/internal/app/controller"
	"network-panel/golang-backend/internal/app/middleware"
	"network-panel/golang-backend/internal/panelupgrade"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(r *gin.Engine) {
	// enable CORS and preflight handling globally
	r.Use(middleware.CORS())
	r.Use(func(c *gin.Context) {
		if panelupgrade.Maintenance() {
			switch c.Request.URL.Path {
			case "/health", "/api/v1/version", "/api/v1/version/upgrade-status", "/api/v1/version/upgrade-stream":
			default:
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"code": -1, "msg": "UPGRADE_VERIFYING: 面板升级验证中，业务和节点连接暂时关闭"})
				return
			}
		}
		c.Next()
	})
	// health
	r.GET("/health", func(c *gin.Context) { c.String(200, "ok") })
	// serve install script for nodes
	r.GET("/install.sh", controller.InstallScript)
	// serve easytier installer and templates
	r.GET("/easytier/:file", func(c *gin.Context) {
		f := c.Param("file")
		if f == "" {
			c.JSON(404, gin.H{"code": 404})
			return
		}
		c.File("easytier/" + f)
	})
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
		user.POST("/register", controller.UserRegister)
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

	// flow endpoints (public, no auth)
	api.Any("/flow/upload", controller.FlowUpload)
	api.Any("/flow/docker", controller.FlowAnyTLSUpload)
	api.Any("/flow/anytls", controller.FlowAnyTLSUpload)
	api.Any("/flow/exit", controller.FlowExitUpload)

	// node
	// all users: see permitted nodes for forwarding
	api.POST("/node/user/node", middleware.AuthOptional(), controller.NodeUserNode)

	node := api.Group("/node")
	node.Use(middleware.Auth(), middleware.ForbidManagedLimited())
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
		// get/ensure anytls cert info for node
		node.POST("/docker-cert-preview", controller.NodeAnyTLSCertPreview)
		node.POST("/anytls-cert-preview", controller.NodeAnyTLSCertPreview)
		// actively verify node anytls cert chain against controller CA
		node.POST("/docker-cert-chain-check", controller.NodeAnyTLSCertChainCheck)
		node.POST("/anytls-cert-chain-check", controller.NodeAnyTLSCertChainCheck)
		// force reissue anytls cert and push to agent immediately
		node.POST("/docker-cert-reissue", controller.NodeAnyTLSCertReissue)
		node.POST("/anytls-cert-reissue", controller.NodeAnyTLSCertReissue)
		// read gost config content
		node.POST("/gost-config", controller.NodeGostConfig)
		// NodeQuality test trigger/result
		node.POST("/nq-test", controller.NodeNQTest)
		node.POST("/nq-result", controller.NodeNQResult)
		// query services on node
		node.POST("/query-services", controller.NodeQueryServices)
		// network stats for node
		node.POST("/network-stats", controller.NodeNetworkStats)
		node.POST("/network-stats-batch", controller.NodeNetworkStatsBatch)
		node.POST("/sysinfo", controller.NodeSysinfo)
		node.POST("/interfaces", controller.NodeInterfaces)
		node.POST("/ops", controller.NodeOps)
		node.POST("/docker-cert-logs", controller.NodeAnyTLSCertLogs)
		node.POST("/anytls-cert-logs", controller.NodeAnyTLSCertLogs)
		node.POST("/docker-runtime-logs", controller.NodeAnyTLSRuntimeLogs)
		node.POST("/anytls-runtime-logs", controller.NodeAnyTLSRuntimeLogs)
		node.POST("/docker-logs", controller.NodeAnyTLSRuntimeLogs)
		node.POST("/anytls-logs", controller.NodeAnyTLSRuntimeLogs)
		node.POST("/restart-gost", controller.NodeRestartGost)
		node.POST("/enable-gost-api", controller.NodeEnableGostAPI)
		node.POST("/self-check", controller.NodeSelfCheck)
		node.POST("/diag/start", controller.NodeDiagStart)
		node.POST("/diag/result", controller.NodeDiagResult)
		node.POST("/diag/iperf3-status", controller.NodeIperf3Status)

		// admin-only: user-node permissions
		nodeAdm := node.Group("")
		nodeAdm.Use(middleware.RequireRole())
		{
			nodeAdm.POST("/user/assign", controller.NodeUserAssign)
			nodeAdm.POST("/user/list", controller.NodeUserList)
			nodeAdm.POST("/user/usage", controller.NodeUserUsageByNode)
			nodeAdm.POST("/user/remove", controller.NodeUserRemove)
			nodeAdm.POST("/user/update", controller.NodeUserUpdate)
			nodeAdm.POST("/agent-upgrade-batch", controller.NodeAgentUpgradeBatch)
			nodeAdm.POST("/pprof/control", controller.NodePprofControl)
			nodeAdm.POST("/pprof/fetch", controller.NodePprofFetch)
		}
	}
	// Terminal WS: 自带 token/admin 校验，不使用 Auth 中间件
	api.GET("/node/:id/terminal", controller.NodeTerminalWS)

	// exit nodes (internal + external)
	exit := api.Group("/exit")
	exit.Use(middleware.Auth())
	{
		exit.POST("/list", controller.ExitNodeList)
		exit.POST("/cleanup", middleware.RequireRole(), controller.ExitCleanup)
		exitAdmin := exit.Group("/external")
		exitAdmin.Use(middleware.RequireRole())
		{
			exitAdmin.POST("/create", controller.ExitExternalCreate)
			exitAdmin.POST("/update", controller.ExitExternalUpdate)
			exitAdmin.POST("/delete", controller.ExitExternalDelete)
		}
	}

	// streaming log push from agent (auth by secret)
	api.POST("/nq/stream", controller.NodeNQStreamPush)
	api.POST("/diag/stream", controller.NodeDiagStreamPush)
	api.GET("/diag/backtrace.sh", controller.DiagBacktraceScript)

	// tunnel
	tunnel := api.Group("/tunnel")
	{
		// all users: see permitted tunnels for forwarding
		tunnel.POST("/user/tunnel", middleware.AuthOptional(), controller.TunnelUserTunnel)

		// authenticated (non-admin allowed): manage own tunnels
		tunAuth := tunnel.Group("")
		tunAuth.Use(middleware.Auth(), middleware.ForbidManagedLimited())
		{
			tunAuth.POST("/create", controller.TunnelCreate)
			tunAuth.POST("/get", controller.TunnelGet)
			tunAuth.POST("/list", controller.TunnelList)
			tunAuth.POST("/update", controller.TunnelUpdate)
			tunAuth.POST("/delete", controller.TunnelDelete)
		}

		// admin-only advanced operations
		adm := tunnel.Group("")
		adm.Use(middleware.RequireRole())
		{
			adm.POST("/path/get", controller.TunnelPathGet)
			adm.POST("/path/set", controller.TunnelPathSet)
			adm.POST("/user/assign", controller.TunnelUserAssign)
			adm.POST("/user/list", controller.TunnelUserList)
			adm.POST("/user/remove", controller.TunnelUserRemove)
			adm.POST("/user/update", controller.TunnelUserUpdate)
			adm.POST("/diagnose", controller.TunnelDiagnose)
			adm.POST("/diagnose-step", controller.TunnelDiagnoseStep)
			adm.POST("/path-check", controller.TunnelPathCheck)
			adm.POST("/iface/get", controller.TunnelIfaceGet)
			adm.POST("/iface/set", controller.TunnelIfaceSet)
			adm.POST("/bind/get", controller.TunnelBindGet)
			adm.POST("/bind/set", controller.TunnelBindSet)
			adm.POST("/cleanup-temp", controller.TunnelCleanupTemp)
		}
	}

	// forward
	forward := api.Group("/forward")
	{
		forward.POST("/create", middleware.Auth(), controller.ForwardCreate)
		forward.POST("/list", middleware.Auth(), controller.ForwardList)
		forward.POST("/update", middleware.Auth(), controller.ForwardUpdate)
		forward.POST("/delete", middleware.Auth(), controller.ForwardDelete)
		forward.POST("/batch-delete", middleware.Auth(), controller.ForwardBatchDelete)
		forward.POST("/force-delete", middleware.Auth(), controller.ForwardForceDelete)
		forward.POST("/pause", middleware.Auth(), controller.ForwardPause)
		forward.POST("/resume", middleware.Auth(), controller.ForwardResume)
		forward.POST("/diagnose", middleware.Auth(), controller.ForwardDiagnose)
		forward.POST("/diagnose-step", middleware.Auth(), controller.ForwardDiagnoseStep)
		forward.POST("/singbox-test", middleware.Auth(), controller.ForwardSingboxTest)
		forward.POST("/update-order", middleware.Auth(), controller.ForwardUpdateOrder)
		forward.POST("/status", middleware.Auth(), controller.ForwardStatusList)
		forward.POST("/status-detail", middleware.Auth(), controller.ForwardStatusDetail)
		forward.POST("/migrate", middleware.Auth(), controller.ForwardMigrateToRoute)
	}

	// speed-limit module removed (per-user node speed is handled in node permissions)

	// open api
	openAPI := api.Group("/open_api")
	{
		openAPI.GET("/sub_store", controller.OpenAPISubStore)
	}

	// subscription
	sub := api.Group("/subscription")
	{
		sub.GET("/clash", controller.SubscriptionClash)
		sub.GET("/clash-meta", controller.SubscriptionClashMeta)
		sub.GET("/shadowrocket", controller.SubscriptionShadowrocket)
		sub.GET("/surge", controller.SubscriptionSurge)
		sub.GET("/singbox", controller.SubscriptionSingbox)
		sub.GET("/docker-ca", controller.SubscriptionAnyTLSCA)
		sub.GET("/anytls-ca", controller.SubscriptionAnyTLSCA)
		sub.GET("/docker-ca/check", controller.SubscriptionAnyTLSCACheck)
		sub.GET("/anytls-ca/check", controller.SubscriptionAnyTLSCACheck)
		sub.GET("/v2ray", controller.SubscriptionV2ray)
		sub.GET("/qx", controller.SubscriptionQX)
		subAdmin := sub.Group("")
		subAdmin.Use(middleware.RequireRole())
		{
			subAdmin.GET("/docker-ca/status", controller.SubscriptionAnyTLSCAStatus)
			subAdmin.GET("/anytls-ca/status", controller.SubscriptionAnyTLSCAStatus)
			subAdmin.POST("/docker-ca/generate", controller.SubscriptionAnyTLSCAGenerate)
			subAdmin.POST("/anytls-ca/generate", controller.SubscriptionAnyTLSCAGenerate)
		}
	}

	// heartbeat inventory (agents/controllers)
	stats := api.Group("/stats")
	{
		stats.POST("/heartbeat", controller.HeartbeatReport)
		stats.GET("/heartbeat/summary", middleware.RequireRole(), controller.HeartbeatSummary)
	}

	// version
	api.GET("/version", controller.Version)
	api.GET("/version/latest", controller.VersionLatest)
	api.POST("/version/upgrade", middleware.RequireRole(), controller.VersionUpgrade)
	api.GET("/version/upgrade-status", middleware.RequireRole(), controller.VersionUpgradeStatus)
	api.GET("/version/upgrade-stream", middleware.RequireRole(), controller.VersionUpgradeStream)

	// public share (read-only views)
	share := api.Group("/share")
	{
		share.POST("/network-list", controller.ShareNetworkList)
		share.POST("/network-stats", controller.ShareNetworkStats)
	}

	// migrate (admin only)
	api.POST("/migrate", middleware.RequireRole(), controller.MigrateFrom)
	api.POST("/migrate/test", middleware.RequireRole(), controller.MigrateTest)
	api.POST("/migrate/start", middleware.RequireRole(), controller.MigrateStart)
	api.GET("/migrate/status", middleware.RequireRole(), controller.MigrateStatus)

	// flow
	r.POST("/flow/config", controller.FlowConfig)
	r.Any("/flow/test", controller.FlowTest)
	r.Any("/flow/upload", controller.FlowUpload)
	r.Any("/flow/docker", controller.FlowAnyTLSUpload)
	r.Any("/flow/anytls", controller.FlowAnyTLSUpload)
	r.Any("/flow/exit", controller.FlowExitUpload)
	// limiter plugin endpoint for gost HTTP plugin data source
	r.POST("/plugin/limiter", controller.LimiterPlugin)
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

	// Serve frontend under /app using one catch-all route to avoid Gin wildcard
	// conflicts. Files are read fully before response to avoid
	// ERR_CONTENT_LENGTH_MISMATCH while replacing dist files during deploy.
	serveApp := func(c *gin.Context) {
		raw := strings.TrimPrefix(c.Param("filepath"), "/")
		if raw == "" {
			raw = "index.html"
		}
		clean := filepath.Clean(raw)
		if clean == "." {
			clean = "index.html"
		}
		if strings.HasPrefix(clean, "..") || strings.Contains(clean, ".."+string(os.PathSeparator)) {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "msg": "invalid path"})
			return
		}

		serveFile := func(full string, cache string) bool {
			st, err := os.Stat(full)
			if err != nil || st.IsDir() {
				return false
			}
			ext := strings.ToLower(filepath.Ext(full))
			ct := mime.TypeByExtension(ext)
			if ct == "" {
				ct = "application/octet-stream"
			}
			c.Header("Cache-Control", cache)
			c.Header("Vary", "Accept-Encoding")
			if c.Request.Method == http.MethodHead {
				c.Header("Content-Type", ct)
				c.Header("Content-Length", strconv.FormatInt(st.Size(), 10))
				c.Status(http.StatusOK)
				return true
			}
			b, err := os.ReadFile(full)
			if err != nil {
				return false
			}
			c.Data(http.StatusOK, ct, b)
			return true
		}

		full := filepath.Join("public", clean)
		assetPrefix := "assets" + string(os.PathSeparator)
		if strings.HasPrefix(clean, assetPrefix) || strings.HasPrefix(clean, "assets/") {
			if serveFile(full, "public, max-age=31536000, immutable") {
				return
			}
			c.JSON(http.StatusNotFound, gin.H{"code": 404, "msg": "not found"})
			return
		}

		if serveFile(full, "no-cache") {
			return
		}

		// SPA fallback for routes like /app/forward/new; but keep real missing
		// files (with extension) as 404.
		if filepath.Ext(clean) == "" {
			if serveFile(filepath.Join("public", "index.html"), "no-cache") {
				return
			}
		}
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "msg": "not found"})
	}
	r.GET("/app", serveApp)
	r.HEAD("/app", serveApp)
	r.GET("/app/*filepath", serveApp)
	r.HEAD("/app/*filepath", serveApp)

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
		agent.POST("/report-services", controller.AgentReportServices)
		agent.POST("/docker-cert", controller.AgentAnyTLSCert)
		agent.POST("/anytls-cert", controller.AgentAnyTLSCert)
		// 手动重新应用全部服务（仅管理员可调用）
		agent.POST("/reconcile-node", middleware.RequireRole(), controller.AgentReconcileNode)
		agent.POST("/probe-targets", controller.AgentProbeTargets)
		agent.POST("/report-probe", controller.AgentReportProbe)
	}
	// easytier stream from agent (secret-auth)
	api.POST("/easytier/stream", controller.EasyTierStreamPush)

	// easytier networking (admin)
	easy := api.Group("/easytier")
	easy.Use(middleware.RequireRole())
	{
		easy.GET("/status", controller.EasyTierStatus)
		easy.POST("/enable", controller.EasyTierEnable)
		easy.POST("/nodes", controller.EasyTierListNodes)
		easy.POST("/join", controller.EasyTierJoin)
		easy.POST("/remove", controller.EasyTierRemove)
		easy.POST("/suggest-port", controller.EasyTierSuggestPort)
		easy.POST("/change-peer", controller.EasyTierChangePeer)
		easy.POST("/auto-assign", controller.EasyTierAutoAssign)
		easy.POST("/redeploy-master", controller.EasyTierRedeployMaster)
		easy.GET("/version", controller.EasyTierVersion)
		easy.POST("/update-all", controller.EasyTierUpdateAll)
		easy.POST("/reapply", controller.EasyTierReapply)
		easy.POST("/operate", controller.EasyTierOperate)
		easy.POST("/operate-batch", controller.EasyTierOperateBatch)
		easy.POST("/log", controller.EasyTierLog)
		easy.GET("/log/stream", controller.EasyTierLogStream)
		easy.GET("/ghproxy/*path", controller.EasyTierProxy)
	}

	// swagger (serve doc.json + simple CDN-based UI)
	r.GET("/swagger/doc.json", func(c *gin.Context) {
		doc := docs.SwaggerInfo.ReadDoc()
		c.Data(http.StatusOK, "application/json", []byte(doc))
	})
	r.GET("/swagger", func(c *gin.Context) {
		const html = `<!doctype html><html><head><title>Swagger UI</title><link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css"></head><body><div id="swagger-ui"></div><script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script><script>window.onload=function(){SwaggerUIBundle({url:"/swagger/doc.json",dom_id:"#swagger-ui"});};</script></body></html>`
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
	})
}
