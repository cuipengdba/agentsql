BINARY_DIR := bin
VERSION ?= v0.3.0
GO_VERSION ?= 1.25.14
GOPROXY ?= https://goproxy.cn,direct
GO ?= go
GOFLAGS ?=
ROCKY_IMAGE ?= rockylinux:8
VERSION_PACKAGE := github.com/cuipengdba/agentsql/internal/version
LDFLAGS := -s -w -X $(VERSION_PACKAGE).Version=$(VERSION)

.PHONY: build test race vet fmt lint webui release docker-build docker-linux-amd64 package-release

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

# pg_query_go requires cgo. Build release linux/amd64 binaries with the
# RHEL/Rocky 8 (glibc 2.28) toolchain for RHEL 8 family, Alibaba Cloud Linux 3,
# Kylin V10, UnionTech UOS, and other compatible systems.
docker-linux-amd64:
	docker run --rm --platform linux/amd64 -e VERSION=$(VERSION) -e GO_VERSION=$(GO_VERSION) -e GOPROXY=$(GOPROXY) -v "$(CURDIR):/src" -w /src -v "$(CURDIR)/scripts:/buildscripts:ro" $(ROCKY_IMAGE) sh -c "tr -d '\r' < /buildscripts/build-release-linux.sh | sh"

package-release:
	VERSION=$(VERSION) sh scripts/package-release.sh
