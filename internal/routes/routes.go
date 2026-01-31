package routes

import (
	"os"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"vajraBackend/internal/handlers"
	"vajraBackend/internal/middleware"
	"vajraBackend/internal/ocpp"
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
	ocppServer := ocpp.NewServer()
	ocppServer.AttachDB(db.DB)
	chargingHandler := handlers.NewChargingHandler(db, ocppServer)

	r.GET("/ocpp", func(c *gin.Context) {
		ocppServer.HandleWS(c.Writer, c.Request)
	})

	r.GET("/ocpp/:id", func(c *gin.Context) {
		q := c.Request.URL.Query()
		q.Set("id", c.Param("id"))
		c.Request.URL.RawQuery = q.Encode()
		ocppServer.HandleWS(c.Writer, c.Request)
	})

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
		protected.POST("/chargers/verify", chargingHandler.VerifyCharger)
		protected.POST("/charging/start", chargingHandler.StartCharging)
		protected.POST("/charging/stop", chargingHandler.StopCharging)
		protected.GET("/charging/session/:id", chargingHandler.GetSession)
		protected.GET("/charging/active", chargingHandler.GetActiveSession)
		protected.GET("/charging/sessions", chargingHandler.ListSessions)
		protected.GET("/ws/charging/:session_id", chargingHandler.LiveUpdatesWS)
	}

	webhooks := r.Group("/webhooks")
	{
		webhooks.POST("/payment", walletHandler.PaymentWebhook)
	}
}
