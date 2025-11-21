package controller

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"network-panel/golang-backend/internal/app/response"

	"github.com/gin-gonic/gin"
)

type ghRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// GET /api/v1/version/latest
// Returns {tag, assets: {frontendZip, installSh, agents: {...}, servers: {...}}}
func VersionLatest(c *gin.Context) {
	rel, err := fetchLatestRelease()
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("获取最新版本失败: "+err.Error()))
		return
	}
	out := map[string]any{
		"tag":    rel.TagName,
		"name":   rel.Name,
		"assets": classifyAssets(rel),
	}
	c.JSON(http.StatusOK, response.Ok(out))
}

// POST /api/v1/version/upgrade {proxyPrefix?: string}
// Downloads latest frontend-dist.zip -> ./public, install.sh -> ./install.sh,
// flux-agent binaries -> ./public/flux-agent, server binaries -> ./public/server
func VersionUpgrade(c *gin.Context) {
	var p struct {
		ProxyPrefix string `json:"proxyPrefix"`
	}
	_ = c.ShouldBindJSON(&p)
	rel, err := fetchLatestRelease()
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("获取最新版本失败: "+err.Error()))
		return
	}
	assets := classifyAssets(rel)
	errs := []string{}
	made := map[string]string{}

	// Ensure directories
	_ = os.MkdirAll("public", 0o755)
	_ = os.MkdirAll("public/flux-agent", 0o755)
	_ = os.MkdirAll("public/server", 0o755)

	// 1) Frontend
	if url, _ := assets["frontendZip"].(string); url != "" {
		if p.ProxyPrefix != "" {
			url = p.ProxyPrefix + url
		}
		if file, err := downloadToTmp(url); err != nil {
			errs = append(errs, fmt.Sprintf("frontend-dist.zip 下载失败: %v", err))
		} else {
			if err := unzipTo(file, "public"); err != nil {
				errs = append(errs, fmt.Sprintf("frontend-dist.zip 解压失败: %v", err))
			} else {
				made["frontend"] = "public/"
			}
			_ = os.Remove(file)
		}
	} else {
		errs = append(errs, "未找到前端资源(frontend-dist.zip)")
	}

	// 2) install.sh
	if url, _ := assets["installSh"].(string); url != "" {
		if p.ProxyPrefix != "" {
			url = p.ProxyPrefix + url
		}
		if err := downloadToPath(url, "install.sh", 0o755); err != nil {
			errs = append(errs, fmt.Sprintf("install.sh 下载失败: %v", err))
		} else {
			made["install.sh"] = "install.sh"
		}
	} else {
		errs = append(errs, "未找到 install.sh 资源")
	}

	// 3) flux-agent binaries
	if m, ok := assets["agents"].(map[string]string); ok {
		for name, url := range m {
			u := url
			if p.ProxyPrefix != "" {
				u = p.ProxyPrefix + url
			}
			dst := filepath.Join("public/flux-agent", name)
			if err := downloadToPath(u, dst, 0o755); err != nil {
				errs = append(errs, fmt.Sprintf("%s 下载失败: %v", name, err))
			} else {
				made[name] = dst
			}
		}
	}

	// 4) server binaries (save under public/server for manual adoption/restart)
	if m, ok := assets["servers"].(map[string]string); ok {
		for name, url := range m {
			u := url
			if p.ProxyPrefix != "" {
				u = p.ProxyPrefix + url
			}
			dst := filepath.Join("public/server", name)
			if err := downloadToPath(u, dst, 0o755); err != nil {
				errs = append(errs, fmt.Sprintf("%s 下载失败: %v", name, err))
			} else {
				made[name] = dst
			}
		}
	}

	out := map[string]any{
		"tag":     rel.TagName,
		"created": made,
	}
	if len(errs) > 0 {
		out["errors"] = errs
	}
	// Extra step: fetch install assets from main branch and place into install dir
	installDir := detectInstallDir()
	if installDir == "" {
		installDir = "/opt/network-panel"
	}
	_ = os.MkdirAll(installDir, 0o755)
	_ = os.MkdirAll(filepath.Join(installDir, "easytier"), 0o755)

	// Raw files from main branch
	staticBase := "https://panel-static.199028.xyz/network-panel"
	ghRawBase := "https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main"
	rawInstall := staticBase + "/install.sh"
	rawEtConf := staticBase + "/easytier/default.conf"
	rawEtInstall := staticBase + "/easytier/install.sh"
	ghInstall := ghRawBase + "/install.sh"
	ghEtConf := ghRawBase + "/easytier/default.conf"
	ghEtInstall := ghRawBase + "/easytier/install.sh"
	// allow proxyPrefix for these raw downloads
	ri := rawInstall
	rc := rawEtConf
	re := rawEtInstall
	ghi := ghInstall
	ghc := ghEtConf
	ghe := ghEtInstall
	if p.ProxyPrefix != "" {
		ri = p.ProxyPrefix + rawInstall
		rc = p.ProxyPrefix + rawEtConf
		re = p.ProxyPrefix + rawEtInstall
		ghi = p.ProxyPrefix + ghInstall
		ghc = p.ProxyPrefix + ghEtConf
		ghe = p.ProxyPrefix + ghEtInstall
	}
	tryRaw := func(primary, fallback, dst, key string, mode os.FileMode) {
		if err := downloadToPath(primary, dst, mode); err != nil {
			if err2 := downloadToPath(fallback, dst, mode); err2 != nil {
				errs = append(errs, fmt.Sprintf("%s 下载失败: %v; 兜底失败: %v", key, err, err2))
				return
			}
		}
		made[key] = dst
	}

	tryRaw(ri, ghi, filepath.Join(installDir, "install.sh"), "install.sh", 0o755)
	tryRaw(rc, ghc, filepath.Join(installDir, "easytier", "default.conf"), "easytier/default.conf", 0o644)
	tryRaw(re, ghe, filepath.Join(installDir, "easytier", "install.sh"), "easytier/install.sh", 0o755)

	out["created"] = made
	if len(errs) > 0 {
		out["errors"] = errs
	}

	// Try to restart service/process
	if isDocker() {
		// attempt to replace /app/server with arch-matched binary and exec-replace
		if m, ok := assets["servers"].(map[string]string); ok {
			if url := pickServerAsset(m); url != "" {
				u := url
				if p.ProxyPrefix != "" {
					u = p.ProxyPrefix + url
				}
				dst := "/app/server.new"
				if err := downloadToPath(u, dst, 0o755); err != nil {
					errs = append(errs, fmt.Sprintf("server binary 下载失败: %v", err))
				} else {
					_ = os.Chmod(dst, 0o755)
					if err := os.Rename(dst, "/app/server"); err != nil {
						errs = append(errs, fmt.Sprintf("server 覆盖失败: %v", err))
					} else {
						made["server"] = "/app/server"
						out["created"] = made
						if len(errs) > 0 {
							out["errors"] = errs
						}
						out["restart"] = "exec"
						c.JSON(http.StatusOK, response.Ok(out))
						go func() {
							time.Sleep(500 * time.Millisecond)
							_ = syscall.Exec("/app/server", os.Args, os.Environ())
							_ = exec.Command("/app/server", os.Args[1:]...).Start()
							os.Exit(0)
						}()
						return
					}
				}
			} else {
				errs = append(errs, "未匹配到当前架构的 server 资产")
			}
		}
		out["restart"] = "docker-exit"
		if len(errs) > 0 {
			out["errors"] = errs
		}
		c.JSON(http.StatusOK, response.Ok(out))
		go func() { time.Sleep(1 * time.Second); os.Exit(0) }()
		return
	}
	// binary install: attempt systemctl restart network-panel
	if err := exec.Command("systemctl", "restart", "network-panel").Run(); err != nil {
		errs = append(errs, fmt.Sprintf("重启服务失败: %v", err))
		out["errors"] = errs
		out["restart"] = "manual"
	} else {
		out["restart"] = "systemctl"
	}
	c.JSON(http.StatusOK, response.Ok(out))
}

