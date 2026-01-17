package models

type WalletTopup struct {
	ID              string  `db:"id" json:"id"`
	UserID          string  `db:"user_id" json:"user_id"`
	WalletID        string  `db:"wallet_id" json:"wallet_id"`
	Amount          float64 `db:"amount" json:"amount"`
	Currency        string  `db:"currency" json:"currency"`
	Status          string  `db:"status" json:"status"`
	GatewayOrderID  string  `db:"gateway_order_id" json:"gateway_order_id"`
	GatewayPaymentID string `db:"gateway_payment_id" json:"gateway_payment_id"`
	CreatedAt       string  `db:"created_at" json:"created_at"`
	UpdatedAt       string  `db:"updated_at" json:"updated_at"`
}
