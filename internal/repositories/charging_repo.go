package repositories

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"vajraBackend/internal/models"

	"github.com/jmoiron/sqlx"
)

var (
	ErrConnectorStatusUnknown = errors.New("connector status unknown")
	ErrConnectorUnavailable   = errors.New("connector not available")
	ErrChargerOffline         = errors.New("charger is offline")
	ErrActiveSessionExists    = errors.New("active session already exists")
	ErrConnectorBusy          = errors.New("connector already has an active session")
	ErrSessionNotFound        = errors.New("session not found")
	ErrForbiddenSession       = errors.New("session does not belong to the user")
	ErrSessionTerminal        = errors.New("session is already finalized")
)

type ChargingRepository struct {
	db *sqlx.DB
}

func NewChargingRepository(db *sqlx.DB) *ChargingRepository {
	return &ChargingRepository{db: db}
}

func (r *ChargingRepository) CreateSession(session *models.ChargingSession) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	insertQuery := `
		INSERT INTO charging_sessions (charger_id, connector_id, user_id, status, is_active)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id,
		          start_time::text,
		          status
	`
	if err := tx.QueryRowx(insertQuery, session.ChargerID, session.ConnectorID, session.UserID, session.Status, false).
		Scan(&session.ID, &session.StartTime, &session.Status); err != nil {
		return err
	}

	return tx.Commit()
}

func (r *ChargingRepository) UpdateRemoteStartID(sessionID string, remoteStartID string) error {
	_, err := r.db.Exec(`
		UPDATE charging_sessions
		SET remote_start_id = $2
		WHERE id = $1
	`, sessionID, nullIfEmpty(remoteStartID))
	return err
}

func (r *ChargingRepository) GetSessionByID(id string) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	err := r.db.Get(&session, sessionSelectQuery(`WHERE id = $1`), id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) GetSessionByIDTx(tx *sqlx.Tx, id string) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	err := tx.Get(&session, sessionSelectQuery(`WHERE id = $1`), id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) GetSessionByTransactionID(transactionRef string) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	err := r.db.Get(&session, sessionSelectQuery(`WHERE transaction_ref = $1 OR ocpp_transaction_id = $1 ORDER BY start_time DESC LIMIT 1`), transactionRef)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) GetSessionByTransactionIDTx(tx *sqlx.Tx, transactionRef string) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	err := tx.Get(&session, sessionSelectQuery(`WHERE transaction_ref = $1 OR ocpp_transaction_id = $1 ORDER BY start_time DESC LIMIT 1`), transactionRef)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) GetLatestSessionByChargerConnectorTx(tx *sqlx.Tx, chargerID string, connectorID int, statuses []string) (*models.ChargingSession, error) {
	if len(statuses) == 0 {
		return nil, nil
	}

	placeholders := make([]string, 0, len(statuses))
	args := make([]any, 0, len(statuses)+2)
	args = append(args, chargerID, connectorID)
	for i, status := range statuses {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+3))
		args = append(args, status)
	}

	query := sessionSelectQuery(fmt.Sprintf(
		`WHERE charger_id = $1 AND connector_id = $2 AND status IN (%s) ORDER BY start_time DESC LIMIT 1`,
		strings.Join(placeholders, ", "),
	))

	session := models.ChargingSession{}
	err := tx.Get(&session, query, args...)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) GetActiveSessionByUserID(userID string) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	err := r.db.Get(&session, sessionSelectQuery(`WHERE user_id = $1 AND status IN ('pending', 'starting', 'charging', 'stopping') ORDER BY start_time DESC LIMIT 1`), userID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) GetActiveSessionByConnector(chargerID string, connectorID int) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	err := r.db.Get(&session, sessionSelectQuery(`WHERE charger_id = $1 AND connector_id = $2 AND status IN ('pending', 'starting', 'charging', 'stopping') ORDER BY start_time DESC LIMIT 1`), chargerID, connectorID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) ListSessionsByUserID(userID string, status string) ([]models.ChargingSession, error) {
	sessions := []models.ChargingSession{}
	query := sessionSelectQuery(`WHERE user_id = $1`)
	args := []any{userID}
	if status != "" {
		query = sessionSelectQuery(`WHERE user_id = $1 AND status = $2`)
		args = append(args, status)
	}
	query += ` ORDER BY start_time DESC`
	err := r.db.Select(&sessions, query, args...)
	return sessions, err
}

