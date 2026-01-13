package repositories

import (
	"database/sql"

	"vijraBackend/internal/models"

	"github.com/jmoiron/sqlx"
)

type UserRepository struct {
	db *sqlx.DB
}

func NewUserRepository(db *sqlx.DB) *UserRepository {
	return &UserRepository{db}
}

func (r *UserRepository) Create(user *models.User) error {
	query := `
		INSERT INTO users (name, email)
		VALUES ($1, $2)
		RETURNING id, created_at
	`

	return r.db.QueryRowx(
		query,
		user.Name,
		user.Email,
	).Scan(&user.ID, &user.CreatedAt)
}

func (r *UserRepository) GetByEmail(email string) (*models.User, error) {
	user := models.User{}
	query := `SELECT * FROM users WHERE email = $1`

	err := r.db.Get(&user, query, email)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &user, err
}

func (r *UserRepository) GetByID(id int) (*models.User, error) {
	user := models.User{}
	query := `SELECT * FROM users WHERE id = $1`

	err := r.db.Get(&user, query, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &user, err
}
