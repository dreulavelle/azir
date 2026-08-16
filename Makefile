COMPOSE := docker compose -f deploy/compose.yaml

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: check
check: fmt vet test ## Format, vet and test

.PHONY: fmt
fmt: ## gofmt every package
	@gofmt -l -w .

.PHONY: vet
vet: ## go vet
	@go vet ./...

.PHONY: test
test: ## Run the Go test suite (store tests need AZIR_TEST_DATABASE_URL)
	@go test ./... -race -count=1

.PHONY: test-db
test-db: ## Start Postgres and create the test database
	@$(COMPOSE) up -d postgres
	@until docker exec azir-postgres-1 pg_isready -U azir -d azir >/dev/null 2>&1; do sleep 1; done
	@docker exec azir-postgres-1 psql -U azir -d azir -c "CREATE DATABASE azir_test" 2>/dev/null || true
	@echo 'export AZIR_TEST_DATABASE_URL="postgres://azir:azir_dev_only@localhost:5433/azir_test?sslmode=disable"'

.PHONY: psql
psql: ## Open a psql session against the running database
	@docker exec -it azir-postgres-1 psql -U azir -d azir

.PHONY: build
build: ## Build all binaries into bin/
	@go build -o bin/ ./cmd/...

.PHONY: up
up: ## Start the full stack
	@$(COMPOSE) up --build -d

.PHONY: down
down: ## Stop the stack
	@$(COMPOSE) down

.PHONY: logs
logs: ## Tail stack logs
	@$(COMPOSE) logs -f

.PHONY: smoke
smoke: ## Verify discovery and a tool round-trip against a running stack
	@./scripts/smoke.sh

.PHONY: keygen
keygen: ## Generate a master key for AZIR_MASTER_KEY
	@openssl rand -base64 32