func fetchLatestRelease() (*ghRelease, error) {
	url := "https://api.github.com/repos/NiuStar/network-panel/releases/latest"
	cli := &http.Client{Timeout: 8 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func classifyAssets(rel *ghRelease) map[string]any {
	out := map[string]any{}
	agents := map[string]string{}
	servers := map[string]string{}
	for _, a := range rel.Assets {
		n := a.Name
		u := a.BrowserDownloadURL
		switch {
		case n == "frontend-dist.zip":
			out["frontendZip"] = u
		case n == "install.sh":
			out["installSh"] = u
		case strings.HasPrefix(n, "flux-agent-"):
			agents[n] = u
		case strings.HasPrefix(n, "network-panel-server-"):
			servers[n] = u
		}
	}
	if len(agents) > 0 {
		out["agents"] = agents
	}
	if len(servers) > 0 {
		out["servers"] = servers
	}
	return out
}

func downloadToTmp(url string) (string, error) {
	cli := &http.Client{Timeout: 30 * time.Second}
	resp, err := cli.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	f, err := os.CreateTemp("", "np_dl_")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func downloadToPath(url, dst string, mode os.FileMode) error {
	cli := &http.Client{Timeout: 45 * time.Second}
	resp, err := cli.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func unzipTo(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		rp := filepath.Clean(filepath.Join(destDir, f.Name))
		if !strings.HasPrefix(rp, filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue // skip traversal
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(rp, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(rp), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		tmp := rp + ".tmp"
		out, err := os.Create(tmp)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
		if err := os.Rename(tmp, rp); err != nil {
			return err
		}
	}
	return nil
}

// pick server asset URL for current linux architecture
func pickServerAsset(servers map[string]string) string {
	arch := runtime.GOARCH
	key := "network-panel-server-linux-" + arch
	if v, ok := servers[key]; ok {
		return v
	}
	if arch == "arm" {
		if v, ok := servers["network-panel-server-linux-armv7"]; ok {
			return v
		}
		if v, ok := servers["network-panel-server-linux-armv6"]; ok {
			return v
		}
		if v, ok := servers["network-panel-server-linux-armv5"]; ok {
			return v
		}
	}
	if arch == "amd64" {
		if v, ok := servers["network-panel-server-linux-amd64v3"]; ok {
			return v
		}
	}
	return ""
}

// detectInstallDir returns container or binary install directory
func detectInstallDir() string {
	if isDocker() {
		return "/app"
	}
	return "/opt/network-panel"
}

// isDocker tries to detect if running inside a Docker container
func isDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	// best-effort: check cgroup info
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		if strings.Contains(s, ":/docker/") || strings.Contains(s, "/docker-") || strings.Contains(strings.ToLower(s), "containerd") {
			return true
		}
	}
	return false
}
