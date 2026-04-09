package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"vajraBackend/internal/repositories"
)

const (
	minTopupAmount = 1
	maxTopupAmount = 10000
)

type WalletHandler struct {
	walletRepo *repositories.WalletRepository
	userRepo   *repositories.UserRepository
}

type TopupInitiateRequest struct {
	Amount float64 `json:"amount"`
}

type TopupInitiateResponse struct {
	OrderID  string  `json:"order_id"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Key      string  `json:"key"`
}

func NewWalletHandler(db *sqlx.DB) *WalletHandler {
	return &WalletHandler{
		walletRepo: repositories.NewWalletRepository(db),
		userRepo:   repositories.NewUserRepository(db),
	}
}

// InitiateTopup godoc
// @Summary Initiate wallet top-up
// @Description Create a Razorpay order and a pending top-up record
// @Tags wallet
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body handlers.TopupInitiateRequest true "Topup payload"
// @Success 200 {object} handlers.TopupInitiateResponse
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /wallet/topup/initiate [post]
func (h *WalletHandler) InitiateTopup(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req TopupInitiateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Amount < minTopupAmount || req.Amount > maxTopupAmount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be between 100 and 10000"})
		return
	}

	user, err := h.userRepo.GetByID(userID)
	if err != nil {
		respondWithInternalError(c, "failed to fetch user", err)
		return
	}
	if user == nil || strings.ToLower(user.Status) != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "user is not active"})
		return
	}

	wallet, err := h.walletRepo.GetWalletByUserID(userID)
	if err != nil {
		respondWithInternalError(c, "failed to fetch wallet", err)
		return
	}
	if wallet == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "wallet not found"})
		return
	}
	if strings.ToLower(wallet.Status) != "active" {
		c.JSON(http.StatusForbidden, gin.H{"error": "wallet is not active"})
		return
	}

	orderID, currency, err := createRazorpayOrder(req.Amount)
	log.Println("Razorpay order created with ID:", orderID, "currency:", currency, "error:", err)
	if err != nil {
		respondWithInternalError(c, "failed to create payment order", err)
		return
	}

	_, err = h.walletRepo.CreateTopupIntent(userID, wallet.ID, req.Amount, currency, orderID)
	log.Println("Topup intent created for order ID:", orderID, "error:", err)
	if err != nil {
		respondWithInternalError(c, "failed to store topup intent", err)
		return
	}

	c.JSON(http.StatusOK, TopupInitiateResponse{
		OrderID:  orderID,
		Amount:   req.Amount,
		Currency: currency,
		Key:      os.Getenv("RAZORPAY_API_KEY"),
	})
}

// GetBalance godoc
// @Summary Get wallet balance
// @Tags wallet
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /wallet/balance [get]
func (h *WalletHandler) GetBalance(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	wallet, err := h.walletRepo.GetWalletByUserID(userID)
	if err != nil {
		respondWithInternalError(c, "failed to fetch wallet", err)
		return
	}
	if wallet == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "wallet not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"balance":       wallet.Balance,
		"currency":      wallet.Currency,
		"status":        wallet.Status,
		"locked_amount": wallet.LockedAmount,
	})
}

// GetTransactions godoc
// @Summary Get wallet transactions
// @Tags wallet
// @Produce json
// @Security BearerAuth
// @Param limit query int false "Max number of transactions"
// @Success 200 {array} models.WalletTransaction
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /wallet/transactions [get]
func (h *WalletHandler) GetTransactions(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	wallet, err := h.walletRepo.GetWalletByUserID(userID)
	if err != nil {
		respondWithInternalError(c, "failed to fetch wallet", err)
		return
	}
	if wallet == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "wallet not found"})
		return
	}

	limit := 50
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil {
			limit = parsed
		}
	}

	transactions, err := h.walletRepo.ListTransactions(wallet.ID, limit)
	if err != nil {
		respondWithInternalError(c, "failed to fetch transactions", err)
		return
	}

	c.JSON(http.StatusOK, transactions)
}

// PaymentWebhook godoc
// @Summary Razorpay payment webhook
// @Description Credits wallet after verified payment
// @Tags webhooks
// @Accept json
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /webhooks/payment [post]
func (h *WalletHandler) PaymentWebhook(c *gin.Context) {
	secret := os.Getenv("RAZORPAY_SECRET_WEBHOOK")
	if secret == "" {
		log.Println("webhook: missing RAZORPAY_SECRET_WEBHOOK")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "missing webhook secret"})
		return
	}

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("webhook: failed to read body: %v", err)
		respondWithError(c, http.StatusBadRequest, "invalid body", err)
		return
	}

	signature := c.GetHeader("X-Razorpay-Signature")
	if signature == "" {
		log.Println("webhook: missing X-Razorpay-Signature")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing signature"})
		return
	}

	if !verifyRazorpaySignature(payload, signature, secret) {
		log.Println("webhook: invalid signature")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}

	var hook razorpayWebhook
	if err := json.Unmarshal(payload, &hook); err != nil {
		log.Printf("webhook: invalid payload json: %v", err)
		respondWithError(c, http.StatusBadRequest, "invalid payload", err)
		return
	}

	payment := hook.Payload.Payment.Entity
	if payment.ID == "" || payment.OrderID == "" {
		log.Printf("webhook: missing payment details id=%q order_id=%q", payment.ID, payment.OrderID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing payment details"})
		return
	}

	amount := float64(payment.Amount) / 100.0
	amount = roundToTwo(amount)

	log.Printf("webhook: order_id=%s payment_id=%s status=%s amount=%.2f currency=%s", payment.OrderID, payment.ID, payment.Status, amount, payment.Currency)
	processed, err := h.walletRepo.ProcessTopupWebhook(payment.OrderID, payment.ID, payment.Status, payment.Currency, amount)
	if err != nil {
		if errors.Is(err, repositories.ErrTopupNotFound) {
			log.Printf("webhook: topup not found for order_id=%s", payment.OrderID)
			c.JSON(http.StatusOK, gin.H{"message": "ignored"})
			return
		}
		log.Printf("webhook: processing failed: %v", err)
		respondWithExactError(c, http.StatusInternalServerError, err, "failed to process webhook")
		return
	}

	if !processed {
		log.Printf("webhook: ignored for order_id=%s", payment.OrderID)
		c.JSON(http.StatusOK, gin.H{"message": "ignored"})
		return
	}

	log.Printf("webhook: processed order_id=%s payment_id=%s", payment.OrderID, payment.ID)
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

type razorpayWebhook struct {
	Event   string `json:"event"`
	Payload struct {
		Payment struct {
			Entity razorpayPaymentEntity `json:"entity"`
		} `json:"payment"`
	} `json:"payload"`
}

type razorpayPaymentEntity struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	OrderID  string `json:"order_id"`
}

func createRazorpayOrder(amount float64) (string, string, error) {
	key := os.Getenv("RAZORPAY_API_KEY")
	secret := os.Getenv("RAZORPAY_API_KEY_SECRET")
	if key == "" || secret == "" {
		return "", "", errors.New("missing razorpay credentials")
	}

	payload := map[string]interface{}{
		"amount":          int64(math.Round(amount * 100)),
		"currency":        "INR",
		"receipt":         "wallet_topup",
		"payment_capture": 1,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}

	request, err := http.NewRequest("POST", "https://api.razorpay.com/v1/orders", strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}

	basic := base64.StdEncoding.EncodeToString([]byte(key + ":" + secret))
	request.Header.Set("Authorization", "Basic "+basic)
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", "", errors.New("razorpay order creation failed")
	}

	var response struct {
		ID       string `json:"id"`
		Currency string `json:"currency"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", "", err
	}
	if response.ID == "" {
		return "", "", errors.New("razorpay order missing id")
	}

	return response.ID, response.Currency, nil
}

func verifyRazorpaySignature(payload []byte, signature, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)
	decodedSig, err := hexDecode(signature)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, decodedSig)
}

func hexDecode(value string) ([]byte, error) {
	decoded := make([]byte, len(value)/2)
	_, err := hex.Decode(decoded, []byte(value))
	return decoded, err
}

func roundToTwo(val float64) float64 {
	return math.Round(val*100) / 100
}
