package models

type WalletTransaction struct {
	ID              string  `db:"id" json:"id"`
	WalletID        string  `db:"wallet_id" json:"wallet_id"`
	TransactionType string  `db:"transaction_type" json:"transaction_type"`
	Amount          float64 `db:"amount" json:"amount"`
	Currency        string  `db:"currency" json:"currency"`
	Source          string  `db:"source" json:"source"`
	ReferenceID     string  `db:"reference_id" json:"reference_id"`
	IdempotencyKey  string  `db:"idempotency_key" json:"idempotency_key"`
	Status          string  `db:"status" json:"status"`
	Description     string  `db:"description" json:"description"`
	CreatedAt       string  `db:"created_at" json:"created_at"`
}
