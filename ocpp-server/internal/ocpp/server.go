package ocpp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type ChargerConnection struct {
	ID                string
	Conn              *websocket.Conn
	StatusByConnector map[int]string
	LastSeen          time.Time
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

func (r *Registry) GetConnection(id string) (*websocket.Conn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	charger, ok := r.chargers[id]
	if !ok {
		return nil, false
	}
	return charger.Conn, true
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

	seqMu          sync.Mutex
	transactionSeq int

	meterMu     sync.Mutex
	meterStarts map[int]float64

	webhooks *WebhookClient
}

func NewServer(webhookURL string, webhookSecret string, timeoutSec int) *Server {
	return &Server{
		Registry: NewRegistry(),
		Upgrader: websocket.Upgrader{
			Subprotocols: []string{"ocpp1.6", "ocpp1.6j", "ocpp1.6-json"},
			CheckOrigin:  func(r *http.Request) bool { return true },
		},
		pending:     make(map[string]pendingCall),
		meterStarts: make(map[int]float64),
		webhooks:    NewWebhookClient(webhookURL, webhookSecret, timeoutSec),
	}
}

type remoteStartRequest struct {
	SessionID   string `json:"session_id"`
	ChargerID   string `json:"charger_id"`
	ConnectorID int    `json:"connector_id"`
	IDTag       string `json:"id_tag"`
}

type remoteStopRequest struct {
	SessionID     string `json:"session_id"`
	ChargerID     string `json:"charger_id"`
	TransactionID int    `json:"transaction_id"`
}

func (s *Server) HandleChargerReadAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/chargers/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	chargerID := parts[0]
	if chargerID == "" {
		http.Error(w, "missing charger id", http.StatusBadRequest)
		return
	}

	if len(parts) == 2 && parts[1] == "connectivity" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"connected": s.Registry.IsConnected(chargerID),
		})
		return
	}

	if len(parts) == 4 && parts[1] == "connectors" && parts[3] == "status" {
		connectorID, err := strconv.Atoi(parts[2])
		if err != nil || connectorID <= 0 {
			http.Error(w, "invalid connector id", http.StatusBadRequest)
			return
		}
		status, ok := s.Registry.GetStatus(chargerID, connectorID)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": status,
			"known":  ok,
		})
		return
	}

	http.NotFound(w, r)
}

func (s *Server) HandleRemoteStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req remoteStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" || req.ChargerID == "" || req.ConnectorID <= 0 || req.IDTag == "" {
		http.Error(w, "session_id, charger_id, connector_id, id_tag are required", http.StatusBadRequest)
		return
	}

	messageID, err := s.SendRemoteStartTransaction(req.ChargerID, req.ConnectorID, req.IDTag)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.pendingMu.Lock()
	s.pending[messageID] = pendingCall{Action: "RemoteStartTransaction", SessionID: req.SessionID}
	s.pendingMu.Unlock()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) HandleRemoteStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req remoteStopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if req.SessionID == "" || req.ChargerID == "" || req.TransactionID <= 0 {
		http.Error(w, "session_id, charger_id, transaction_id are required", http.StatusBadRequest)
		return
	}

	messageID, err := s.SendRemoteStopTransaction(req.ChargerID, req.TransactionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.pendingMu.Lock()
	s.pending[messageID] = pendingCall{Action: "RemoteStopTransaction", SessionID: req.SessionID}
	s.pendingMu.Unlock()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
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
		s.sendBootNotificationResponse(conn, messageID)
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
		s.sendNotSupported(conn, messageID)
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
		s.webhooks.Emit("remote_start_result", map[string]interface{}{
			"session_id": pending.SessionID,
			"accepted":   accepted,
		})
	case "RemoteStopTransaction":
		s.webhooks.Emit("remote_stop_result", map[string]interface{}{
			"session_id": pending.SessionID,
			"accepted":   accepted,
		})
	}
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

	s.webhooks.Emit("start_transaction", map[string]interface{}{
		"charge_point_id": cpID,
		"connector_id":    payload.ConnectorID,
		"transaction_id":  txID,
		"meter_start_kwh": meterStartKwh,
	})
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

	s.webhooks.Emit("stop_transaction", map[string]interface{}{
		"charge_point_id": cpID,
		"transaction_id":  payload.TransactionID,
		"meter_stop_kwh":  meterStopKwh,
	})
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

	if energyKwh, ok := parseEnergyKwh(payload); ok {
		if start, okStart := s.getMeterStart(payload.TransactionID); okStart && energyKwh >= start {
			energyKwh -= start
		}
		s.webhooks.Emit("meter_values", map[string]interface{}{
			"charge_point_id": cpID,
			"connector_id":    payload.ConnectorID,
			"transaction_id":  payload.TransactionID,
			"energy_kwh":      energyKwh,
		})
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

func (s *Server) sendNotSupported(conn *websocket.Conn, messageID string) {
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

func writeJSON(w http.ResponseWriter, code int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
