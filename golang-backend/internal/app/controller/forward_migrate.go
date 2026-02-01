package controller

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/app/util"
	dbpkg "network-panel/golang-backend/internal/db"
)

// ForwardMigrateToRoute 批量将旧转发转换为新版线路编辑结构
// 规则：
// 1) 将 forward.remote_addr 转换为 external exit（exit_node_external）
// 2) 若 tunnel.out_node_id 存在，则追加到 tunnel_path 末尾后清空 out_node_id
// 3) tunnel.out_exit_id 指向新建的 external exit
// 4) linkModes 末段强制为 direct（外部出口不支持 tunnel）
func ForwardMigrateToRoute(c *gin.Context) {
	token := extractToken(c)
	if token == "" || !util.ValidateToken(token) {
		c.JSON(http.StatusUnauthorized, response.ErrMsg("未登录或token无效"))
		return
	}
	if util.GetRoleID(token) != 0 {
		c.JSON(http.StatusOK, response.ErrMsg("无权限"))
		return
	}

	var forwards []model.Forward
	if err := dbpkg.DB.Where("status IS NULL OR status = 1").Find(&forwards).Error; err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("获取转发失败"))
		return
	}
	if len(forwards) == 0 {
		c.JSON(http.StatusOK, response.Ok(map[string]any{"total": 0, "converted": 0}))
		return
	}

	// 预加载隧道
	tunnelIDs := make([]int64, 0, len(forwards))
	tunnelSet := map[int64]struct{}{}
	for _, f := range forwards {
		if f.TunnelID <= 0 {
			continue
		}
		if _, ok := tunnelSet[f.TunnelID]; ok {
			continue
		}
		tunnelSet[f.TunnelID] = struct{}{}
		tunnelIDs = append(tunnelIDs, f.TunnelID)
	}
	var tunnels []model.Tunnel
	if len(tunnelIDs) > 0 {
		dbpkg.DB.Where("id IN ?", tunnelIDs).Find(&tunnels)
	}
	tunnelMap := map[int64]model.Tunnel{}
	for _, t := range tunnels {
		tunnelMap[t.ID] = t
	}

	// 预加载已有 external exits
	var exts []model.ExitNodeExternal
	dbpkg.DB.Find(&exts)
	extByKey := map[string]model.ExitNodeExternal{}
	for _, e := range exts {
		key := strings.ToLower(strings.TrimSpace(e.Host)) + ":" + strconv.Itoa(e.Port) + ":" + strings.ToLower(strings.TrimSpace(ptrString(e.Protocol)))
		extByKey[key] = e
	}

	converted := 0
	skipped := 0
	force := strings.EqualFold(strings.TrimSpace(c.Query("force")), "1") ||
		strings.EqualFold(strings.TrimSpace(c.Query("force")), "true")
	for _, f := range forwards {
		t, ok := tunnelMap[f.TunnelID]
		if !ok {
			skipped++
			continue
		}
		if hasRouteConfig(t.ID) {
			skipped++
			continue
		}
		if t.Type == 2 && !force {
			skipped++
			continue
		}
		if t.Type == 2 && force {
			t.Type = 1
		}
		remote := strings.TrimSpace(f.RemoteAddr)
		if remote == "" {
			skipped++
			continue
		}
		host, port, ok := parseFirstRemoteHostPort(remote)
		if !ok {
			skipped++
			continue
		}
		proto := ""
		if t.Protocol != nil {
			proto = strings.TrimSpace(*t.Protocol)
		}
		key := strings.ToLower(strings.TrimSpace(host)) + ":" + strconv.Itoa(port) + ":" + strings.ToLower(strings.TrimSpace(proto))
		ext, exists := extByKey[key]
		if !exists {
			now := time.Now().UnixMilli()
			name := strings.TrimSpace(f.Name)
			if name == "" {
				name = "external-exit"
			}
			rec := model.ExitNodeExternal{
				BaseEntity: model.BaseEntity{CreatedTime: now, UpdatedTime: now},
				Name:       name,
				Host:       host,
				Port:       port,
			}
			if proto != "" {
				rec.Protocol = &proto
			}
			if err := dbpkg.DB.Create(&rec).Error; err != nil {
				skipped++
				continue
			}
			ext = rec
			extByKey[key] = rec
		}

		// 追加出口节点到 tunnel_path 并清空 out_node_id
		path := getTunnelPathNodes(t.ID)
		if t.OutNodeID != nil && *t.OutNodeID > 0 {
			// 避免重复
			found := false
			for _, pid := range path {
				if pid == *t.OutNodeID {
					found = true
					break
				}
			}
			if !found {
				path = append(path, *t.OutNodeID)
			}
			t.OutNodeID = nil
		}
		t.OutExitID = &ext.ID
		_ = dbpkg.DB.Save(&t).Error

		// 保存 path 与 linkModes（最后一段强制 direct）
		modes := normalizeLinkModes(getTunnelLinkModes(t.ID), len(path)+1, "direct")
		if len(modes) > 0 {
			modes[len(modes)-1] = "direct"
		}
		saveTunnelPathAndModes(t.ID, path, modes)
		converted++
	}

	c.JSON(http.StatusOK, response.Ok(map[string]any{
		"total":     len(forwards),
		"converted": converted,
		"skipped":   skipped,
	}))
}

