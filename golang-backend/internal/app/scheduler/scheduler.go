package scheduler

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"network-panel/golang-backend/internal/app/controller"
	"network-panel/golang-backend/internal/app/model"
	appver "network-panel/golang-backend/internal/app/version"
	dbpkg "network-panel/golang-backend/internal/db"
)

func Start() {
	go billingChecker()
	go controllerHeartbeat()
	go pruneOldData()
}

func billingChecker() {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		checkOnce()
		<-ticker.C
	}
}

func checkOnce() {
	var nodes []model.Node
	dbpkg.DB.Find(&nodes)
	now := time.Now().UnixMilli()
	dayMs := int64(24 * 3600 * 1000)
	for _, n := range nodes {
		if n.CycleDays == nil || *n.CycleDays <= 0 || n.StartDateMs == nil || *n.StartDateMs <= 0 {
			continue
		}
		cycleMs := int64(*n.CycleDays) * dayMs
		if cycleMs <= 0 {
			continue
		}
		if now < *n.StartDateMs {
			continue
		}
		elapsed := now - *n.StartDateMs
		// remaining in current period
		rem := cycleMs - (elapsed % cycleMs)
		// if <= 1 day, trigger reminder
		if rem <= dayMs {
			msg := fmt.Sprintf("节点即将到期，剩余 %d 天", (rem+dayMs-1)/dayMs)
			// alert record
			name := n.Name
			nid := n.ID
			a := model.Alert{TimeMs: now, Type: "due", NodeID: &nid, NodeName: &name, Message: msg}
			_ = dbpkg.DB.Create(&a).Error
			// callback
			controller.TriggerCallback("node_due", n, map[string]any{"remainMs": rem})
		}
	}
}

// controllerHeartbeat reports controller presence/version to heartbeat table hourly.
func controllerHeartbeat() {
	tick := time.NewTicker(1 * time.Hour)
	defer tick.Stop()
	uid := controllerUID()
	created := controllerCreatedAt()
	endpoint := heartbeatEndpoint()
	if endpoint == "" {
		return
	}
	send := func() {
		mode := installMode()
		ip := fetchExternalIP()
		_ = postHeartbeat(endpoint, "controller", uid, appver.Get(), runtime.GOOS, runtime.GOARCH, created, mode, ip)
		// 也写入本地，便于本地面板查看
		_ = controller.SaveHeartbeat("controller", uid, appver.Get(), runtime.GOOS, runtime.GOARCH, created, mode, ip)
	}
	send()
	for range tick.C {
		send()
	}
}

func controllerUID() string {
	if v := strings.TrimSpace(os.Getenv("CENTER_UID")); v != "" {
		return v
	}
	paths := []string{"/etc/machine-id", "/var/lib/dbus/machine-id"}
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return "ctrl-" + s
			}
		}
	}
	store := "/opt/network-panel/controller_uid"
	if b, err := os.ReadFile(store); err == nil && len(b) > 0 {
		return strings.TrimSpace(string(b))
	}
	_ = os.MkdirAll("/opt/network-panel", 0o755)
	if ip := fetchExternalIP(); ip != "" {
		sum := md5.Sum([]byte(ip))
		id := fmt.Sprintf("ctrl-hip-%x", sum[:4])
		_ = os.WriteFile(store, []byte(id), 0o644)
		return id
	}
	now := time.Now().UnixNano()
	id := fmt.Sprintf("ctrl-%d", now)
	_ = os.WriteFile(store, []byte(id), 0o644)
	return id
}

func controllerCreatedAt() int64 {
	store := "/opt/network-panel/controller_created_ms"
	if b, err := os.ReadFile(store); err == nil {
		if ms, err2 := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err2 == nil && ms > 0 {
			return ms
		}
	}
	now := time.Now().UnixMilli()
	_ = os.WriteFile(store, []byte(fmt.Sprintf("%d", now)), 0o644)
	return now
}

func installMode() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	if strings.ToLower(strings.TrimSpace(os.Getenv("RUNNING_IN_DOCKER"))) == "1" {
		return "docker"
	}
	return "binary"
}

func fetchExternalIP() string {
	url := strings.TrimSpace(os.Getenv("IP_LOOKUP_URL"))
	if url == "" {
		url = "https://api.ip.sb/ip"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	if resp, err := client.Get(url); err == nil {
		defer resp.Body.Close()
		if b, err2 := io.ReadAll(resp.Body); err2 == nil {
			ip := strings.TrimSpace(string(b))
			if ip != "" {
				return ip
			}
		}
	}
	return ""
}

func heartbeatEndpoint() string {
	if v := strings.TrimSpace(os.Getenv("HEARTBEAT_ENDPOINT")); v != "" {
		return v
	}
	return "https://flux.529851.xyz/api/v1/stats/heartbeat"
}

func postHeartbeat(endpoint, kind, uid, version, osName, arch string, createdAt int64, mode string, ip string) error {
	payload := map[string]any{
		"kind":        kind,
		"uniqueId":    uid,
		"version":     version,
		"os":          osName,
		"arch":        arch,
		"createdAtMs": createdAt,
		"installMode": mode,
		"ip":          ip,
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

// pruneOldData cleans time-series tables older than 3 days
func pruneOldData() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	cutoff := func() int64 { return time.Now().Add(-72 * time.Hour).UnixMilli() }
	clean := func(table any, col string) {
		_ = dbpkg.DB.Where(col+" < ?", cutoff()).Delete(table).Error
	}
	for {
		clean(&model.NodeOpLog{}, "time_ms")
		clean(&model.NodeProbeResult{}, "time_ms")
		clean(&model.NodeSysInfo{}, "time_ms")
		clean(&model.FlowTimeseries{}, "time_ms")
		clean(&model.NQResult{}, "time_ms")
		<-ticker.C
	}
}
