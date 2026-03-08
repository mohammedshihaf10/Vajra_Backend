package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"

	"vajraBackend/internal/charging"
	"vajraBackend/internal/models"
	"vajraBackend/internal/repositories"
)

const (
	minStartBalance = 10.0
	costPerKwh      = 24.90
)

type ChargingHandler struct {
	gateway      charging.GatewayClient
	walletRepo   *repositories.WalletRepository
	chargingRepo *repositories.ChargingRepository
	hub          *ChargingHub
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

func (h *ChargingHub) Publish(sessionID string, payload interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for conn := range h.conns[sessionID] {
		_ = conn.WriteJSON(payload)
	}
}

type ChargerVerifyRequest struct {
	ChargerID   string `json:"charger_id"`
	ConnectorID int    `json:"connector_id"`
}

type ChargerVerifyResponse struct {
	ChargerID   string  `json:"charger_id"`
	ConnectorID int     `json:"connector_id"`
	Status      string  `json:"status"`
	Available   bool    `json:"available"`
	ChargerType string  `json:"charger_type"`
	PowerKW     float64 `json:"power_kw"`
	Location    string  `json:"location"`
}

type StartChargingRequest struct {
	ChargerID   string `json:"charger_id"`
	ConnectorID int    `json:"connector_id"`
}

type StartChargingResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type StopChargingRequest struct {
	SessionID string `json:"session_id"`
}

type StopChargingResponse struct {
	Status         string  `json:"status"`
	FinalEnergyKwh float64 `json:"final_energy_kwh"`
	FinalCost      float64 `json:"final_cost"`
	EndedAt        string  `json:"ended_at"`
}

func NewChargingHandler(db *sqlx.DB, gateway charging.GatewayClient) *ChargingHandler {
	return &ChargingHandler{
		gateway:      gateway,
		walletRepo:   repositories.NewWalletRepository(db),
		chargingRepo: repositories.NewChargingRepository(db),
		hub:          NewChargingHub(),
	}
}

// VerifyCharger godoc
// @Summary Verify charger availability
// @Tags charging
// @Accept json
// @Produce json
// @Param body body handlers.ChargerVerifyRequest true "Verify payload"
// @Success 200 {object} handlers.ChargerVerifyResponse
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /chargers/verify [post]
func (h *ChargingHandler) VerifyCharger(c *gin.Context) {
	var req ChargerVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.ChargerID == "" || req.ConnectorID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "charger_id and connector_id are required"})
		return
	}

	connected, err := h.gateway.IsConnected(c.Request.Context(), req.ChargerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify charger connectivity"})
		return
	}
	if !connected {
		c.JSON(http.StatusNotFound, gin.H{"error": "charger not connected"})
		return
	}

	status, ok, err := h.gateway.GetConnectorStatus(c.Request.Context(), req.ChargerID, req.ConnectorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch connector status"})
		return
	}
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "connector status unknown"})
		return
	}
	if status != "Available" {
		c.JSON(http.StatusConflict, gin.H{"error": "charger not available"})
		return
	}

	c.JSON(http.StatusOK, ChargerVerifyResponse{
		ChargerID:   req.ChargerID,
		ConnectorID: req.ConnectorID,
		Status:      status,
		Available:   status == "Available",
		ChargerType: "DC",
		PowerKW:     30,
		Location:    "Unknown",
	})
}

