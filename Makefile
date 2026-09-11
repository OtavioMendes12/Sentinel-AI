GO  ?= go
BIN := bin/sentinel-reviewer

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the reviewer binary
	$(GO) build -trimpath -o $(BIN) ./cmd/reviewer

.PHONY: run
run: ## Run the reviewer locally using .env
	$(GO) run ./cmd/reviewer -env-file .env

.PHONY: test
test: ## Run unit tests with the race detector
	$(GO) test -race -count=1 ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: secrets
secrets: ## Scan the full git history for secrets
	gitleaks git --redact --no-banner --config .gitleaks.toml .

.PHONY: check
check: secrets vet lint test ## Run every check CI runs, in CI order

.PHONY: hooks
hooks: ## Enable the repository git hooks
	git config core.hooksPath .githooks
	@echo "git hooks enabled (.githooks)"