func hasRouteConfig(tunnelID int64) bool {
	keys := []string{
		tunnelPathKey(tunnelID),
		tunnelLinkModeKey(tunnelID),
		tunnelBindKey(tunnelID),
		tunnelIfaceKey(tunnelID),
	}
	for _, k := range keys {
		var cfg model.ViteConfig
		if err := dbpkg.DB.Where("name = ?", k).First(&cfg).Error; err == nil {
			if strings.TrimSpace(cfg.Value) != "" && cfg.Value != "null" {
				return true
			}
		}
	}
	return false
}

func parseFirstRemoteHostPort(remote string) (string, int, bool) {
	addr := strings.TrimSpace(remote)
	if addr == "" {
		return "", 0, false
	}
	if strings.Contains(addr, ",") {
		addr = strings.Split(addr, ",")[0]
	}
	addr = strings.TrimSpace(addr)
	if strings.Contains(addr, "://") {
		if u, err := url.Parse(addr); err == nil && u.Host != "" {
			addr = u.Host
		}
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// try last colon split (IPv6 without brackets or raw host:port)
		idx := strings.LastIndex(addr, ":")
		if idx <= 0 {
			return "", 0, false
		}
		host = strings.Trim(addr[:idx], "[]")
		portStr = addr[idx+1:]
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return "", 0, false
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return "", 0, false
	}
	return host, p, true
}

func saveTunnelPathAndModes(tunnelID int64, path []int64, modes []string) {
	uniq := make([]int64, 0, len(path))
	seen := map[int64]struct{}{}
	for _, id := range path {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		var n model.Node
		if dbpkg.DB.First(&n, id).Error == nil {
			uniq = append(uniq, id)
			seen[id] = struct{}{}
		}
	}
	b, _ := json.Marshal(uniq)
	key := tunnelPathKey(tunnelID)
	now := time.Now().UnixMilli()
	var cfg model.ViteConfig
	if err := dbpkg.DB.Where("name = ?", key).First(&cfg).Error; err == nil {
		cfg.Value = string(b)
		cfg.Time = now
		_ = dbpkg.DB.Save(&cfg).Error
	} else {
		_ = dbpkg.DB.Create(&model.ViteConfig{Name: key, Value: string(b), Time: now}).Error
	}
	if len(modes) > 0 {
		modes = normalizeLinkModes(modes, len(uniq)+1, "direct")
		mb, _ := json.Marshal(modes)
		mkey := tunnelLinkModeKey(tunnelID)
		var mc model.ViteConfig
		if err := dbpkg.DB.Where("name = ?", mkey).First(&mc).Error; err == nil {
			mc.Value = string(mb)
			mc.Time = now
			_ = dbpkg.DB.Save(&mc).Error
		} else {
			_ = dbpkg.DB.Create(&model.ViteConfig{Name: mkey, Value: string(mb), Time: now}).Error
		}
	}
}
