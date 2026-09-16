BINARY_DIR := bin
VERSION ?= v0.2.0
GO ?= go
GOFLAGS ?=
VERSION_PACKAGE := github.com/cuipengdba/agentsql/internal/version
LDFLAGS := -s -w -X $(VERSION_PACKAGE).Version=$(VERSION)

.PHONY: build test race vet fmt lint webui release docker-build docker-linux-amd64

build:
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=1 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/agentsql ./cmd/agentsql
	CGO_ENABLED=1 $(GO) build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/agentsqlctl ./cmd/agentsqlctl

test:
	$(GO) test $(GOFLAGS) ./...

race:
	CGO_ENABLED=1 $(GO) test $(GOFLAGS) -race ./...

vet:
	$(GO) vet $(GOFLAGS) ./...

fmt:
	$(GO) fmt ./...

lint:
	golangci-lint run ./...

# The checked-in internal/webui/dist is used by normal builds. Run this target
# manually before a release when the web application source has changed.
webui:
	cd web && npm ci && npm run build

release: build
	cd $(BINARY_DIR) && { command -v sha256sum >/dev/null 2>&1 && sha256sum agentsql agentsqlctl || shasum -a 256 agentsql agentsqlctl; } > SHA256SUMS

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t agentsql:$(VERSION) .

# pg_query_go requires cgo. Portable cross-compilation remains a v0.2 task;
# v0.1 produces glibc linux/amd64 artifacts inside the official Go container.
docker-linux-amd64:
	docker run --rm --platform linux/amd64 -v "$(CURDIR):/src" -w /src -e CGO_ENABLED=1 -e GOOS=linux -e GOARCH=amd64 golang:1.25-bookworm sh -c 'mkdir -p $(BINARY_DIR) && go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/agentsql-linux-amd64 ./cmd/agentsql && go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY_DIR)/agentsqlctl-linux-amd64 ./cmd/agentsqlctl'
