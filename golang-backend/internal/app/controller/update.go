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
	logs, out, errs, post := runUpgradeWithRestart(p.ProxyPrefix, nil)
	resp := map[string]any{
		"tag":     out["tag"],
		"created": out["created"],
		"logs":    logs,
	}
	resp["restart"] = out["restart"]
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	c.JSON(http.StatusOK, response.Ok(resp))
	if post != nil {
		go post()
	}
}

// doVersionUpgradeWithProxy performs upgrade and collects logs; returns logs, data map, errs.
// Kept for reuse in both JSON和流模式。
func doVersionUpgradeWithProxy(proxyPrefix string) ([]string, map[string]any, []string) {
	logs, out, errs, _ := runUpgradeWithRestart(proxyPrefix, nil)
	return logs, out, errs
}

// runUpgradeWithRestart runs the full upgrade flow (download, unpack, fallback assets) and prepares restart.
// externalLog will receive real-time messages; logs slice always collected for API response.
func runUpgradeWithRestart(proxyPrefix string, externalLog func(string, ...any)) ([]string, map[string]any, []string, func()) {
	logs := []string{}
	emit := func(format string, a ...any) {
		msg := fmt.Sprintf("%s "+format, append([]any{time.Now().Format("15:04:05.000")}, a...)...)
		logs = append(logs, msg)
		if externalLog != nil {
			externalLog(msg)
		}
	}

	out, errs := performUpgrade(proxyPrefix, emit)
	if len(errs) > 0 && len(out) == 0 {
		return logs, out, errs, nil
	}
	restartMode, restartErrs, post := attemptRestart(out, proxyPrefix, emit)
	if restartMode != "" {
		out["restart"] = restartMode
	}
	if len(restartErrs) > 0 {
		errs = append(errs, restartErrs...)
	}
	return logs, out, errs, post
}

