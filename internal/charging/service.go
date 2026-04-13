package charging

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"vajraBackend/internal/models"
	"vajraBackend/internal/repositories"
)

const (
	DefaultMinStartBalance = 10.0
	DefaultCostPerKwh      = 24.90
	MaxOCPPIDTagLength     = 20
	MaxOCPPRemoteStartID   = 2147483647
	remoteStopPollLease    = 30 * time.Second
)

type Publisher interface {
	Publish(sessionID string, payload any)
}

type ServiceConfig struct {
	IDTokenType            string
	CallbackURL            string
	UseWebhookEvents       bool
	StartTimeout           time.Duration
	SyncTimeout            time.Duration
	SyncInterval           time.Duration
	RetryMaxAttempts       int
	RetryBaseDelay         time.Duration
	CostPerKwh             float64
	MinStartBalance        float64
	RemoteStopInitialDelay time.Duration
	RemoteStopPollInterval time.Duration
	RemoteStopMaxAttempts  int
}

type Service struct {
	db           *sqlx.DB
	client       CitrineClient
	statusClient StationStatusClient
	authClient   AuthorizationClient
	chargingRepo *repositories.ChargingRepository
	userRepo     *repositories.UserRepository
	walletRepo   *repositories.WalletRepository
	logger       *slog.Logger
	retryQueue   *RetryQueue
	metrics      *Metrics
	stopPollMu   sync.Mutex
	stopPollers  map[string]struct{}

	idTokenType            string
	costPerKwh             float64
	minStartBalance        float64
	startTimeout           time.Duration
	syncTimeout            time.Duration
	syncInterval           time.Duration
	useWebhookEvents       bool
	remoteStopInitialDelay time.Duration
	remoteStopPollInterval time.Duration
	remoteStopMaxAttempts  int
}

type StartSessionInput struct {
	UserID      string
	ChargerID   string
	ConnectorID int
}

type StopSessionInput struct {
	UserID    string
	SessionID string
}

type StatusNotificationEvent struct {
	EventID     string          `json:"event_id"`
	ChargerID   string          `json:"charger_id"`
	ConnectorID int             `json:"connector_id"`
	EVSEID      *int            `json:"evse_id"`
	Status      string          `json:"status"`
	LastSeenAt  string          `json:"last_seen_at"`
	Metadata    json.RawMessage `json:"metadata"`
}

type WebhookEnvelope struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

type RemoteStartResultEvent struct {
	SessionID string `json:"session_id"`
	Accepted  bool   `json:"accepted"`
	Reason    string `json:"reason"`
}

type RemoteStopResultEvent struct {
	SessionID string `json:"session_id"`
	Accepted  bool   `json:"accepted"`
	Reason    string `json:"reason"`
}

type StartTransactionEvent struct {
	ChargerID     string  `json:"charger_id"`
	ConnectorID   int     `json:"connector_id"`
	TransactionID string  `json:"transaction_id"`
	MeterStartKwh float64 `json:"meter_start_kwh"`
}

type MeterValuesEvent struct {
	ChargerID     string  `json:"charger_id"`
	ConnectorID   int     `json:"connector_id"`
	TransactionID string  `json:"transaction_id"`
	EnergyKwh     float64 `json:"energy_kwh"`
}

type StopTransactionEvent struct {
	ChargerID     string  `json:"charger_id"`
	TransactionID string  `json:"transaction_id"`
	MeterStopKwh  float64 `json:"meter_stop_kwh"`
}

