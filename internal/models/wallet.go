package models

type Wallet struct {
	ID           string  `db:"id" json:"id"`
	UserID       string  `db:"user_id" json:"user_id"`
	Balance      float64 `db:"balance" json:"balance"`
	Currency     string  `db:"currency" json:"currency"`
	Status       string  `db:"status" json:"status"`
	LockedAmount float64 `db:"locked_amount" json:"locked_amount"`
	CreatedAt    string  `db:"created_at" json:"created_at"`
	UpdatedAt    string  `db:"updated_at" json:"updated_at"`
}
