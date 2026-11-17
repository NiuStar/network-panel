package controller

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

func nodeListBoundsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(1)
	old := dbpkg.DB
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = old; pool.Close() })
	if err := db.AutoMigrate(&model.Node{}, &model.NodeRuntime{}, &model.AnyTLSSetting{}, &model.AnyTLSPortEgress{}, &model.NodeOpLog{}, &model.NodeAnyTLSCertLog{}, &model.ViteConfig{}, &model.UserNode{}, &model.Forward{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestNodeListLatestBindBoundedFullRows(t *testing.T) {
	db := nodeListBoundsDB(t)
	raw := `{"loopbackOnly":true}`
	logs := []model.NodeOpLog{
		{ID: 1, NodeID: 1, TimeMs: 100, Cmd: "OpLog:gost_api", Message: "bind verify", Stdout: &raw},
		{ID: 2, NodeID: 1, TimeMs: 100, Cmd: "OpLog:gost_api", Message: "bind verify retry", RequestID: "full-row", Success: 1, Stderr: &raw},
		{ID: 3, NodeID: 1, TimeMs: 101, Cmd: "OpLog:gost_api", Message: "unrelated", Stdout: &raw},
		{ID: 4, NodeID: 2, TimeMs: 90, Cmd: "OpLog:gost_api", Message: "bind verify", Stdout: &raw},
		{ID: 5, NodeID: 2, TimeMs: 80, Cmd: "OpLog:gost_api", Message: "bind verify", Stdout: &raw},
		{ID: 6, NodeID: 3, TimeMs: 200, Cmd: "OpLog:gost_api_err", Message: "bind verify failed, try normalize services"},
	}
	for _, id := range []int64{1, 2, 3, 4} {
		if err := db.Create(&model.Node{BaseEntity: model.BaseEntity{ID: id}}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	got, err := latestNodeBindLogs([]int64{1, 2, 3, 4}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.NodeOpLog{logs[1], logs[3]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("latest full rows: got %#v want %#v", got, want)
	}
	got, err = latestNodeBindLogs([]int64{3, 4}, true)
	if err != nil || !reflect.DeepEqual(got, logs[5:]) {
		t.Fatalf("fallback rows=%#v err=%v", got, err)
	}
	got, err = latestNodeBindLogs(nil, false)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty IDs: %#v %v", got, err)
	}
}

func TestNodeListJSONLatestInvalidRuntimeAndVisibility(t *testing.T) {
	db := nodeListBoundsDB(t)
	now := time.Now().UnixMilli()
	raw := `{"loopbackOnly":true,"detail":"old"}`
	invalid := "not JSON"
	mismatch := `{"sniMismatch":"yes"}`
	reset := `{"kind":"conn_reset_by_peer"}`
	owner := int64(7)
	for _, id := range []int64{1, 2, 3, 4} {
		n := model.Node{BaseEntity: model.BaseEntity{ID: id}, Name: "test", OwnerID: &owner, Secret: "DO_NOT_LEAK"}
		if err := db.Create(&n).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := []model.NodeOpLog{
		{NodeID: 1, TimeMs: 1, Cmd: "OpLog:gost_api", Message: "bind verify", Stdout: &raw},
		{NodeID: 1, TimeMs: 2, Cmd: "OpLog:gost_api", Message: "bind verify retry", Stdout: &invalid},
		{NodeID: 1, TimeMs: 3, Cmd: "OpLog:gost_api_err", Message: "bind verify failed, try normalize services"},
		{NodeID: 2, TimeMs: 4, Cmd: "OpLog:gost_api_err", Message: "bind verify failed, try normalize services"},
		{NodeID: 3, TimeMs: 5, Cmd: "OpLog:gost_api", Message: "bind verify", Stdout: &raw},
		{NodeID: 3, TimeMs: 5, Cmd: "OpLog:gost_api", Message: "bind verify"},
		{NodeID: 1, TimeMs: now, Cmd: "OpLog:anytls_start"},
		{NodeID: 1, TimeMs: now, Cmd: "OpLog:anytls_tls_client_hello", Stdout: &mismatch},
		{NodeID: 1, TimeMs: now, Cmd: "OpLog:anytls_tls_handshake_err", Stdout: &reset},
		{NodeID: 1, TimeMs: now - 1000000, Cmd: "OpLog:anytls_start"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	// Counts describe the entire time window, not an arbitrary recent-row cap.
	starts := make([]model.NodeOpLog, 1200)
	for i := range starts {
		starts[i] = model.NodeOpLog{NodeID: 1, TimeMs: now, Cmd: "OpLog:anytls_start"}
	}
	if err := db.CreateInBatches(&starts, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AnyTLSSetting{NodeID: 1, Port: 443}).Error; err != nil {
		t.Fatal(err)
	}
	// Disable certificate UI estimates so the entire public projection is stable.
	t.Setenv("ANYTLS_CERT_ENABLED", "false")
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", nil)
	NodeList(c)
	var got struct {
		Code int              `json:"code"`
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != 0 || len(got.Data) != 4 {
		t.Fatalf("response %s", w.Body.String())
	}
	for i, m := range got.Data {
		want := map[string]any{"id": float64(i + 1), "name": "test", "ip": "", "serverIp": "", "portSta": float64(0), "portEnd": float64(0), "version": "", "status": nil, "priceCents": nil, "startDateMs": nil, "shared": false, "assignedPortRanges": "", "gostApi": float64(0), "gostRunning": float64(0)}
		switch i {
		case 0:
			want["gostApiBindCheckedAtMs"] = float64(2)
			want["gostApiBindDetail"] = "bind verify retry"
			want["anytlsPort"] = float64(443)
			want["anytlsRuntime"] = map[string]any{"state": "degraded", "windowSec": float64(900), "recentCount": float64(1203), "starts": float64(1201), "acceptErr": float64(0), "connReject": float64(0), "handshakeTimeout": float64(0), "tlsClientHello": float64(1), "tlsHandshakeErr": float64(1), "tlsConnResetByPeer": float64(1), "tlsSniMismatch": float64(1), "listenErr": float64(0), "streamErr": float64(0), "authFail": float64(0), "readErr": float64(0), "outboundErr": float64(0), "egressDialErr": float64(0), "lastLogMs": float64(now)}
		case 1:
			want["gostApiBindCheckedAtMs"] = float64(4)
			want["gostApiBindDetail"] = "bind verify failed, try normalize services"
			want["gostApiBindLoopbackOnly"] = false
		case 2:
			want["gostApiBindCheckedAtMs"] = float64(5)
			want["gostApiBindDetail"] = "bind verify"
		}
		if !reflect.DeepEqual(m, want) {
			t.Fatalf("node %d full projection got=%#v want=%#v", i+1, m, want)
		}
	}
	// Non-owner sees no nodes and no diagnostic data.
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", nil)
	c.Set("role_id", 1)
	c.Set("user_id", int64(8))
	NodeList(c)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 0 {
		t.Fatalf("non-owner visibility leaked: %s", w.Body.String())
	}
}