// StartCharging godoc
// @Summary Start charging session
// @Tags charging
// @Accept json
// @Produce json
// @Param body body handlers.StartChargingRequest true "Start payload"
// @Success 200 {object} handlers.StartChargingResponse
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /charging/start [post]
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

	if req.ChargerID == "" || req.ConnectorID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "charger_id and connector_id are required"})
		return
	}

	wallet, err := h.walletRepo.GetWalletByUserID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch wallet"})
		return
	}
	if wallet == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "wallet not found"})
		return
	}
	if wallet.Balance < minStartBalance {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient wallet balance"})
		return
	}

	connected, err := h.gateway.IsConnected(c.Request.Context(), req.ChargerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify charger connectivity"})
		return
	}
	if !connected {
		c.JSON(http.StatusNotFound, gin.H{"error": "charger not connected"})
		return
	}

	status, ok, err := h.gateway.GetConnectorStatus(c.Request.Context(), req.ChargerID, req.ConnectorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch connector status"})
		return
	}
	log.Println("Charger status for", ok, "is", status)
	if !ok || status != "Available" {
		c.JSON(http.StatusConflict, gin.H{"error": "charger not available"})
		return
	}

	if err := h.chargingRepo.UpsertCharger(req.ChargerID, status); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to upsert charger"})
		return
	}

	session := &models.ChargingSession{
		ChargerID:   req.ChargerID,
		ConnectorID: req.ConnectorID,
		UserID:      userID,
		Status:      "starting",
	}

	if err := h.chargingRepo.CreateSession(session); err != nil {
		log.Println("Error creating charging session:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}

	if err := h.gateway.RemoteStart(c.Request.Context(), session.ID, req.ChargerID, req.ConnectorID, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start charger"})
		return
	}

	h.hub.Publish(session.ID, gin.H{"status": "starting", "charger_name": req.ChargerID})

	c.JSON(http.StatusOK, StartChargingResponse{SessionID: session.ID, Status: "starting"})
}

// StopCharging godoc
// @Summary Stop charging session
// @Tags charging
// @Accept json
// @Produce json
// @Param body body handlers.StopChargingRequest true "Stop payload"
// @Success 200 {object} handlers.StopChargingResponse
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /charging/stop [post]
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
	if req.SessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	session, err := h.chargingRepo.GetSessionByID(req.SessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch session"})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if session.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	if session.TransactionID == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "transaction_id not set yet"})
		return
	}

	if err := h.gateway.RemoteStop(c.Request.Context(), session.ID, session.ChargerID, session.TransactionID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to stop charger"})
		return
	}

	if err := h.chargingRepo.UpdateSessionStatus(session.ID, "stopping"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update session"})
		return
	}

	h.hub.Publish(session.ID, gin.H{"status": "stopping", "charger_name": session.ChargerID})

	c.JSON(http.StatusOK, StopChargingResponse{Status: "stopping"})
}

// GetSession godoc
// @Summary Get charging session
// @Tags charging
// @Produce json
// @Param id path string true "Session ID"
// @Success 200 {object} models.ChargingSession
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /charging/session/{id} [get]
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

// GetActiveSession godoc
// @Summary Get active charging session
// @Tags charging
// @Produce json
// @Success 200 {object} models.ChargingSession
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /charging/active [get]
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

// ListSessions godoc
// @Summary List charging sessions
// @Tags charging
// @Produce json
// @Param status query string false "Filter by status"
// @Success 200 {array} models.ChargingSession
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /charging/sessions [get]
func (h *ChargingHandler) ListSessions(c *gin.Context) {
	userID := c.GetString("user_id")
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	status := c.Query("status")
	log.Println("Listing sessions with status filter:", status)
	if status != "" {
		switch status {
		case "all":
			status = ""
		case "pending", "starting", "charging", "stopping", "stopped", "failed":
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status filter"})
			return
		}
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
		log.Printf("live ws: upgrade failed for session %s: %v", sessionID, err)
		return
	}
	defer conn.Close()

	log.Printf("live ws: client connected session=%s", sessionID)
	h.hub.Add(sessionID, conn)
	defer h.hub.Remove(sessionID, conn)
	defer log.Printf("live ws: client disconnected session=%s", sessionID)

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			log.Printf("live ws: read error session=%s: %v", sessionID, err)
			return
		}
	}
}

type ocppWebhookEnvelope struct {
	EventType string          `json:"event_type"`
	Data      json.RawMessage `json:"data"`
}

type remoteStartResultEvent struct {
	SessionID string `json:"session_id"`
	Accepted  bool   `json:"accepted"`
}

type remoteStopResultEvent struct {
	SessionID string `json:"session_id"`
	Accepted  bool   `json:"accepted"`
}

type startTransactionEvent struct {
	ChargePointID string  `json:"charge_point_id"`
	ConnectorID   int     `json:"connector_id"`
	TransactionID int     `json:"transaction_id"`
	MeterStartKwh float64 `json:"meter_start_kwh"`
}

type meterValuesEvent struct {
	ChargePointID string  `json:"charge_point_id"`
	ConnectorID   int     `json:"connector_id"`
	TransactionID int     `json:"transaction_id"`
	EnergyKwh     float64 `json:"energy_kwh"`
}

type stopTransactionEvent struct {
	ChargePointID string  `json:"charge_point_id"`
	TransactionID int     `json:"transaction_id"`
	MeterStopKwh  float64 `json:"meter_stop_kwh"`
}