func NewService(db *sqlx.DB, client CitrineClient, statusClient StationStatusClient, authClient AuthorizationClient, logger *slog.Logger, cfg ServiceConfig) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.IDTokenType == "" {
		cfg.IDTokenType = "Central"
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 5 * time.Minute
	}
	if cfg.SyncTimeout <= 0 {
		cfg.SyncTimeout = 5 * time.Minute
	}
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = 5 * time.Second
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = time.Second
	}
	if cfg.RetryMaxAttempts <= 0 {
		cfg.RetryMaxAttempts = 5
	}
	if cfg.CostPerKwh <= 0 {
		cfg.CostPerKwh = DefaultCostPerKwh
	}
	if cfg.MinStartBalance <= 0 {
		cfg.MinStartBalance = DefaultMinStartBalance
	}
	if cfg.RemoteStopInitialDelay <= 0 {
		cfg.RemoteStopInitialDelay = 2 * time.Second
	}
	if cfg.RemoteStopPollInterval <= 0 {
		cfg.RemoteStopPollInterval = 2 * time.Second
	}
	if cfg.RemoteStopMaxAttempts <= 0 {
		cfg.RemoteStopMaxAttempts = 20
	}

	return &Service{
		db:                     db,
		client:                 client,
		statusClient:           statusClient,
		authClient:             authClient,
		chargingRepo:           repositories.NewChargingRepository(db),
		userRepo:               repositories.NewUserRepository(db),
		walletRepo:             repositories.NewWalletRepository(db),
		logger:                 logger.With("component", "charging_service"),
		retryQueue:             NewRetryQueue(logger, cfg.RetryMaxAttempts, cfg.RetryBaseDelay),
		metrics:                NewMetrics(),
		stopPollers:            make(map[string]struct{}),
		idTokenType:            cfg.IDTokenType,
		costPerKwh:             cfg.CostPerKwh,
		minStartBalance:        cfg.MinStartBalance,
		startTimeout:           cfg.StartTimeout,
		syncTimeout:            cfg.SyncTimeout,
		syncInterval:           cfg.SyncInterval,
		useWebhookEvents:       cfg.UseWebhookEvents,
		remoteStopInitialDelay: cfg.RemoteStopInitialDelay,
		remoteStopPollInterval: cfg.RemoteStopPollInterval,
		remoteStopMaxAttempts:  cfg.RemoteStopMaxAttempts,
	}
}

func (s *Service) Metrics() MetricsSnapshot {
	return s.metrics.Snapshot()
}

func (s *Service) WebhookEventsEnabled() bool {
	return s.useWebhookEvents
}

func (s *Service) StartRemoteStopPolling(sessionID string, publisher Publisher) {
	session, err := s.chargingRepo.GetSessionByID(sessionID)
	if err != nil || session == nil {
		return
	}
	if session.Status != StateStopping || strings.TrimSpace(session.OCPPTransactionID) == "" {
		return
	}

	s.stopPollMu.Lock()
	if _, exists := s.stopPollers[sessionID]; exists {
		s.stopPollMu.Unlock()
		return
	}
	s.stopPollers[sessionID] = struct{}{}
	s.stopPollMu.Unlock()

	claimed, err := s.chargingRepo.ClaimStopPolling(sessionID, remoteStopPollLease)
	if err != nil {
		s.stopPollMu.Lock()
		delete(s.stopPollers, sessionID)
		s.stopPollMu.Unlock()
		s.logger.Warn("failed to claim remote stop poll lease", "session_id", sessionID, "error", err)
		return
	}
	if !claimed {
		s.stopPollMu.Lock()
		delete(s.stopPollers, sessionID)
		s.stopPollMu.Unlock()
		return
	}

	go s.pollRemoteStopResult(sessionID, publisher)
}

func (s *Service) StartCharging(ctx context.Context, input StartSessionInput) (*models.ChargingSession, error) {
	s.metrics.RecordStartRequest(false)

	wallet, err := s.walletRepo.GetWalletByUserID(input.UserID)
	if err != nil {
		return nil, err
	}
	if wallet == nil {
		return nil, repositories.ErrWalletNotFound
	}
	if wallet.Balance < s.minStartBalance {
		return nil, repositories.ErrInsufficientBalance
	}

	connector, err := s.ResolveConnectorStatus(ctx, input.ChargerID, input.ConnectorID)
	if err != nil {
		return nil, err
	}
	if connector == nil {
		return nil, repositories.ErrConnectorStatusUnknown
	}
	if !isConnectorOnline(connector) {
		return nil, repositories.ErrChargerOffline
	}
	if !isStatusAvailable(connector.Status) {
		return nil, repositories.ErrConnectorUnavailable
	}

	active, err := s.chargingRepo.GetActiveSessionByUserID(input.UserID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return nil, repositories.ErrActiveSessionExists
	}

	existing, err := s.chargingRepo.GetActiveSessionByConnector(input.ChargerID, input.ConnectorID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, repositories.ErrConnectorBusy
	}

	session := &models.ChargingSession{
		ChargerID:   input.ChargerID,
		ConnectorID: input.ConnectorID,
		UserID:      input.UserID,
		Status:      StateStarting,
	}
	if err := s.chargingRepo.CreateSession(session); err != nil {
		return nil, err
	}
	session.RemoteStartID = makeOCPPRemoteStartID(session.ID)
	if err := s.chargingRepo.UpdateRemoteStartID(session.ID, session.RemoteStartID); err != nil {
		return nil, err
	}

	user, err := s.userRepo.GetByID(input.UserID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, fmt.Errorf("user not found")
	}

	idTag := strings.TrimSpace(user.IDTag)
	if idTag == "" {
		idTag = makeOCPPIDTag(input.UserID)
		if err := s.userRepo.SetIDTag(input.UserID, idTag); err != nil {
			return nil, fmt.Errorf("persist idTag for user %q: %w", input.UserID, err)
		}
	}
	if s.authClient != nil {
		if err := s.authClient.EnsureAuthorization(ctx, idTag); err != nil {
			return nil, fmt.Errorf("ensure authorization for idTag %q: %w", idTag, err)
		}
	}

	startReq := StartTransactionRequest{
		Identifier:    input.ChargerID,
		IDTag:         idTag,
		ConnectorID:   intPtr(input.ConnectorID),
		RemoteStartID: session.RemoteStartID,
	}

	if err := s.client.StartTransaction(ctx, startReq); err != nil {
		s.logger.Warn("start transaction dispatch failed, scheduling retry", "session_id", session.ID, "error", err)
		s.enqueueStartRetry(session, startReq)
	} else {
		s.metrics.RecordStartRequest(true)
	}

	return s.chargingRepo.GetSessionByID(session.ID)
}