func (r *ChargingRepository) UpdateSessionState(id string, allowedFrom []string, toStatus string, failureReason string) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := r.UpdateSessionStateTx(tx, id, allowedFrom, toStatus, failureReason); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *ChargingRepository) UpdateSessionStateTx(tx *sqlx.Tx, id string, allowedFrom []string, toStatus string, failureReason string) error {
	if len(allowedFrom) == 0 {
		return errors.New("allowedFrom cannot be empty")
	}
	placeholders := make([]string, 0, len(allowedFrom))
	args := make([]any, 0, len(allowedFrom)+3)
	args = append(args, toStatus, nullIfEmpty(failureReason), id)
	for i, status := range allowedFrom {
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+4))
		args = append(args, status)
	}

	query := fmt.Sprintf(`
		UPDATE charging_sessions
		SET status = $1,
		    failure_reason = COALESCE($2, failure_reason)
		WHERE id = $3
		  AND status IN (%s)
	`, strings.Join(placeholders, ", "))
	result, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return nil
	}
	return nil
}

func (r *ChargingRepository) UpdateSessionStopping(id string) error {
	query := `
		UPDATE charging_sessions
		SET status = 'stopping',
		    stop_requested_at = NOW(),
		    stop_poll_claimed_at = NULL
		WHERE id = $1
		  AND status IN ('starting', 'charging')
	`
	_, err := r.db.Exec(query, id)
	return err
}

