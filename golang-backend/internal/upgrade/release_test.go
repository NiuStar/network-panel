package upgrade

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func zipData(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for n, v := range files {
		f, e := w.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		f.Write([]byte(v))
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func fixture(t *testing.T) (*http.Client, map[string][]byte, *release, *int) {
	t.Helper()
	data := map[string][]byte{
		"frontend-dist.zip":                zipData(t, map[string]string{"index.html": "new-ui"}),
		"panel-runtime-scripts.zip":        zipData(t, map[string]string{"install.sh": "new-install", "easytier/run.sh": "new-script"}),
		"network-panel-server-linux-amd64": []byte("not ELF yet"),
		"BUILD-INFO.json":                  []byte(`{"tag":"v2.0.0.31","commit":"1111111111111111111111111111111111111111"}`),
	}
	for _, a := range []string{"amd64", "arm64", "armv7"} {
		for _, n := range []string{"flux-agent", "flux-agent2"} {
			data[n+"-linux-"+a] = []byte(n + a)
		}
	}
	rel := &release{Tag: "v2.0.0.31", Commit: strings.Repeat("1", 40)}
	calls := new(int)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var b []byte
		if req.URL.String() == latestURL {
			*calls++
			refreshRelease(data, rel)
			b, _ = json.Marshal(rel)
		} else {
			b = data[filepath.Base(req.URL.Path)]
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header), Request: req}, nil
	})}
	return client, data, rel, calls
}
func refreshRelease(data map[string][]byte, r *release) {
	var sums strings.Builder
	for n, b := range data {
		if n != "SHA256SUMS" {
			sums.WriteString(sum(b) + "  " + n + "\n")
		}
	}
	data["SHA256SUMS"] = []byte(sums.String())
	r.Assets = nil
	for n, b := range data {
		r.Assets = append(r.Assets, asset{Name: n, URL: downloadBase + r.Tag + "/" + n, Digest: "sha256:" + sum(b), Size: int64(len(b))})
	}
}
func TestStageReleaseAndMetadataGate(t *testing.T) {
	client, _, _, calls := fixture(t)
	dir := t.TempDir()
	s, e := stageRelease(context.Background(), Options{HTTPClient: client}, dir, "amd64")
	if e != nil {
		t.Fatal(e)
	}
	if s.Tag != "v2.0.0.31" || *calls != 1 {
		t.Fatalf("%+v calls %d", s, *calls)
	}
	b, e := os.ReadFile(filepath.Join(dir, "public", "flux-agent", "flux-agent2-linux-armv7"))
	if e != nil || string(b) != "flux-agent2armv7" {
		t.Fatalf("agent: %q %v", b, e)
	}
}
func TestStageRejectsUnsafeZip(t *testing.T) {
	for _, name := range []string{"../escape", "/escape", "a/../escape", "a\\evil", "flux-agent/evil"} {
		t.Run(name, func(t *testing.T) {
			client, data, _, _ := fixture(t)
			data["frontend-dist.zip"] = zipData(t, map[string]string{name: "evil", "index.html": "ui"})
			if _, e := stageRelease(context.Background(), Options{HTTPClient: client}, t.TempDir(), "amd64"); e == nil {
				t.Fatal("accepted unsafe frontend")
			}
		})
	}
}
