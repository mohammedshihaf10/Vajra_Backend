package db

import (
	"log"
	"os"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

func Connect() *sqlx.DB {
	dsn := os.Getenv("DB_DSN")
	log.Println("Connecting to database:", dsn)

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		log.Fatal("DB connection failed:", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)

	log.Println("Database connected ✅")
	return db
}
