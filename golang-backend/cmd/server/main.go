package main

import (
	"log"
	"os"

	app "network-panel/golang-backend/internal/app"
	"network-panel/golang-backend/internal/app/scheduler"
	"network-panel/golang-backend/internal/app/util"
	appver "network-panel/golang-backend/internal/app/version"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
)

func main() {
	// load .env if present
	util.LoadEnv()
	if err := dbpkg.Init(); err != nil {
		log.Fatalf("db init error: %v", err)
	}
	// start schedulers
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
