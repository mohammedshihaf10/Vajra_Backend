package models

type User struct {
	ID        int    `db:"id" json:"id"`
	Name      string `db:"name" json:"name"`
	Email     string `db:"email" json:"email"`
	CreatedAt string `db:"created_at" json:"created_at"`
}