// performUpgrade executes downloads/unzip/fallback copies and returns metadata + errs.
func performUpgrade(proxyPrefix string, logf func(string, ...any)) (map[string]any, []string) {
	errs := []string{}

	rel, err := fetchLatestRelease()
	if err != nil {
		msg := "获取最新版本失败: " + err.Error()
		logf(msg)
		return map[string]any{}, []string{msg}
	}
	logf("最新版本: %s (%s)", rel.TagName, rel.Name)
	assets := classifyAssets(rel)
	made := map[string]string{}

	// Ensure directories
	_ = os.MkdirAll("public", 0o755)
	_ = os.MkdirAll("public/flux-agent", 0o755)
	_ = os.MkdirAll("public/server", 0o755)

	downloadAssets := func(name, url, dst string, mode os.FileMode) {
		start := time.Now()
		if err := downloadToPathLogged(url, dst, mode, logf); err != nil {
			msg := fmt.Sprintf("%s 下载失败: %v", name, err)
			errs = append(errs, msg)
			logf(msg)
			return
		}
		made[name] = dst
		logf("%s 已更新，耗时 %.2fs", name, time.Since(start).Seconds())
	}

	// 1) Frontend
	if url, _ := assets["frontendZip"].(string); url != "" {
		if proxyPrefix != "" {
			url = proxyPrefix + url
		}
		start := time.Now()
		if file, err := downloadToTmpLogged(url, logf); err != nil {
			msg := fmt.Sprintf("frontend-dist.zip 下载失败: %v", err)
			errs = append(errs, msg)
			logf(msg)
		} else {
			logf("frontend-dist.zip 下载完成，开始解压到 public/")
			if err := unzipTo(file, "public"); err != nil {
				msg := fmt.Sprintf("frontend-dist.zip 解压失败: %v", err)
				errs = append(errs, msg)
				logf(msg)
			} else {
				made["frontend"] = "public/"
				logf("frontend-dist.zip 解压到 public/ (耗时 %.2fs)", time.Since(start).Seconds())
			}
			_ = os.Remove(file)
		}
	} else {
		msg := "未找到前端资源(frontend-dist.zip)"
		errs = append(errs, msg)
		logf(msg)
	}

	// 2) install.sh
	if url, _ := assets["installSh"].(string); url != "" {
		if proxyPrefix != "" {
			url = proxyPrefix + url
		}
		downloadAssets("install.sh", url, "install.sh", 0o755)
	} else {
		logf("release 未包含 install.sh，后续使用仓库原始文件覆盖")
	}

	// 3) flux-agent binaries
	if m, ok := assets["agents"].(map[string]string); ok {
		for name, url := range m {
			u := url
			if proxyPrefix != "" {
				u = proxyPrefix + url
			}
			dst := filepath.Join("public/flux-agent", name)
			downloadAssets(name, u, dst, 0o755)
		}
	}

	// 4) server binaries (save under public/server for manual adoption/restart)
	if m, ok := assets["servers"].(map[string]string); ok {
		for name, url := range m {
			u := url
			if proxyPrefix != "" {
				u = proxyPrefix + url
			}
			dst := filepath.Join("public/server", name)
			downloadAssets(name, u, dst, 0o755)
		}
	}

	out := map[string]any{
		"tag":           rel.TagName,
		"created":       made,
		"assetsServers": assets["servers"],
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
	if proxyPrefix != "" {
		ri = proxyPrefix + rawInstall
		rc = proxyPrefix + rawEtConf
		re = proxyPrefix + rawEtInstall
		ghi = proxyPrefix + ghInstall
		ghc = proxyPrefix + ghEtConf
		ghe = proxyPrefix + ghEtInstall
	}
	tryRaw := func(primary, fallback, dst, key string, mode os.FileMode) {
		logf("%s 下载：主线路=%s 兜底=%s", key, primary, fallback)
		if err := downloadToPathLogged(primary, dst, mode, logf); err != nil {
			logf("%s 主线路失败，尝试兜底: %v", key, err)
			if err2 := downloadToPathLogged(fallback, dst, mode, logf); err2 != nil {
				msg := fmt.Sprintf("%s 下载失败: %v; 兜底失败: %v", key, err, err2)
				errs = append(errs, msg)
				logf(msg)
				return
			}
		}
		made[key] = dst
		logf("%s 写入 %s", key, dst)
	}

	tryRaw(ri, ghi, filepath.Join(installDir, "install.sh"), "install.sh", 0o755)
	tryRaw(rc, ghc, filepath.Join(installDir, "easytier/default.conf"), "easytier/default.conf", 0o644)
	tryRaw(re, ghe, filepath.Join(installDir, "easytier/install.sh"), "easytier/install.sh", 0o755)

	out["created"] = made
	if len(errs) > 0 {
		out["errors"] = errs
	}
	return out, errs
}

// attemptRestart tries to refresh running service and returns restart mode + errors and optional post action.
func attemptRestart(out map[string]any, proxyPrefix string, logf func(string, ...any)) (string, []string, func()) {
	errs := []string{}
	assetsServers, _ := out["assetsServers"].(map[string]string)

	if isDocker() {
		usingLauncher := os.Getenv("NP_LAUNCHER") != ""
		if usingLauncher {
			logf("检测到 launcher 环境，将通过信号触发重启")
		}
		if url := pickServerAsset(assetsServers); url != "" {
			u := url
			if proxyPrefix != "" {
				u = proxyPrefix + url
			}
			logf("Docker 环境，准备拉取匹配的 server 二进制: %s", u)
			dst := "/app/server.new"
			if err := downloadToPathLogged(u, dst, 0o755, logf); err != nil {
				msg := fmt.Sprintf("server binary 下载失败: %v", err)
				errs = append(errs, msg)
				logf(msg)
			} else {
				_ = os.Chmod(dst, 0o755)
				if err := os.Rename(dst, "/app/server"); err != nil {
					msg := fmt.Sprintf("server 覆盖失败: %v", err)
					errs = append(errs, msg)
					logf(msg)
				} else {
					logf("server 已覆盖")
					if usingLauncher {
						if notifyLauncherRestart(logf) {
							return "launcher-signal", errs, nil
						}
						logf("未能通知 launcher（ppid=%d），使用自重启兜底", os.Getppid())
					} else {
						logf("未检测到 launcher，准备直接 exec 重启")
						return "exec", errs, func() {
							time.Sleep(800 * time.Millisecond)
							_ = syscall.Exec("/app/server", os.Args, os.Environ())
							_ = exec.Command("/app/server", os.Args[1:]...).Start()
							os.Exit(0)
						}
					}
				}
			}
		}
		// 无法直接通知 launcher 或下载失败时，兜底自我重启
		exe, _ := os.Executable()
		logf("Docker 环境无法直接替换二进制，尝试自我重启: %s", exe)
		return "docker-exit", errs, func() {
			time.Sleep(800 * time.Millisecond)
			_ = syscall.Exec(exe, os.Args, os.Environ())
			_ = exec.Command(exe, os.Args[1:]...).Start()
			os.Exit(0)
		}
	}

	// binary install: attempt systemctl restart network-panel
	logf("尝试 systemctl restart network-panel")
	if err := exec.Command("systemctl", "restart", "network-panel").Run(); err != nil {
		msg := fmt.Sprintf("重启服务失败: %v", err)
		errs = append(errs, msg)
		logf(msg)
		// 兜底：尝试自我重启
		exe, _ := os.Executable()
		logf("尝试自我重启: %s", exe)
		return "self-exec", errs, func() {
			time.Sleep(800 * time.Millisecond)
			_ = syscall.Exec(exe, os.Args, os.Environ())
			_ = exec.Command(exe, os.Args[1:]...).Start()
			os.Exit(0)
		}
	}
	logf("systemctl restart network-panel 成功")
	return "systemctl", errs, nil
}

// notifyLauncherRestart sends SIGHUP to parent (launcher) to trigger a restart.
func notifyLauncherRestart(logf func(string, ...any)) bool {
	ppid := os.Getppid()
	if ppid <= 1 {
		logf("launcher 重启失败：父进程无效(ppid=%d)", ppid)
		return false
	}
	proc, err := os.FindProcess(ppid)
	if err != nil {
		logf("launcher 重启失败：%v", err)
		return false
	}
	if err := proc.Signal(syscall.SIGHUP); err != nil {
		logf("launcher 重启信号发送失败: %v", err)
		return false
	}
	logf("已向 launcher(ppid=%d) 发送 SIGHUP 请求重启", ppid)
	return true
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

type countingWriter struct {
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

func downloadToTmpLogged(url string, logf func(string, ...any)) (string, error) {
	start := time.Now()
	logf("GET %s -> 临时文件 开始", url)
	cli := &http.Client{Timeout: 45 * time.Second}
	resp, err := cli.Get(url)
	if err != nil {
		logf("GET %s 失败: %v", url, err)
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
		logf("GET %s 失败: %v", url, err)
		return "", err
	}
	f, err := os.CreateTemp("", "np_dl_")
	if err != nil {
		logf("创建临时文件失败: %v", err)
		return "", err
	}
	cw := &countingWriter{}
	if _, err := io.Copy(io.MultiWriter(f, cw), resp.Body); err != nil {
		f.Close()
		logf("GET %s 传输失败: %v", url, err)
		return "", err
	}
	f.Close()
	logf("GET %s 成功 status=%d bytes=%d 耗时=%.2fs", url, resp.StatusCode, cw.n, time.Since(start).Seconds())
	return f.Name(), nil
}

func downloadToPathLogged(url, dst string, mode os.FileMode, logf func(string, ...any)) error {
	start := time.Now()
	logf("GET %s -> %s 开始", url, dst)
	cli := &http.Client{Timeout: 45 * time.Second}
	resp, err := cli.Get(url)
	if err != nil {
		logf("GET %s 失败: %v", url, err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
		logf("GET %s 失败: %v", url, err)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		logf("创建目录失败 %s: %v", filepath.Dir(dst), err)
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		logf("写入临时文件失败 %s: %v", tmp, err)
		return err
	}
	cw := &countingWriter{}
	if _, err := io.Copy(io.MultiWriter(f, cw), resp.Body); err != nil {
		f.Close()
		logf("GET %s 传输失败: %v", url, err)
		return err
	}
	f.Close()
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		logf("chmod %s 失败: %v", tmp, err)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		logf("替换 %s 失败: %v", dst, err)
		return err
	}
	logf("GET %s 成功 status=%d bytes=%d 耗时=%.2fs", url, resp.StatusCode, cw.n, time.Since(start).Seconds())
	return nil
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
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if v := os.Getenv("container"); v != "" {
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
