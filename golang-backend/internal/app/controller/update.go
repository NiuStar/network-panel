package controller

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// VersionLatest 获取最新版本
// @Summary 获取最新版本
// @Tags version
// @Produce json
// @Success 200 {object} SwaggerVersionLatestResp
// @Router /api/v1/version/latest [get]
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
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

func getEnvInt64(key string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func maxUpdateDownloadBytes() int64 {
	return getEnvInt64("UPDATE_DOWNLOAD_MAX_BYTES", 512*1024*1024)
}

func maxUnzipEntryBytes() int64 {
	return getEnvInt64("UPDATE_UNZIP_MAX_ENTRY_BYTES", 256*1024*1024)
}

func checkHTTPContentLength(resp *http.Response, maxBytes int64) error {
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		return fmt.Errorf("response content-length %d exceeds max download size %d", resp.ContentLength, maxBytes)
	}
	return nil
}

func copyLimited(dst io.Writer, src io.Reader, maxBytes int64, label string) (int64, error) {
	if maxBytes <= 0 {
		return io.Copy(dst, src)
	}
	n, err := io.Copy(dst, io.LimitReader(src, maxBytes+1))
	if err != nil {
		return n, err
	}
	if n > maxBytes {
		return n, fmt.Errorf("%s exceeds max download size %d", label, maxBytes)
	}
	return n, nil
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
	if err := checkHTTPContentLength(resp, maxUpdateDownloadBytes()); err != nil {
		logf("GET %s 失败: %v", url, err)
		return "", err
	}
	f, err := os.CreateTemp("", "np_dl_")
	if err != nil {
		logf("创建临时文件失败: %v", err)
		return "", err
	}
	cw := &countingWriter{}
	if _, err := copyLimited(io.MultiWriter(f, cw), resp.Body, maxUpdateDownloadBytes(), "download body"); err != nil {
		f.Close()
		_ = os.Remove(f.Name())
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
	if err := checkHTTPContentLength(resp, maxUpdateDownloadBytes()); err != nil {
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
	if _, err := copyLimited(io.MultiWriter(f, cw), resp.Body, maxUpdateDownloadBytes(), "download body"); err != nil {
		f.Close()
		_ = os.Remove(tmp)
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	if err := checkHTTPContentLength(resp, maxUpdateDownloadBytes()); err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "np_dl_")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := copyLimited(f, resp.Body, maxUpdateDownloadBytes(), "download body"); err != nil {
		_ = os.Remove(f.Name())
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	if err := checkHTTPContentLength(resp, maxUpdateDownloadBytes()); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := copyLimited(f, resp.Body, maxUpdateDownloadBytes(), "download body"); err != nil {
		f.Close()
		_ = os.Remove(tmp)
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
		if max := maxUnzipEntryBytes(); max > 0 && int64(f.UncompressedSize64) > max {
			return fmt.Errorf("zip entry %s size %d exceeds max unzip entry size %d", f.Name, f.UncompressedSize64, max)
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
		if _, err := copyLimited(out, rc, maxUnzipEntryBytes(), "zip entry "+f.Name); err != nil {
			out.Close()
			rc.Close()
			_ = os.Remove(tmp)
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

func cleanCompressedAssets(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".gz") || strings.HasSuffix(name, ".br") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
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
