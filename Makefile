GO ?= go
BIN := bin/entregaviva
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PORT ?= 3003

.PHONY: run build test lint fmt vet tidy docker-build docker-run clean

run: ## Run the server locally
	PORT=$(PORT) $(GO) run ./cmd/server

build: ## Build a static binary into bin/
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/server

test: ## Run tests with the race detector
	$(GO) test -race -count=1 ./...

lint: ## gofmt check, go vet and golangci-lint
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "run make fmt" && exit 1)
	$(GO) vet ./...
	golangci-lint run ./...

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t entregaviva:local .

docker-run: docker-build
	docker run --rm -p $(PORT):3003 -v entregaviva-data:/data entregaviva:local

clean:
	rm -rf bin
