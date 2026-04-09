package repositories

import (
	"database/sql"
	"errors"

	"vajraBackend/internal/models"

	"github.com/jmoiron/sqlx"
)

var ErrTopupNotFound = errors.New("topup not found")
var ErrWalletNotFound = errors.New("wallet not found")
var ErrInsufficientBalance = errors.New("insufficient wallet balance")

type WalletRepository struct {
	db *sqlx.DB
}

func NewWalletRepository(db *sqlx.DB) *WalletRepository {
	return &WalletRepository{db}
}

func (r *WalletRepository) GetWalletByUserID(userID string) (*models.Wallet, error) {
	wallet := models.Wallet{}
	query := `SELECT id, user_id, balance, currency, status, locked_amount, created_at, updated_at FROM wallets WHERE user_id = $1`

	err := r.db.Get(&wallet, query, userID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &wallet, err
}

func (r *WalletRepository) CreateTopupIntent(userID, walletID string, amount float64, currency, orderID string) (*models.WalletTopup, error) {
	topup := models.WalletTopup{}
	query := `
		INSERT INTO wallet_topups (user_id, wallet_id, amount, currency, status, gateway_order_id)
		VALUES ($1, $2, $3, $4, 'pending', $5)
		RETURNING id, user_id, wallet_id, amount, currency, status, gateway_order_id, COALESCE(gateway_payment_id, '') AS gateway_payment_id, created_at, updated_at
	`

	err := r.db.Get(&topup, query, userID, walletID, amount, currency, orderID)
	if err != nil {
		return nil, err
	}
	return &topup, nil
}

func (r *WalletRepository) ListTransactions(walletID string, limit int) ([]models.WalletTransaction, error) {
	if limit <= 0 {
		limit = 50
	}

	transactions := []models.WalletTransaction{}
	query := `
		SELECT id, wallet_id, transaction_type, amount, currency, source, reference_id, idempotency_key, status, description, created_at
		FROM wallet_transactions
		WHERE wallet_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`

	err := r.db.Select(&transactions, query, walletID, limit)
	return transactions, err
}

func (r *WalletRepository) ProcessTopupWebhook(orderID, paymentID, status, currency string, amount float64) (bool, error) {
	tx, err := r.db.Beginx()
	if err != nil {
		return false, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	topup := models.WalletTopup{}
	query := `
		SELECT id, user_id, wallet_id, amount, currency, status, gateway_order_id, COALESCE(gateway_payment_id, '') AS gateway_payment_id, created_at, updated_at
		FROM wallet_topups
		WHERE gateway_order_id = $1
		FOR UPDATE
	`
	err = tx.Get(&topup, query, orderID)
	if err == sql.ErrNoRows {
		return false, ErrTopupNotFound
	}
	if err != nil {
		return false, err
	}

	if topup.Status == "captured" {
		return true, tx.Commit()
	}

	if currency != "" && topup.Currency != "" && currency != topup.Currency {
		return false, errors.New("currency mismatch")
	}
	if amount > 0 && topup.Amount != amount {
		return false, errors.New("amount mismatch")
	}

	if status != "captured" {
		updateQuery := `
			UPDATE wallet_topups
			SET status = 'failed', gateway_payment_id = $1, updated_at = NOW()
			WHERE id = $2
		`
		if _, err := tx.Exec(updateQuery, paymentID, topup.ID); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}

	wallet := models.Wallet{}
	walletQuery := `
		SELECT id, user_id, balance, currency, status, locked_amount, created_at, updated_at
		FROM wallets
		WHERE id = $1
		FOR UPDATE
	`
	err = tx.Get(&wallet, walletQuery, topup.WalletID)
	if err != nil {
		return false, err
	}
	if wallet.Status != "active" {
		return false, errors.New("wallet is not active")
	}

	insertQuery := `
		INSERT INTO wallet_transactions (
			wallet_id, transaction_type, amount, currency, source, reference_id, idempotency_key, status, description
		)
		VALUES ($1, 'CREDIT', $2, $3, 'topup', $4, $4, 'success', 'Wallet top-up')
		ON CONFLICT (idempotency_key) DO NOTHING
	`
	result, err := tx.Exec(insertQuery, wallet.ID, topup.Amount, topup.Currency, paymentID)
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	if affected > 0 {
		updateWallet := `
			UPDATE wallets
			SET balance = balance + $1, updated_at = NOW()
			WHERE id = $2
		`
		if _, err := tx.Exec(updateWallet, topup.Amount, wallet.ID); err != nil {
			return false, err
		}
	}

	updateTopup := `
		UPDATE wallet_topups
		SET status = 'captured', gateway_payment_id = $1, updated_at = NOW()
		WHERE id = $2
	`
	if _, err := tx.Exec(updateTopup, paymentID, topup.ID); err != nil {
		return false, err
	}

	return true, tx.Commit()
}

func (r *WalletRepository) DebitWalletForSession(userID string, amount float64, sessionID string) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	_, err = r.DebitWalletForSessionTx(tx, userID, amount, sessionID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *WalletRepository) DebitWalletForSessionTx(tx *sqlx.Tx, userID string, amount float64, sessionID string) (bool, error) {
	if amount <= 0 {
		return false, nil
	}

	wallet := models.Wallet{}
	walletQuery := `
		SELECT id, user_id, balance, currency, status, locked_amount, created_at, updated_at
		FROM wallets
		WHERE user_id = $1
		FOR UPDATE
	`
	if err := tx.Get(&wallet, walletQuery, userID); err != nil {
		if err == sql.ErrNoRows {
			return false, ErrWalletNotFound
		}
		return false, err
	}

	if wallet.Status != "active" {
		return false, errors.New("wallet is not active")
	}

	if wallet.Balance < amount {
		return false, ErrInsufficientBalance
	}

	insertQuery := `
		INSERT INTO wallet_transactions (
			wallet_id, transaction_type, amount, currency, source, reference_id, idempotency_key, status, description
		)
		VALUES ($1, 'DEBIT', $2, $3, 'charging', $4, $4, 'success', 'Charging cost')
		ON CONFLICT (idempotency_key) DO NOTHING
	`
	result, err := tx.Exec(insertQuery, wallet.ID, amount, wallet.Currency, sessionID)
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	if affected > 0 {
		updateWallet := `
			UPDATE wallets
			SET balance = balance - $1, updated_at = NOW()
			WHERE id = $2
		`
		if _, err := tx.Exec(updateWallet, amount, wallet.ID); err != nil {
			return false, err
		}
	}

	return affected > 0, nil
}
