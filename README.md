run:
	go run cmd/api/main.go

swagger:
	swag init -g cmd/api/main.go

migrate-up:
	migrate -database "$(DB_DSN)" -path migrations up
