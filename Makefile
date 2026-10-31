.DEFAULT_GOAL := help
GOLANGCI_VERSION := v2.15.0
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LINT := $(shell command -v golangci-lint 2>/dev/null || echo docker run --rm -v $(CURDIR):/app -w /app golangci/golangci-lint:$(GOLANGCI_VERSION) golangci-lint)
LOAD_ENV := set -a; [ -f .env ] || { echo "missing .env: run make env" >&2; exit 1; }; . ./.env; set +a

.PHONY: help env up down logs run migrate seed test test-integration lint fmt tidy

help: ## List the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-18s %s\n", $$1, $$2}'

env: ## Create .env from .env.example with generated secrets (never overwrites .env)
	@if [ -e .env ]; then echo ".env already exists; left unchanged."; exit 0; fi; \
	set -e; umask 077; tmp=$$(mktemp .env.XXXXXX); trap 'rm -f "$$tmp"' EXIT; \
	ds=$$(openssl rand -hex 32); ap=$$(openssl rand -hex 12); pp=$$(openssl rand -hex 16); \
	sed -e "s/^DOWNLOAD_SECRET=.*/DOWNLOAD_SECRET=$$ds/" \
	    -e "s/^ADMIN_PASS=.*/ADMIN_PASS=$$ap/" \
	    -e "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$$pp/" .env.example > "$$tmp"; \
	mv "$$tmp" .env; \
	echo "Created .env with generated DOWNLOAD_SECRET, ADMIN_PASS and POSTGRES_PASSWORD."; \
	echo "Now set MERCHANT_ADDRESS in .env to the address of a devnet-only wallet you control."; \
	echo "The shop needs only the address, never the secret key. Ways to get one:"; \
	echo "  Phantom with Testnet Mode on (Solana Devnet): copy the account address"; \
	echo "  solana-keygen pubkey <keypair.json>"; \
	echo "  go run ./cmd/devpay -new-keypair data/merchant.json   (prints the address)"; \
	echo "  With Docker only (no Go, no Solana CLI), the same devpay command:"; \
	echo '    docker run --rm -u "$$(id -u):$$(id -g)" -e HOME=/tmp -v "$$PWD":/src -w /src golang:1.27.3-alpine3.24 go run ./cmd/devpay -new-keypair data/merchant.json'

up: ## Build and start the stack (Postgres, migrations, demo seed, app) in the background
	VERSION=$(VERSION) docker compose up --build -d || { docker compose logs --no-log-prefix migrate seed; exit 1; }
	@echo "solpay-shop is up; make logs follows it"

down: ## Stop the stack and keep the database volume
	docker compose down

logs: ## Follow the app logs
	docker compose logs -f app

run: ## Run the server locally against the compose database
	docker compose up -d --wait db
	$(LOAD_ENV); go run ./cmd/server

migrate: ## Apply pending migrations to the database named in .env (DATABASE_URL)
	$(LOAD_ENV); go run ./cmd/server -migrate-only

seed: ## Load the demo products (needs a migrated database; safe to repeat)
	docker compose run --rm --no-deps seed

test: ## Unit tests with the race detector
	go test -race ./...

test-integration: export DOCKER_HOST ?= $(shell docker context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null)
test-integration: ## Unit + Postgres integration tests (needs a running Docker daemon)
	go test -race -tags=integration -count=1 ./...

lint: ## Run golangci-lint (local binary, or the pinned Docker image)
	$(LINT) run

fmt: ## Format the code with golangci-lint's formatters
	$(LINT) fmt

tidy: ## Tidy go.mod and go.sum
	go mod tidy
