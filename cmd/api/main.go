package main

import (
	"log"
	"log/slog"
	"os"

	"github.com/gin-gonic/gin"

	_ "vajraBackend/docs"
	"vajraBackend/internal/config"
	"vajraBackend/internal/db"
	"vajraBackend/internal/middleware"
	"vajraBackend/internal/routes"
)

// @title Vijra Backend API
// @version 1.0
// @description API documentation for Vijra Backend
// @host localhost:8080
// @BasePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Enter `Bearer <token>`
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// Load env
	config.Load()

	// DB connection
	database := db.Connect()

	// Gin router
	r := gin.Default()
	// includes: logger + recovery middleware
	r.Use(middleware.ErrorTracker(database))

	// Routes
	routes.RegisterRoutes(r, database)

	log.Println("🚀 Server running on 127.0.0.1:8080")
	r.Run("127.0.0.1:8080")
}
