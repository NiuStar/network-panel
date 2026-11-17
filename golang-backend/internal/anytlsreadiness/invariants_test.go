package anytlsreadiness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"network-panel/golang-backend/internal/app/model"
)

func TestRuntimeInvalidationOrdering(t *testing.T) {
	for _, tc := range []struct {
		name, step, process, data string
		match                     bool
		offset                    int64
		want                      bool
	}{
		{name: "new_failure", step: "reload_err", process: "p", offset: 1},
		{name: "old_failure", step: "reload_err", process: "p", offset: -2, want: true},
		{name: "new_boot", step: "agent_boot", process: "p2", offset: 1},
		{name: "malformed_boot", step: "agent_boot", data: `{"processId":`, offset: 1},
		{name: "empty_boot", step: "agent_boot", offset: 1},
		{name: "malformed_installed", step: "installed_check", data: `{"processId":`, offset: 1},
		{name: "new_mismatch", step: "installed_check", process: "p", offset: 1},
		{name: "wrong_process", step: "installed_check", process: "q", match: true, offset: 1},
		{name: "old_mismatch", step: "installed_check", process: "p", offset: -2, want: true},
		{name: "same_ms_later_id_mismatch", step: "installed_check", process: "p"},
		{name: "same_ms_later_id_failure", step: "reload_err", process: "p"},
		{name: "same_ms_later_id_boot", step: "agent_boot", process: "p"},
		{name: "later_confirmation", step: "installed_check", process: "p", match: true, offset: 1, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			now := time.Now()
			for _, e := range []model.NodeAnyTLSCertLog{event("agent_boot", "p", now.UnixMilli()-1, true), event("installed_check", "p", now.UnixMilli(), true)} {
				if err := Persist(db, 1, e); err != nil {
					t.Fatal(err)
				}
			}
			e := event(tc.step, tc.process, now.UnixMilli()+tc.offset, tc.match)
			if tc.data != "" {
				e.Data = tc.data
			}
			if err := Persist(db, 1, e); err != nil {
				t.Fatal(err)
			}
			state, err := Load(db, 1)
			if err != nil {
				t.Fatal(err)
			}
			if got := Ready(state, strings.Repeat("ab", 32), now); got != tc.want {
				t.Fatalf("ready=%t want=%t state=%+v", got, tc.want, state)
			}
		})
	}
}

func TestFreshnessAndPinBoundaries(t *testing.T) {
	db := testDB(t)
	now := time.Now().Truncate(time.Millisecond)
	pin := strings.Repeat("ab", 32)
	for _, e := range []model.NodeAnyTLSCertLog{event("agent_boot", "p", now.UnixMilli()-1, true), event("installed_check", "p", now.UnixMilli(), true)} {
		if err := Persist(db, 1, e); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		delta time.Duration
		want  bool
	}{
		{"fresh", 0, true}, {"fresh_at_two_minutes", 2 * time.Minute, true}, {"stale", 2*time.Minute + time.Millisecond, false},
		{"future_at_five_seconds", -5 * time.Second, true}, {"too_far_future", -5*time.Second - time.Millisecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Ready(s, pin, now.Add(tc.delta)); got != tc.want {
				t.Fatalf("ready=%t", got)
			}
		})
	}
	if Ready(s, strings.Repeat("cd", 32), now) {
		t.Fatal("wrong pin accepted")
	}
	if !Ready(s, strings.ToUpper(pin), now) {
		t.Fatal("uppercase exact pin rejected")
	}
}

func TestColdHistoryNeverInventsBoot(t *testing.T) {
	for _, tc := range []struct {
		name                string
		boot, installed     bool
		bootAt, installedAt int64
		process             string
		want                bool
	}{
		{name: "empty"}, {name: "installed_only", installed: true, installedAt: 2, process: "p"},
		{name: "complete", boot: true, installed: true, bootAt: 1, installedAt: 2, process: "p", want: true},
		{name: "wrong_process", boot: true, installed: true, bootAt: 1, installedAt: 2, process: "q"},
		{name: "boot_after_installed", boot: true, installed: true, bootAt: 3, installedAt: 2, process: "p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			now := time.Now()
			base := now.UnixMilli() - 10
			if tc.boot {
				e := event("agent_boot", "p", base+tc.bootAt, true)
				e.NodeID = 1
				if err := db.Create(&e).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.installed {
				e := event("installed_check", tc.process, base+tc.installedAt, true)
				e.NodeID = 1
				if err := db.Create(&e).Error; err != nil {
					t.Fatal(err)
				}
			}
			s, err := Load(db, 1)
			if err != nil {
				t.Fatal(err)
			}
			if Ready(s, strings.Repeat("ab", 32), now) != tc.want {
				t.Fatalf("cold import state=%+v", s)
			}
			// A newer live event must fence retained evidence forever, including after
			// direct insertion of a historical row with a much higher audit ID.
			if err := Persist(db, 1, event("agent_boot", "replacement", now.UnixMilli(), true)); err != nil {
				t.Fatal(err)
			}
			old := event("installed_check", "p", base+4, true)
			old.NodeID = 1
			if err := db.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
			s, err = Load(db, 1)
			if err != nil {
				t.Fatal(err)
			}
			if Ready(s, strings.Repeat("ab", 32), now) {
				t.Fatal("cold history resurrected old readiness")
			}
		})
	}
}

