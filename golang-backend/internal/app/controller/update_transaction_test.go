package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type upgradeRejectTransport struct{ calls *int }

func (r upgradeRejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*r.calls++
	return nil, errors.New("test refuses outbound network")
}

func TestPanelUpgradeDockerRejectsBeforeNetworkAndNeverReturnsSuccess(t *testing.T) {
	if !isDocker() {
		t.Skip("requires container detection for production Docker boundary")
	}
	calls := 0
	old := http.DefaultTransport
	http.DefaultTransport = upgradeRejectTransport{&calls}
	defer func() { http.DefaultTransport = old }()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/upgrade", VersionUpgrade)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/upgrade", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "IMAGE_UPGRADE_REQUIRED") {
		t.Fatalf("must reject in-container mutation, status=%d body=%s", w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Fatalf("Docker rejection made %d network requests", calls)
	}
}

func TestPanelUpgradeStreamWithoutTransactionNeverStartsOrEmitsDone(t *testing.T) {
	calls := 0
	old := http.DefaultTransport
	http.DefaultTransport = upgradeRejectTransport{&calls}
	defer func() { http.DefaultTransport = old }()
	r := gin.New()
	r.GET("/stream", VersionUpgradeStream)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/stream", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing id must be bad request, got %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"event":"done"`) {
		t.Fatalf("invalid request emitted success: %s", w.Body.String())
	}
	if calls != 0 {
		t.Fatalf("read-only stream triggered %d downloads", calls)
	}
}
