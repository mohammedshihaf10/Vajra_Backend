package routes

import (
	"os"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"vajraBackend/internal/handlers"
	"vajraBackend/internal/middleware"
)

func RegisterRoutes(r *gin.Engine, db *sqlx.DB) {
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	r.GET("/ping", handlers.Ping)

	// Health check (optional)
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	userHandler := handlers.NewUserHandler(db)
	authHandler := handlers.NewAuthHandler(db)
	walletHandler := handlers.NewWalletHandler(db)

	users := r.Group("/users")
	{
		users.POST("/create", userHandler.CreateUser)
		users.GET("/email/:email", userHandler.GetUserByEmail)
		users.GET("/:id", userHandler.GetUser)
	}

	auth := r.Group("/auth")
	{
		auth.POST("/register", authHandler.Register)
		auth.POST("/login", authHandler.Login)
		auth.POST("/logout", authHandler.Logout)
		auth.POST("/refresh", authHandler.Refresh)
		auth.POST("/send-otp", authHandler.SendOTP)
	}

	secret := []byte(os.Getenv("JWT_SECRET"))
	protected := r.Group("/")
	protected.Use(middleware.RequireAuth(secret))
	{
		protected.GET("/me", authHandler.Me)
		protected.POST("/wallet/topup/initiate", walletHandler.InitiateTopup)
		protected.GET("/wallet/balance", walletHandler.GetBalance)
		protected.GET("/wallet/transactions", walletHandler.GetTransactions)
	}

	webhooks := r.Group("/webhooks")
	{
		webhooks.POST("/payment", walletHandler.PaymentWebhook)
	}
}