func (s *Service) SyncSessionFromActiveTransaction(ctx context.Context, sessionID string, publisher Publisher) {
	session, err := s.chargingRepo.GetSessionByID(sessionID)
	if err != nil || session == nil {
		return
	}
	go s.syncSessionFromActiveTransaction(session.ID, session.ChargerID, session.ConnectorID, publisher)
}

func (s *Service) ResolveConnectorStatus(ctx context.Context, chargerID string, connectorID int) (*models.ConnectorStatus, error) {
	connector, err := s.statusClient.GetConnectorStatus(ctx, chargerID, connectorID)
	if err != nil {
		s.logger.Warn("graphql connector status lookup failed", "charger_id", chargerID, "connector_id", connectorID, "error", err)
		return nil, nil
	}
	if connector == nil {
		return nil, nil
	}

	if err := s.chargingRepo.UpsertConnectorStatus(
		connector.ChargerID,
		connector.ConnectorID,
		connector.EVSEID,
		connector.Status,
		connector.Metadata,
		connector.LastSeen,
	); err != nil {
		s.logger.Warn("failed to cache graphql connector status", "charger_id", chargerID, "connector_id", connectorID, "error", err)
	}
	return connector, nil
}

func (s *Service) StopCharging(ctx context.Context, input StopSessionInput) (*models.ChargingSession, error) {
	session, err := s.chargingRepo.GetSessionByID(input.SessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, repositories.ErrSessionNotFound
	}
	if session.UserID != input.UserID {
		return nil, repositories.ErrForbiddenSession
	}
	if session.Status == StateStopped || session.Status == StateFailed {
		return nil, repositories.ErrSessionTerminal
	}
	if session.Status == StateStopping {
		return session, nil
	}
	if !CanTransition(session.Status, StateStopping) {
		return nil, fmt.Errorf("cannot stop session in status %s", session.Status)
	}

	if err := s.chargingRepo.UpdateSessionStopping(session.ID); err != nil {
		return nil, err
	}

	if session.OCPPTransactionID == "" {
		return s.chargingRepo.GetSessionByID(session.ID)
	}

	stopReq := StopTransactionRequest{
		Identifier:    session.ChargerID,
		TransactionID: parseInt(session.OCPPTransactionID),
	}
	s.metrics.RecordStopRequest(false)
	if err := s.client.StopTransaction(ctx, stopReq); err != nil {
		s.logger.Warn("stop transaction dispatch failed, scheduling retry", "session_id", session.ID, "error", err)
		s.enqueueStopRetry(session.ID, stopReq)
	} else {
		s.metrics.RecordStopRequest(true)
	}

	return s.chargingRepo.GetSessionByID(session.ID)
}

