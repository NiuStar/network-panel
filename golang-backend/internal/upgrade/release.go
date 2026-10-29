package upgrade

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const latestURL = "https://api.github.com/repos/NiuStar/network-panel/releases/latest"
const downloadBase = "https://github.com/NiuStar/network-panel/releases/download/"

var tagPattern = regexp.MustCompile(`^v[0-9]+(\.[0-9]+){2,3}$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

type asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}
type release struct {
	Tag        string  `json:"tag_name"`
	Commit     string  `json:"target_commitish"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

func limits(l Limits) Limits {
	if l.AssetBytes <= 0 || l.AssetBytes > 256<<20 {
		l.AssetBytes = 256 << 20
	}
	if l.TotalBytes <= 0 || l.TotalBytes > 1<<30 {
		l.TotalBytes = 1 << 30
	}
	if l.ZipBytes <= 0 || l.ZipBytes > 512<<20 {
		l.ZipBytes = 512 << 20
	}
	if l.ZipEntries <= 0 || l.ZipEntries > 20000 {
		l.ZipEntries = 20000
	}
	return l
}
func get(ctx context.Context, c *http.Client, url string) (*http.Response, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "network-panel-upgrader")
	resp, e := c.Do(req)
	if e != nil {
		return nil, e
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	return resp, nil
}
func readSmall(r io.Reader, max int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, max+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > max {
		return nil, errors.New("response too large")
	}
	return b, nil
}
func required(arch string) ([]string, error) {
	switch arch {
	case "amd64", "arm64", "armv7":
	default:
		return nil, errors.New("unsupported architecture")
	}
	names := []string{"SHA256SUMS", "BUILD-INFO.json", "panel-runtime-scripts.zip", "frontend-dist.zip", "network-panel-server-linux-" + arch}
	for _, a := range []string{"amd64", "arm64", "armv7"} {
		for _, n := range []string{"flux-agent", "flux-agent2"} {
			names = append(names, n+"-linux-"+a)
		}
	}
	return names, nil
}
func metadata(r release, arch, expected string, l Limits) (map[string]asset, []string, error) {
	names, e := required(arch)
	if e != nil {
		return nil, nil, e
	}
	if !tagPattern.MatchString(r.Tag) || r.Draft || r.Prerelease || !commitPattern.MatchString(r.Commit) || (expected != "" && r.Tag != expected) {
		return nil, nil, errors.New("invalid release identity or unexpected tag")
	}
	all := map[string]asset{}
	for _, a := range r.Assets {
		if _, ok := all[a.Name]; ok {
			return nil, nil, errors.New("duplicate asset metadata")
		}
		all[a.Name] = a
	}
	selected := map[string]asset{}
	var total int64
	for _, n := range names {
		a, ok := all[n]
		if !ok {
			return nil, nil, fmt.Errorf("missing required asset %s", n)
		}
		if a.URL != downloadBase+r.Tag+"/"+n || !strings.HasPrefix(a.Digest, "sha256:") || !shaPattern.MatchString(strings.TrimPrefix(a.Digest, "sha256:")) || a.Size <= 0 || a.Size > l.AssetBytes {
			return nil, nil, fmt.Errorf("untrusted asset metadata: %s", n)
		}
		if a.Size > l.TotalBytes-total {
			return nil, nil, errors.New("total asset limit")
		}
		total += a.Size
		selected[n] = a
	}
	return selected, names, nil
}
func download(ctx context.Context, c *http.Client, a asset, dest string, l Limits) (string, error) {
	resp, e := get(ctx, c, a.URL)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	if resp.ContentLength > a.Size {
		return "", errors.New("asset content length mismatch")
	}
	f, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, a.Size+1))
	if e == nil && n != a.Size {
		e = errors.New("asset size mismatch")
	}
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return "", e
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if "sha256:"+digest != a.Digest {
		return "", fmt.Errorf("API digest mismatch: %s", a.Name)
	}
	return digest, nil
}
func checksumList(file string) (map[string]string, error) {
	f, e := os.Open(file)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := readSmall(f, 2<<20)
	if e != nil {
		return nil, e
	}
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 67 || !shaPattern.MatchString(line[:64]) || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return nil, errors.New("invalid checksum line")
		}
		n := line[66:]
		if n == "" || path.Base(n) != n || strings.ContainsAny(n, "\\\x00\r") {
			return nil, errors.New("invalid checksum name")
		}
		if _, ok := out[n]; ok {
			return nil, errors.New("duplicate checksum")
		}
		out[n] = line[:64]
	}
	return out, sc.Err()
}
func proxyPrefix(p string) (string, *url.URL, error) {
	if p == "" {
		return "", nil, nil
	}
	u, e := url.Parse(p)
	if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(p, "\r\n\t ") {
		return "", nil, errors.New("invalid asset proxy prefix")
	}
	return strings.TrimRight(p, "/") + "/", u, nil
}
func assetClient(base *http.Client, proxy *url.URL) *http.Client {
	c := *base
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		host := req.URL.Hostname()
		github := req.URL.Scheme == "https" && req.URL.Port() == "" && (host == "github.com" || host == "release-assets.githubusercontent.com" || host == "objects.githubusercontent.com")
		sameProxy := proxy != nil && req.URL.Scheme == proxy.Scheme && req.URL.Host == proxy.Host
		if len(via) >= 5 || req.URL.User != nil || (!github && !sameProxy) {
			return errors.New("untrusted asset redirect")
		}
		return nil
	}
	return &c
}
func stageRelease(ctx context.Context, o Options, dir, arch string) (Status, error) {
	var s Status
	l := limits(o.Limits)
	prefix, proxy, e := proxyPrefix(o.ProxyPrefix)
	if e != nil {
		return s, e
	}
	if _, e := required(arch); e != nil {
		return s, e
	}
	c := o.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Minute}
	}
	// Metadata never follows redirects or the asset proxy. The production transport
	// does not consult HTTP_PROXY/HTTPS_PROXY; only tests may inject a Transport.
	api := *c
	api.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("release API redirect forbidden") }
	if api.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		api.Transport = transport
		defer transport.CloseIdleConnections()
	}
	resp, e := get(ctx, &api, latestURL)
	if e != nil {
		return s, e
	}
	b, e := readSmall(resp.Body, 2<<20)
	resp.Body.Close()
	if e != nil {
		return s, e
	}
	var r release
	if e = json.Unmarshal(b, &r); e != nil {
		return s, e
	}
	assets, names, e := metadata(r, arch, o.Tag, l)
	if e != nil {
		return s, e
	}
	if o.CurrentVersion != "" {
		if e = increasingVersion(r.Tag, o.CurrentVersion); e != nil {
			return s, e
		}
	}
	s.Tag = r.Tag
	s.Commit = r.Commit
	c = assetClient(c, proxy)
	for name, a := range assets {
		a.URL = prefix + a.URL
		assets[name] = a
	}
	downloads := filepath.Join(dir, "downloads")
	if e = os.Mkdir(downloads, 0700); e != nil {
		return s, e
	}
	sumsPath := filepath.Join(downloads, "SHA256SUMS")
	if _, e = download(ctx, c, assets["SHA256SUMS"], sumsPath, l); e != nil {
		return s, e
	}
	sums, e := checksumList(sumsPath)
	if e != nil {
		return s, e
	}
	// Complete checksum/API consistency check precedes every payload download.
	for _, n := range names {
		if n == "SHA256SUMS" {
			continue
		}
		if sums[n] == "" || assets[n].Digest != "sha256:"+sums[n] {
			return s, fmt.Errorf("checksum/API disagreement: %s", n)
		}
	}
	for _, n := range names {
		if n == "SHA256SUMS" {
			continue
		}
		d, e := download(ctx, c, assets[n], filepath.Join(downloads, n), l)
		if e != nil {
			return s, e
		}
		if d != sums[n] {
			return s, errors.New("checksum mismatch")
		}
	}
	b, e = os.ReadFile(filepath.Join(downloads, "BUILD-INFO.json"))
	if e != nil {
		return s, e
	}
	var info struct {
		Tag    string `json:"tag"`
		Commit string `json:"commit"`
	}
	if e = json.Unmarshal(b, &info); e != nil {
		return s, e
	}
	if info.Tag != r.Tag || info.Commit != r.Commit {
		return s, errors.New("BUILD-INFO identity mismatch")
	}
	count := 0
	var expanded int64
	if e = unzip(filepath.Join(downloads, "frontend-dist.zip"), filepath.Join(dir, "public"), false, l, &count, &expanded); e != nil {
		return s, e
	}
	if e = unzip(filepath.Join(downloads, "panel-runtime-scripts.zip"), filepath.Join(dir, "scripts"), true, l, &count, &expanded); e != nil {
		return s, e
	}
	for _, p := range []string{filepath.Join(dir, "public", "index.html"), filepath.Join(dir, "scripts", "install.sh")} {
		st, e := os.Stat(p)
		if e != nil || !st.Mode().IsRegular() {
			return s, fmt.Errorf("missing required staged file %s", p)
		}
	}
	if st, e := os.Stat(filepath.Join(dir, "scripts", "easytier")); e != nil || !st.IsDir() {
		return s, errors.New("missing easytier scripts")
	}
	agentDir := filepath.Join(dir, "public", "flux-agent")
	if e = os.Mkdir(agentDir, 0755); e != nil {
		return s, e
	}
	for _, n := range names {
		if strings.HasPrefix(n, "flux-agent") {
			if e = os.Rename(filepath.Join(downloads, n), filepath.Join(agentDir, n)); e != nil {
				return s, e
			}
			if e = os.Chmod(filepath.Join(agentDir, n), 0755); e != nil {
				return s, e
			}
		}
	}
	server := "network-panel-server-linux-" + arch
	s.ServerSHA256 = sums[server]
	if e = os.Rename(filepath.Join(downloads, server), filepath.Join(dir, "server")); e != nil {
		return s, e
	}
	if e = os.Chmod(filepath.Join(dir, "server"), 0755); e != nil {
		return s, e
	}
	return s, nil
}
func unzip(src, dest string, scripts bool, l Limits, count *int, expanded *int64) error {
	z, e := zip.OpenReader(src)
	if e != nil {
		return e
	}
	defer z.Close()
	if e = os.Mkdir(dest, 0755); e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, f := range z.File {
		n := strings.TrimSuffix(f.Name, "/")
		*count++
		if *count > l.ZipEntries || n == "" || n == "." || path.Clean(n) != n || strings.HasPrefix(n, "/") || strings.ContainsAny(n, "\\\x00:") || n == ".." || strings.HasPrefix(n, "../") || seen[n] {
			return errors.New("unsafe or duplicate zip path")
		}
		seen[n] = true
		m := f.Mode()
		if !m.IsRegular() && !m.IsDir() {
			return errors.New("zip special file")
		}
		if scripts {
			if n != "install.sh" && n != "easytier" && !strings.HasPrefix(n, "easytier/") {
				return errors.New("unexpected runtime script path")
			}
		} else {
			first := strings.SplitN(n, "/", 2)[0]
			if first == "flux-agent" || first == "server" || first == ".env" || first == "certs" {
				return errors.New("reserved public path")
			}
		}
		if f.UncompressedSize64 > uint64(l.ZipBytes-*expanded) {
			return errors.New("zip expanded limit")
		}
		p := filepath.Join(dest, filepath.FromSlash(n))
		if m.IsDir() {
			if e = os.MkdirAll(p, 0755); e != nil {
				return e
			}
			continue
		}
		if e = os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			return e
		}
		r, e := f.Open()
		if e != nil {
			return e
		}
		mode := os.FileMode(0644)
		if scripts {
			mode = 0755
		}
		out, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if e != nil {
			r.Close()
			return e
		}
		nbytes, copyErr := io.Copy(out, io.LimitReader(r, l.ZipBytes-*expanded+1))
		*expanded += nbytes
		if copyErr == nil && *expanded > l.ZipBytes {
			copyErr = errors.New("zip expanded limit")
		}
		if copyErr == nil {
			copyErr = out.Sync()
		}
		e = errors.Join(copyErr, r.Close(), out.Close())
		if e != nil {
			return e
		}
	}
	return nil
}
