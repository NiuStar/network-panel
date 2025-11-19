package middleware

import (
	"net/http"

	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/app/util"

	"github.com/gin-gonic/gin"
)

// Auth enforces presence of valid JWT in Authorization header
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token == "" || !util.ValidateToken(token) {
			c.JSON(http.StatusUnauthorized, response.ErrMsg("未登录或token无效"))
			c.Abort()
			return
		}
		c.Set("user_id", util.GetUserID(token))
		c.Set("role_id", util.GetRoleID(token))
		c.Next()
	}
}

// AuthOptional parses token if present; otherwise continues.
func AuthOptional() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token != "" && util.ValidateToken(token) {
			c.Set("user_id", util.GetUserID(token))
			c.Set("role_id", util.GetRoleID(token))
		}
		c.Next()
	}
}

// RequireRole requires admin role (role_id == 0)
func RequireRole() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token == "" || !util.ValidateToken(token) {
			c.JSON(http.StatusUnauthorized, response.ErrMsg("未登录或token无效"))
			c.Abort()
			return
		}
		roleID := util.GetRoleID(token)
		if roleID != 0 {
			c.JSON(http.StatusForbidden, response.ErrMsg("权限不足"))
			c.Abort()
			return
		}
		c.Set("user_id", util.GetUserID(token))
		c.Set("role_id", roleID)
		c.Next()
	}
}

// ForbidManagedLimited forbids role_id == 2 (admin-created limited users)
// These users are only allowed to manage forwards; they cannot access
// node monitoring or tunnel management APIs.
func ForbidManagedLimited() gin.HandlerFunc {
    return func(c *gin.Context) {
        token := c.GetHeader("Authorization")
        if token == "" || !util.ValidateToken(token) {
            c.JSON(http.StatusUnauthorized, response.ErrMsg("未登录或token无效"))
            c.Abort()
            return
        }
        roleID := util.GetRoleID(token)
        if roleID == 2 { // managed-limited user
            c.JSON(http.StatusForbidden, response.ErrMsg("权限不足"))
            c.Abort()
            return
        }
        c.Set("user_id", util.GetUserID(token))
        c.Set("role_id", roleID)
        c.Next()
    }
}
