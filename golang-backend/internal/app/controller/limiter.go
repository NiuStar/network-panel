package controller

import (
	"net/http"
	"strconv"
	"strings"

	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
)

// Default limiter name used for tunnel entry services
const panelLimiterName = "limiter_panel"

// buildLimiterSpec constructs a limiter spec that uses the panel HTTP plugin endpoint.
// limiters:
//   - name: limiter_panel
//     plugin:
//     type: http
//     addr: http://SERVER/plugin/limiter?secret=SECRET
func buildLimiterSpec(nodeID int64) (string, map[string]any) {
	secret := nodeSecret(nodeID)
	base := serverBaseURL()
	if secret == "" || base == "" {
		return "", nil
	}
	addr := "http://" + base + "/plugin/limiter?secret=" + secret
	if tpl := strings.TrimSpace(getCfg("limiter_plugin_template")); tpl != "" {
		addr = strings.ReplaceAll(tpl, "{SERVER}", base)
		addr = strings.ReplaceAll(addr, "{SECRET}", secret)
	}
	spec := map[string]any{
		"name": panelLimiterName,
		"plugin": map[string]any{
			"type": "http",
			"addr": addr,
		},
	}
	return panelLimiterName, spec
}

// attachLimiter wires the shared limiter and registers the limiter plugin spec on the service.
func attachLimiter(svc map[string]any, nodeID int64) {
	if svc == nil {
		return
	}
	name, spec := buildLimiterSpec(nodeID)
	if name == "" || spec == nil {
		return
	}
	ensureLimiterOnNode(nodeID, spec)
	svc["limiter"] = name
	// register limiter definition on the fly (compatible with Gost config PUT)
	if lst, ok := svc["limiters"].([]any); ok {
		svc["limiters"] = appendIfLimiterMissing(lst, name, spec)
	} else {
		svc["limiters"] = []any{spec}
	}
	if existing, ok := svc["_limiters"].([]any); ok {
		svc["_limiters"] = append(existing, spec)
	} else {
		svc["_limiters"] = []any{spec}
	}
	// Avoid overwriting existing metadata for UpdateService patches; only set refresh interval when metadata exists.
	if meta, ok := svc["metadata"].(map[string]any); ok {
		if _, ok := meta["limiter.refreshInterval"]; !ok {
			meta["limiter.refreshInterval"] = "30s"
		}
	}
}

// limiterPatch constructs a minimal UpdateService payload that only injects limiter fields.
func limiterPatch(nodeID int64) map[string]any {
	name, spec := buildLimiterSpec(nodeID)
	if name == "" || spec == nil {
		return nil
	}
	ensureLimiterOnNode(nodeID, spec)
	return map[string]any{
		"limiter":   name,
		"limiters":  []any{spec},
		"_limiters": []any{spec},
	}
}

// mergeLimiterIntoService injects limiter settings into a full service definition (for PUT).
// Returns true if any change was applied.
func mergeLimiterIntoService(svc map[string]any, nodeID int64) bool {
	if svc == nil {
		return false
	}
	name, spec := buildLimiterSpec(nodeID)
	if name == "" || spec == nil {
		return false
	}
	changed := false
	if v, _ := svc["limiter"].(string); v != name {
		svc["limiter"] = name
		changed = true
	}
	if lst, ok := svc["limiters"].([]any); ok {
		nl := appendIfLimiterMissing(lst, name, spec)
		if len(nl) != len(lst) {
			svc["limiters"] = nl
			changed = true
		}
	} else {
		svc["limiters"] = []any{spec}
		changed = true
	}
	if lst, ok := svc["_limiters"].([]any); ok {
		seen := false
		for _, it := range lst {
			if m, ok2 := it.(map[string]any); ok2 {
				if n, _ := m["name"].(string); n == name {
					seen = true
					break
				}
			}
		}
		if !seen {
			svc["_limiters"] = append(lst, spec)
			changed = true
		}
	} else {
		svc["_limiters"] = []any{spec}
		changed = true
	}
	if meta, ok := svc["metadata"].(map[string]any); ok {
		if _, ok := meta["limiter.refreshInterval"]; !ok {
			meta["limiter.refreshInterval"] = "30s"
			changed = true
		}
	}
	return changed
}

