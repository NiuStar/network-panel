package upgrade

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestProxyUsesDirectAPIAuthority(t *testing.T) {
	base, _, _, _ := fixture(t)
	var urls []string
	c := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		urls = append(urls, r.URL.String())
		return base.Transport.RoundTrip(r)
	})}
	if _, e := stageRelease(context.Background(), Options{HTTPClient: c, ProxyPrefix: "https://proxy.example/"}, t.TempDir(), "amd64"); e != nil {
		t.Fatal(e)
	}
	if len(urls) < 2 || urls[0] != latestURL {
		t.Fatalf("API not direct %v", urls)
	}
	for _, u := range urls[1:] {
		if !strings.HasPrefix(u, "https://proxy.example/https://github.com/") {
			t.Fatalf("asset not proxied %s", u)
		}
	}
}
func TestProxyPrefixValidation(t *testing.T) {
	for _, p := range []string{"https://user:pass@proxy/", "ftp://proxy/", "https://proxy/?token=secret", "https://proxy/#x", "https://proxy/ bad"} {
		if _, e := stageRelease(context.Background(), Options{ProxyPrefix: p}, t.TempDir(), "amd64"); e == nil {
			t.Fatalf("accepted %s", p)
		}
	}
}
