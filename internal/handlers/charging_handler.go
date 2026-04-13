package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"

	"vajraBackend/internal/charging"
	"vajraBackend/internal/models"
	"vajraBackend/internal/repositories"
)

type ChargingHandler struct {
	service      *charging.Service
	chargingRepo *repositories.ChargingRepository
	hub          *ChargingHub
	logger       *slog.Logger
}

type ChargingHub struct {
	mu       sync.RWMutex
	conns    map[string]map[*websocket.Conn]struct{}
	upgrader websocket.Upgrader
}

func NewChargingHub() *ChargingHub {
	return &ChargingHub{
		conns:    make(map[string]map[*websocket.Conn]struct{}),
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
	}
}

func (h *ChargingHub) Add(sessionID string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[sessionID] == nil {
		h.conns[sessionID] = make(map[*websocket.Conn]struct{})
	}
	h.conns[sessionID][conn] = struct{}{}
}

func (h *ChargingHub) Remove(sessionID string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[sessionID] == nil {
		return
	}
	delete(h.conns[sessionID], conn)
	if len(h.conns[sessionID]) == 0 {
		delete(h.conns, sessionID)
	}
}

func (h *ChargingHub) Publish(sessionID string, payload any) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for conn := range h.conns[sessionID] {
		_ = conn.WriteJSON(payload)
	}
}

type ChargerVerifyRequest struct {
	ChargerID   string `json:"charger_id" binding:"required"`
	ConnectorID int    `json:"connector_id" binding:"required,min=1"`
}

type ChargerStatusResponse struct {
	ChargerID   string `json:"charger_id"`
	ConnectorID int    `json:"connector_id"`
	Status      string `json:"status"`
	Available   bool   `json:"available"`
	LastSeen    string `json:"last_seen"`
}

type StartChargingRequest struct {
	ChargerID   string `json:"charger_id" binding:"required"`
	ConnectorID int    `json:"connector_id" binding:"required,min=1"`
}

type StartChargingResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type StopChargingRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}

type StopChargingResponse struct {
	SessionID      string  `json:"session_id"`
	Status         string  `json:"status"`
	FinalEnergyKwh float64 `json:"final_energy_kwh,omitempty"`
	FinalCost      float64 `json:"final_cost,omitempty"`
	FailureReason  string  `json:"failure_reason,omitempty"`
}

func NewChargingHandler(db *sqlx.DB, service *charging.Service) *ChargingHandler {
	handler := &ChargingHandler{
		service:      service,
		chargingRepo: repositories.NewChargingRepository(db),
		hub:          NewChargingHub(),
		logger:       slog.Default().With("component", "charging_handler"),
	}

	go handler.runRecoveryLoop()
	return handler
}

func (h *ChargingHandler) VerifyCharger(c *gin.Context) {
	var req ChargerVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	connector, err := h.service.ResolveConnectorStatus(c.Request.Context(), req.ChargerID, req.ConnectorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch charger status"})
		return
	}
	if connector == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "connector status unknown"})
		return
	}

	c.JSON(http.StatusOK, ChargerStatusResponse{
		ChargerID:   connector.ChargerID,
		ConnectorID: connector.ConnectorID,
		Status:      connector.Status,
		Available:   strings.EqualFold(connector.Status, "Available"),
		LastSeen:    connector.LastSeen,
	})
}

func (h *ChargingHandler) GetChargerStatus(c *gin.Context) {
	chargerID := c.Param("id")
	if chargerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing charger id"})
		return
	}

	connectorID := 1
	if raw := c.Query("connector_id"); raw != "" {
		if _, err := fmtSscanf(raw, &connectorID); err != nil || connectorID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid connector_id"})
			return
		}
	}

	connector, err := h.service.ResolveConnectorStatus(c.Request.Context(), chargerID, connectorID)
	h.logger.Debug("fetching charger status", "connector", connector, "connector_id", connectorID, "error", err)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch connector status"})
		return
	}
	if connector == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "connector status unknown"})
		return
	}

	c.JSON(http.StatusOK, ChargerStatusResponse{
		ChargerID:   connector.ChargerID,
		ConnectorID: connector.ConnectorID,
		Status:      connector.Status,
		Available:   strings.EqualFold(connector.Status, "Available"),
		LastSeen:    connector.LastSeen,
	})
}

func (h *ChargingHandler) StartCharging(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req StartChargingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	session, err := h.service.StartCharging(c.Request.Context(), charging.StartSessionInput{
		UserID:      userID,
		ChargerID:   req.ChargerID,
		ConnectorID: req.ConnectorID,
	})
	if err != nil {
		h.logger.Info("start charging rejected", "charger_id", req.ChargerID, "connector_id", req.ConnectorID, "error", err)
		switch err {
		case repositories.ErrWalletNotFound:
			c.JSON(http.StatusNotFound, gin.H{
				"error":   err.Error(),
				"code":    "wallet_not_found",
				"user_id": userID,
			})
		case repositories.ErrInsufficientBalance:
			c.JSON(http.StatusForbidden, gin.H{
				"error": err.Error(),
				"code":  "insufficient_balance",
			})
		case repositories.ErrConnectorStatusUnknown:
			c.JSON(http.StatusNotFound, gin.H{
				"error":        err.Error(),
				"code":         "connector_status_unknown",
				"charger_id":   req.ChargerID,
				"connector_id": req.ConnectorID,
				"hint":         "No cached StatusNotification exists for this charger/connector yet. Send a status_notification webhook or query charger status after CitrineOS has populated availability.",
			})
		case repositories.ErrChargerOffline, repositories.ErrConnectorUnavailable, repositories.ErrActiveSessionExists, repositories.ErrConnectorBusy:
			c.JSON(http.StatusConflict, gin.H{
				"error":        err.Error(),
				"code":         startConflictCode(err),
				"charger_id":   req.ChargerID,
				"connector_id": req.ConnectorID,
			})
		default:
			h.logger.Error("start charging failed", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":        "failed to start charging",
				"code":         "charging_start_failed",
				"charger_id":   req.ChargerID,
				"connector_id": req.ConnectorID,
			})
		}
		return
	}

	h.hub.Publish(session.ID, sessionUpdatePayload(session))
	h.service.SyncSessionFromActiveTransaction(c.Request.Context(), session.ID, h.hub)
	c.JSON(http.StatusAccepted, StartChargingResponse{SessionID: session.ID, Status: session.Status})
}