func (s *Service) pollRemoteStopResult(sessionID string, publisher Publisher) {
	defer func() {
		if err := s.chargingRepo.ReleaseStopPolling(sessionID); err != nil {
			s.logger.Warn("failed to release remote stop poll lease", "session_id", sessionID, "error", err)
		}
		s.stopPollMu.Lock()
		delete(s.stopPollers, sessionID)
		s.stopPollMu.Unlock()
	}()

	if s.statusClient == nil {
		return
	}

	time.Sleep(s.remoteStopInitialDelay)

	for attempt := 1; attempt <= s.remoteStopMaxAttempts; attempt++ {
		session, err := s.chargingRepo.GetSessionByID(sessionID)
		if err != nil {
			s.logger.Warn("failed to reload session during remote stop polling", "session_id", sessionID, "attempt", attempt, "error", err)
			return
		}
		if session == nil || session.Status != StateStopping {
			return
		}

		txRecord, err := s.resolveTransactionForStopPolling(context.Background(), session)
		if err != nil {
			s.logger.Warn("failed to fetch transaction during remote stop polling", "session_id", sessionID, "attempt", attempt, "transaction_id", session.OCPPTransactionID, "transaction_ref", session.TransactionRef, "error", err)
		} else if txRecord != nil {
			s.logger.Info("remote stop polling transaction snapshot",
				"session_id", sessionID,
				"attempt", attempt,
				"transaction_ref", session.TransactionRef,
				"transaction_id", txRecord.TransactionID,
				"source", txRecord.Source,
				"is_active", txRecord.IsActive,
				"charging_state", txRecord.ChargingState,
				"total_kwh", txRecord.TotalKwh,
				"updated_at", txRecord.UpdatedAt,
			)
		}
		if err == nil && txRecord != nil && (!txRecord.IsActive || isTerminalChargingState(txRecord.ChargingState)) {
			s.logger.Info("remote stop polling finalized transaction",
				"session_id", sessionID,
				"attempt", attempt,
				"transaction_ref", session.TransactionRef,
				"transaction_id", txRecord.TransactionID,
				"source", txRecord.Source,
				"is_active", txRecord.IsActive,
				"charging_state", txRecord.ChargingState,
				"total_kwh", txRecord.TotalKwh,
				"updated_at", txRecord.UpdatedAt,
			)
			finalSession, err := s.finalizeStoppedSessionFromTransaction(context.Background(), session.ID, txRecord)
			if err != nil {
				s.logger.Warn("failed to finalize stopped session from polled transaction", "session_id", sessionID, "attempt", attempt, "transaction_id", session.OCPPTransactionID, "error", err)
				return
			}
			if finalSession != nil && publisher != nil {
				publisher.Publish(finalSession.ID, sessionPublishPayload(finalSession, finalSession.ChargingState, false, txRecord.UpdatedAt))
			}
			return
		}

		if attempt < s.remoteStopMaxAttempts {
			time.Sleep(s.remoteStopPollInterval)
		}
	}

	session, err := s.chargingRepo.GetSessionByID(sessionID)
	if err != nil || session == nil || session.Status != StateStopping {
		return
	}
	if err := s.chargingRepo.UpdateSessionState(session.ID, []string{StateStopping}, StateCharging, "Remote stop timeout"); err != nil {
		s.logger.Warn("failed to revert session after remote stop timeout", "session_id", sessionID, "error", err)
		return
	}
	session.Status = StateCharging
	session.IsActive = true
	session.FailureReason = "Remote stop timeout"
	s.logger.Warn("remote stop timeout", "session_id", sessionID, "transaction_id", session.OCPPTransactionID)
	if publisher != nil {
		publisher.Publish(session.ID, sessionPublishPayload(session, session.ChargingState, session.IsActive, ""))
	}
}

func (s *Service) resolveTransactionForStopPolling(ctx context.Context, session *models.ChargingSession) (*ChargingTransaction, error) {
	if session == nil || s.statusClient == nil {
		return nil, nil
	}

	if txID := parseInt(strings.TrimSpace(session.TransactionRef)); txID > 0 {
		txRecord, err := s.statusClient.GetTransactionByID(ctx, txID)
		if err == nil && txRecord != nil {
			return txRecord, nil
		}
		if err != nil {
			s.logger.Warn("failed to fetch transaction by hasura id during remote stop polling", "session_id", session.ID, "transaction_ref", session.TransactionRef, "error", err)
		}
	}

	return s.statusClient.GetTransactionByOCPPTransactionID(ctx, session.OCPPTransactionID)
}

func (s *Service) RecoverStaleStarts(ctx context.Context, publisher Publisher) error {
	sessionIDs, err := s.chargingRepo.FailStaleStartingSessions(s.startTimeout)
	if err != nil {
		return err
	}
	for _, sessionID := range sessionIDs {
		if publisher != nil {
			publisher.Publish(sessionID, map[string]any{
				"status":         StateFailed,
				"failure_reason": "start timeout",
			})
		}
	}
	return nil
}

