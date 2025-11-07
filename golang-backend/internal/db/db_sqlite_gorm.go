//go:build !loong64

package db

import (
    sqlite "github.com/glebarez/sqlite"
    "gorm.io/gorm"
)

// openSQLiteGorm opens a SQLite database using the pure-Go driver.
func openSQLiteGorm(path string, cfg *gorm.Config) (*gorm.DB, error) {
    return gorm.Open(sqlite.Open(path), cfg)
}

