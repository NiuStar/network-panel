package controller

import (
	"fmt"
	"strings"
	"time"

	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

// anytlsUserPassword derives a per-user password from the base anytls password.
func anytlsUserPassword(base string, userID int64) string {
	base = strings.TrimSpace(base)
	if base == "" || userID <= 0 {
		return ""
	}
	return fmt.Sprintf("u%d:%s", userID, base)
}

// speedLimitBytesByID resolves speed_id -> bytes/sec. Returns 0 when unlimited/invalid.
// legacy fallback; prefer SpeedMbps on user_node.
func speedLimitBytesByID(speedID *int64) int64 {
	if speedID == nil || *speedID == 0 {
		return 0
	}
	var sl model.SpeedLimit
	if err := dbpkg.DB.First(&sl, *speedID).Error; err != nil {
		return 0
	}
	if sl.Status != 1 || sl.Speed <= 0 {
		return 0
	}
	return mbpsToBytesPerSec(sl.Speed)
}

func speedLimitBytesByUserNode(n model.UserNode) int64 {
	if n.SpeedMbps > 0 {
		return mbpsToBytesPerSec(n.SpeedMbps)
	}
	return speedLimitBytesByID(n.SpeedID)
}

// buildAnyTLSUsersForNode builds per-user anytls auth + speed rules for a node.
func buildAnyTLSUsersForNode(nodeID int64, basePassword string) []map[string]any {
	if nodeID == 0 || strings.TrimSpace(basePassword) == "" {
		return nil
	}
	var rows []model.UserNode
	dbpkg.DB.Where("node_id = ? AND status = 1", nodeID).Order("user_id asc").Find(&rows)
	if len(rows) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		pass := anytlsUserPassword(basePassword, r.UserID)
		if pass == "" {
			continue
		}
		out = append(out, map[string]any{
			"userId":   r.UserID,
			"password": pass,
			"speedBps": speedLimitBytesByUserNode(r),
		})
	}
	return out
}

func defaultAnyTLSBaseUserID() int64 {
	var u model.User
	if err := dbpkg.DB.Where("role_id = 0").Order("id asc").First(&u).Error; err == nil {
		return u.ID
	}
	return 0
}

// pushAnyTLSConfigToNode pushes latest anytls config (including per-user rules) to agent.
func pushAnyTLSConfigToNode(nodeID int64) {
	if nodeID == 0 {
		return
	}
	var st model.AnyTLSSetting
	if err := dbpkg.DB.Where("node_id = ?", nodeID).First(&st).Error; err != nil || st.ID == 0 {
		return
	}
	exitIP := getAnyTLSExitIP(nodeID)
	allowFallback := getAnyTLSExitFallback(nodeID)
	baseUserID := int64(0)
	if st.BaseUserID != nil {
		baseUserID = *st.BaseUserID
	}
	if baseUserID == 0 {
		baseUserID = defaultAnyTLSBaseUserID()
	}
	req := map[string]any{
		"requestId":     RandUUID(),
		"port":          st.Port,
		"password":      st.Password,
		"allowFallback": allowFallback,
		"users":         buildAnyTLSUsersForNode(nodeID, st.Password),
	}
	if baseUserID > 0 {
		req["baseUserId"] = baseUserID
	}
	if exitIP != "" {
		req["exitIp"] = exitIP
	}
	_, _ = RequestOp(nodeID, "SetAnyTLS", req, 10*time.Second)
}
