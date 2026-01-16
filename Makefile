.PHONY: all deps build test clean lint run-server run-cli

# Build variables
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

# Go settings
GO := go
GOFMT := gofmt
GOLINT := golangci-lint
GOMOD := $(shell go list -m)

# Output directories
BIN_DIR := bin
BUILD_DIR := build

all: deps build

deps:
	@echo "Downloading dependencies..."
	$(GO) mod download
	$(GO) install github.com/golang/mock/mockgen@latest
	$(GO) install github.com/sqlc/sqlc/cmd/sqlc@latest
	@echo "Dependencies downloaded successfully."

build: $(BIN_DIR)/ksa-server $(BIN_DIR)/ksa

$(BIN_DIR)/ksa-server: cmd/server/main.go
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/server

$(BIN_DIR)/ksa: cmd/cli/main.go
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/cli

test:
	@echo "Running tests..."
	$(GO) test -v -race -cover ./...
	@echo "Tests completed."

lint:
	@echo "Running linter..."
	$(GOLINT) run ./...
	@echo "Linting completed."

clean:
	@echo "Cleaning..."
	rm -rf $(BIN_DIR) $(BUILD_DIR)
	$(GO) clean -cache
	@echo "Clean completed."

run-server: $(BIN_DIR)/ksa-server
	@echo "Starting KSA Server..."
	./$(BIN_DIR)/ksa-server

run-cli: $(BIN_DIR)/ksa
	@echo "Starting KSA CLI..."
	./$(BIN_DIR)/ksa

# Database migrations
migrate-up:
	$(GO) run ./cmd/migrate/main.go up

migrate-down:
	$(GO) run ./cmd/migrate/main.go down

# Generate mocks
generate-mocks:
	$(GO) generate ./...

# Proto generation
proto:
	@echo "Generating protobuf code..."
	sqlc generate
	@echo "Protobuf code generated."

# Docker build
docker:
	@echo "Building Docker image..."
	docker build -t kube-static-pool-autoscaler:$(VERSION) -f Dockerfile .
	@echo "Docker image built."

help:
	@echo "Available targets:"
	@echo "  all          - Build everything (default)"
	@echo "  deps         - Download dependencies"
	@echo "  build        - Build binaries"
	@echo "  test         - Run tests"
	@echo "  lint         - Run linter"
	@echo "  clean        - Clean build artifacts"
	@echo "  run-server   - Run server"
	@echo "  run-cli      - Run CLI"
	@echo "  migrate-up   - Run database migrations up"
	@echo "  migrate-down - Run database migrations down"
	@echo "  generate-mocks - Generate mock files"
	@echo "  proto        - Generate protobuf code"
	@echo "  docker       - Build Docker image"
	@echo "  help         - Show this help"
