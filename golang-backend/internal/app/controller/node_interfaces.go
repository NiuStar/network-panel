package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"
)

// NodeInterfaces 获取节点网卡IP列表
// @Summary 获取节点网卡IP列表
// @Tags node
// @Accept json
// @Produce json
// @Param data body SwaggerNodeInterfacesReq true "节点ID"
// @Success 200 {object} SwaggerResp
// @Router /api/v1/node/interfaces [post]
// POST /api/v1/node/interfaces {nodeId}
func NodeInterfaces(c *gin.Context) {
	var p struct {
		NodeID int64 `json:"nodeId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	// collect base list from runtime interfaces
	ipsSet := map[string]struct{}{}
	var out []string
	var r model.NodeRuntime
	if err := dbpkg.DB.First(&r, "node_id = ?", p.NodeID).Error; err == nil && r.Interfaces != nil {
		var arr []string
		_ = json.Unmarshal([]byte(*r.Interfaces), &arr)
		for _, ip := range arr {
			ip = strings.TrimSpace(ip)
			if ip == "" {
				continue
			}
			if _, ok := ipsSet[ip]; !ok {
				ipsSet[ip] = struct{}{}
				out = append(out, ip)
			}
		}
	}
	// include node configured IP / ServerIP
	var n model.Node
	if err := dbpkg.DB.First(&n, p.NodeID).Error; err == nil {
		for _, ip := range []string{n.IP, n.ServerIP} {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				if _, ok := ipsSet[ip]; !ok {
					ipsSet[ip] = struct{}{}
					out = append(out, ip)
				}
			}
		}
	}
	// enrich with public IPs using agent (best-effort)
	req := map[string]interface{}{
		"requestId":  fmt.Sprintf("%d", time.Now().UnixNano()),
		"timeoutSec": 8,
		"content":    "#!/bin/sh\nset +e\nIP4=\"\"; IP6=\"\";\nif command -v ip >/dev/null 2>&1; then\n  if ip -o -4 addr show up scope global 2>/dev/null | grep -q .; then\n    IP4=$(curl -4 -fsS --connect-timeout 2 --max-time 3 ip.sb 2>/dev/null || wget -4 -qO- --timeout=3 --tries=1 ip.sb 2>/dev/null);\n  fi\n  if ip -o -6 addr show up scope global 2>/dev/null | grep -q .; then\n    IP6=$(curl -6 -fsS --connect-timeout 2 --max-time 3 ip.sb 2>/dev/null || wget -6 -qO- --timeout=3 --tries=1 ip.sb 2>/dev/null);\n  fi\nelse\n  IP4=$(curl -4 -fsS --connect-timeout 2 --max-time 3 ip.sb 2>/dev/null || wget -4 -qO- --timeout=3 --tries=1 ip.sb 2>/dev/null);\n  IP6=$(curl -6 -fsS --connect-timeout 2 --max-time 3 ip.sb 2>/dev/null || wget -6 -qO- --timeout=3 --tries=1 ip.sb 2>/dev/null);\nfi\necho IP4=$IP4; echo IP6=$IP6; exit 0\n",
	}
	if res, ok := RequestOp(p.NodeID, "RunScript", req, 9*time.Second); ok {
		if data, _ := res["data"].(map[string]interface{}); data != nil {
			if so, _ := data["stdout"].(string); so != "" {
				lines := strings.Split(so, "\n")
				for _, ln := range lines {
					ln = strings.TrimSpace(ln)
					if strings.HasPrefix(ln, "IP4=") || strings.HasPrefix(ln, "IP6=") {
						val := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(ln, "IP4="), "IP6="))
						if val != "" {
							if _, ok := ipsSet[val]; !ok {
								ipsSet[val] = struct{}{}
								out = append(out, val)
							}
						}
					}
				}
			}
		}
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"ips": out}))
}