func TestRuntimeDatabaseFailuresAreClosed(t *testing.T) {
	for _, table := range []string{"anytls_runtime_state", "node_anytls_cert_log"} {
		t.Run(table, func(t *testing.T) {
			db := testDB(t)
			if err := db.Exec("DROP TABLE " + table).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := Load(db, 1); err == nil {
				t.Fatal("missing table ignored")
			}
			if err := Persist(db, 1, event("agent_boot", "p", time.Now().UnixMilli(), true)); err == nil {
				t.Fatal("missing table write ignored")
			}
		})
	}
	if _, err := Load(nil, 1); err == nil {
		t.Fatal("nil DB accepted")
	}
	if err := Persist(nil, 1, model.NodeAnyTLSCertLog{}); err == nil {
		t.Fatal("nil DB write accepted")
	}
	db := testDB(t)
	if err := db.Exec(`CREATE TRIGGER reject_audit BEFORE INSERT ON node_anytls_cert_log BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := Persist(db, 1, event("agent_boot", "p", 1, true)); err == nil {
		t.Fatal("audit failure ignored")
	}
	var count int64
	db.Model(&model.AnyTLSRuntimeState{}).Count(&count)
	if count != 0 {
		t.Fatal("state survived failed audit transaction")
	}
}

func TestAtomicStateFailureRollsBackAudit(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	if err := Persist(db, 1, event("agent_boot", "p", now.UnixMilli()-1, true)); err != nil {
		t.Fatal(err)
	}
	// Permit no-op row-lock UPDATE, reject final state UPDATE after audit insert.
	if err := db.Exec(`CREATE TRIGGER reject_confirmation BEFORE UPDATE ON anytls_runtime_state WHEN NEW.installed_id > 0 BEGIN SELECT RAISE(ABORT, 'injected final write failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := Persist(db, 1, event("installed_check", "p", now.UnixMilli(), true)); err == nil {
		t.Fatal("state failure ignored")
	}
	var count int64
	db.Model(&model.NodeAnyTLSCertLog{}).Count(&count)
	if count != 1 {
		t.Fatalf("audit append was not rolled back: %d", count)
	}
	if _, err := Load(db, 1); err == nil {
		t.Fatal("failed write did not block local reads")
	}
}

func TestQuarantineRequiresNewBoot(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	if err := Persist(db, 1, event("agent_boot", "p", now.UnixMilli()-2, true)); err != nil {
		t.Fatal(err)
	}
	if err := Persist(db, 1, event("installed_check", "p", now.UnixMilli()-1, true)); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON node_anytls_cert_log BEGIN SELECT RAISE(ABORT, 'injected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := Persist(db, 1, event("reload_err", "p", now.UnixMilli(), false)); err == nil {
		t.Fatal("expected error")
	}
	if err := db.Exec(`DROP TRIGGER fail_insert`).Error; err != nil {
		t.Fatal(err)
	}
	if err := Persist(db, 1, event("installed_check", "p", now.UnixMilli()+1, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(db, 1); err == nil {
		t.Fatal("confirmation alone cleared quarantine")
	}
	if err := Persist(db, 1, event("agent_boot", "p", now.UnixMilli()+2, true)); err != nil {
		t.Fatal(err)
	}
	s, err := Load(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if Ready(s, strings.Repeat("ab", 32), now) {
		t.Fatal("new boot failed to invalidate")
	}
	if err := Persist(db, 1, event("installed_check", "p", now.UnixMilli()+3, true)); err != nil {
		t.Fatal(err)
	}
	s, err = Load(db, 1)
	if err != nil || !Ready(s, strings.Repeat("ab", 32), now) {
		t.Fatalf("auth reconnect failed recovery: %v %+v", err, s)
	}
}

func TestAuditAndStateRemainBounded(t *testing.T) {
	db := testDB(t)
	e := event("agent_boot", strings.Repeat("s", 10000), 1, true)
	e.Message = strings.Repeat("secret", 20000)
	e.Data = `{"processId":"p","password":"MUST_NOT_RETAIN","privateKey":"MUST_NOT_RETAIN","arbitrary":"` + strings.Repeat("x", 32000) + `"}`
	if err := Persist(db, 1, e); err != nil {
		t.Fatal(err)
	}
	var rec model.NodeAnyTLSCertLog
	if err := db.First(&rec).Error; err != nil {
		t.Fatal(err)
	}
	if len(rec.Data) > 1024 || rec.Message != "" || strings.Contains(rec.Data, "MUST_NOT_RETAIN") {
		t.Fatalf("unsafe audit retained, bytes=%d", len(rec.Data))
	}
	state, err := Load(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Boot.ProcessHash) != 64 {
		t.Fatal("bounded digest missing")
	}
}

func fileDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sql, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql.Close() })
	if err := db.Exec("PRAGMA busy_timeout = 20").Error; err != nil {
		t.Fatal(err)
	}
	return db
}

// Separate OS processes bypass the local mutex and use separate connection
// pools, exercising real SQLite transaction contention and persistence.
func TestSQLiteContentionRetriesAndFailsClosed(t *testing.T) {
	for _, release := range []bool{true, false} {
		t.Run(fmt.Sprintf("release_%t", release), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "locked.db")
			db := fileDB(t, path)
			locker := fileDB(t, path)
			if err := db.AutoMigrate(&model.NodeAnyTLSCertLog{}, &model.AnyTLSRuntimeState{}); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err := Persist(db, 1, event("agent_boot", "p", now.UnixMilli()-1, true)); err != nil {
				t.Fatal(err)
			}
			tx := locker.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			if err := tx.Exec("UPDATE anytls_runtime_state SET node_id=node_id WHERE node_id=1").Error; err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- Persist(db, 1, event("installed_check", "p", now.UnixMilli(), true)) }()
			if release {
				time.Sleep(35 * time.Millisecond)
				if err := tx.Commit().Error; err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if release && err != nil {
					t.Fatalf("bounded lock retry did not recover: %v", err)
				}
				if !release && err == nil {
					t.Fatal("write succeeded through held SQLite lock")
				}
			case <-time.After(2 * time.Second):
				tx.Rollback()
				t.Fatal("SQLite retries exceeded bound")
			}
			if !release {
				tx.Rollback()
				if _, err := Load(db, 1); err == nil {
					t.Fatal("exhausted write retries left state readable")
				}
				var n int64
				db.Model(&model.NodeAnyTLSCertLog{}).Count(&n)
				if n != 1 {
					t.Fatalf("failed retry leaked audit rows: %d", n)
				}
			}
		})
	}
}

