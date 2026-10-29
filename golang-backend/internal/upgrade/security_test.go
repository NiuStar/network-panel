package upgrade

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixedClient(t *testing.T, r *release, data map[string][]byte, requests *[]string) *http.Client {
	t.Helper()
	refreshRelease(data, r)
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		*requests = append(*requests, req.URL.String())
		var b []byte
		if req.URL.String() == latestURL {
			b, _ = json.Marshal(r)
		} else {
			b = data[filepath.Base(req.URL.Path)]
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Request: req, Header: make(http.Header)}, nil
	})}
}
func TestMetadataRequiredSetValidatedBeforeDownloads(t *testing.T) {
	for _, fault := range []string{"missing", "duplicate", "digest", "url", "size", "total", "tag", "commit", "draft", "prerelease", "arch", "expected-tag"} {
		t.Run(fault, func(t *testing.T) {
			_, data, r, _ := fixture(t)
			var requests []string
			c := fixedClient(t, r, data, &requests)
			o := Options{HTTPClient: c}
			arch := "amd64"
			switch fault {
			case "missing":
				r.Assets = r.Assets[1:]
			case "duplicate":
				r.Assets = append(r.Assets, r.Assets[0])
			case "digest":
				r.Assets[0].Digest = ""
			case "url":
				r.Assets[0].URL = "https://evil.invalid/file"
			case "size":
				r.Assets[0].Size = 1 << 40
			case "total":
				o.Limits.TotalBytes = 1
			case "tag":
				r.Tag = "v2.0.0/evil"
			case "commit":
				r.Commit = "main"
			case "draft":
				r.Draft = true
			case "prerelease":
				r.Prerelease = true
			case "arch":
				arch = "loong64"
			case "expected-tag":
				o.Tag = "v2.0.0.30"
			}
			if _, e := stageRelease(context.Background(), o, t.TempDir(), arch); e == nil {
				t.Fatal("bad metadata accepted")
			}
			for _, req := range requests {
				if req != latestURL {
					t.Fatalf("download before metadata validation %s", req)
				}
			}
		})
	}
}
func TestDigestChecksHaveIndependentAuthority(t *testing.T) {
	for _, fault := range []string{"sums-api-digest", "payload-api-digest", "sums-api-disagreement", "build-identity", "payload-size"} {
		t.Run(fault, func(t *testing.T) {
			_, data, r, _ := fixture(t)
			var requests []string
			c := fixedClient(t, r, data, &requests)
			switch fault {
			case "sums-api-digest":
				data["SHA256SUMS"] = []byte(strings.Repeat("0", 64) + "  install.sh\n")
			case "payload-api-digest":
				b := data["BUILD-INFO.json"]
				data["BUILD-INFO.json"] = bytes.Repeat([]byte("x"), len(b))
			case "payload-size":
				data["BUILD-INFO.json"] = append(data["BUILD-INFO.json"], ' ')
			case "sums-api-disagreement":
				b := bytes.Replace(data["SHA256SUMS"], []byte(sum(data["frontend-dist.zip"])), []byte(strings.Repeat("0", 64)), 1)
				data["SHA256SUMS"] = b
				for i := range r.Assets {
					if r.Assets[i].Name == "SHA256SUMS" {
						r.Assets[i].Digest = "sha256:" + sum(b)
					}
				}
			case "build-identity":
				data["BUILD-INFO.json"] = []byte(`{"tag":"v2.0.0.29","commit":"` + strings.Repeat("1", 40) + `"}`)
				refreshRelease(data, r)
			}
			if _, e := stageRelease(context.Background(), Options{HTTPClient: c}, t.TempDir(), "amd64"); e == nil {
				t.Fatal("tamper accepted")
			}
		})
	}
}
func makeZipEntries(t *testing.T, headers []zip.FileHeader) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, h := range headers {
		f, e := w.CreateHeader(&h)
		if e != nil {
			t.Fatal(e)
		}
		f.Write([]byte("payload"))
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestZipSpecialDuplicateAndResourceLimits(t *testing.T) {
	for _, fault := range []string{"duplicate", "symlink", "fifo", "device", "entries", "bytes", "scripts-extra", "prefix-conflict"} {
		t.Run(fault, func(t *testing.T) {
			h := zip.FileHeader{Name: "index.html", Method: zip.Store}
			headers := []zip.FileHeader{h}
			l := limits(Limits{})
			scripts := false
			switch fault {
			case "duplicate":
				headers = append(headers, h)
			case "symlink":
				headers[0].SetMode(os.ModeSymlink | 0777)
			case "fifo":
				headers[0].SetMode(os.ModeNamedPipe | 0600)
			case "device":
				headers[0].SetMode(os.ModeDevice | 0600)
			case "entries":
				headers = append(headers, zip.FileHeader{Name: "other"})
				l.ZipEntries = 1
			case "bytes":
				l.ZipBytes = 1
			case "scripts-extra":
				scripts = true
			case "prefix-conflict":
				headers = append(headers, zip.FileHeader{Name: "index.html/child"})
			}
			src := filepath.Join(t.TempDir(), "data.zip")
			os.WriteFile(src, makeZipEntries(t, headers), 0600)
			count := 0
			var expanded int64
			if e := unzip(src, filepath.Join(t.TempDir(), "extracted"), scripts, l, &count, &expanded); e == nil {
				t.Fatal("unsafe zip accepted")
			}
		})
	}
}
func TestReleaseRedirectRejected(t *testing.T) {
	c := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.invalid/payload"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})}
	if _, e := stageRelease(context.Background(), Options{HTTPClient: c}, t.TempDir(), "amd64"); e == nil {
		t.Fatal("redirect accepted")
	}
}
func TestAgentArchitecturesRequiredAndNoFallback(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64", "armv7"} {
		names, e := required(arch)
		if e != nil || len(names) != 11 {
			t.Fatalf("%s %v %v", arch, names, e)
		}
	}
	if _, e := required("riscv64"); e == nil {
		t.Fatal("unsupported arch accepted")
	}
}
