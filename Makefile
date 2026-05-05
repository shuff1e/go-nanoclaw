BINARY_NAME := nanoclaw
MODULE := go-nanoclaw
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildTime=$(BUILD_TIME)

GO := go
GOLANGCI_LINT := golangci-lint

.PHONY: all build test lint vet fmt clean install coverage docker-build docker-run help

all: lint test build

build:
	CGO_ENABLED=0 $(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY_NAME) ./cmd/nanoclaw/

install:
	$(GO) install -ldflags '$(LDFLAGS)' ./cmd/nanoclaw/

test:
	$(GO) test ./... -count=1

test-race:
	$(GO) test -race ./... -count=1

coverage:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out
	@echo "HTML report: go tool cover -html=coverage.out"

lint:
	$(GOLANGCI_LINT) run ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...
	$(GOLANGCI_LINT) run --fix ./...

release-snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin/ coverage.out dist/

docker-build:
	docker build -t $(BINARY_NAME):$(VERSION) .

docker-run: docker-build
	docker run --rm -it \
		-e ANTHROPIC_API_KEY \
		-e OPENAI_API_KEY \
		-v $(HOME)/.nanoclaw:/home/nanoclaw/.nanoclaw \
		$(BINARY_NAME):$(VERSION)

help:
	@echo "Available targets:"
	@echo "  build        - Build binary to bin/"
	@echo "  install      - Install binary to GOPATH/bin"
	@echo "  test         - Run all tests"
	@echo "  test-race    - Run tests with race detector"
	@echo "  coverage     - Run tests with coverage report"
	@echo "  lint         - Run golangci-lint"
	@echo "  vet          - Run go vet"
	@echo "  fmt          - Format code"
	@echo "  clean        - Remove build artifacts"
	@echo "  docker-build     - Build Docker image"
	@echo "  docker-run       - Run Docker container"
	@echo "  release-snapshot - Build release snapshot (local test)"
	@echo "  all              - Run lint, test, build"
