package repositories

import (
	"database/sql"

	"vajraBackend/internal/models"

	"github.com/jmoiron/sqlx"
)

type ChargingRepository struct {
	db *sqlx.DB
}

func NewChargingRepository(db *sqlx.DB) *ChargingRepository {
	return &ChargingRepository{db}
}

func (r *ChargingRepository) CreateSession(session *models.ChargingSession) error {
	query := `
		INSERT INTO charging_sessions (charger_id, connector_id, user_id, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id, start_time, status
	`
	return r.db.QueryRowx(query, session.ChargerID, session.ConnectorID, session.UserID, session.Status).Scan(&session.ID, &session.StartTime, &session.Status)
}

func (r *ChargingRepository) GetSessionByID(id string) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	query := `
		SELECT id, charger_id, connector_id, user_id, start_time, COALESCE(end_time::text, '') AS end_time, energy_kwh, cost, status, COALESCE(transaction_id, 0) AS transaction_id
		FROM charging_sessions
		WHERE id = $1
	`
	err := r.db.Get(&session, query, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) GetSessionByTransactionID(transactionID int) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	query := `
		SELECT id, charger_id, connector_id, user_id, start_time, COALESCE(end_time::text, '') AS end_time, energy_kwh, cost, status, COALESCE(transaction_id, 0) AS transaction_id
		FROM charging_sessions
		WHERE transaction_id = $1
		ORDER BY start_time DESC
		LIMIT 1
	`
	err := r.db.Get(&session, query, transactionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) GetActiveSessionByUserID(userID string) (*models.ChargingSession, error) {
	session := models.ChargingSession{}
	query := `
		SELECT id, charger_id, connector_id, user_id, start_time, COALESCE(end_time::text, '') AS end_time, energy_kwh, cost, status, COALESCE(transaction_id, 0) AS transaction_id
		FROM charging_sessions
		WHERE user_id = $1 AND status IN ('pending', 'starting', 'charging', 'stopping')
		ORDER BY start_time DESC
		LIMIT 1
	`
	err := r.db.Get(&session, query, userID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (r *ChargingRepository) ListSessionsByUserID(userID string, status string) ([]models.ChargingSession, error) {
	sessions := []models.ChargingSession{}
	if status == "" {
		query := `
			SELECT id, charger_id, connector_id, user_id, start_time, COALESCE(end_time::text, '') AS end_time, energy_kwh, cost, status, COALESCE(transaction_id, 0) AS transaction_id
			FROM charging_sessions
			WHERE user_id = $1
			ORDER BY start_time DESC
		`
		err := r.db.Select(&sessions, query, userID)
		return sessions, err
	}

	query := `
		SELECT id, charger_id, connector_id, user_id, start_time, COALESCE(end_time::text, '') AS end_time, energy_kwh, cost, status, COALESCE(transaction_id, 0) AS transaction_id
		FROM charging_sessions
		WHERE user_id = $1 AND status = $2
		ORDER BY start_time DESC
	`
	err := r.db.Select(&sessions, query, userID, status)
	return sessions, err
}

func (r *ChargingRepository) UpdateSessionStatus(id, status string) error {
	query := `UPDATE charging_sessions SET status = $1 WHERE id = $2`
	_, err := r.db.Exec(query, status, id)
	return err
}

func (r *ChargingRepository) UpdateSessionForStartTransaction(chargerID string, connectorID int, transactionID int) (string, error) {
	var sessionID string
	query := `
		UPDATE charging_sessions
		SET transaction_id = $1, status = 'charging'
		WHERE id = (
			SELECT id FROM charging_sessions
			WHERE charger_id = $2 AND connector_id = $3 AND status IN ('pending', 'starting')
			ORDER BY start_time DESC
			LIMIT 1
		)
		RETURNING id
	`
	if err := r.db.QueryRowx(query, transactionID, chargerID, connectorID).Scan(&sessionID); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return sessionID, nil
}

func (r *ChargingRepository) UpdateSessionEnergyCost(id string, energy float64, cost float64) error {
	query := `
		UPDATE charging_sessions
		SET energy_kwh = $1, cost = $2
		WHERE id = $3
	`
	_, err := r.db.Exec(query, energy, cost, id)
	return err
}

func (r *ChargingRepository) StopSession(id string, energy float64, cost float64) error {
	query := `
		UPDATE charging_sessions
		SET status = 'stopped', end_time = NOW(), energy_kwh = $1, cost = $2
		WHERE id = $3
	`
	_, err := r.db.Exec(query, energy, cost, id)
	return err
}

func (r *ChargingRepository) UpsertCharger(id string, status string) error {
	query := `
		INSERT INTO chargers (id, status, last_seen)
		VALUES ($1, $2, NOW())
		ON CONFLICT (id)
		DO UPDATE SET status = EXCLUDED.status, last_seen = NOW()
	`
	_, err := r.db.Exec(query, id, status)
	return err
}