func (s *Service) ProcessWebhook(ctx context.Context, rawPayload []byte, envelope WebhookEnvelope, publisher Publisher) error {
	if !s.useWebhookEvents {
		s.logger.Info("ignoring charging webhook because webhook flow is disabled", "event_type", envelope.EventType)
		return nil
	}

	eventID := strings.TrimSpace(envelope.EventID)
	if eventID == "" {
		sum := sha256.Sum256(rawPayload)
		eventID = hex.EncodeToString(sum[:])
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	inserted, err := s.chargingRepo.RecordWebhookEventTx(tx, eventID, envelope.EventType, rawPayload)
	if err != nil {
		return err
	}
	if !inserted {
		s.metrics.RecordWebhookDuplicate()
		return tx.Commit()
	}

	var publishSessionID string
	var publishPayload map[string]any

	switch envelope.EventType {
	case "status_notification":
		var evt StatusNotificationEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil {
			return err
		}
		if evt.ChargerID == "" || evt.ConnectorID <= 0 || evt.Status == "" {
			return errors.New("invalid status_notification payload")
		}
		if err := s.chargingRepo.UpsertConnectorStatusTx(tx, evt.ChargerID, evt.ConnectorID, evt.EVSEID, evt.Status, string(evt.Metadata)); err != nil {
			return err
		}
	case "remote_start_result":
		var evt RemoteStartResultEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil {
			return err
		}
		if evt.SessionID == "" {
			return errors.New("invalid remote_start_result payload")
		}
		if !evt.Accepted {
			if err := s.chargingRepo.UpdateSessionStateTx(tx, evt.SessionID, []string{StateStarting}, StateFailed, evt.Reason); err != nil {
				return err
			}
			publishSessionID = evt.SessionID
			publishPayload = map[string]any{"status": StateFailed, "failure_reason": evt.Reason}
		}
	case "remote_stop_result":
		var evt RemoteStopResultEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil {
			return err
		}
		if evt.SessionID == "" {
			return errors.New("invalid remote_stop_result payload")
		}
		if !evt.Accepted {
			if err := s.chargingRepo.UpdateSessionStateTx(tx, evt.SessionID, []string{StateStopping}, StateCharging, evt.Reason); err != nil {
				return err
			}
			publishSessionID = evt.SessionID
			publishPayload = map[string]any{"status": StateCharging, "failure_reason": evt.Reason}
		}
	case "start_transaction":
		var evt StartTransactionEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil {
			return err
		}
		if evt.ChargerID == "" || evt.ConnectorID <= 0 || evt.TransactionID == "" {
			return errors.New("invalid start_transaction payload")
		}
		session, err := s.chargingRepo.AttachTransactionTx(tx, evt.ChargerID, evt.ConnectorID, evt.TransactionID, evt.MeterStartKwh, s.costPerKwh)
		if err != nil {
			return err
		}
		if session != nil {
			publishSessionID = session.ID
			publishPayload = sessionPublishPayload(session, session.ChargingState, session.IsActive, "")
			if session.Status == StateStopping {
				stopReq := StopTransactionRequest{Identifier: session.ChargerID, TransactionID: parseInt(session.OCPPTransactionID)}
				defer s.enqueueStopRetry(session.ID, stopReq)
			}
		}
	case "meter_values":
		var evt MeterValuesEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil {
			return err
		}
		if evt.TransactionID == "" {
			return errors.New("invalid meter_values payload")
		}
		session, err := s.chargingRepo.UpdateSessionMeterValuesTx(tx, evt.TransactionID, evt.EnergyKwh, "", true, s.costPerKwh)
		if err != nil {
			return err
		}
		if session != nil {
			publishSessionID = session.ID
			publishPayload = sessionPublishPayload(session, session.ChargingState, session.IsActive, "")
		}
	case "stop_transaction":
		var evt StopTransactionEvent
		if err := json.Unmarshal(envelope.Data, &evt); err != nil {
			return err
		}
		if evt.TransactionID == "" {
			return errors.New("invalid stop_transaction payload")
		}
		session, err := s.chargingRepo.GetSessionByTransactionIDTx(tx, evt.TransactionID)
		if err != nil {
			return err
		}
		var txRecord *ChargingTransaction
		if s.statusClient != nil {
			txRecord, err = s.statusClient.GetTransactionByOCPPTransactionID(ctx, evt.TransactionID)
			if err != nil {
				s.logger.Warn("hasura transaction lookup failed for stop_transaction", "transaction_id", evt.TransactionID, "error", err)
			}
		}
		if session == nil && txRecord != nil {
			session, err = s.chargingRepo.GetLatestSessionByChargerConnectorTx(tx, txRecord.StationID, txRecord.ConnectorID, []string{StateStopping, StateCharging, StateStarting})
			if err != nil {
				return err
			}
		}
		if session != nil {
			energy := evt.MeterStopKwh
			if txRecord != nil && txRecord.TotalKwh > 0 {
				energy = txRecord.TotalKwh
			}
			if energy <= 0 {
				energy = session.EnergyKwh
			}
			cost := roundCurrency(energy * s.costPerKwh)
			billed, billErr := s.walletRepo.DebitWalletForSessionTx(tx, session.UserID, cost, session.ID)
			finalChargingState := session.ChargingState
			if txRecord != nil && strings.TrimSpace(txRecord.ChargingState) != "" {
				finalChargingState = txRecord.ChargingState
			}
			if err := s.chargingRepo.FinalizeStoppedSessionTx(tx, session.ID, energy, cost, billed, billErr, finalChargingState); err != nil {
				return err
			}
			publishSessionID = session.ID
			session.Status = StateStopped
			session.EnergyKwh = energy
			session.Cost = cost
			session.IsActive = false
			session.ChargingState = finalChargingState
			publishPayload = sessionPublishPayload(session, session.ChargingState, false, "")
			startTime, endTime := parseSessionTimes(session.StartTime, time.Now().UTC().Format(time.RFC3339))
			s.metrics.RecordChargingDuration(startTime, endTime)
			if billErr != nil {
				s.logger.Error("wallet debit deferred after stop", "session_id", session.ID, "error", billErr)
			}
		}
	default:
		return fmt.Errorf("unsupported event_type: %s", envelope.EventType)
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	if publisher != nil && publishSessionID != "" && publishPayload != nil {
		publisher.Publish(publishSessionID, publishPayload)
	}
	return nil
}

func (s *Service) enqueueStartRetry(session *models.ChargingSession, req StartTransactionRequest) {
	s.retryQueue.Enqueue(RetryTask{
		Key:    "start:" + session.ID,
		Action: "start_transaction",
		Execute: func(ctx context.Context) error {
			return s.client.StartTransaction(ctx, req)
		},
	})
}

func (s *Service) enqueueStopRetry(sessionID string, req StopTransactionRequest) {
	s.retryQueue.Enqueue(RetryTask{
		Key:    "stop:" + sessionID,
		Action: "stop_transaction",
		Execute: func(ctx context.Context) error {
			return s.client.StopTransaction(ctx, req)
		},
	})
}

func isStatusAvailable(status string) bool {
	return strings.EqualFold(status, "Available")
}

func isConnectorOnline(connector *models.ConnectorStatus) bool {
	if connector == nil || connector.Metadata == "" {
		return true
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(connector.Metadata), &metadata); err != nil {
		return true
	}
	value, ok := metadata["isOnline"]
	if !ok {
		return true
	}
	online, ok := value.(bool)
	if !ok {
		return true
	}
	return online
}

func parseInt64(raw string) int64 {
	value, _ := strconv.ParseInt(raw, 10, 64)
	return value
}

func parseInt(raw string) int {
	value, _ := strconv.Atoi(raw)
	return value
}

func intPtr(v int) *int {
	return &v
}

func roundCurrency(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

func parseSessionTimes(startRaw, endRaw string) (time.Time, time.Time) {
	start, _ := time.Parse(time.RFC3339, startRaw)
	end, _ := time.Parse(time.RFC3339, endRaw)
	return start, end
}

func (s *Service) finalizeStoppedSessionFromTransaction(ctx context.Context, sessionID string, txRecord *ChargingTransaction) (*models.ChargingSession, error) {
	if txRecord == nil {
		return nil, nil
	}

	dbtx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = dbtx.Rollback()
	}()

	session, err := s.chargingRepo.GetSessionByIDTx(dbtx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		if err := dbtx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if session.Status == StateStopped {
		if err := dbtx.Commit(); err != nil {
			return nil, err
		}
		return session, nil
	}
	if session.Status != StateStopping {
		if err := dbtx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	energy := txRecord.TotalKwh
	if energy <= 0 {
		energy = session.EnergyKwh
	}
	cost := roundCurrency(energy * s.costPerKwh)
	billed, billErr := s.walletRepo.DebitWalletForSessionTx(dbtx, session.UserID, cost, session.ID)
	if err := s.chargingRepo.FinalizeStoppedSessionTx(dbtx, session.ID, energy, cost, billed, billErr, txRecord.ChargingState); err != nil {
		return nil, err
	}

	if err := dbtx.Commit(); err != nil {
		return nil, err
	}

	session.Status = StateStopped
	session.EnergyKwh = energy
	session.Cost = cost
	session.IsActive = false
	session.ChargingState = txRecord.ChargingState
	startTime, endTime := parseSessionTimes(session.StartTime, time.Now().UTC().Format(time.RFC3339))
	s.metrics.RecordChargingDuration(startTime, endTime)
	if billErr != nil {
		s.logger.Error("wallet debit deferred after polled stop finalization", "session_id", session.ID, "error", billErr)
	}
	return session, nil
}

func (s *Service) syncSessionFromActiveTransaction(sessionID string, chargerID string, connectorID int, publisher Publisher) {
	if s.statusClient == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.syncTimeout)
	defer cancel()

	ticker := time.NewTicker(s.syncInterval)
	defer ticker.Stop()

	for {
		session, err := s.chargingRepo.GetSessionByID(sessionID)
		if err != nil {
			s.logger.Warn("failed to reload session during transaction sync", "session_id", sessionID, "error", err)
			return
		}
		if session == nil || session.Status == StateFailed || session.Status == StateStopped {
			return
		}

		select {
		case <-ctx.Done():
			s.logger.Warn("active transaction sync timed out", "session_id", sessionID, "charger_id", chargerID, "connector_id", connectorID)
			return
		default:
		}

		done, err := s.trySyncSessionFromActiveTransaction(ctx, session, publisher)
		if err != nil {
			s.logger.Warn("active transaction sync failed", "session_id", sessionID, "charger_id", chargerID, "connector_id", connectorID, "error", err)
		}
		if done {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) trySyncSessionFromActiveTransaction(ctx context.Context, session *models.ChargingSession, publisher Publisher) (bool, error) {
	hasuraTransactionID := parseInt(session.TransactionRef)

	if hasuraTransactionID == 0 {
		mapped, err := s.findTransactionForStartingSession(ctx, session)
		if err != nil {
			return false, err
		}
		if mapped == nil {
			return false, nil
		}
		hasuraTransactionID = mapped.ID
	}

	txRecord, err := s.statusClient.GetTransactionByID(ctx, hasuraTransactionID)
	if err != nil {
		return false, err
	}
	if txRecord == nil {
		return false, nil
	}

	nextSession, finalized, err := s.applyTransactionSnapshot(ctx, session, txRecord)
	if err != nil {
		return false, err
	}
	if session.Status == StateStopping && nextSession != nil && strings.TrimSpace(nextSession.OCPPTransactionID) != "" && strings.TrimSpace(session.OCPPTransactionID) == "" {
		stopReq := StopTransactionRequest{
			Identifier:    nextSession.ChargerID,
			TransactionID: parseInt(nextSession.OCPPTransactionID),
		}
		s.enqueueStopRetry(nextSession.ID, stopReq)
	}
	if nextSession != nil && publisher != nil {
		publisher.Publish(nextSession.ID, sessionPublishPayload(nextSession, txRecord.ChargingState, txRecord.IsActive, txRecord.UpdatedAt))
	}
	return finalized || isTerminalChargingState(txRecord.ChargingState) || !txRecord.IsActive, nil
}

func (s *Service) findTransactionForStartingSession(ctx context.Context, session *models.ChargingSession) (*ActiveTransaction, error) {
	if strings.TrimSpace(session.RemoteStartID) != "" {
		match, err := s.statusClient.GetTransactionByRemoteStartID(ctx, parseInt(session.RemoteStartID))
		if err != nil {
			return nil, err
		}
		if match != nil {
			return match, nil
		}
	}
	if session.Status != StateStarting {
		if session.Status != StateStopping {
			return nil, nil
		}
	}

	fallbackMatches, err := s.statusClient.FindFallbackTransactions(ctx, session.ChargerID, session.ConnectorID, session.StartTime)
	if err != nil {
		return nil, err
	}
	if len(fallbackMatches) == 0 {
		return nil, nil
	}
	if len(fallbackMatches) > 1 {
		s.logger.Warn("multiple fallback transactions matched starting session; skipping attach",
			"session_id", session.ID,
			"charger_id", session.ChargerID,
			"connector_id", session.ConnectorID,
			"match_count", len(fallbackMatches),
		)
		return nil, nil
	}
	return &fallbackMatches[0], nil
}

func (s *Service) applyTransactionSnapshot(ctx context.Context, session *models.ChargingSession, txRecord *ChargingTransaction) (*models.ChargingSession, bool, error) {
	dbtx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		_ = dbtx.Rollback()
	}()

	nextSession := session
	transactionRef := strconv.Itoa(txRecord.ID)
	ocppTransactionID := strings.TrimSpace(txRecord.TransactionID)

	if strings.TrimSpace(session.TransactionRef) == "" {
		nextSession, err = s.chargingRepo.AttachTransactionToSessionTx(
			dbtx,
			session.ID,
			session.ChargerID,
			session.ConnectorID,
			transactionRef,
			ocppTransactionID,
			txRecord.TotalKwh,
			txRecord.ChargingState,
			txRecord.IsActive,
			s.costPerKwh,
		)
		if err != nil {
			return nil, false, err
		}
	} else if session.TransactionRef != transactionRef {
		if err := dbtx.Commit(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}

	if nextSession == nil {
		nextSession, err = s.chargingRepo.UpdateSessionMeterValuesTx(dbtx, transactionRef, txRecord.TotalKwh, txRecord.ChargingState, txRecord.IsActive, s.costPerKwh)
		if err != nil {
			return nil, false, err
		}
	}
	if nextSession == nil {
		if err := dbtx.Commit(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}

	finalized := false
	if !txRecord.IsActive || isTerminalChargingState(txRecord.ChargingState) {
		cost := roundCurrency(txRecord.TotalKwh * s.costPerKwh)
		billed, billErr := s.walletRepo.DebitWalletForSessionTx(dbtx, nextSession.UserID, cost, nextSession.ID)
		if err := s.chargingRepo.FinalizeStoppedSessionTx(dbtx, nextSession.ID, txRecord.TotalKwh, cost, billed, billErr, txRecord.ChargingState); err != nil {
			return nil, false, err
		}
		startTime, endTime := parseSessionTimes(nextSession.StartTime, time.Now().UTC().Format(time.RFC3339))
		s.metrics.RecordChargingDuration(startTime, endTime)
		if billErr != nil {
			s.logger.Error("wallet debit deferred after graphql transaction sync", "session_id", nextSession.ID, "error", billErr)
		}
		finalized = true
		nextSession.Status = StateStopped
		nextSession.EnergyKwh = txRecord.TotalKwh
		nextSession.Cost = cost
		nextSession.IsActive = false
		nextSession.ChargingState = txRecord.ChargingState
	} else {
		if session.Status == StateStopping {
			nextSession.Status = StateStopping
		} else {
			nextSession.Status = StateCharging
		}
		nextSession.EnergyKwh = txRecord.TotalKwh
		nextSession.Cost = roundCurrency(txRecord.TotalKwh * s.costPerKwh)
		nextSession.IsActive = txRecord.IsActive
		nextSession.ChargingState = txRecord.ChargingState
		nextSession.TransactionRef = transactionRef
		nextSession.OCPPTransactionID = ocppTransactionID
	}

	if err := dbtx.Commit(); err != nil {
		return nil, false, err
	}
	return nextSession, finalized, nil
}

func isTerminalChargingState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "stopped", "finished", "completed", "ended", "terminated":
		return true
	default:
		return false
	}
}

func sessionPublishPayload(session *models.ChargingSession, chargingState string, isActive bool, updatedAt string) map[string]any {
	resolvedState := firstNonEmptyString(chargingState, session.ChargingState)
	resolvedActive := session.IsActive
	if strings.TrimSpace(chargingState) != "" || updatedAt != "" {
		resolvedActive = isActive
	}
	return map[string]any{
		"status":              session.Status,
		"energy_kwh":          session.EnergyKwh,
		"cost":                session.Cost,
		"transaction_ref":     session.TransactionRef,
		"transaction_id":      session.OCPPTransactionID,
		"charging_state":      resolvedState,
		"is_active":           resolvedActive,
		"transaction_updated": updatedAt,
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func makeOCPPIDTag(userID string) string {
	trimmed := strings.TrimSpace(userID)
	if trimmed == "" {
		return "vajra"
	}
	if len(trimmed) <= MaxOCPPIDTagLength {
		return trimmed
	}

	sum := sha1.Sum([]byte(trimmed))
	encoded := hex.EncodeToString(sum[:])
	return "u" + encoded[:MaxOCPPIDTagLength-1]
}

func makeOCPPRemoteStartID(sessionID string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(sessionID)))
	value := int(sum[0])<<24 | int(sum[1])<<16 | int(sum[2])<<8 | int(sum[3])
	if value < 0 {
		value = -value
	}
	value = value % MaxOCPPRemoteStartID
	if value == 0 {
		value = 1
	}
	return strconv.Itoa(value)
}
