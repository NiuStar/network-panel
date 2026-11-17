package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type nodeListIndex struct {
	table, name, columns string
}

// ensureNodeListIndexes adds lookup indexes only; existing LONGTEXT columns and
// diagnostic history are never rewritten. Existing names must match exactly.
func ensureNodeListIndexes(db *gorm.DB) error {
	if db.Dialector.Name() == "mysql" {
		// Session settings and DDL must use the same physical connection.
		return db.Connection(func(tx *gorm.DB) error { return ensureNodeListIndexesOnConn(tx, true) })
	}
	return ensureNodeListIndexesOnConn(db, false)
}

func ensureNodeListIndexesOnConn(db *gorm.DB, mysql bool) (result error) {
	indexes := []nodeListIndex{
		{"node_op_log", "idx_node_op_node_time", "node_id, time_ms, id"},
		{"node_op_log", "idx_node_op_cmd_node_time", "cmd, node_id, time_ms, id"},
		{"node_anytls_cert_log", "idx_cert_node_time", "node_id, time_ms, id"},
		{"node_anytls_cert_log", "idx_cert_node_step_time", "node_id, step, time_ms, id"},
	}
	// Validate every existing name before making any additive changes.
	missing := []nodeListIndex{}
	for _, idx := range indexes {
		exists, err := verifyNodeListIndex(db, idx, mysql)
		if err != nil {
			return err
		}
		if !exists {
			missing = append(missing, idx)
		}
	}
	if len(missing) == 0 {
		return nil // Pre-created production indexes: metadata reads only.
	}
	if mysql {
		var previous int64
		if err := db.Raw("SELECT @@session.lock_wait_timeout").Scan(&previous).Error; err != nil {
			return err
		}
		// Restore even when setting the bound or executing DDL fails. Do not use
		// a canceled DDL context for cleanup on the pinned connection.
		defer func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(db.Statement.Context), 5*time.Second)
			defer cancel()
			err := db.WithContext(ctx).Exec(fmt.Sprintf("SET SESSION lock_wait_timeout = %d", previous)).Error
			if err != nil {
				result = errors.Join(result, fmt.Errorf("restore lock_wait_timeout: %w", err))
			}
		}()
		if err := db.Exec("SET SESSION lock_wait_timeout = 5").Error; err != nil {
			return err
		}
	}
	for _, idx := range missing {
		statement := fmt.Sprintf("CREATE INDEX `%s` ON `%s` (%s)", idx.name, idx.table, idx.columns)
		if mysql {
			columns := strings.ReplaceAll(strings.ReplaceAll(idx.columns, "cmd,", "cmd(64),"), "step,", "step(64),")
			statement = fmt.Sprintf("ALTER TABLE `%s` ADD INDEX `%s` (%s), ALGORITHM=INPLACE, LOCK=NONE", idx.table, idx.name, columns)
		}
		ctx, cancel := context.WithTimeout(db.Statement.Context, 30*time.Second)
		err := db.WithContext(ctx).Exec(statement).Error
		cancel()
		if err != nil {
			// Only a duplicate-name race may be accepted, and only after exact
			// verification. Timeouts and all other DDL errors remain failures.
			var myerr *mysqldriver.MySQLError
			duplicate := mysql && errors.As(err, &myerr) && myerr.Number == 1061
			if !mysql {
				duplicate = strings.Contains(err.Error(), "index "+idx.name+" already exists")
			}
			if !duplicate {
				return fmt.Errorf("create %s: %w", idx.name, err)
			}
		}
		exists, verifyErr := verifyNodeListIndex(db, idx, mysql)
		if verifyErr != nil {
			return verifyErr
		}
		if !exists {
			return fmt.Errorf("index %s missing after creation: %v", idx.name, err)
		}
	}
	return nil
}

func verifyNodeListIndex(db *gorm.DB, idx nodeListIndex, mysql bool) (bool, error) {
	type indexColumn struct {
		Position   int
		ColumnName sql.NullString
		SubPart    sql.NullInt64
	}
	var columns []indexColumn
	if mysql {
		if err := db.Raw(`SELECT SEQ_IN_INDEX AS position, COLUMN_NAME AS column_name, SUB_PART AS sub_part
			FROM information_schema.statistics WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?
			ORDER BY SEQ_IN_INDEX`, idx.table, idx.name).Scan(&columns).Error; err != nil {
			return false, err
		}
	} else {
		var indexes []struct{ Name string }
		if err := db.Raw("PRAGMA index_list(`" + idx.table + "`)").Scan(&indexes).Error; err != nil {
			return false, err
		}
		found := false
		for _, existing := range indexes {
			found = found || existing.Name == idx.name
		}
		if !found {
			return false, nil
		}
		var rows []struct {
			Seqno int
			Name  sql.NullString
		}
		if err := db.Raw("PRAGMA index_info(`" + idx.name + "`)").Scan(&rows).Error; err != nil {
			return false, err
		}
		for _, row := range rows {
			columns = append(columns, indexColumn{Position: row.Seqno + 1, ColumnName: row.Name})
		}
	}
	if len(columns) == 0 {
		return false, nil
	}
	want := strings.Split(idx.columns, ", ")
	valid := len(columns) == len(want)
	for i, column := range columns {
		if i >= len(want) {
			valid = false
			break
		}
		prefix := sql.NullInt64{}
		if mysql && (want[i] == "cmd" || want[i] == "step") {
			prefix = sql.NullInt64{Int64: 64, Valid: true}
		}
		if column.Position != i+1 || !column.ColumnName.Valid || column.ColumnName.String != want[i] || column.SubPart != prefix {
			valid = false
		}
	}
	if !valid {
		return false, fmt.Errorf("index %s on %s has unexpected ordered columns or prefixes (want %s)", idx.name, idx.table, idx.columns)
	}
	return true, nil
}
