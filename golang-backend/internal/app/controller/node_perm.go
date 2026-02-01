package controller

import (
	"strings"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

// nodeAccess resolves node access for current user.
// allowShared=true allows user_node-based access (shared nodes).
func nodeAccess(c *gin.Context, nodeID int64, allowShared bool) (model.Node, *model.UserNode, bool, bool, bool, string, bool) {
	var node model.Node
	if nodeID <= 0 {
		return node, nil, false, false, false, "参数错误", false
	}
	if err := dbpkg.DB.First(&node, nodeID).Error; err != nil {
		return node, nil, false, false, false, "节点不存在", false
	}
	roleInf, roleOK := c.Get("role_id")
	if !roleOK || roleInf == nil || roleInf == 0 {
		return node, nil, true, false, false, "", true
	}
	uidInf, ok := c.Get("user_id")
	if !ok {
		return node, nil, false, false, false, "无权限", false
	}
	uid := uidInf.(int64)
	if node.OwnerID != nil && *node.OwnerID == uid {
		return node, nil, false, true, false, "", true
	}
	if !allowShared {
		return node, nil, false, false, false, "无权限", false
	}
	var un model.UserNode
	if err := dbpkg.DB.Where("user_id = ? AND node_id = ? AND status = 1", uid, nodeID).First(&un).Error; err != nil {
		return node, nil, false, false, false, "无权限", false
	}
	return node, &un, false, false, true, "", true
}

func portAllowedForShared(un *model.UserNode, port int) bool {
	if un == nil || port <= 0 {
		return false
	}
	if strings.TrimSpace(un.PortRanges) == "" {
		return false
	}
	return portAllowed(port, un.PortRanges)
}
