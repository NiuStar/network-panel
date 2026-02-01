package controller

import (
	"net/http"
	"strconv"
	"strings"

	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
)

// Default limiter name prefix for per-user-node services
const panelLimiterName = "limiter_user"

// buildLimiterSpec constructs a limiter spec that uses inline limits (no panel plugin).
// limits:
//   - "$ <in> <out>"  (service-level bandwidth limit)
func buildLimiterSpec(nodeID int64, userID int64) (string, map[string]any) {
	if nodeID == 0 || userID == 0 {
		return "", nil
	}
	limitBps := resolveUserNodeSpeedBps(nodeID, userID)
	if limitBps <= 0 {
		return "", nil
	}
	rate := formatLimiterRate(limitBps)
	if rate == "" {
		return "", nil
	}
	name := panelLimiterName + "_" + strconv.FormatInt(userID, 10) + "_" + strconv.FormatInt(nodeID, 10)
	spec := map[string]any{
		"name":   name,
		"limits": []string{"$ " + rate + " " + rate},
	}
	return name, spec
}

// attachLimiter wires per-user-node limiter and registers limiter spec on the service.
func attachLimiter(svc map[string]any, nodeID int64, userID int64) {
	if svc == nil {
		return
	}
	name, spec := buildLimiterSpec(nodeID, userID)
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
	// Avoid overwriting existing metadata for UpdateService patches; only set limiter scope when metadata exists.
	if meta, ok := svc["metadata"].(map[string]any); ok {
		if _, ok := meta["limiter.scope"]; !ok {
			meta["limiter.scope"] = "service"
		}
	}
}

// limiterPatch constructs a minimal UpdateService payload that only injects limiter fields.
func limiterPatch(nodeID int64, userID int64) map[string]any {
	name, spec := buildLimiterSpec(nodeID, userID)
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
func mergeLimiterIntoService(svc map[string]any, nodeID int64, userID int64) bool {
	if svc == nil {
		return false
	}
	name, spec := buildLimiterSpec(nodeID, userID)
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
		if _, ok := meta["limiter.scope"]; !ok {
			meta["limiter.scope"] = "service"
			changed = true
		}
	}
	return changed
}

// resolveUserNodeSpeedBps resolves per-user-node speed limit in bytes/s. Returns 0 when unlimited/invalid.
func resolveUserNodeSpeedBps(nodeID int64, userID int64) int64 {
	if nodeID == 0 || userID == 0 {
		return 0
	}
	var un model.UserNode
	if err := dbpkg.DB.Where("user_id = ? AND node_id = ? AND status = 1", userID, nodeID).First(&un).Error; err != nil {
		return 0
	}
	return speedLimitBytesByUserNode(un)
}

func formatLimiterRate(bps int64) string {
	if bps <= 0 {
		return ""
	}
	const (
		KB = 1024
		MB = 1024 * 1024
		GB = 1024 * 1024 * 1024
	)
	if bps >= GB {
		v := (bps + GB - 1) / GB
		return strconv.FormatInt(v, 10) + "GB"
	}
	if bps >= MB {
		v := (bps + MB - 1) / MB
		return strconv.FormatInt(v, 10) + "MB"
	}
	if bps >= KB {
		v := (bps + KB - 1) / KB
		return strconv.FormatInt(v, 10) + "KB"
	}
	return strconv.FormatInt(bps, 10) + "B"
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
	limitBps := resolveSpeedLimit(req.Service, node.ID)
	c.JSON(http.StatusOK, gin.H{"in": limitBps, "out": limitBps})
}

// resolveSpeedLimit converts the service name into a per-user node limit in bytes/s.
// Returns 0 for unlimited or when no matching rule exists.
func resolveSpeedLimit(serviceName string, nodeID int64) int64 {
	parts := strings.Split(serviceName, "_")
	if len(parts) < 3 {
		return 0
	}
	userID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || userID == 0 {
		return 0
	}
	var un model.UserNode
	if err := dbpkg.DB.Where("user_id = ? AND node_id = ? AND status = 1", userID, nodeID).First(&un).Error; err != nil {
		return 0
	}
	if un.SpeedMbps > 0 {
		return mbpsToBytesPerSec(un.SpeedMbps)
	}
	return speedLimitBytesByID(un.SpeedID)
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
