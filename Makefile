.PHONY: test vet build env up down logs

test:
	go test ./...

vet:
	go vet ./...

build:
	CGO_ENABLED=0 go build -o bin/filekeeper ./cmd/filekeeper

env:
	sh scripts/gen-env.sh

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f filekeeper
