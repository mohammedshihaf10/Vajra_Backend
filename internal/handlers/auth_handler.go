package handlers

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"vajraBackend/internal/auth"
	"vajraBackend/internal/models"
	"vajraBackend/internal/repositories"
)

const (
	accessTTL  = 14 * 24 * time.Hour
	refreshTTL = 30 * 24 * time.Hour
)

type AuthHandler struct {
	repo   *repositories.UserRepository
	secret []byte
}

type AuthResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
}

type LoginRequest struct {
	FullName    string `json:"full_name"`
	PhoneNumber string `json:"phone_number"`
	OTP         string `json:"otp"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type SendOTPRequest struct {
	PhoneNumber string `json:"phone_number"`
}

func NewAuthHandler(db *sqlx.DB) *AuthHandler {
	secret := []byte(os.Getenv("JWT_SECRET"))
	return &AuthHandler{
		repo:   repositories.NewUserRepository(db),
		secret: secret,
	}
}

// Register godoc
// @Summary Register a new user
// @Description Create a user and return access + refresh tokens
// @Tags auth
// @Accept json
// @Produce json
// @Param user body models.User true "User payload"
// @Success 201 {object} handlers.AuthResponse
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /auth/register [post]
func (h *AuthHandler) Register(c *gin.Context) {
	var user models.User
	if err := c.ShouldBindJSON(&user); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if user.PhoneNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "phone_number is required"})
		return
	}

	if err := h.repo.Create(&user); err != nil {
		respondWithInternalError(c, "could not create user", err)
		return
	}

	accessToken, accessExp, err := auth.GenerateToken(user.ID, accessTTL, h.secret)
	if err != nil {
		respondWithInternalError(c, "failed to generate access token", err)
		return
	}

	refreshToken, refreshExp, err := auth.GenerateToken(user.ID, refreshTTL, h.secret)
	if err != nil {
		respondWithInternalError(c, "failed to generate refresh token", err)
		return
	}

	if err := h.repo.SetAuthToken(user.ID, refreshToken, refreshExp); err != nil {
		respondWithInternalError(c, "failed to store refresh token", err)
		return
	}

	c.JSON(http.StatusCreated, AuthResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    accessExp.Format(time.RFC3339),
	})
}

// Login godoc
// @Summary Login
// @Description Login with phone number and OTP, optionally creating the user
// @Tags auth
// @Accept json
// @Produce json
// @Param body body handlers.LoginRequest true "Login payload"
// @Success 200 {object} handlers.AuthResponse
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.PhoneNumber == "" || req.OTP == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "phone_number and otp are required"})
		return
	}

	valid, err := h.repo.VerifyPhoneOTP(req.PhoneNumber, req.OTP)
	log.Println("OTP verification result:", valid, "error:", err) // Debug log
	if err != nil {
		respondWithInternalError(c, "failed to verify otp", err)
		return
	}
	if !valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired otp"})
		return
	}

	user, err := h.repo.GetByPhoneNumber(req.PhoneNumber)
	log.Println("Fetched user:", user, "error:", err) // Debug log
	if err != nil {
		respondWithInternalError(c, "failed to fetch user", err)
		return
	}

	if user == nil {
		if req.FullName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "full_name is required for new users"})
			return
		}
		user = &models.User{
			FullName:    req.FullName,
			PhoneNumber: req.PhoneNumber,
		}
		if err := h.repo.Create(user); err != nil {
			respondWithInternalError(c, "could not create user", err)
			return
		}
	}

	if err := h.repo.MarkPhoneVerified(user.ID); err != nil {
		respondWithInternalError(c, "failed to verify phone", err)
		return
	}

	accessToken, accessExp, err := auth.GenerateToken(user.ID, accessTTL, h.secret)
	if err != nil {
		respondWithInternalError(c, "failed to generate access token", err)
		return
	}

	refreshToken, refreshExp, err := auth.GenerateToken(user.ID, refreshTTL, h.secret)
	if err != nil {
		respondWithInternalError(c, "failed to generate refresh token", err)
		return
	}

	if err := h.repo.SetAuthToken(user.ID, refreshToken, refreshExp); err != nil {
		respondWithInternalError(c, "failed to store refresh token", err)
		return
	}

	c.JSON(http.StatusOK, AuthResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    accessExp.Format(time.RFC3339),
	})
}

// Logout godoc
// @Summary Logout
// @Description Revoke refresh token
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body handlers.RefreshRequest true "Refresh payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.RefreshToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "refresh_token is required"})
		return
	}

	user, err := h.repo.GetByAuthToken(req.RefreshToken)
	if err != nil {
		respondWithInternalError(c, "failed to fetch user", err)
		return
	}

	if user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
		return
	}

	if err := h.repo.ClearAuthToken(user.ID); err != nil {
		respondWithInternalError(c, "failed to revoke token", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// Refresh godoc
// @Summary Refresh access token
// @Description Rotate refresh token and return new access + refresh tokens
// @Tags auth
// @Accept json
// @Produce json
// @Param body body handlers.RefreshRequest true "Refresh payload"
// @Success 200 {object} handlers.AuthResponse
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /auth/refresh [post]
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.RefreshToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "refresh_token is required"})
		return
	}

	user, err := h.repo.GetByAuthToken(req.RefreshToken)
	if err != nil {
		respondWithInternalError(c, "failed to fetch user", err)
		return
	}
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}

	claims, err := auth.ParseAndVerify(req.RefreshToken, h.secret)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}
	if claims.Sub != user.ID {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}

	accessToken, accessExp, err := auth.GenerateToken(user.ID, accessTTL, h.secret)
	if err != nil {
		respondWithInternalError(c, "failed to generate access token", err)
		return
	}

	refreshToken, refreshExp, err := auth.GenerateToken(user.ID, refreshTTL, h.secret)
	if err != nil {
		respondWithInternalError(c, "failed to generate refresh token", err)
		return
	}

	if err := h.repo.SetAuthToken(user.ID, refreshToken, refreshExp); err != nil {
		respondWithInternalError(c, "failed to store refresh token", err)
		return
	}

	c.JSON(http.StatusOK, AuthResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    accessExp.Format(time.RFC3339),
	})
}

// Me godoc
// @Summary Get current user
// @Description Fetch the user tied to the access token
// @Tags auth
// @Produce json
// @Security BearerAuth
// @Success 200 {object} models.User
// @Failure 401 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /me [get]
func (h *AuthHandler) Me(c *gin.Context) {
	userID := c.GetString("user_id")
	log.Println("Me endpoint accessed by user_id:", userID) // Debug log
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	user, err := h.repo.GetByID(userID)
	log.Println("Fetched user in Me endpoint:", user, "error:", err) // Debug log
	if err != nil {
		respondWithInternalError(c, "failed to fetch user", err)
		return
	}

	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	c.JSON(http.StatusOK, user)
}

// SendOTP godoc
// @Summary Send OTP to phone number
// @Description Generate and send OTP to the provided phone number
// @Tags auth
// @Accept json
// @Produce json
// @Param body body handlers.SendOTPRequest true "OTP payload"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /auth/send-otp [post]
func (h *AuthHandler) SendOTP(c *gin.Context) {
	var req SendOTPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.PhoneNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "phone_number is required"})
		return
	}

	code, err := generateNumericOTP(6)
	if err != nil {
		respondWithInternalError(c, "failed to generate otp", err)
		return
	}

	expiresAt := time.Now().Add(5 * time.Minute)
	if err := h.repo.SetPhoneOTP(req.PhoneNumber, code, expiresAt); err != nil {
		respondWithInternalError(c, "failed to store otp", err)
		return
	}
	// if err := sendFast2SMS(req.PhoneNumber, code); err != nil {
	// 	log.Printf("failed to send otp: %v", err)
	// 	c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send otp", "details": err.Error()})
	// 	return
	// }

	// TODO: Integrate with SMS provider to deliver the OTP.
	c.JSON(http.StatusOK, gin.H{
		"message":    "otp sent",
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

func sendFast2SMS(phone, otp string) error {
	url := "https://www.fast2sms.com/dev/bulkV2"
	apiKey := os.Getenv("FAST2SMS_API_KEY")
	if apiKey == "" {
		return errors.New("missing FAST2SMS_API_KEY")
	}

	payload := map[string]interface{}{
		"variables_values": otp,
		"route":            "otp",
		"numbers":          phone,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Add("authorization", apiKey)
	req.Header.Add("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("Fast2SMS error response: status=%d body=%s", resp.StatusCode, string(respBody))
		return fmt.Errorf("fast2sms returned status %d", resp.StatusCode)
	}

	return nil
}

func generateNumericOTP(length int) (string, error) {
	if length <= 0 {
		return "", nil
	}

	otp := make([]byte, length)
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		otp[i] = byte('0' + n.Int64())
	}

	return string(otp), nil
}
