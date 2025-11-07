//go:build loong64

package db

import (
    "fmt"
    "gorm.io/gorm"
)

// openSQLiteGorm is not supported on loong64 due to upstream C library constraints.
func openSQLiteGorm(path string, cfg *gorm.Config) (*gorm.DB, error) {
    return nil, fmt.Errorf("sqlite backend is not supported on loong64 builds; use MySQL (set DB_DIALECT empty and configure DB_HOST/DB_*), or build without the loong64 target")
}

