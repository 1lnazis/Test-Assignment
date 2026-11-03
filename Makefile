.PHONY: fmt vet test test-integration build up down run logs

fmt:
	gofmt -s -w .

vet:
	go vet ./...

test:
	go test -race -count=1 ./...

test-integration:
	docker compose run --build --rm test

build:
	docker compose build app

up:
	docker compose up -d --build --wait

down:
	docker compose down

run:
	docker compose up --build

logs:
	docker compose logs -f app