func (r *ChargingRepository) ClaimStopPolling(sessionID string, lease time.Duration) (bool, error) {
	if lease <= 0 {
		lease = time.Minute
	}
	result, err := r.db.Exec(`
		UPDATE charging_sessions
		SET stop_poll_claimed_at = NOW()
		WHERE id = $1
		  AND status = 'stopping'
		  AND (
			stop_poll_claimed_at IS NULL
			OR stop_poll_claimed_at <= NOW() - ($2 * INTERVAL '1 second')
		  )
	`, sessionID, lease.Seconds())
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (r *ChargingRepository) ReleaseStopPolling(sessionID string) error {
	_, err := r.db.Exec(`
		UPDATE charging_sessions
		SET stop_poll_claimed_at = NULL
		WHERE id = $1
	`, sessionID)
	return err
}

func (r *ChargingRepository) AttachTransactionTx(tx *sqlx.Tx, chargerID string, connectorID int, transactionRef string, meterStartKwh float64, costPerKwh float64) (*models.ChargingSession, error) {
	return r.AttachTransactionToSessionTx(tx, "", chargerID, connectorID, transactionRef, transactionRef, meterStartKwh, "Charging", true, costPerKwh)
}

func (r *ChargingRepository) AttachTransactionToSessionTx(tx *sqlx.Tx, sessionID string, chargerID string, connectorID int, transactionRef string, ocppTransactionID string, energyKwh float64, chargingState string, isActive bool, costPerKwh float64) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	query := `
		WITH target AS (
			SELECT id, status, transaction_ref
			FROM charging_sessions
			WHERE charger_id = $1
			  AND connector_id = $2
			  AND status IN ('starting', 'stopping')
			  AND ($3 = '' OR id = $3::uuid)
			ORDER BY start_time DESC
			LIMIT 1
			FOR UPDATE
		)
		UPDATE charging_sessions cs
		SET transaction_ref = CASE
		        WHEN COALESCE(target.transaction_ref, '') = '' THEN $4
		        ELSE cs.transaction_ref
		    END,
		    ocpp_transaction_id = CASE
		        WHEN COALESCE(target.transaction_ref, '') = '' THEN NULLIF($5, '')
		        ELSE cs.ocpp_transaction_id
		    END,
		    energy_kwh = $6,
		    cost = $7,
		    charging_state = NULLIF($8, ''),
		    is_active = $9,
		    status = CASE
		        WHEN target.status = 'stopping' THEN 'stopping'
		        WHEN COALESCE(target.transaction_ref, '') = '' OR target.transaction_ref = $4 THEN 'charging'
		        ELSE cs.status
		    END
		FROM target
		WHERE cs.id = target.id
		  AND (COALESCE(target.transaction_ref, '') = '' OR target.transaction_ref = $4)
		RETURNING cs.id,
		          cs.charger_id,
		          cs.connector_id,
		          cs.user_id,
		          cs.start_time::text AS start_time,
		          COALESCE(cs.end_time::text, '') AS end_time,
		          cs.energy_kwh,
		          cs.cost,
		          cs.status,
		          COALESCE(cs.transaction_id, 0) AS transaction_id,
		          COALESCE(cs.transaction_ref, '') AS transaction_ref,
		          COALESCE(cs.ocpp_transaction_id, '') AS ocpp_transaction_id,
		          COALESCE(cs.remote_start_id::text, '') AS remote_start_id,
		          COALESCE(cs.charging_state, '') AS charging_state,
		          COALESCE(cs.is_active, false) AS is_active,
		          COALESCE(cs.failure_reason, '') AS failure_reason,
		          COALESCE(cs.stop_requested_at::text, '') AS stop_requested_at,
		          COALESCE(cs.billed_at::text, '') AS billed_at
	`
	err := tx.Get(&session, query, chargerID, connectorID, sessionID, transactionRef, ocppTransactionID, energyKwh, roundToTwo(energyKwh*costPerKwh), chargingState, isActive)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) UpdateSessionMeterValuesTx(tx *sqlx.Tx, transactionRef string, energyKwh float64, chargingState string, isActive bool, costPerKwh float64) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	query := `
		UPDATE charging_sessions
		SET energy_kwh = $2,
		    cost = $3,
		    charging_state = NULLIF($4, ''),
		    is_active = $5,
		    status = CASE WHEN status = 'starting' THEN 'charging' ELSE status END
		WHERE transaction_ref = $1
		   OR ocpp_transaction_id = $1
		RETURNING id,
		          charger_id,
		          connector_id,
		          user_id,
		          start_time::text AS start_time,
		          COALESCE(end_time::text, '') AS end_time,
		          energy_kwh,
		          cost,
		          status,
		          COALESCE(transaction_id, 0) AS transaction_id,
		          COALESCE(transaction_ref, '') AS transaction_ref,
		          COALESCE(ocpp_transaction_id, '') AS ocpp_transaction_id,
		          COALESCE(remote_start_id::text, '') AS remote_start_id,
		          COALESCE(charging_state, '') AS charging_state,
		          COALESCE(is_active, false) AS is_active,
		          COALESCE(failure_reason, '') AS failure_reason,
		          COALESCE(stop_requested_at::text, '') AS stop_requested_at,
		          COALESCE(billed_at::text, '') AS billed_at
	`
	err := tx.Get(&session, query, transactionRef, energyKwh, roundToTwo(energyKwh*costPerKwh), chargingState, isActive)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) FinalizeStoppedSessionTx(tx *sqlx.Tx, sessionID string, energyKwh float64, cost float64, billed bool, billErr error, chargingState string) error {
	query := `
		UPDATE charging_sessions
		SET status = 'stopped',
		    end_time = COALESCE(end_time, NOW()),
		    energy_kwh = $2,
		    cost = $3,
		    billed_at = CASE WHEN $4 THEN NOW() ELSE billed_at END,
		    failure_reason = CASE WHEN $5 <> '' THEN $5 ELSE failure_reason END,
		    charging_state = COALESCE(NULLIF($6, ''), charging_state),
		    is_active = false
		WHERE id = $1
	`
	reason := ""
	if billErr != nil {
		reason = billErr.Error()
	}
	_, err := tx.Exec(query, sessionID, energyKwh, cost, billed, reason, chargingState)
	return err
}

