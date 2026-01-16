package main

import (
	"log"

	"github.com/gin-gonic/gin"

	_ "vajraBackend/docs"
	"vajraBackend/internal/config"
	"vajraBackend/internal/db"
	"vajraBackend/internal/routes"
)

// @title Vijra Backend API
// @version 1.0
// @description API documentation for Vijra Backend
// @host localhost:8080
// @BasePath /
func main() {
	// Load env
	config.Load()

	// DB connection
	database := db.Connect()

	// Gin router
	r := gin.Default()
	// includes: logger + recovery middleware

	// Routes
	routes.RegisterRoutes(r, database)

	log.Println("🚀 Server running on :8080")
	r.Run(":8080")
}
