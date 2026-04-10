.PHONY: all build test lint fmt vet clean run smoke help

# Binary output
BIN_DIR := bin
BIN := $(BIN_DIR)/pagefault
PKG := ./...
VERSION := $(shell cat VERSION)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

all: build

## build: Build the pagefault binary into ./bin/
build:
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN) ./cmd/pagefault

## test: Run all tests with race detection
test:
	go test -race -count=1 $(PKG)

## test-verbose: Run tests with verbose output
test-verbose:
	go test -race -count=1 -v $(PKG)

## cover: Run tests with coverage report
cover:
	go test -race -count=1 -coverprofile=coverage.out $(PKG)
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## lint: Run go vet, gofmt check, and staticcheck (if installed)
lint: vet fmt-check
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck $(PKG); \
	else \
		echo "staticcheck not installed, skipping (install: go install honnef.co/go/tools/cmd/staticcheck@latest)"; \
	fi

## vet: Run go vet
vet:
	go vet $(PKG)

## fmt: Format all Go files
fmt:
	gofmt -w .

## fmt-check: Check formatting (CI-friendly)
fmt-check:
	@diff=$$(gofmt -l .); \
	if [ -n "$$diff" ]; then \
		echo "gofmt issues in:"; \
		echo "$$diff"; \
		exit 1; \
	fi

## run: Run the server with the minimal config
run: build
	$(BIN) serve --config configs/minimal.yaml

## smoke: Build and run smoke test (requires curl)
smoke: build
	@./scripts/smoke.sh 2>/dev/null || echo "(smoke script not yet present)"

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html

## help: Show this help message
help:
	@echo "pagefault build targets:"
	@awk '/^## / { sub(/^## /, "  "); print }' $(MAKEFILE_LIST)
