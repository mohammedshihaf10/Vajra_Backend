package repositories

import (
	"database/sql"
	"log"
	"time"

	"vajraBackend/internal/models"

	"github.com/jmoiron/sqlx"
)

type UserRepository struct {
	db *sqlx.DB
}

func NewUserRepository(db *sqlx.DB) *UserRepository {
	return &UserRepository{db}
}

func (r *UserRepository) Create(user *models.User) error {
	tx, err := r.db.Beginx()
	if err != nil {
		return err
	}

	var emailValue interface{}
	if user.Email == "" {
		emailValue = nil
	} else {
		emailValue = user.Email
	}

	query := `
		INSERT INTO users (full_name, email, phone_number)
		VALUES ($1, $2, $3)
		RETURNING id, created_at, updated_at
	`

	err = tx.QueryRowx(
		query,
		user.FullName,
		emailValue,
		user.PhoneNumber,
	).Scan(&user.ID, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	walletQuery := `
		INSERT INTO wallets (user_id, balance, currency, status, locked_amount)
		VALUES ($1, 0, 'INR', 'active', 0)
	`

	if _, err = tx.Exec(walletQuery, user.ID); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (r *UserRepository) GetByEmail(email string) (*models.User, error) {
	user := models.User{}
	query := `SELECT id, full_name, COALESCE(email, '') AS email, phone_number, auth_provider, is_email_verified, is_phone_verified, status, timezone, auth_token, auth_token_expires_at, created_at, updated_at FROM users WHERE email = $1`

	err := r.db.Get(&user, query, email)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &user, err
}

func (r *UserRepository) GetByPhoneNumber(phoneNumber string) (*models.User, error) {
	user := models.User{}
	query := `
	SELECT
		id,
		full_name,
		COALESCE(email, '') AS email,
		phone_number,
		auth_provider,
		is_email_verified,
		is_phone_verified,
		status,
		timezone,
		auth_token,
		auth_token_expires_at,
		created_at,
		updated_at
	FROM users
	WHERE phone_number = $1
	`
	err := r.db.Get(&user, query, phoneNumber)
	log.Println("GetByPhoneNumber query executed with phone number:", phoneNumber, "error:", err)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &user, err
}

func (r *UserRepository) GetByID(id string) (*models.User, error) {
	user := models.User{}
	query := `SELECT id, full_name, COALESCE(email, '') AS email, phone_number, auth_provider, is_email_verified, is_phone_verified, status, timezone, auth_token, auth_token_expires_at, created_at, updated_at FROM users WHERE id = $1`

	err := r.db.Get(&user, query, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &user, err
}

func (r *UserRepository) SetAuthToken(userID, token string, expiresAt time.Time) error {
	query := `
		UPDATE users
		SET auth_token = $1, auth_token_expires_at = $2
		WHERE id = $3
	`
	_, err := r.db.Exec(query, token, expiresAt, userID)
	return err
}

func (r *UserRepository) GetByAuthToken(token string) (*models.User, error) {
	user := models.User{}
	query := `
		SELECT id, full_name, COALESCE(email, '') AS email, phone_number, auth_provider, is_email_verified, is_phone_verified, status, timezone, auth_token, auth_token_expires_at, created_at, updated_at
		FROM users
		WHERE auth_token = $1 AND auth_token_expires_at > NOW()
	`

	err := r.db.Get(&user, query, token)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &user, err
}

func (r *UserRepository) ClearAuthToken(userID string) error {
	query := `
		UPDATE users
		SET auth_token = NULL, auth_token_expires_at = NULL
		WHERE id = $1
	`
	_, err := r.db.Exec(query, userID)
	return err
}

func (r *UserRepository) SetPhoneOTP(phoneNumber, code string, expiresAt time.Time) error {
	query := `
		INSERT INTO phone_otps (phone_number, code, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (phone_number)
		DO UPDATE SET code = EXCLUDED.code, expires_at = EXCLUDED.expires_at, updated_at = NOW()
	`
	_, err := r.db.Exec(query, phoneNumber, code, expiresAt)
	return err
}

func (r *UserRepository) VerifyPhoneOTP(phoneNumber, code string) (bool, error) {
	tx, err := r.db.Beginx()
	if err != nil {
		return false, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var storedCode string
	query := `
		SELECT code
		FROM phone_otps
		WHERE phone_number = $1 AND expires_at > NOW()
		FOR UPDATE
	`
	err = tx.Get(&storedCode, query, phoneNumber)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if storedCode != code {
		return false, nil
	}

	deleteQuery := `DELETE FROM phone_otps WHERE phone_number = $1`
	if _, err := tx.Exec(deleteQuery, phoneNumber); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *UserRepository) MarkPhoneVerified(userID string) error {
	query := `UPDATE users SET is_phone_verified = TRUE WHERE id = $1`
	_, err := r.db.Exec(query, userID)
	return err
}
