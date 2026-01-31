package ocpp

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type ChargerConnection struct {
	ID                string
	Conn              *websocket.Conn
	StatusByConnector map[int]string
	LastSeen          time.Time
	Vendor            string
	Model             string
}

type Registry struct {
	mu       sync.RWMutex
	chargers map[string]*ChargerConnection
}

func NewRegistry() *Registry {
	return &Registry{chargers: make(map[string]*ChargerConnection)}
}

func (r *Registry) Add(id string, conn *websocket.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chargers[id] = &ChargerConnection{
		ID:                id,
		Conn:              conn,
		StatusByConnector: make(map[int]string),
		LastSeen:          time.Now().UTC(),
	}
}

func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.chargers, id)
}

func (r *Registry) IsConnected(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.chargers[id]
	return ok
}

func (r *Registry) UpdateStatus(id string, connectorID int, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	charger, ok := r.chargers[id]
	if !ok {
		return
	}
	charger.StatusByConnector[connectorID] = status
	charger.LastSeen = time.Now().UTC()
}

func (r *Registry) GetStatus(id string, connectorID int) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	charger, ok := r.chargers[id]
	if !ok {
		return "", false
	}
	status, ok := charger.StatusByConnector[connectorID]
	return status, ok
}

func (r *Registry) UpdateBootInfo(id, vendor, model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	charger, ok := r.chargers[id]
	if !ok {
		return
	}
	charger.Vendor = vendor
	charger.Model = model
	charger.LastSeen = time.Now().UTC()
}

func (r *Registry) GetConnection(id string) (*websocket.Conn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	charger, ok := r.chargers[id]
	if !ok {
		return nil, false
	}
	return charger.Conn, true
}

type Callbacks struct {
	OnRemoteStartResult func(sessionID string, accepted bool)
	OnRemoteStopResult  func(sessionID string, accepted bool)
	OnStartTransaction  func(cpID string, connectorID int, transactionID int, meterStartKwh float64)
	OnMeterValues       func(cpID string, connectorID int, transactionID int, energyKwh float64)
	OnStopTransaction   func(cpID string, transactionID int, meterStopKwh float64)
}

type pendingCall struct {
	Action    string
	SessionID string
}

type Server struct {
	Registry *Registry
	Upgrader websocket.Upgrader

	pendingMu sync.Mutex
	pending   map[string]pendingCall

	callbacks Callbacks

	seqMu          sync.Mutex
	transactionSeq int

	meterMu     sync.Mutex
	meterStarts map[int]float64

	db *sql.DB
}

func NewServer() *Server {
	return &Server{
		Registry: NewRegistry(),
		Upgrader: websocket.Upgrader{
			Subprotocols: []string{"ocpp1.6", "ocpp1.6j", "ocpp1.6-json"},
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		pending:     make(map[string]pendingCall),
		meterStarts: make(map[int]float64),
	}
}

func (s *Server) AttachDB(db *sql.DB) {
	s.db = db
}

func (s *Server) SetCallbacks(callbacks Callbacks) {
	s.callbacks = callbacks
}

func (s *Server) RegisterPending(messageID, action, sessionID string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pending[messageID] = pendingCall{Action: action, SessionID: sessionID}
}

func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	cpID := r.URL.Query().Get("id")
	if cpID == "" {
		http.Error(w, "missing charge point id", http.StatusBadRequest)
		return
	}

	conn, err := s.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("ocpp: upgrade error:", err)
		return
	}

	log.Println("ocpp: charger connected:", cpID, "subprotocol:", conn.Subprotocol())
	s.Registry.Add(cpID, conn)

	go s.handleConnection(cpID, conn)
}

func (s *Server) handleConnection(cpID string, conn *websocket.Conn) {
	defer func() {
		conn.Close()
		s.Registry.Remove(cpID)
		log.Println("ocpp: charger disconnected:", cpID)
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Println("ocpp: read error:", err)
			return
		}

		log.Printf("ocpp: message from %s: %s", cpID, string(msg))
		s.handleMessage(cpID, conn, msg)
	}
}