func (h *ChargingHandler) StopCharging(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req StopChargingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	session, err := h.service.StopCharging(c.Request.Context(), charging.StopSessionInput{
		UserID:    userID,
		SessionID: req.SessionID,
	})
	if err != nil {
		switch err {
		case repositories.ErrSessionNotFound:
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case repositories.ErrForbiddenSession:
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		case repositories.ErrSessionTerminal:
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			h.logger.Error("stop charging failed", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to stop charging"})
		}
		return
	}

	h.hub.Publish(session.ID, sessionUpdatePayload(session))
	if strings.TrimSpace(session.OCPPTransactionID) == "" {
		h.service.SyncSessionFromActiveTransaction(c.Request.Context(), session.ID, h.hub)
	} else {
		h.service.StartRemoteStopPolling(session.ID, h.hub)
	}
	c.JSON(http.StatusAccepted, StopChargingResponse{
		SessionID:     session.ID,
		Status:        session.Status,
		FailureReason: session.FailureReason,
	})
}

func (h *ChargingHandler) GetSession(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing session id"})
		return
	}

	session, err := h.chargingRepo.GetSessionByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch session"})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	c.JSON(http.StatusOK, session)
}

func (h *ChargingHandler) GetActiveSession(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	session, err := h.chargingRepo.GetActiveSessionByUserID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch session"})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no active session"})
		return
	}

	c.JSON(http.StatusOK, session)
}

func (h *ChargingHandler) ListSessions(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	status := c.Query("status")
	if status == "all" {
		status = ""
	}

	sessions, err := h.chargingRepo.ListSessionsByUserID(userID, status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch sessions"})
		return
	}
	c.JSON(http.StatusOK, sessions)
}

func (h *ChargingHandler) LiveUpdatesWS(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing session id"})
		return
	}

	conn, err := h.hub.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Warn("websocket upgrade failed", "session_id", sessionID, "error", err)
		return
	}
	defer conn.Close()

	h.hub.Add(sessionID, conn)
	defer h.hub.Remove(sessionID, conn)

	if session, err := h.chargingRepo.GetSessionByID(sessionID); err == nil && session != nil {
		_ = conn.WriteJSON(sessionUpdatePayload(session))
	}

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *ChargingHandler) OCPPWebhook(c *gin.Context) {
	if !h.service.WebhookEventsEnabled() {
		c.JSON(http.StatusAccepted, gin.H{"status": "ignored", "reason": "charging webhook processing disabled"})
		return
	}

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	secret := os.Getenv("OCPP_WEBHOOK_SECRET")
	if secret != "" {
		signature := c.GetHeader("X-OCPP-Signature")
		if signature == "" || !verifyOCPPWebhookSignature(payload, signature, secret) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
			return
		}
	}

	var envelope charging.WebhookEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	if envelope.EventType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing event_type"})
		return
	}

	if err := h.service.ProcessWebhook(c.Request.Context(), payload, envelope, h.hub); err != nil {
		h.logger.Error("webhook processing failed", "event_type", envelope.EventType, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "webhook processing failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *ChargingHandler) ChargingMetrics(c *gin.Context) {
	c.JSON(http.StatusOK, h.service.Metrics())
}

func (h *ChargingHandler) runRecoveryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if err := h.service.RecoverStaleStarts(context.Background(), h.hub); err != nil {
			h.logger.Error("stale start recovery failed", "error", err)
		}
	}
}

func sessionUpdatePayload(session *models.ChargingSession) map[string]any {
	return map[string]any{
		"session_id":      session.ID,
		"status":          session.Status,
		"energy_kwh":      session.EnergyKwh,
		"cost":            session.Cost,
		"transaction_ref": session.TransactionRef,
		"transaction_id":  session.OCPPTransactionID,
		"charging_state":  session.ChargingState,
		"is_active":       session.IsActive,
		"failure_reason":  session.FailureReason,
		"stop_requested":  session.StopRequestedAt != "",
		"billed_at":       session.BilledAt,
		"charger_id":      session.ChargerID,
		"connector_id":    session.ConnectorID,
	}
}

func verifyOCPPWebhookSignature(payload []byte, signature string, secret string) bool {
	signature = strings.TrimSpace(signature)
	signature = strings.TrimPrefix(signature, "sha256=")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)

	decoded, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	return hmac.Equal(decoded, expected)
}

func fmtSscanf(raw string, out *int) (int, error) {
	var v int
	n, err := fmt.Sscanf(raw, "%d", &v)
	if err == nil {
		*out = v
	}
	return n, err
}

func startConflictCode(err error) string {
	switch err {
	case repositories.ErrChargerOffline:
		return "charger_offline"
	case repositories.ErrConnectorUnavailable:
		return "connector_unavailable"
	case repositories.ErrActiveSessionExists:
		return "active_session_exists"
	case repositories.ErrConnectorBusy:
		return "connector_busy"
	default:
		return "charging_conflict"
	}
}
