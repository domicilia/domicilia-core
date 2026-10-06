# domicilia-core — atajos de desarrollo.
# Las herramientas (sqlc, air, golangci-lint) están declaradas como `tool` en
# go.mod y se ejecutan con `go tool`: no hay nada que instalar aparte de Go.

.PHONY: help run dev test race cover lint fmt vet tidy check build clean sqlc migrate migrate-status

help: ## Lista los comandos
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

run: ## Arranca el servicio (necesita CORE_DATABASE_URL y GOTRUE_JWT_SECRET)
	go run ./cmd/api

dev: ## Arranca con recarga en caliente (air)
	go tool air

test: ## Pruebas (sin cgo: funcionan en Windows). Las de base de datos necesitan Docker
	go test ./...

race: ## Pruebas con detector de carreras (necesita cgo/gcc: CI y Docker, no Windows)
	go test -race ./...

cover: ## Cobertura
	go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

lint: ## golangci-lint
	go tool golangci-lint run

fmt: ## Formatea el código
	go tool golangci-lint fmt

vet: ## go vet
	go vet ./...

tidy: ## Ordena go.mod
	go mod tidy

check: vet lint test ## Lo mismo que corre el CI

build: ## Binario en bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/api ./cmd/api

sqlc: ## Regenera internal/store desde db/queries y db/migrations
	go tool sqlc generate

migrate: ## Aplica las migraciones pendientes (necesita CORE_DATABASE_URL)
	go run ./cmd/api migrate up

migrate-status: ## Estado de las migraciones
	go run ./cmd/api migrate status

clean:
	rm -rf bin coverage.out
