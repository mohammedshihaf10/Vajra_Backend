package routes

import (
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"vajraBackend/internal/charging"
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
	publicHandler := handlers.NewPublicHandler(db)
	citrineBaseURL := strings.TrimSpace(os.Getenv("CITRINE_BASE_URL"))
	if citrineBaseURL == "" {
		citrineBaseURL = strings.TrimSpace(os.Getenv("OCPP_SERVICE_BASE_URL"))
	}
	if citrineBaseURL == "" {
		log.Fatal("missing CITRINE_BASE_URL")
	}
	tenantRaw := firstNonEmptyEnv("CITRINE_TENANT_ID", "OCPP_SERVICE_TENANT_ID")
	tenantID, err := strconv.Atoi(strings.TrimSpace(tenantRaw))
	if err != nil || tenantID <= 0 {
		log.Fatal("missing or invalid CITRINE_TENANT_ID")
	}
	callbackURL := firstNonEmptyEnv("CITRINE_CALLBACK_URL", "CHARGING_WEBHOOK_URL")
	client := charging.NewHTTPCitrineClient(charging.CitrineConfig{
		BaseURL:          citrineBaseURL,
		APIKey:           firstNonEmptyEnv("CITRINE_API_KEY", "OCPP_SERVICE_API_KEY"),
		TenantID:         tenantID,
		CallbackURL:      callbackURL,
		Timeout:          readDurationEnv("CITRINE_TIMEOUT", 10*time.Second),
		MaxRetries:       readIntEnv("CITRINE_MAX_RETRIES", 3),
		BackoffInitial:   readDurationEnv("CITRINE_BACKOFF_INITIAL", 500*time.Millisecond),
		BackoffMax:       readDurationEnv("CITRINE_BACKOFF_MAX", 4*time.Second),
		CircuitThreshold: readIntEnv("CITRINE_CIRCUIT_THRESHOLD", 5),
		CircuitCooldown:  readDurationEnv("CITRINE_CIRCUIT_COOLDOWN", 15*time.Second),
	}, slog.Default())
	statusClient := charging.NewHasuraStationStatusClient(
		firstNonEmptyEnv("CHARGING_STATUS_GRAPHQL_URL", "HASURA_GRAPHQL_URL"),
		firstNonEmptyEnv("HASURA_GRAPHQL_ADMIN_SECRET"),
		firstNonEmptyEnv("HASURA_GRAPHQL_BEARER_TOKEN"),
		slog.Default(),
	)
	chargingService := charging.NewService(db, client, statusClient, slog.Default(), charging.ServiceConfig{
		IDTokenType:      firstNonEmptyEnv("CITRINE_ID_TOKEN_TYPE"),
		CallbackURL:      callbackURL,
		StartTimeout:     readDurationEnv("CHARGING_START_TIMEOUT", 2*time.Minute),
		RetryMaxAttempts: readIntEnv("CHARGING_RETRY_MAX_ATTEMPTS", 5),
		RetryBaseDelay:   readDurationEnv("CHARGING_RETRY_BASE_DELAY", time.Second),
	})
	chargingHandler := handlers.NewChargingHandler(db, chargingService)

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

	public := r.Group("/public")
	{
		public.GET("/data", publicHandler.ListPublicTableData)
	}

	secret := []byte(os.Getenv("JWT_SECRET"))
	protected := r.Group("/")
	protected.Use(middleware.RequireAuth(secret))
	{
		protected.GET("/me", authHandler.Me)
		protected.POST("/wallet/topup/initiate", walletHandler.InitiateTopup)
		protected.GET("/wallet/balance", walletHandler.GetBalance)
		protected.GET("/wallet/transactions", walletHandler.GetTransactions)
		protected.POST("/chargers/verify", middleware.RateLimit(20, time.Minute), chargingHandler.VerifyCharger)
		protected.GET("/chargers/:id/status", chargingHandler.GetChargerStatus)
		protected.POST("/charging/start", middleware.RateLimit(10, time.Minute), chargingHandler.StartCharging)
		protected.POST("/charging/stop", middleware.RateLimit(20, time.Minute), chargingHandler.StopCharging)
		protected.GET("/charging/session/:id", chargingHandler.GetSession)
		protected.GET("/charging/active", chargingHandler.GetActiveSession)
		protected.GET("/charging/sessions", chargingHandler.ListSessions)
		protected.GET("/ws/charging/:session_id", chargingHandler.LiveUpdatesWS)
		protected.GET("/metrics/charging", chargingHandler.ChargingMetrics)
	}

	webhooks := r.Group("/webhooks")
	{
		webhooks.POST("/payment", walletHandler.PaymentWebhook)
		webhooks.POST("/ocpp", middleware.RateLimit(120, time.Minute), chargingHandler.OCPPWebhook)
	}
}

func readIntEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func readDurationEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return value
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
