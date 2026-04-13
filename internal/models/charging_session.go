package models

type ChargingSession struct {
	ID                  string  `db:"id" json:"id"`
	ChargerID           string  `db:"charger_id" json:"charger_id"`
	ConnectorID         int     `db:"connector_id" json:"connector_id"`
	UserID              string  `db:"user_id" json:"user_id"`
	StartTime           string  `db:"start_time" json:"start_time"`
	EndTime             string  `db:"end_time" json:"end_time"`
	EnergyKwh           float64 `db:"energy_kwh" json:"energy_kwh"`
	Cost                float64 `db:"cost" json:"cost"`
	Status              string  `db:"status" json:"status"`
	LegacyTransactionID int     `db:"transaction_id" json:"legacy_transaction_id"`
	TransactionRef      string  `db:"transaction_ref" json:"transaction_ref"`
	OCPPTransactionID   string  `db:"ocpp_transaction_id" json:"transaction_id"`
	RemoteStartID       string  `db:"remote_start_id" json:"remote_start_id"`
	ChargingState       string  `db:"charging_state" json:"charging_state"`
	IsActive            bool    `db:"is_active" json:"is_active"`
	FailureReason       string  `db:"failure_reason" json:"failure_reason"`
	StopRequestedAt     string  `db:"stop_requested_at" json:"stop_requested_at"`
	StopPollClaimedAt   string  `db:"stop_poll_claimed_at" json:"stop_poll_claimed_at"`
	BilledAt            string  `db:"billed_at" json:"billed_at"`
}