func (r *ChargingRepository) FailStaleStartingSessions(timeout time.Duration) ([]string, error) {
	rows, err := r.db.Queryx(`
		UPDATE charging_sessions
		SET status = 'failed',
		    failure_reason = 'start timeout'
		WHERE status = 'starting'
		  AND start_time <= NOW() - ($1 * INTERVAL '1 second')
		RETURNING id
	`, timeout.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, err
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	return sessionIDs, rows.Err()
}

func (r *ChargingRepository) RecordWebhookEventTx(tx *sqlx.Tx, eventID string, eventType string, payload []byte) (bool, error) {
	result, err := tx.Exec(`
		INSERT INTO ocpp_webhook_events (event_id, event_type, payload)
		VALUES ($1, $2, $3::jsonb)
		ON CONFLICT (event_id) DO NOTHING
	`, eventID, eventType, string(payload))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (r *ChargingRepository) UpsertCharger(id string, status string) error {
	_, err := r.db.Exec(`
		INSERT INTO chargers (id, status, last_seen)
		VALUES ($1, $2, NOW())
		ON CONFLICT (id)
		DO UPDATE SET status = EXCLUDED.status, last_seen = NOW()
	`, id, status)
	return err
}

func (r *ChargingRepository) UpsertConnectorStatus(chargerID string, connectorID int, evseID *int, status string, metadata string, lastSeen string) error {
	if err := r.UpsertCharger(chargerID, status); err != nil {
		return err
	}
	query := `
		INSERT INTO charger_connectors (charger_id, connector_id, evse_id, status, last_seen, metadata)
		VALUES (
			$1,
			$2,
			$3,
			$4,
			COALESCE(NULLIF($5, '')::timestamptz, NOW()),
			COALESCE(NULLIF($6, '')::jsonb, '{}'::jsonb)
		)
		ON CONFLICT (charger_id, connector_id)
		DO UPDATE SET
			evse_id = EXCLUDED.evse_id,
			status = EXCLUDED.status,
			last_seen = EXCLUDED.last_seen,
			metadata = EXCLUDED.metadata
	`
	_, err := r.db.Exec(query, chargerID, connectorID, evseID, status, lastSeen, metadata)
	return err
}

func (r *ChargingRepository) UpsertConnectorStatusTx(tx *sqlx.Tx, chargerID string, connectorID int, evseID *int, status string, metadata string) error {
	if err := r.UpsertCharger(chargerID, status); err != nil {
		return err
	}
	_, err := tx.Exec(`
		INSERT INTO charger_connectors (charger_id, connector_id, evse_id, status, last_seen, metadata)
		VALUES ($1, $2, $3, $4, NOW(), COALESCE(NULLIF($5, ''), '{}'::jsonb))
		ON CONFLICT (charger_id, connector_id)
		DO UPDATE SET
			evse_id = EXCLUDED.evse_id,
			status = EXCLUDED.status,
			last_seen = NOW(),
			metadata = EXCLUDED.metadata
	`, chargerID, connectorID, evseID, status, metadata)
	return err
}

func (r *ChargingRepository) GetConnectorStatus(chargerID string, connectorID int) (*models.ConnectorStatus, error) {
	status := models.ConnectorStatus{}
	err := r.db.Get(&status, `
		SELECT charger_id,
		       connector_id,
		       evse_id,
		       status,
		       last_seen::text,
		       metadata::text
		FROM charger_connectors
		WHERE charger_id = $1 AND connector_id = $2
	`, chargerID, connectorID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &status, err
}

func sessionSelectQuery(whereClause string) string {
	return `
		SELECT id,
		       charger_id,
		       connector_id,
		       user_id,
		       start_time::text,
		       COALESCE(end_time::text, '') AS end_time,
		       energy_kwh,
		       cost,
		       status,
		       COALESCE(transaction_id, 0) AS transaction_id,
		       COALESCE(transaction_ref, '') AS transaction_ref,
		       COALESCE(ocpp_transaction_id, '') AS ocpp_transaction_id,
		       COALESCE(remote_start_id::text, '') AS remote_start_id,
		       COALESCE(charging_state, '') AS charging_state,
		       COALESCE(is_active, false) AS is_active,
		       COALESCE(failure_reason, '') AS failure_reason,
		       COALESCE(stop_requested_at::text, '') AS stop_requested_at,
		       COALESCE(stop_poll_claimed_at::text, '') AS stop_poll_claimed_at,
		       COALESCE(billed_at::text, '') AS billed_at
		FROM charging_sessions
	` + whereClause
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func roundToTwo(value float64) float64 {
	return math.Round(value*100) / 100
}
