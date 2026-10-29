package main

import (
	"context"
	"fmt"
	"log"
	"network-panel/golang-backend/internal/panelupgrade"
	"os"
	"time"

	_ "network-panel/golang-backend/docs" // swag init generated docs
	app "network-panel/golang-backend/internal/app"
	"network-panel/golang-backend/internal/app/scheduler"
	"network-panel/golang-backend/internal/app/util"
	appver "network-panel/golang-backend/internal/app/version"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
)

// @title network-panel API
// @version 1.0
// @description 面板后端接口文档
// @BasePath /
func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version-only" {
		fmt.Println(appver.Get())
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "--upgrade-worker" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		if err := panelupgrade.Worker(ctx, os.Args[2], appver.Get()); err != nil {
			if persistErr := panelupgrade.MarkWorkerFailure(os.Args[2], err); persistErr != nil {
				log.Printf("upgrade worker closeout: %v", persistErr)
			}
			log.Printf("upgrade transaction failed: %v", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		log.Fatal("unknown server arguments")
	}
	fmt.Println("version:", appver.Get())
	// load .env if present
	util.LoadEnv()
	if err := panelupgrade.CheckStartup(); err != nil {
		log.Fatalf("upgrade database guard: %v", err)
	}
	if err := dbpkg.Init(); err != nil {
		log.Fatalf("db init error: %v", err)
	}
	// Bind the candidate's readiness to the database it actually opened.
	if os.Getenv("DB_DIALECT") == "sqlite" && panelupgrade.Maintenance() {
		var databases []struct {
			Name string
			File string
		}
		if err := dbpkg.DB.Raw("PRAGMA database_list").Scan(&databases).Error; err != nil {
			log.Fatal(err)
		}
		actual := ""
		for _, database := range databases {
			if database.Name == "main" {
				actual = database.File
			}
		}
		if err := panelupgrade.RecordReadiness(actual, appver.Get()); err != nil {
			log.Fatalf("upgrade readiness: %v", err)
		}
	}
	// Delay all external side effects until the upgrade worker commits readiness.
	go func() {
		for panelupgrade.Maintenance() {
			time.Sleep(500 * time.Millisecond)
		}
		scheduler.Start()
	}()

	r := gin.Default()
	gin.SetMode(gin.DebugMode)
	app.RegisterRoutes(r)

	port := os.Getenv("PORT")
	if port == "" {
		port = "6365"
	}
	log.Printf("network-panel server version %s", appver.Get())
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
