package controller

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"network-panel/golang-backend/internal/app/model"
	dbpkg "network-panel/golang-backend/internal/db"
)

func TestSharedAnyTLSLeafRenewalPreservesOtherControllerWinner(t *testing.T) {
	// Separate connections to an actual SQLite file model separate controllers.
	// Explicit scheduling avoids the process-local issuance mutex hiding the race.
	dsn := filepath.Join(t.TempDir(), "shared-leaf.db")
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	a, b := open(), open()
	oldDB := dbpkg.DB
	t.Cleanup(func() { dbpkg.DB = oldDB })
	dbpkg.DB = a
	if err := a.AutoMigrate(&model.ViteConfig{}, &model.AnyTLSCAAuthority{}, &model.AnyTLSNodeLeaf{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(anyTLSCertDirEnv, t.TempDir())
	const nodeID int64 = 7302
	const domain = "renewal.example.com"
	initial, err := issueAnyTLSNodeCert(nodeID, domain)
	if err != nil {
		t.Fatal(err)
	}
	ca, caKey, caPEM, err := ensureAnyTLSCA()
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(initial.CertPEM), []byte(initial.KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	key := anyTLSLeafDBKey(nodeID, domain, 0, ca)
	makeRow := func(serial int64, lifetime time.Duration) model.AnyTLSNodeLeaf {
		template := *leaf
		template.SerialNumber = big.NewInt(serial)
		template.NotAfter = time.Now().Add(lifetime)
		der, err := x509.CreateCertificate(rand.Reader, &template, ca, leaf.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return model.AnyTLSNodeLeaf{Key: key, CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), KeyPEM: initial.KeyPEM}
	}
	stale := makeRow(730201, 24*time.Hour)
	candidateA := makeRow(730202, anyTLSLeafValidity)
	if _, ok := selectSharedAnyTLSLeaf(candidateA, nodeID, domain, 0, ca, caPEM); !ok {
		t.Fatal("candidate A must be valid")
	}
	setRow := func(db *gorm.DB, row model.AnyTLSNodeLeaf) {
		if err := db.Model(&model.AnyTLSNodeLeaf{}).Where("leaf_key = ?", key).Updates(map[string]interface{}{"cert_pem": row.CertPEM, "key_pem": row.KeyPEM}).Error; err != nil {
			t.Fatal(err)
		}
	}
	assertStored := func(winner *anyTLSNodeCertBundle) {
		t.Helper()
		for _, db := range []*gorm.DB{a, b} {
			var stored model.AnyTLSNodeLeaf
			if err := db.Where("leaf_key = ?", key).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.CertPEM != winner.CertPEM || stored.KeyPEM != winner.KeyPEM {
				t.Fatal("persisted certificate/key differ from returned shared winner")
			}
		}
	}

	setRow(a, stale)
	// Controller A pauses after its initial invalid-cache observation.
	if _, ok := readAnyTLSNodeCertDB(key, nodeID, domain, 0, ca, caPEM); ok {
		t.Fatal("near-expiry leaf unexpectedly accepted")
	}
	// Controller B completes real public issuance, replacing the invalid row.
	dbpkg.DB = b
	winnerB, err := issueAnyTLSNodeCert(nodeID, domain)
	if err != nil {
		t.Fatal(err)
	}
	if winnerB.CertPEM == stale.CertPEM || winnerB.CertPEM == candidateA.CertPEM {
		t.Fatal("controller B did not publish a distinct fresh leaf")
	}
	assertStored(winnerB)
	// Resume A at the exact production publication boundary, without a mutex.
	dbpkg.DB = a
	winnerA, err := publishSharedAnyTLSLeaf(candidateA, nodeID, domain, 0, ca, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	if *winnerA != *winnerB {
		t.Fatal("late contender overwrote or returned a different winner")
	}
	assertStored(winnerB)

	// Also force a lost CAS: B writes after A reads invalid prior but before
	// A's UPDATE. This callback belongs only to handle A, not a global hook.
	setRow(a, stale)
	var updateRows int64 = -1
	if err := a.Callback().Update().Before("gorm:update").Register("test:other_controller_wins", func(tx *gorm.DB) {
		setRow(b, model.AnyTLSNodeLeaf{Key: key, CertPEM: winnerB.CertPEM, KeyPEM: winnerB.KeyPEM})
	}); err != nil {
		t.Fatal(err)
	}
	defer a.Callback().Update().Remove("test:other_controller_wins")
	if err := a.Callback().Update().After("gorm:update").Register("test:observe_lost_cas", func(tx *gorm.DB) { updateRows = tx.RowsAffected }); err != nil {
		t.Fatal(err)
	}
	defer a.Callback().Update().Remove("test:observe_lost_cas")
	winnerA, err = publishSharedAnyTLSLeaf(candidateA, nodeID, domain, 0, ca, caPEM)
	if err != nil {
		t.Fatal(err)
	}
	if updateRows != 0 || *winnerA != *winnerB {
		t.Fatalf("lost CAS did not read back winner: rows=%d", updateRows)
	}
	assertStored(winnerB)
}
