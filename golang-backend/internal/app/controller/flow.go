package controller

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"network-panel/golang-backend/internal/app/dto"
	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

func FlowConfig(c *gin.Context) { c.String(http.StatusOK, "ok") }
func FlowTest(c *gin.Context)   { c.String(http.StatusOK, "test") }

// POST /flow/upload?secret=...
// Updates forward/user/usertunnel flow counters and pauses when limits exceeded.
func FlowUpload(c *gin.Context) {
	secret := c.Query("secret")
	// validate node by secret (silent fail to avoid leaking info)
	var nodeCount int64
	dbpkg.DB.Model(&model.Node{}).Where("secret = ?", secret).Count(&nodeCount)
	if nodeCount == 0 {
		c.String(http.StatusOK, "ok")
		return
	}

	var payload dto.FlowDto
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.String(http.StatusOK, "ok")
		return
	}
	// ignore internal reporter name
	if payload.N == "web_api" {
		c.String(http.StatusOK, "ok")
		return
	}

	// parse service name: forwardId_userId_userTunnelId
	parts := strings.Split(payload.N, "_")
	if len(parts) < 3 {
		c.String(http.StatusOK, "ok")
		return
	}
	fwdID, _ := strconv.ParseInt(parts[0], 10, 64)
	userID, _ := strconv.ParseInt(parts[1], 10, 64)
	utID, _ := strconv.ParseInt(parts[2], 10, 64)

	// load forward and tunnel
	var fwd model.Forward
	if err := dbpkg.DB.First(&fwd, fwdID).Error; err != nil {
		c.String(http.StatusOK, "ok")
		return
	}
	var tun model.Tunnel
	_ = dbpkg.DB.First(&tun, fwd.TunnelID).Error

	// Adjust flow by tunnel.flow (1 single, 2 double). Default double.
	inInc, outInc := payload.U, payload.D
	if tun.Flow == 1 { // single direction: count only one side (use total as out)
		outInc = payload.U + payload.D
		inInc = 0
	}

	// Forward increments
	dbpkg.DB.Model(&model.Forward{}).Where("id = ?", fwdID).
		Updates(map[string]any{
			"in_flow":      gorm.Expr("in_flow + ?", inInc),
			"out_flow":     gorm.Expr("out_flow + ?", outInc),
			"updated_time": time.Now().UnixMilli(),
		})

	// User increments
	dbpkg.DB.Model(&model.User{}).Where("id = ?", userID).
		Updates(map[string]any{
			"in_flow":      gorm.Expr("in_flow + ?", inInc),
			"out_flow":     gorm.Expr("out_flow + ?", outInc),
			"updated_time": time.Now().UnixMilli(),
		})

	// UserTunnel increments when applicable
	if utID != 0 {
		dbpkg.DB.Model(&model.UserTunnel{}).Where("id = ?", utID).
			Updates(map[string]any{
				"in_flow":  gorm.Expr("in_flow + ?", inInc),
				"out_flow": gorm.Expr("out_flow + ?", outInc),
			})
	}

	// Reload latest user and userTunnel to check limits
	var user model.User
	if err := dbpkg.DB.First(&user, userID).Error; err == nil {
		// check total flow and expiry
		if overUserLimit(user) || expired(user.ExpTime) || user.Status != nil && *user.Status != 1 {
			pauseAllUserForwards(user.ID)
			// set user status 0 if not already
			s := 0
			user.Status = &s
			_ = dbpkg.DB.Save(&user).Error
		}
	}

	if utID != 0 {
		var ut model.UserTunnel
		if err := dbpkg.DB.First(&ut, utID).Error; err == nil {
			if overUTunnelLimit(ut) || expired(ut.ExpTime) || ut.Status != 1 {
				pauseUserTunnelForwards(ut.UserID, ut.TunnelID)
				ut.Status = 0
				_ = dbpkg.DB.Save(&ut).Error
			}
		}
	}

	c.String(http.StatusOK, "ok")
}

// Over user limit if flow(GiB) <= in + out
func overUserLimit(u model.User) bool {
	limit := u.Flow * 1024 * 1024 * 1024
	return limit > 0 && (u.InFlow+u.OutFlow) > limit
}
func overUTunnelLimit(ut model.UserTunnel) bool {
	limit := ut.Flow * 1024 * 1024 * 1024
	return limit > 0 && (ut.InFlow+ut.OutFlow) > limit
}
func expired(ts *int64) bool { return ts != nil && *ts > 0 && *ts <= time.Now().UnixMilli() }

func pauseAllUserForwards(userID int64) {
	var forwards []model.Forward
	dbpkg.DB.Where("user_id = ?", userID).Find(&forwards)
	for _, f := range forwards {
		dbpkg.DB.Model(&model.Forward{}).Where("id = ?", f.ID).Update("status", 0)
		var t model.Tunnel
		if err := dbpkg.DB.First(&t, f.TunnelID).Error; err == nil {
			name := buildServiceName(f.ID, f.UserID, f.TunnelID)
			_ = sendWSCommand(t.InNodeID, "PauseService", map[string]interface{}{"services": []string{name}})
			if t.Type == 2 {
				_ = sendWSCommand(outNodeIDOr0(t), "PauseService", map[string]interface{}{"services": []string{name}})
			}
		}
	}
}
func pauseUserTunnelForwards(userID, tunnelID int64) {
	var forwards []model.Forward
	dbpkg.DB.Where("user_id = ? AND tunnel_id = ?", userID, tunnelID).Find(&forwards)
	for _, f := range forwards {
		dbpkg.DB.Model(&model.Forward{}).Where("id = ?", f.ID).Update("status", 0)
		var t model.Tunnel
		if err := dbpkg.DB.First(&t, f.TunnelID).Error; err == nil {
			name := buildServiceName(f.ID, f.UserID, f.TunnelID)
			_ = sendWSCommand(t.InNodeID, "PauseService", map[string]interface{}{"services": []string{name}})
			if t.Type == 2 {
				_ = sendWSCommand(outNodeIDOr0(t), "PauseService", map[string]interface{}{"services": []string{name}})
			}
		}
	}
}
