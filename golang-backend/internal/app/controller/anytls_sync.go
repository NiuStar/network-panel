package controller

import (
	"fmt"
	"log"
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

func speedLimitBytesByUserNodeWithCache(n model.UserNode, speedMap map[int64]model.SpeedLimit) int64 {
	if n.SpeedMbps > 0 {
		return mbpsToBytesPerSec(n.SpeedMbps)
	}
	if n.SpeedID == nil || *n.SpeedID == 0 {
		return 0
	}
	sl, ok := speedMap[*n.SpeedID]
	if !ok || sl.Status != 1 || sl.Speed <= 0 {
		return 0
	}
	return mbpsToBytesPerSec(sl.Speed)
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
	speedIDs := make([]int64, 0, len(rows))
	seenSpeedIDs := map[int64]struct{}{}
	for _, r := range rows {
		if r.SpeedMbps > 0 || r.SpeedID == nil || *r.SpeedID == 0 {
			continue
		}
		if _, ok := seenSpeedIDs[*r.SpeedID]; ok {
			continue
		}
		seenSpeedIDs[*r.SpeedID] = struct{}{}
		speedIDs = append(speedIDs, *r.SpeedID)
	}
	speedMap := map[int64]model.SpeedLimit{}
	if len(speedIDs) > 0 {
		var limits []model.SpeedLimit
		_ = dbpkg.DB.Where("id in ?", speedIDs).Find(&limits).Error
		for _, sl := range limits {
			speedMap[sl.ID] = sl
		}
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
			"speedBps": speedLimitBytesByUserNodeWithCache(r, speedMap),
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

type anyTLSForwardInstance struct {
	Port   int
	ExitIP string
}

func listAnyTLSForwardInstances(nodeID int64) []anyTLSForwardInstance {
	if nodeID == 0 {
		return nil
	}
	var rows []struct {
		Port  int
		OutIP *string
	}
	err := dbpkg.DB.
		Table("forward f").
		Select("f.out_port as port, t.out_ip as out_ip").
		Joins("left join tunnel t on t.id = f.tunnel_id").
		Where("t.out_node_id = ? AND f.out_port IS NOT NULL AND f.out_port > 0 AND lower(trim(coalesce(t.protocol, ''))) = 'anytls' AND (f.status IS NULL OR f.status = 1)", nodeID).
		Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil
	}
	seen := map[int]struct{}{}
	out := make([]anyTLSForwardInstance, 0, len(rows))
	for _, row := range rows {
		if row.Port <= 0 {
			continue
		}
		if _, ok := seen[row.Port]; ok {
			continue
		}
		seen[row.Port] = struct{}{}
		exitIP := ""
		if row.OutIP != nil {
			exitIP = strings.TrimSpace(*row.OutIP)
		}
		out = append(out, anyTLSForwardInstance{
			Port:   row.Port,
			ExitIP: exitIP,
		})
	}
	return out
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
	allowFallback := getAnyTLSExitFallback(nodeID)
	portMappings := listAnyTLSPortMappings(nodeID)
	if len(portMappings) == 0 && st.Port > 0 {
		portMappings = []anyTLSPortMapping{{
			Port:   st.Port,
			ExitIP: getAnyTLSExitIP(nodeID),
		}}
	}
	if len(portMappings) == 0 {
		return
	}
	baseUserID := int64(0)
	if st.BaseUserID != nil {
		baseUserID = *st.BaseUserID
	}
	if baseUserID == 0 {
		baseUserID = defaultAnyTLSBaseUserID()
	}
	users := buildAnyTLSUsersForNode(nodeID, st.Password)
	buildReq := func(port int, exitIP string, allowFallback bool) map[string]any {
		req := map[string]any{
			"requestId":     RandUUID(),
			"port":          port,
			"password":      st.Password,
			"allowFallback": allowFallback,
			"users":         users,
		}
		if baseUserID > 0 {
			req["baseUserId"] = baseUserID
		}
		if strings.TrimSpace(exitIP) != "" {
			req["exitIp"] = strings.TrimSpace(exitIP)
		}
		if certPayload, err := anyTLSCertPayload(nodeID, ""); err == nil {
			for k, v := range certPayload {
				req[k] = v
			}
		} else {
			log.Printf("{\"event\":\"anytls_cert_payload_err\",\"nodeId\":%d,\"error\":%q}", nodeID, err.Error())
			return nil
		}
		return req
	}

	for _, pm := range portMappings {
		if pm.Port <= 0 {
			continue
		}
		req := buildReq(pm.Port, pm.ExitIP, allowFallback)
		if req == nil {
			return
		}
		_, _ = RequestOp(nodeID, "SetAnyTLS", req, 10*time.Second)
	}
	cleanupAnyTLSGostLoopServices(nodeID)
}