type statusNotificationPayload struct {
	ConnectorID int    `json:"connectorId"`
	Status      string `json:"status"`
	ErrorCode   string `json:"errorCode"`
}

type bootNotificationPayload struct {
	ChargePointVendor string `json:"chargePointVendor"`
	ChargePointModel  string `json:"chargePointModel"`
}

type startTransactionPayload struct {
	ConnectorID int   `json:"connectorId"`
	MeterStart  int64 `json:"meterStart"`
}

type stopTransactionPayload struct {
	TransactionID int   `json:"transactionId"`
	MeterStop     int64 `json:"meterStop"`
}

type meterValuesPayload struct {
	ConnectorID   int `json:"connectorId"`
	TransactionID int `json:"transactionId"`
	MeterValue    []struct {
		SampledValue []struct {
			Value     string `json:"value"`
			Measurand string `json:"measurand"`
			Unit      string `json:"unit"`
		} `json:"sampledValue"`
	} `json:"meterValue"`
}

func (s *Server) handleMessage(cpID string, conn *websocket.Conn, msg []byte) {
	var parts []json.RawMessage
	if err := json.Unmarshal(msg, &parts); err != nil {
		log.Println("ocpp: invalid message json:", err)
		return
	}
	if len(parts) < 3 {
		log.Println("ocpp: invalid message length")
		return
	}

	var msgType int
	if err := json.Unmarshal(parts[0], &msgType); err != nil {
		log.Println("ocpp: invalid message type:", err)
		return
	}

	switch msgType {
	case 2:
		s.handleCall(cpID, conn, parts)
	case 3:
		s.handleCallResult(parts)
	default:
		return
	}
}

func (s *Server) handleCall(cpID string, conn *websocket.Conn, parts []json.RawMessage) {
	var messageID string
	if err := json.Unmarshal(parts[1], &messageID); err != nil {
		log.Println("ocpp: invalid message id:", err)
		return
	}

	var action string
	if err := json.Unmarshal(parts[2], &action); err != nil {
		log.Println("ocpp: invalid action:", err)
		return
	}

	switch action {
	case "BootNotification":
		s.handleBootNotification(cpID, conn, messageID, parts)
	case "Heartbeat":
		s.sendHeartbeatResponse(conn, messageID)
	case "StatusNotification":
		s.handleStatusNotification(cpID, conn, messageID, parts)
	case "StartTransaction":
		s.handleStartTransaction(cpID, conn, messageID, parts)
	case "StopTransaction":
		s.handleStopTransaction(cpID, conn, messageID, parts)
	case "MeterValues":
		s.handleMeterValues(cpID, conn, messageID, parts)
	default:
		s.sendNotSupported(conn, messageID, action)
	}
}

