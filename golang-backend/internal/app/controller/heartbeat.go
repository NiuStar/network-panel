package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	"network-panel/golang-backend/internal/db"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type heartbeatReq struct {
	Kind        string `json:"kind"`                  // agent | controller
	UniqueID    string `json:"uniqueId"`              // device unique id
	Version     string `json:"version,omitempty"`     // agent/controller version
	CreatedAtMs int64  `json:"createdAtMs,omitempty"` // origin create/start time (ms)
	OS          string `json:"os,omitempty"`          // os version/label
	Arch        string `json:"arch,omitempty"`        // cpu arch
	IP          string `json:"ip,omitempty"`          // optional reported public IP (if behind NAT/proxy)
	InstallMode string `json:"installMode,omitempty"` // controller deploy mode: docker/binary
}

// HeartbeatReport accepts agent/controller heartbeats and upserts metadata.
// No auth on purpose: callers are expected to provide a stable uniqueId.
func HeartbeatReport(c *gin.Context) {
	var req heartbeatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误: "+err.Error()))
		return
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind != "agent" && kind != "controller" {
		c.JSON(http.StatusOK, response.ErrMsg("kind 需为 agent 或 controller"))
		return
	}
	uid := strings.TrimSpace(req.UniqueID)
	if uid == "" {
		c.JSON(http.StatusOK, response.ErrMsg("uniqueId 不能为空"))
		return
	}
	nowMs := time.Now().UnixMilli()

	ip := req.IP
	if ip == "" {
		ip = clientIP(c)
	}
	ipPrefix := ipPrefix(ip)
	country, city := geoLookup(ipPrefix, ip)

	_, err := upsertHeartbeat(kind, uid, req.Version, req.OS, req.Arch, req.CreatedAtMs, nowMs, ip, ipPrefix, country, city, req.InstallMode)
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg(err.Error()))
		return
	}

	c.JSON(http.StatusOK, response.Ok(map[string]any{
		"kind":     kind,
		"uniqueId": uid,
		"at":       nowMs,
	}))
}

