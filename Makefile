BINARY_DIR := bin
VERSION ?= v0.3.0
GO_VERSION ?= 1.25.14
GOPROXY ?= https://goproxy.cn,direct
SOURCE_DATE_EPOCH ?= 1704067200
GO ?= go
GOFLAGS ?=
# Development default only. Pin ROCKY_IMAGE to a reviewed @sha256 digest for a formal release.
ROCKY_IMAGE ?= rockylinux:8
VERSION_PACKAGE := github.com/cuipengdba/agentsql/internal/version
LDFLAGS := -s -w -X $(VERSION_PACKAGE).Version=$(VERSION)
RELEASE_DNF := dnf -y install --nodocs --setopt=install_weak_deps=False gcc glibc-devel glibc-common curl binutils file findutils gawk tar gzip grep sed

.PHONY: build test race vet fmt lint webui release docker-build docker-linux-amd64 docker-linux-arm64 package-release package-linux-amd64 package-linux-arm64 release-linux-amd64 release-linux-arm64

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

# pg_query_go requires cgo. Build release Linux binaries with the matching
# RHEL/Rocky 8 (glibc 2.28) toolchain for RHEL 8 family, Alibaba Cloud Linux 3,
# Kylin V10, UnionTech UOS, and other compatible systems.
docker-linux-amd64:
	docker run --rm --platform linux/amd64 -e ARCH=amd64 -e VERSION=$(VERSION) -e GO_VERSION=$(GO_VERSION) -e GOPROXY=$(GOPROXY) -e SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH) -v "$(CURDIR):/src" -w /src -v "$(CURDIR)/scripts:/buildscripts:ro" $(ROCKY_IMAGE) sh -c "$(RELEASE_DNF) && tr -d '\r' < /buildscripts/build-release-linux.sh | sh"

docker-linux-arm64:
	docker run --rm --platform linux/arm64 -e ARCH=arm64 -e VERSION=$(VERSION) -e GO_VERSION=$(GO_VERSION) -e GOPROXY=$(GOPROXY) -e SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH) -v "$(CURDIR):/src" -w /src -v "$(CURDIR)/scripts:/buildscripts:ro" $(ROCKY_IMAGE) sh -c "$(RELEASE_DNF) && tr -d '\r' < /buildscripts/build-release-linux.sh | sh"

package-linux-amd64:
	docker run --rm --platform linux/amd64 -e ARCH=amd64 -e VERSION=$(VERSION) -e GO_VERSION=$(GO_VERSION) -e GOPROXY=$(GOPROXY) -e SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH) -v "$(CURDIR):/src" -w /src -v "$(CURDIR)/scripts:/buildscripts:ro" $(ROCKY_IMAGE) sh -c "$(RELEASE_DNF) && tr -d '\r' < /buildscripts/package-release.sh | sh"

package-linux-arm64:
	docker run --rm --platform linux/arm64 -e ARCH=arm64 -e VERSION=$(VERSION) -e GO_VERSION=$(GO_VERSION) -e GOPROXY=$(GOPROXY) -e SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH) -v "$(CURDIR):/src" -w /src -v "$(CURDIR)/scripts:/buildscripts:ro" $(ROCKY_IMAGE) sh -c "$(RELEASE_DNF) && tr -d '\r' < /buildscripts/package-release.sh | sh"

release-linux-amd64:
	docker run --rm --platform linux/amd64 -e ARCH=amd64 -e VERSION=$(VERSION) -e GO_VERSION=$(GO_VERSION) -e GOPROXY=$(GOPROXY) -e SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH) -v "$(CURDIR):/src" -w /src -v "$(CURDIR)/scripts:/buildscripts:ro" $(ROCKY_IMAGE) sh -c "$(RELEASE_DNF) && tr -d '\r' < /buildscripts/build-release-linux.sh | sh && GOTOOLCHAIN=local /usr/local/go/bin/go test ./internal/parser && tr -d '\r' < /buildscripts/package-release.sh | sh"

release-linux-arm64:
	docker run --rm --platform linux/arm64 -e ARCH=arm64 -e VERSION=$(VERSION) -e GO_VERSION=$(GO_VERSION) -e GOPROXY=$(GOPROXY) -e SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH) -v "$(CURDIR):/src" -w /src -v "$(CURDIR)/scripts:/buildscripts:ro" $(ROCKY_IMAGE) sh -c "$(RELEASE_DNF) && tr -d '\r' < /buildscripts/build-release-linux.sh | sh && GOTOOLCHAIN=local /usr/local/go/bin/go test ./internal/parser && tr -d '\r' < /buildscripts/package-release.sh | sh"

# Host-side compatibility target; it defaults to amd64. Windows cannot execute
# Linux ELF binaries, so formal releases should use release-linux-* containers.
package-release:
	ARCH=amd64 VERSION=$(VERSION) SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH) sh scripts/package-release.sh