func TestConcurrentProcessWriter(t *testing.T) {
	path := os.Getenv("ANYTLS_TEST_RUNTIME_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	at, err := strconv.ParseInt(os.Getenv("ANYTLS_TEST_RUNTIME_AT"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	db := fileDB(t, path)
	if err := Persist(db, 1, event("installed_check", "p", at, os.Getenv("ANYTLS_TEST_RUNTIME_MATCH") == "true")); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentWritersAndRestartPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	db := fileDB(t, path)
	if err := db.AutoMigrate(&model.NodeAnyTLSCertLog{}, &model.AnyTLSRuntimeState{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	base := now.UnixMilli()
	// Start with a real retained legacy chain and no durable row so all eight
	// processes also race cold import against newer live mismatch evidence.
	boot := event("agent_boot", "p", base-2, true)
	boot.NodeID = 1
	if err := db.Create(&boot).Error; err != nil {
		t.Fatal(err)
	}
	installed := event("installed_check", "p", base-1, true)
	installed.NodeID = 1
	if err := db.Create(&installed).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestConcurrentProcessWriter$", "-test.v")
			cmd.Env = append(os.Environ(), "ANYTLS_TEST_RUNTIME_DB="+path, fmt.Sprintf("ANYTLS_TEST_RUNTIME_AT=%d", base+int64(i)), fmt.Sprintf("ANYTLS_TEST_RUNTIME_MATCH=%t", i != 7))
			if output, err := cmd.CombinedOutput(); err != nil {
				errors <- fmt.Sprintf("writer %d: %v %s", i, err, output)
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for msg := range errors {
		t.Error(msg)
	}
	if t.Failed() {
		return
	}
	sql, _ := db.DB()
	sql.Close()
	reopened := fileDB(t, path)
	s, err := Load(reopened, 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.Installed.TimeMs != base+7 || s.Confirmed || Ready(s, strings.Repeat("ab", 32), now) {
		t.Fatalf("concurrent older writer regressed newest mismatch: %+v", s)
	}
	var count int64
	reopened.Model(&model.AnyTLSRuntimeState{}).Count(&count)
	if count != 1 {
		t.Fatalf("unbounded state rows: %d", count)
	}
	// Retention and connection restart cannot erase the original boot.
	if err := reopened.Where("node_id = ?", 1).Delete(&model.NodeAnyTLSCertLog{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := Persist(reopened, 1, event("installed_check", "p", base+8, true)); err != nil {
		t.Fatal(err)
	}
	s, err = Load(reopened, 1)
	if err != nil || !Ready(s, strings.Repeat("ab", 32), now) {
		t.Fatalf("restart/pruning lost durable boot: %v %+v", err, s)
	}
}
