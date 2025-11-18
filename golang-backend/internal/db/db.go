package db

import (
	"fmt"
	"os"
	"time"

	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/util"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func dsn() string {
	host := os.Getenv("DB_HOST")
	port := os.Getenv("DB_PORT")
	if port == "" {
		port = "3306"
	}
	name := os.Getenv("DB_NAME")
	user := os.Getenv("DB_USER")
	pass := os.Getenv("DB_PASSWORD")
	params := "charset=utf8mb4&parseTime=True&loc=Local&timeout=10s"
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?%s", user, pass, host, port, name, params)
}

func dsnNoDB() string {
	host := os.Getenv("DB_HOST")
	port := os.Getenv("DB_PORT")
	if port == "" {
		port = "3306"
	}
	user := os.Getenv("DB_USER")
	pass := os.Getenv("DB_PASSWORD")
	params := "charset=utf8mb4&parseTime=True&loc=Local&timeout=10s"
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/?%s", user, pass, host, port, params)
}

func ensureDatabase() error {
	if os.Getenv("DB_DIALECT") == "sqlite" {
		return nil
	}
	name := os.Getenv("DB_NAME")
	if name == "" {
		return fmt.Errorf("DB_NAME is empty")
	}
	tmp, err := gorm.Open(mysql.Open(dsnNoDB()), &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)})
	if err != nil {
		return err
	}
	if err := tmp.Exec("CREATE DATABASE IF NOT EXISTS `" + name + "` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci").Error; err != nil {
		return err
	}
	sqlDB, _ := tmp.DB()
	if sqlDB != nil {
		_ = sqlDB.Close()
	}
	return nil
}

func Init() error {
	if err := ensureDatabase(); err != nil {
		return err
	}
	cfg := &gorm.Config{Logger: logger.Default.LogMode(logger.Info)}
	var db *gorm.DB
	var err error
	if os.Getenv("DB_DIALECT") == "sqlite" {
		path := os.Getenv("DB_SQLITE_PATH")
		if path == "" {
			path = "./flux.db"
		}
		db, err = openSQLiteGorm(path, cfg)
	} else {
		db, err = gorm.Open(mysql.Open(dsn()), cfg)
	}
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	DB = db
	// Auto-migrate tables
	if err := DB.AutoMigrate(
		&model.User{},
		&model.Node{},
		&model.Tunnel{},
		&model.Forward{},
		&model.UserTunnel{},
		&model.SpeedLimit{},
		&model.ViteConfig{},
		&model.StatisticsFlow{},
		&model.ExitSetting{},
		&model.ProbeTarget{},
		&model.NodeProbeResult{},
		&model.NodeDisconnectLog{},
		&model.Alert{},
		&model.NodeSysInfo{},
		&model.NodeRuntime{},
		&model.NodeOpLog{},
		&model.ForwardMidPort{},
	); err != nil {
		return err
	}
	// Seed admin user
	if err := seedAdmin(); err != nil {
		return err
	}
	return nil
}

func seedAdmin() error {
	var count int64
	// prefer exact username check
	if err := DB.Model(&model.User{}).Where("user = ?", "admin_user").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	status := 1
	u := model.User{
		BaseEntity:    model.BaseEntity{CreatedTime: now, UpdatedTime: now, Status: &status},
		User:          "admin_user",
		Pwd:           util.MD5("admin_user"),
		RoleID:        0,
		ExpTime:       nil,
		Flow:          0,
		InFlow:        0,
		OutFlow:       0,
		Num:           0,
		FlowResetTime: 0,
	}
	return DB.Create(&u).Error
}
