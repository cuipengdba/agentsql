# syntax=docker/dockerfile:1

FROM golang:1.25-bookworm AS build

ARG VERSION=v0.5.0
# Overridable module proxy for restricted networks, e.g.
# `docker build --build-arg GOPROXY=https://goproxy.cn,direct`.
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc binutils libc6-dev ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

# The repository includes internal/webui/dist; frontend tooling is not run here.
COPY . .
RUN mkdir -p /out \
    && GOFLAGS= CGO_ENABLED=1 go build -trimpath \
        -ldflags "-s -w -X github.com/cuipengdba/agentsql/internal/version.Version=${VERSION}" \
        -o /out/agentsql ./cmd/agentsql \
    && GOFLAGS= CGO_ENABLED=1 go build -trimpath \
        -ldflags "-s -w -X github.com/cuipengdba/agentsql/internal/version.Version=${VERSION}" \
        -o /out/agentsqlctl ./cmd/agentsqlctl \
    && for binary in /out/agentsql /out/agentsqlctl; do \
         if go version -m "$binary" | grep -F 'github.com/yashan-technologies/yashandb-go' >/dev/null; then \
           echo "ERROR: $binary includes the optional YashanDB Go driver" >&2; exit 1; \
         fi; \
         if strings "$binary" | grep -E 'libyas(cli|_infra)(\.so|\.dylib|\.dll|$)' >/dev/null; then \
           echo "ERROR: $binary contains a YashanDB client marker" >&2; exit 1; \
         fi; \
         if readelf -d "$binary" 2>/dev/null | grep -E 'libyas(cli|_infra)' >/dev/null; then \
           echo "ERROR: $binary dynamically links a YashanDB client library" >&2; exit 1; \
         fi; \
       done

FROM debian:bookworm-slim AS runtime

ARG VERSION=v0.5.0
LABEL org.opencontainers.image.title="AgentSQL" \
      org.opencontainers.image.source="https://github.com/cuipengdba/agentsql" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system agentsql \
    && useradd --system --gid agentsql --home-dir /var/lib/agentsql --shell /usr/sbin/nologin agentsql \
    && mkdir -p /var/lib/agentsql /etc/agentsql \
    && chown -R agentsql:agentsql /var/lib/agentsql /etc/agentsql

COPY --from=build /out/agentsql /usr/local/bin/agentsql
COPY --from=build /out/agentsqlctl /usr/local/bin/agentsqlctl
COPY examples/docker/config.yaml /etc/agentsql/config.yaml
COPY LICENSE /usr/share/licenses/agentsql/LICENSE

USER agentsql
WORKDIR /var/lib/agentsql
EXPOSE 7780

HEALTHCHECK --interval=15s --timeout=3s --retries=3 CMD ["agentsqlctl", "health", "--url", "http://127.0.0.1:7780/healthz"]

ENTRYPOINT ["agentsql"]
CMD ["serve", "--config", "/etc/agentsql/config.yaml"]
