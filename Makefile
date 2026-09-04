BINARY_DIR := bin
VERSION ?= dev
GO ?= go
GOFLAGS ?=
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet fmt lint docker-build cross

build:
	mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/agentsql ./cmd/agentsql
	$(GO) build $(GOFLAGS) -trimpath -o $(BINARY_DIR)/agentsqlctl ./cmd/agentsqlctl

test:
	$(GO) test $(GOFLAGS) ./...

vet:
	$(GO) vet $(GOFLAGS) ./...

fmt:
	$(GO) fmt ./...

lint:
	golangci-lint run ./...

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t agentsql:$(VERSION) .

cross:
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/agentsql-linux-amd64 ./cmd/agentsql
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/agentsql-linux-arm64 ./cmd/agentsql
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/agentsql-darwin-amd64 ./cmd/agentsql
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/agentsql-darwin-arm64 ./cmd/agentsql
