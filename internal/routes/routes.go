package routes

import (
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"vijraBackend/internal/handlers"
)

func RegisterRoutes(r *gin.Engine, db *sqlx.DB) {
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	r.GET("/ping", handlers.Ping)

	// Health check (optional)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})
	userHandler := handlers.NewUserHandler(db)

	users := r.Group("/users")
	{
		users.POST("/create", userHandler.CreateUser)
		users.GET("/:id", userHandler.GetUser)
	}
}
