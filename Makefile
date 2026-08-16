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
test: ## Run the Go test suite
	@go test ./... -race -count=1

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