func (s *Server) handleCallResult(parts []json.RawMessage) {
	var messageID string
	if err := json.Unmarshal(parts[1], &messageID); err != nil {
		return
	}

	s.pendingMu.Lock()
	pending, ok := s.pending[messageID]
	if ok {
		delete(s.pending, messageID)
	}
	s.pendingMu.Unlock()
	if !ok {
		return
	}

	if len(parts) < 3 {
		return
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(parts[2], &payload); err != nil {
		return
	}

	status, _ := payload["status"].(string)
	accepted := status == "Accepted"

	switch pending.Action {
	case "RemoteStartTransaction":
		if s.callbacks.OnRemoteStartResult != nil {
			s.callbacks.OnRemoteStartResult(pending.SessionID, accepted)
		}
	case "RemoteStopTransaction":
		if s.callbacks.OnRemoteStopResult != nil {
			s.callbacks.OnRemoteStopResult(pending.SessionID, accepted)
		}
	}
}

func (s *Server) handleBootNotification(cpID string, conn *websocket.Conn, messageID string, parts []json.RawMessage) {
	if len(parts) >= 4 {
		var payload bootNotificationPayload
		if err := json.Unmarshal(parts[3], &payload); err == nil {
			s.Registry.UpdateBootInfo(cpID, payload.ChargePointVendor, payload.ChargePointModel)
		}
	}

	s.sendBootNotificationResponse(conn, messageID)
}

func (s *Server) handleStatusNotification(cpID string, conn *websocket.Conn, messageID string, parts []json.RawMessage) {
	if len(parts) < 4 {
		s.sendCallError(conn, messageID, "FormationViolation", "missing payload")
		return
	}

	var payload statusNotificationPayload
	if err := json.Unmarshal(parts[3], &payload); err != nil {
		s.sendCallError(conn, messageID, "FormationViolation", "invalid payload")
		return
	}

	s.Registry.UpdateStatus(cpID, payload.ConnectorID, payload.Status)
	s.upsertCharger(cpID, payload.Status)
	
	s.sendEmptyResponse(conn, messageID)
}

func (s *Server) handleStartTransaction(cpID string, conn *websocket.Conn, messageID string, parts []json.RawMessage) {
	if len(parts) < 4 {
		s.sendCallError(conn, messageID, "FormationViolation", "missing payload")
		return
	}

	var payload startTransactionPayload
	if err := json.Unmarshal(parts[3], &payload); err != nil {
		s.sendCallError(conn, messageID, "FormationViolation", "invalid payload")
		return
	}

	txID := s.nextTransactionID()
	meterStartKwh := float64(payload.MeterStart) / 1000.0

	s.meterMu.Lock()
	s.meterStarts[txID] = meterStartKwh
	s.meterMu.Unlock()

	response := []interface{}{
		3,
		messageID,
		map[string]interface{}{
			"transactionId": txID,
			"idTagInfo": map[string]interface{}{
				"status": "Accepted",
			},
		},
	}

	if err := conn.WriteJSON(response); err != nil {
		log.Println("ocpp: write start transaction response error:", err)
	}

	if s.callbacks.OnStartTransaction != nil {
		s.callbacks.OnStartTransaction(cpID, payload.ConnectorID, txID, meterStartKwh)
	}
}

func (s *Server) handleStopTransaction(cpID string, conn *websocket.Conn, messageID string, parts []json.RawMessage) {
	if len(parts) < 4 {
		s.sendCallError(conn, messageID, "FormationViolation", "missing payload")
		return
	}

	var payload stopTransactionPayload
	if err := json.Unmarshal(parts[3], &payload); err != nil {
		s.sendCallError(conn, messageID, "FormationViolation", "invalid payload")
		return
	}

	meterStopKwh := float64(payload.MeterStop) / 1000.0
	if start, ok := s.getMeterStart(payload.TransactionID); ok && meterStopKwh >= start {
		meterStopKwh -= start
	}
	s.clearMeterStart(payload.TransactionID)

	response := []interface{}{
		3,
		messageID,
		map[string]interface{}{
			"idTagInfo": map[string]interface{}{
				"status": "Accepted",
			},
		},
	}

	if err := conn.WriteJSON(response); err != nil {
		log.Println("ocpp: write stop transaction response error:", err)
	}

	if s.callbacks.OnStopTransaction != nil {
		s.callbacks.OnStopTransaction(cpID, payload.TransactionID, meterStopKwh)
	}
}

func (s *Server) handleMeterValues(cpID string, conn *websocket.Conn, messageID string, parts []json.RawMessage) {
	if len(parts) < 4 {
		s.sendCallError(conn, messageID, "FormationViolation", "missing payload")
		return
	}

	var payload meterValuesPayload
	if err := json.Unmarshal(parts[3], &payload); err != nil {
		s.sendCallError(conn, messageID, "FormationViolation", "invalid payload")
		return
	}

	energyKwh, ok := parseEnergyKwh(payload)
	if ok {
		if start, okStart := s.getMeterStart(payload.TransactionID); okStart && energyKwh >= start {
			energyKwh -= start
		}
		if s.callbacks.OnMeterValues != nil {
			s.callbacks.OnMeterValues(cpID, payload.ConnectorID, payload.TransactionID, energyKwh)
		}
	}

	s.sendEmptyResponse(conn, messageID)
}

func (s *Server) sendBootNotificationResponse(conn *websocket.Conn, messageID string) {
	response := []interface{}{
		3,
		messageID,
		map[string]interface{}{
			"status":      "Accepted",
			"currentTime": time.Now().UTC().Format(time.RFC3339),
			"interval":    30,
		},
	}

	if err := conn.WriteJSON(response); err != nil {
		log.Println("ocpp: write boot response error:", err)
	}
}

func (s *Server) sendHeartbeatResponse(conn *websocket.Conn, messageID string) {
	response := []interface{}{
		3,
		messageID,
		map[string]interface{}{
			"currentTime": time.Now().UTC().Format(time.RFC3339),
		},
	}

	if err := conn.WriteJSON(response); err != nil {
		log.Println("ocpp: write heartbeat response error:", err)
	}
}

func (s *Server) sendEmptyResponse(conn *websocket.Conn, messageID string) {
	response := []interface{}{3, messageID, map[string]interface{}{}}
	if err := conn.WriteJSON(response); err != nil {
		log.Println("ocpp: write response error:", err)
	}
}

func (s *Server) sendCallError(conn *websocket.Conn, messageID, code, description string) {
	response := []interface{}{4, messageID, code, description, map[string]interface{}{}}
	if err := conn.WriteJSON(response); err != nil {
		log.Println("ocpp: write error response error:", err)
	}
}

func (s *Server) sendNotSupported(conn *websocket.Conn, messageID, action string) {
	s.sendCallError(conn, messageID, "NotSupported", "Action not supported")
}

func (s *Server) SendRemoteStartTransaction(cpID string, connectorID int, idTag string) (string, error) {
	conn, ok := s.Registry.GetConnection(cpID)
	if !ok {
		return "", errors.New("charger not connected")
	}

	messageID := randomID()
	request := []interface{}{
		2,
		messageID,
		"RemoteStartTransaction",
		map[string]interface{}{
			"connectorId": connectorID,
			"idTag":       idTag,
		},
	}

	if err := conn.WriteJSON(request); err != nil {
		return "", err
	}
	return messageID, nil
}

func (s *Server) SendRemoteStopTransaction(cpID string, transactionID int) (string, error) {
	conn, ok := s.Registry.GetConnection(cpID)
	if !ok {
		return "", errors.New("charger not connected")
	}

	messageID := randomID()
	request := []interface{}{
		2,
		messageID,
		"RemoteStopTransaction",
		map[string]interface{}{
			"transactionId": transactionID,
		},
	}

	if err := conn.WriteJSON(request); err != nil {
		return "", err
	}
	return messageID, nil
}

func (s *Server) nextTransactionID() int {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.transactionSeq++
	return s.transactionSeq
}

func (s *Server) getMeterStart(transactionID int) (float64, bool) {
	s.meterMu.Lock()
	defer s.meterMu.Unlock()
	val, ok := s.meterStarts[transactionID]
	return val, ok
}

func (s *Server) clearMeterStart(transactionID int) {
	s.meterMu.Lock()
	defer s.meterMu.Unlock()
	delete(s.meterStarts, transactionID)
}

func randomID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func parseEnergyKwh(payload meterValuesPayload) (float64, bool) {
	for _, mv := range payload.MeterValue {
		for _, sv := range mv.SampledValue {
			if sv.Value == "" {
				continue
			}
			if sv.Measurand != "" && sv.Measurand != "Energy.Active.Import.Register" {
				continue
			}
			val, err := strconv.ParseFloat(sv.Value, 64)
			if err != nil {
				continue
			}
			if sv.Unit == "Wh" {
				return val / 1000.0, true
			}
			return val, true
		}
	}
	return 0, false
}

func (s *Server) upsertCharger(id string, status string) {
	if s.db == nil {
		return
	}
	query := `
		INSERT INTO chargers (id, status, last_seen)
		VALUES ($1, $2, NOW())
		ON CONFLICT (id)
		DO UPDATE SET status = EXCLUDED.status, last_seen = NOW()
	`
	_, _ = s.db.Exec(query, id, status)
}
