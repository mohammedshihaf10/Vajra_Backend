package models

type ConnectorStatus struct {
	ChargerID   string `db:"charger_id" json:"charger_id"`
	ConnectorID int    `db:"connector_id" json:"connector_id"`
	EVSEID      *int   `db:"evse_id" json:"evse_id,omitempty"`
	Status      string `db:"status" json:"status"`
	LastSeen    string `db:"last_seen" json:"last_seen"`
	Metadata    string `db:"metadata" json:"metadata"`
}
