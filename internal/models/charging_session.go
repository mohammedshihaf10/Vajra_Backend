package models

type ChargingSession struct {
	ID          string  `db:"id" json:"id"`
	ChargerID   string  `db:"charger_id" json:"charger_id"`
	ConnectorID int     `db:"connector_id" json:"connector_id"`
	UserID      string  `db:"user_id" json:"user_id"`
	StartTime   string  `db:"start_time" json:"start_time"`
	EndTime     string  `db:"end_time" json:"end_time"`
	EnergyKwh   float64 `db:"energy_kwh" json:"energy_kwh"`
	Cost        float64 `db:"cost" json:"cost"`
	Status      string  `db:"status" json:"status"`
	TransactionID int   `db:"transaction_id" json:"transaction_id"`
}
