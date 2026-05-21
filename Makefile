BINARY       := httpcatch
BIN_DIR      := bin
PKG          := ./cmd/httpcatch
GO           ?= go
DOCKER_IMAGE ?= radarnex/httpcatch
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME   := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: help build test test-race vet fmt fmt-check tidy check run clean docker-build docker-up docker-down docker-logs docker-image-build docker-image-push

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n\nTargets:\n"} \
		/^[a-zA-Z_-]+:.*?##/ {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the httpcatch binary into ./bin/
	$(GO) build -ldflags "-X github.com/radarnex/httpcatch/internal/buildinfo.Version=$$(git rev-parse --short HEAD 2>/dev/null || echo dev) -X github.com/radarnex/httpcatch/internal/buildinfo.BuildTime=$$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o $(BIN_DIR)/$(BINARY) $(PKG)

test: ## Run unit + integration tests
	$(GO) test ./...

test-race: ## Run tests with the race detector
	$(GO) test -race -count=1 ./...

vet: ## go vet
	$(GO) vet ./...

fmt: ## Format all Go files
	$(GO) fmt ./...

fmt-check: ## Fail if any file is not gofmt-clean
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt needed for:"; echo "$$out"; exit 1; \
	fi

tidy: ## go mod tidy
	$(GO) mod tidy

check: fmt-check vet test-race ## Pre-push validation: fmt + vet + race tests

run: build ## Build then run; pass flags via ARGS, e.g. make run ARGS="--config c.yaml"
	./$(BIN_DIR)/$(BINARY) $(ARGS)

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

docker-build:
	docker build \
		--build-arg VERSION=$$(git rev-parse --short HEAD 2>/dev/null || echo dev) \
		--build-arg BUILD_TIME=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
		-t httpcatch:dev .

docker-image-build:
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(DOCKER_IMAGE):$(VERSION) \
		-t $(DOCKER_IMAGE):latest \
		.

docker-image-push:
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(DOCKER_IMAGE):$(VERSION) \
		-t $(DOCKER_IMAGE):latest \
		--push \
		.

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f httpcatch
