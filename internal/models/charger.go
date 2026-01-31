package models

type Charger struct {
	ID       string `db:"id" json:"id"`
	Status   string `db:"status" json:"status"`
	LastSeen string `db:"last_seen" json:"last_seen"`
	Model    string `db:"model" json:"model"`
	Vendor   string `db:"vendor" json:"vendor"`
}
