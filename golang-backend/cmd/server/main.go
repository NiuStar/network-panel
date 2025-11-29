package main

import (
	"fmt"
	"log"
	"os"

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
	fmt.Println("version:", appver.Get())
	// load .env if present
	util.LoadEnv()
	if err := dbpkg.Init(); err != nil {
		log.Fatalf("db init error: %v", err)
	}
	// start schedulerRs
	scheduler.Start()

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