type heartbeatItem struct {
	UniqueID        string `json:"uniqueId"`
	Version         string `json:"version"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	CreatedAtMs     int64  `json:"createdAtMs"`
	FirstSeenMs     int64  `json:"firstSeenMs"`
	LastHeartbeatMs int64  `json:"lastHeartbeatMs"`
	UninstallAtMs   *int64 `json:"uninstallAtMs,omitempty"`
	IP              string `json:"ip,omitempty"`
	IPPrefix        string `json:"ipPrefix,omitempty"`
	Country         string `json:"country,omitempty"`
	City            string `json:"city,omitempty"`
	InstallMode     string `json:"installMode,omitempty"`
}

type heartbeatSummary struct {
	Total  int             `json:"total"`
	Active int             `json:"active"`
	Items  []heartbeatItem `json:"items"`
}

// HeartbeatSummary returns aggregated counts + detail lists for agents/controllers.
func HeartbeatSummary(c *gin.Context) {
	nowMs := time.Now().UnixMilli()
	markUninstalled(nowMs)

	collect := func(kind string) heartbeatSummary {
		var rows []model.HeartbeatRecord
		_ = db.DB.Where("kind = ?", kind).
			Order("first_seen_ms asc").
			Find(&rows).Error
		s := heartbeatSummary{Total: len(rows)}
		day2Ms := int64((48 * time.Hour) / time.Millisecond)
		for _, r := range rows {
			if nowMs-r.LatestHeartbeatMs <= day2Ms {
				s.Active++
			}
			ip := r.IP
			if ip == "" {
				ip = r.IPPrefix
			}
			s.Items = append(s.Items, heartbeatItem{
				UniqueID:        r.UniqueID,
				Version:         r.Version,
				OS:              r.OS,
				Arch:            r.Arch,
				CreatedAtMs:     r.CreatedAtMs,
				FirstSeenMs:     r.FirstSeenMs,
				LastHeartbeatMs: r.LatestHeartbeatMs,
				UninstallAtMs:   r.UninstallAtMs,
				IP:              ip,
				IPPrefix:        r.IPPrefix,
				Country:         r.Country,
				City:            r.City,
				InstallMode:     r.InstallMode,
			})
		}
		return s
	}

	c.JSON(http.StatusOK, response.Ok(gin.H{
		"agents":      collect("agent"),
		"controllers": collect("controller"),
	}))
}

// markUninstalled stamps uninstall_at_ms once a record is stale (>2 days).
func markUninstalled(nowMs int64) {
	dayMs := int64((24 * time.Hour) / time.Millisecond)
	cutoff := nowMs - 2*dayMs
	_ = db.DB.Model(&model.HeartbeatRecord{}).
		Where("uninstall_at_ms IS NULL AND last_hb_ms < ?", cutoff).
		UpdateColumn("uninstall_at_ms", clause.Expr{SQL: "last_hb_ms + ?", Vars: []any{dayMs}}).Error
}

// fallbackCreated returns the best-effort created time:
// prefer the incoming value; else keep existing; else default to now.
func fallbackCreated(incoming, existing, nowMs int64) int64 {
	if incoming > 0 {
		return incoming
	}
	if existing > 0 {
		return existing
	}
	return nowMs
}

func clientIP(c *gin.Context) string {
	ip := strings.TrimSpace(c.ClientIP())
	return ip
}

// ipPrefix returns first 3 octets for IPv4, or first 4 hextets for IPv6.
func ipPrefix(ip string) string {
	if ip == "" {
		return ""
	}
	if p, err := netip.ParseAddr(ip); err == nil {
		if p.Is4() {
			parts := strings.Split(p.String(), ".")
			if len(parts) >= 3 {
				return strings.Join(parts[:3], ".")
			}
			return p.String()
		}
		parts := strings.Split(p.String(), ":")
		if len(parts) >= 4 {
			return strings.ToLower(strings.Join(parts[:4], ":"))
		}
		return p.String()
	}
	// fallback simple split
	if strings.Count(ip, ".") >= 3 {
		parts := strings.Split(ip, ".")
		if len(parts) >= 3 {
			return strings.Join(parts[:3], ".")
		}
	}
	if strings.Count(ip, ":") >= 3 {
		parts := strings.Split(ip, ":")
		if len(parts) >= 4 {
			return strings.ToLower(strings.Join(parts[:4], ":"))
		}
	}
	return ip
}

var geoCache = struct {
	data map[string][2]string
}{data: map[string][2]string{}}

func geoLookup(prefix string, ip string) (string, string) {
	if prefix == "" {
		return "", ""
	}
	if v, ok := geoCache.data[prefix]; ok {
		return v[0], v[1]
	}
	api := os.Getenv("GEO_API_URL")
	if api == "" {
		api = "https://ipapi.co/%s/json"
	}
	url := fmt.Sprintf(api, ip)
	client := &http.Client{Timeout: 2 * time.Second}
	type geoResp struct {
		Country     string `json:"country"`
		CountryName string `json:"country_name"`
		City        string `json:"city"`
	}
	var country, city string
	if resp, err := client.Get(url); err == nil {
		defer resp.Body.Close()
		var gr geoResp
		if json.NewDecoder(resp.Body).Decode(&gr) == nil {
			if gr.CountryName != "" {
				country = gr.CountryName
			} else {
				country = gr.Country
			}
			city = gr.City
		}
	}
	geoCache.data[prefix] = [2]string{country, city}
	return country, city
}

// SaveHeartbeat allows internal callers (controller cron) to record heartbeat without HTTP context.
func SaveHeartbeat(kind, uid, version, osName, arch string, createdAtMs int64, installMode string, ip string) error {
	prefix := ipPrefix(ip)
	country, city := geoLookup(prefix, ip)
	_, err := upsertHeartbeat(kind, uid, version, osName, arch, createdAtMs, time.Now().UnixMilli(), ip, prefix, country, city, installMode)
	return err
}

func upsertHeartbeat(kind, uid, version, osName, arch string, createdAtMs, nowMs int64, ip, ipPrefix, country, city, installMode string) (model.HeartbeatRecord, error) {
	var rec model.HeartbeatRecord
	err := db.DB.Where("kind = ? AND unique_id = ?", kind, uid).First(&rec).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return rec, fmt.Errorf("查询失败: %w", err)
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		rec = model.HeartbeatRecord{
			Kind:              kind,
			UniqueID:          uid,
			Version:           version,
			OS:                osName,
			Arch:              arch,
			CreatedAtMs:       fallbackCreated(createdAtMs, 0, nowMs),
			FirstSeenMs:       nowMs,
			LatestHeartbeatMs: nowMs,
			UninstallAtMs:     nil,
			IP:                ip,
			IPPrefix:          ipPrefix,
			Country:           country,
			City:              city,
			InstallMode:       installMode,
		}
		if err := db.DB.Create(&rec).Error; err != nil {
			return rec, fmt.Errorf("写入失败: %w", err)
		}
		return rec, nil
	}

	rec.Version = version
	rec.OS = osName
	rec.Arch = arch
	rec.LatestHeartbeatMs = nowMs
	rec.CreatedAtMs = fallbackCreated(createdAtMs, rec.CreatedAtMs, nowMs)
	rec.UninstallAtMs = nil
	if ip != "" {
		rec.IP = ip
	}
	rec.IPPrefix = ipPrefix
	rec.Country = country
	rec.City = city
	if installMode != "" {
		rec.InstallMode = installMode
	}
	if err := db.DB.Model(&model.HeartbeatRecord{}).
		Where("id = ?", rec.ID).
		Updates(map[string]any{
			"version":         rec.Version,
			"os":              rec.OS,
			"arch":            rec.Arch,
			"created_at_ms":   rec.CreatedAtMs,
			"last_hb_ms":      rec.LatestHeartbeatMs,
			"uninstall_at_ms": nil,
			"ip":              rec.IP,
			"ip_prefix":       rec.IPPrefix,
			"country":         rec.Country,
			"city":            rec.City,
			"install_mode":    rec.InstallMode,
		}).Error; err != nil {
		return rec, fmt.Errorf("更新失败: %w", err)
	}
	return rec, nil
}
