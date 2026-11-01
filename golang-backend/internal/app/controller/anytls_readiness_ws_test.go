package controller

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	sqlite "github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"network-panel/golang-backend/internal/anytlsreadiness"
	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

func TestAnyTLSReadinessIngestUsesAuthenticatedWebSocketNode(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	defer sql.Close()
	old := dbpkg.DB
	dbpkg.DB = db
	defer func() { dbpkg.DB = old }()
	if err := db.AutoMigrate(&model.Node{}, &model.NodeDisconnectLog{}, &model.ViteConfig{}, &model.NodeAnyTLSCertLog{}, &model.AnyTLSRuntimeState{}); err != nil {
		t.Fatal(err)
	}
	node := model.Node{BaseEntity: model.BaseEntity{ID: 123}, Name: "ws-test", Secret: "fixture-only-ws-secret"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_AUTO_UPGRADE", "false")
	router := gin.New()
	router.GET("/ws", SystemInfoWS)
	server := httptest.NewServer(router)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?type=1&secret=" + node.Secret
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, step := range []string{"agent_boot", "installed_check"} {
		payload := map[string]any{"type": "AnyTLSCertLog", "nodeId": 999, "timeMs": int64(1), "step": step, "data": map[string]any{"processId": "auth-process", "nodeId": 999, "context": "post_reload", "matchLatest": true, "password": "MUST_NOT_RETAIN", "installed": map[string]any{"fingerprintSha256": strings.Repeat("ab", 32)}}}
		if err := conn.WriteJSON(payload); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		state, err := anytlsreadiness.Load(db, node.ID)
		if err == nil && anytlsreadiness.Ready(state, strings.Repeat("ab", 32), time.Now()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("authenticated WS did not retain runtime evidence: %v %+v", err, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var logs []model.NodeAnyTLSCertLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 {
		t.Fatalf("audit count=%d", len(logs))
	}
	for _, rec := range logs {
		if rec.NodeID != node.ID || rec.TimeMs <= 1 {
			t.Fatal("payload identity/time escaped authentication boundary")
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(rec.Data), &data); err != nil {
			t.Fatal(err)
		}
		if _, ok := data["password"]; ok {
			t.Fatal("audit retained secret")
		}
	}
	// Rejected credentials never reach the persistence seam.
	bad, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?type=1&secret=wrong", nil)
	if err == nil {
		defer bad.Close()
		_ = bad.WriteJSON(map[string]any{"type": "AnyTLSCertLog", "step": "agent_boot", "nodeId": 999})
		_ = bad.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, err = bad.ReadMessage(); err == nil {
			t.Fatal("invalid agent channel remained open")
		}
	}
	var count int64
	db.Model(&model.AnyTLSRuntimeState{}).Where("node_id = ?", 999).Count(&count)
	if count != 0 {
		t.Fatal("unauthenticated/payload identity created runtime state")
	}
	conn.Close()
	// Wait for the real disconnect cleanup before restoring the global test DB.
	for IsNodeWSOnline(node.ID) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if IsNodeWSOnline(node.ID) {
		t.Fatal("websocket connection was not removed")
	}
}
