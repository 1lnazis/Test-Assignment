# Строка подключения к локальной базе из docker-compose. Можно переопределить:
#   make run DATABASE_URL=postgres://...
DATABASE_URL ?= postgres://quotes:quotes@localhost:5433/quotes?sslmode=disable

.PHONY: fmt vet test test-integration up down run

fmt:
	gofmt -s -w .

vet:
	go vet ./...

# Интеграционные тесты без TEST_DATABASE_URL пропускаются.
test:
	go test -race -count=1 ./...

test-integration:
	TEST_DATABASE_URL="$(DATABASE_URL)" go test -race -count=1 ./...

# Поднять Postgres и дождаться, пока он станет здоровым.
up:
	docker compose up -d --wait db

down:
	docker compose down

run:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/app
