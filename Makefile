# jk — Pipeline-native Jenkins CLI
#
# Targets prefixed with `test-` separate unit, integration, and end-to-end
# scopes (see SPEC.md §Testing Strategy). `make test` runs unit + integration;
# end-to-end is opt-in via `make test-e2e` and requires a real Jenkins.

GO        ?= go
PKG       := ./...
BIN_DIR   := bin
BIN       := $(BIN_DIR)/jk
LDFLAGS   := -s -w -X github.com/addozhang/jk/internal/cli.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN{FS=":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the jk binary into ./bin/jk.
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/jk

.PHONY: install
install: ## Install jk to $GOBIN (or $GOPATH/bin).
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' ./cmd/jk

.PHONY: fmt
fmt: ## Run gofmt + goimports-style formatting on the whole tree.
	$(GO) fmt $(PKG)

.PHONY: vet
vet: ## Run go vet across the module.
	$(GO) vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint (v2).
	golangci-lint run $(PKG)

.PHONY: test
test: test-unit test-integration ## Run unit + integration tests with race detector.

.PHONY: test-unit
test-unit: ## Run unit tests under internal/.
	$(GO) test -race -count=1 ./internal/...

.PHONY: test-integration
test-integration: ## Run integration tests under test/integration (when present).
	@if [ -d test/integration ]; then \
		$(GO) test -race -count=1 ./test/integration/...; \
	else \
		echo "no test/integration directory yet; skipping"; \
	fi

.PHONY: test-e2e
test-e2e: ## Run end-to-end tests against a real Jenkins (-tags=e2e).
	$(GO) test -race -tags=e2e ./test/e2e/...

.PHONY: e2e-up
e2e-up: ## Build + start the Jenkins e2e harness; wait for it to be healthy.
	docker compose -f test/e2e/docker-compose.yml up -d --build
	@echo "waiting for Jenkins to become healthy..."
	@for i in $$(seq 1 60); do \
		status=$$(docker inspect -f '{{.State.Health.Status}}' jk-e2e-jenkins 2>/dev/null || echo "starting"); \
		if [ "$$status" = "healthy" ]; then \
			echo "Jenkins is healthy at http://localhost:18080"; \
			exit 0; \
		fi; \
		sleep 2; \
	done; \
	echo "Jenkins did not become healthy within 120s"; exit 1

.PHONY: e2e-down
e2e-down: ## Stop the Jenkins e2e harness and remove its volume.
	docker compose -f test/e2e/docker-compose.yml down -v

.PHONY: e2e-logs
e2e-logs: ## Tail the Jenkins container logs.
	docker compose -f test/e2e/docker-compose.yml logs -f jenkins

.PHONY: cover
cover: ## Generate and open an HTML coverage report for internal/.
	$(GO) test -race -coverprofile=coverage.txt -covermode=atomic ./internal/...
	$(GO) tool cover -html=coverage.txt -o coverage.html
	@echo "coverage report: coverage.html"

.PHONY: tidy
tidy: ## Refresh go.mod/go.sum.
	$(GO) mod tidy

.PHONY: release-snapshot
release-snapshot: ## Build a snapshot release for all platforms via GoReleaser (no publish).
	goreleaser release --snapshot --clean

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf $(BIN_DIR) dist coverage.txt coverage.html

.DEFAULT_GOAL := help
