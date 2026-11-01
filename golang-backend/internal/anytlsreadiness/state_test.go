package anytlsreadiness

import (
	"fmt"
	"strings"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"network-panel/golang-backend/internal/app/model"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { sql.Close() })
	if err := db.AutoMigrate(&model.NodeAnyTLSCertLog{}, &model.AnyTLSRuntimeState{}); err != nil {
		t.Fatal(err)
	}
	return db
}
func event(step, process string, at int64, match bool) model.NodeAnyTLSCertLog {
	return model.NodeAnyTLSCertLog{Step: step, TimeMs: at, Data: fmt.Sprintf(`{"processId":%q,"context":"post_reload","matchLatest":%t,"installed":{"fingerprintSha256":%q}}`, process, match, strings.Repeat("ab", 32))}
}
func TestFailedInvalidationBlocksReads(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	for _, e := range []model.NodeAnyTLSCertLog{event("agent_boot", "p", now.UnixMilli()-1, true), event("installed_check", "p", now.UnixMilli(), true)} {
		if err := Persist(db, 20, e); err != nil {
			t.Fatal(err)
		}
	}
	// Real database failure after the audit insert must roll back both writes.
	if err := db.Exec(`CREATE TRIGGER reject_runtime BEFORE UPDATE ON anytls_runtime_state BEGIN SELECT RAISE(ABORT, 'injected state write failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := Persist(db, 20, event("reload_err", "p", now.UnixMilli()+1, false)); err == nil {
		t.Fatal("failed state write accepted")
	}
	var count int64
	db.Model(&model.NodeAnyTLSCertLog{}).Count(&count)
	if count != 2 {
		t.Fatalf("partial audit write survived rollback: %d", count)
	}
	if _, err := Load(db, 20); err == nil {
		t.Fatal("failed invalidation left old ready state readable")
	}
}

func TestPersistSurvivesPruning(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	pin := strings.Repeat("ab", 32)
	for _, e := range []model.NodeAnyTLSCertLog{event("agent_boot", "p", now.UnixMilli()-1, true), event("installed_check", "p", now.UnixMilli(), true)} {
		e.NodeID = 999 // payload identity must never win over authenticated identity
		if err := Persist(db, 20, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Where("node_id = ?", 20).Delete(&model.NodeAnyTLSCertLog{}).Error; err != nil {
		t.Fatal(err)
	}
	state, err := Load(db, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !Ready(state, pin, now) {
		t.Fatalf("pruning lost readiness: %+v", state)
	}
	var count int64
	db.Model(&model.AnyTLSRuntimeState{}).Where("node_id = ?", 999).Count(&count)
	if count != 0 {
		t.Fatal("payload node identity was trusted")
	}
}
