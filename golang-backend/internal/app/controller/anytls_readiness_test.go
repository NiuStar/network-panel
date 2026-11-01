package controller

import (
	"fmt"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"network-panel/golang-backend/internal/anytlsreadiness"
	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

// Reproduce the recurrence using the real log ingestion seam, then remove
// diagnostic history just as retention does. No boot may be reconstructed.
func TestAnyTLSPinRejectsRevisionDatabaseErrors(t *testing.T) {
	old := dbpkg.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = old })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}, &model.AnyTLSRuntimeState{}, &model.NodeAnyTLSCertLog{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	bundle, err := issueAnyTLSNodeCert(20, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	pin := anyTLSCertPinFromPEM(bundle.CertPEM)
	now := time.Now().UnixMilli()
	for _, rec := range []model.NodeAnyTLSCertLog{
		{Step: "agent_boot", TimeMs: now - 1, Data: `{"processId":"p"}`},
		{Step: "installed_check", TimeMs: now, Data: fmt.Sprintf(`{"processId":"p","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin)},
	} {
		if err := anytlsreadiness.Persist(db, 20, rec); err != nil {
			t.Fatal(err)
		}
	}
	if got := anyTLSNodeCertPin(20, bundle.Domain); got != pin {
		t.Fatalf("initial fixture not ready: %q", got)
	}
	// CA and leaf remain readable; only the revision source fails. Returning
	// revision zero on a DB failure would silently accept the existing leaf.
	if err := db.Exec("DROP TABLE vite_config").Error; err != nil {
		t.Fatal(err)
	}
	if got := anyTLSNodeCertPin(20, bundle.Domain); got != "" {
		t.Fatal("revision read failure published pin")
	}
	if got := anyTLSCAForPin(20, bundle.Domain, pin); got != "" {
		t.Fatal("revision read failure published CA")
	}
}

func TestAnyTLSRuntimePinSurvivesAuditPruning(t *testing.T) {
	old := dbpkg.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dbpkg.DB = db
	t.Cleanup(func() { dbpkg.DB = old })
	if err := db.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}, &model.AnyTLSRuntimeState{}, &model.NodeAnyTLSCertLog{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	bundle, err := issueAnyTLSNodeCert(20, "node.example.com")
	if err != nil {
		t.Fatal(err)
	}
	pin := anyTLSCertPinFromPEM(bundle.CertPEM)
	now := time.Now().UnixMilli()
	for _, rec := range []model.NodeAnyTLSCertLog{
		{NodeID: 20, TimeMs: now - 1, Step: "agent_boot", Data: `{"processId":"current"}`},
		{NodeID: 20, TimeMs: now, Step: "installed_check", Data: fmt.Sprintf(`{"processId":"current","context":"post_reload","matchLatest":true,"installed":{"fingerprintSha256":%q}}`, pin)},
	} {
		if err := anytlsreadiness.Persist(db, 20, rec); err != nil {
			t.Fatal(err)
		}
	}
	if got := anyTLSNodeCertPin(20, bundle.Domain); got != pin {
		t.Fatalf("initial pin=%q", got)
	}
	if err := db.Where("node_id = ? AND step = ?", 20, "agent_boot").Delete(&model.NodeAnyTLSCertLog{}).Error; err != nil {
		t.Fatal(err)
	}
	if got := anyTLSNodeCertPin(20, bundle.Domain); got != pin {
		t.Fatalf("fresh same-process pin lost after audit pruning: %q", got)
	}
}