func appendIfLimiterMissing(lst []any, name string, spec map[string]any) []any {
	for _, it := range lst {
		if m, ok := it.(map[string]any); ok {
			if n, _ := m["name"].(string); n == name {
				return lst
			}
		}
	}
	return append(lst, spec)
}

// ensureLimiterOnNode asks agent to upsert limiter definition via GOST Web API.
func ensureLimiterOnNode(nodeID int64, spec map[string]any) {
	if spec == nil {
		return
	}
	_ = sendWSCommand(nodeID, "UpsertLimiters", []map[string]any{spec})
}

// LimiterPlugin handles gost limiter HTTP plugin requests.
// It derives the user tunnel from service name forwardId_userId_userTunnelId and returns the
// configured speed (bytes/s) for both in and out directions. 0/negative -> unlimited.
func LimiterPlugin(c *gin.Context) {
	secret := strings.TrimSpace(c.Query("secret"))
	if secret == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"msg": "missing secret"})
		return
	}
	var node model.Node
	if err := dbpkg.DB.Select("id").Where("secret = ?", secret).First(&node).Error; err != nil || node.ID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"msg": "invalid secret"})
		return
	}
	var req struct {
		Scope   string `json:"scope"`
		Service string `json:"service"`
		Network string `json:"network"`
		Addr    string `json:"addr"`
		Client  string `json:"client"`
		Src     string `json:"src"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"msg": "invalid payload"})
		return
	}
	limitBps := resolveSpeedLimit(req.Service)
	c.JSON(http.StatusOK, gin.H{"in": limitBps, "out": limitBps})
}

// resolveSpeedLimit converts the service name into a per-user tunnel limit in bytes/s.
// Returns 0 for unlimited or when no matching rule exists.
func resolveSpeedLimit(serviceName string) int64 {
	parts := strings.Split(serviceName, "_")
	if len(parts) < 3 {
		return 0
	}
	utID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || utID == 0 {
		return 0
	}
	var ut model.UserTunnel
	if err := dbpkg.DB.First(&ut, utID).Error; err != nil {
		return 0
	}
	var sl model.SpeedLimit
	found := false
	// Prefer explicit user-tunnel binding
	if ut.SpeedID != nil && *ut.SpeedID != 0 {
		if err := dbpkg.DB.First(&sl, *ut.SpeedID).Error; err == nil {
			found = true
		}
	}
	// Fallback: tunnel-level active speed limit if user tunnel未绑定
	if !found && ut.TunnelID != 0 {
		if tls, ok := findActiveTunnelLimit(ut.TunnelID); ok {
			sl = tls
			found = true
		}
	}
	if !found || sl.Status != 1 || sl.Speed <= 0 || sl.TunnelID != ut.TunnelID {
		return 0
	}
	return mbpsToBytesPerSec(sl.Speed)
}

// mbpsToBytesPerSec converts Mbps to bytes/sec (MiB-based).
func mbpsToBytesPerSec(mbps int) int64 {
	if mbps <= 0 {
		return 0
	}
	return int64(mbps) * 1024 * 1024 / 8
}

// findActiveTunnelLimit returns an active speed limit bound to a tunnel (status=1, speed>0), preferring the newest updated.
func findActiveTunnelLimit(tunnelID int64) (model.SpeedLimit, bool) {
	var sl model.SpeedLimit
	if err := dbpkg.DB.Where("tunnel_id = ? AND status = 1 AND speed > 0", tunnelID).Order("updated_time desc").First(&sl).Error; err == nil {
		return sl, true
	}
	return model.SpeedLimit{}, false
}