// OCPPWebhook godoc
// @Summary OCPP event webhook
// @Description Receives asynchronous charging events from external OCPP service
// @Tags webhooks
// @Accept json
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /webhooks/ocpp [post]
func (h *ChargingHandler) OCPPWebhook(c *gin.Context) {
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

	var envelope ocppWebhookEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	switch envelope.EventType {
	case "remote_start_result":
		var evt remoteStartResultEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil || evt.SessionID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid remote_start_result payload"})
			return
		}
		h.onRemoteStartResult(evt.SessionID, evt.Accepted)
	case "remote_stop_result":
		var evt remoteStopResultEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil || evt.SessionID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid remote_stop_result payload"})
			return
		}
		h.onRemoteStopResult(evt.SessionID, evt.Accepted)
	case "start_transaction":
		var evt startTransactionEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil || evt.ChargePointID == "" || evt.ConnectorID <= 0 || evt.TransactionID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start_transaction payload"})
			return
		}
		h.onStartTransaction(evt.ChargePointID, evt.ConnectorID, evt.TransactionID, evt.MeterStartKwh)
	case "meter_values":
		var evt meterValuesEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil || evt.ChargePointID == "" || evt.ConnectorID <= 0 || evt.TransactionID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid meter_values payload"})
			return
		}
		h.onMeterValues(evt.ChargePointID, evt.ConnectorID, evt.TransactionID, evt.EnergyKwh)
	case "stop_transaction":
		var evt stopTransactionEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil || evt.ChargePointID == "" || evt.TransactionID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid stop_transaction payload"})
			return
		}
		h.onStopTransaction(evt.ChargePointID, evt.TransactionID, evt.MeterStopKwh)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported event_type"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *ChargingHandler) onRemoteStartResult(sessionID string, accepted bool) {
	if accepted {
		return
	}
	_ = h.chargingRepo.UpdateSessionStatus(sessionID, "failed")
	h.hub.Publish(sessionID, gin.H{"status": "failed"})
}

func (h *ChargingHandler) onRemoteStopResult(sessionID string, accepted bool) {
	if accepted {
		return
	}
	_ = h.chargingRepo.UpdateSessionStatus(sessionID, "charging")
	h.hub.Publish(sessionID, gin.H{"status": "charging"})
}

func (h *ChargingHandler) onStartTransaction(cpID string, connectorID int, transactionID int, meterStartKwh float64) {
	sessionID, err := h.chargingRepo.UpdateSessionForStartTransaction(cpID, connectorID, transactionID)
	if err != nil || sessionID == "" {
		return
	}

	h.hub.Publish(sessionID, gin.H{
		"status":       "charging",
		"charger_name": cpID,
		"energy_kwh":   0,
		"cost":         0,
	})
}

func (h *ChargingHandler) onMeterValues(cpID string, connectorID int, transactionID int, energyKwh float64) {
	session, err := h.chargingRepo.GetSessionByTransactionID(transactionID)
	if err != nil || session == nil {
		return
	}

	cost := roundToTwoCharging(energyKwh * costPerKwh)
	_ = h.chargingRepo.UpdateSessionEnergyCost(session.ID, energyKwh, cost)

	h.hub.Publish(session.ID, gin.H{
		"status":       session.Status,
		"charger_name": cpID,
		"energy_kwh":   energyKwh,
		"cost":         cost,
	})
}

func (h *ChargingHandler) onStopTransaction(cpID string, transactionID int, meterStopKwh float64) {
	session, err := h.chargingRepo.GetSessionByTransactionID(transactionID)
	if err != nil || session == nil {
		return
	}

	energy := meterStopKwh
	if energy <= 0 {
		energy = session.EnergyKwh
	}

	cost := roundToTwoCharging(energy * costPerKwh)
	_ = h.chargingRepo.StopSession(session.ID, energy, cost)
	_ = h.walletRepo.DebitWalletForSession(session.UserID, cost, session.ID)

	h.hub.Publish(session.ID, gin.H{
		"status":       "stopped",
		"charger_name": cpID,
		"energy_kwh":   energy,
		"cost":         cost,
	})
}

func roundToTwoCharging(val float64) float64 {
	return math.Round(val*100) / 100
}

func verifyOCPPWebhookSignature(payload []byte, signature string, secret string) bool {
	signature = strings.TrimSpace(signature)
	signature = strings.TrimPrefix(signature, "sha256=")

	expectedMAC := hmac.New(sha256.New, []byte(secret))
	expectedMAC.Write(payload)
	expected := expectedMAC.Sum(nil)

	decodedSig, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	return hmac.Equal(decodedSig, expected)
}
