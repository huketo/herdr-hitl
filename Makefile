BIN      := bin/herdr-hitl
PKG      := ./...
GOFLAGS  ?=
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build the herdr-hitl binary into bin/
	go build $(GOFLAGS) -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/herdr-hitl

.PHONY: install
install: ## Install herdr-hitl into GOBIN
	go install $(GOFLAGS) -trimpath -ldflags '$(LDFLAGS)' ./cmd/herdr-hitl

.PHONY: test
test: ## Run unit tests with race detector
	go test -race -count=1 $(PKG)

.PHONY: cover
cover: ## Run tests and write coverage.out
	go test -race -count=1 -coverprofile=coverage.out -covermode=atomic $(PKG)
	go tool cover -func=coverage.out | tail -1

.PHONY: fmt
fmt: ## Format sources
	go run mvdan.cc/gofumpt@v0.9.1 -l -w .

.PHONY: vet
vet: ## Run go vet
	go vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.6.2 run

.PHONY: tidy
tidy: ## Tidy go.mod/go.sum
	go mod tidy

.PHONY: check
check: vet lint test ## Run every quality gate

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.out

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
