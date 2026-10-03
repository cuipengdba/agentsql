# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS build

ARG VERSION=v0.5.0
# Overridable module proxy for restricted networks, e.g.
# `docker build --build-arg GOPROXY=https://goproxy.cn,direct`.
ARG GOPROXY=https://proxy.golang.org,direct
ARG TARGETARCH
ARG YASHAN_CLIENT_VERSION=23.4.7.100
ARG YASHAN_CLIENT_REVISION=a72b24d63ba0e43820c43d7443c0e4fd0ab304fd
ENV GOPROXY=${GOPROXY}
WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc binutils libc6-dev ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

RUN case "${TARGETARCH}" in \
      amd64) client_arch=x86_64; client_sha=403ff0852712a7cfbaeb70269e4acf2391960dddcc374bee4227e68c4680d95f ;; \
      arm64) client_arch=aarch64; client_sha=69752b7bac8962ab0470687052462c7d7f0edb279959e5416910935b984a43d4 ;; \
      *) echo "ERROR: unsupported YashanDB client architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && client_archive="yashandb-client-${YASHAN_CLIENT_VERSION}-linux-${client_arch}.tar.gz" \
    && client_url="https://raw.githubusercontent.com/yashan-technologies/yashandb-client/${YASHAN_CLIENT_REVISION}/${client_archive}" \
    && curl -fsSL --retry 3 -o /tmp/yashandb-client.tar.gz "${client_url}" \
    && printf '%s  %s\n' "${client_sha}" /tmp/yashandb-client.tar.gz | sha256sum -c - \
    && mkdir -p /opt/yashandb-client \
    && tar -xzf /tmp/yashandb-client.tar.gz -C /opt/yashandb-client \
    && test -f /opt/yashandb-client/lib/libyascli.so \
    && test -f /opt/yashandb-client/lib/libyas_infra.so \
    && rm -f /tmp/yashandb-client.tar.gz

# The repository includes internal/webui/dist; frontend tooling is not run here.
COPY . .
RUN mkdir -p /out \
    && GOFLAGS= CGO_ENABLED=1 go build -tags yashan -trimpath \
        -ldflags "-s -w -X github.com/cuipengdba/agentsql/internal/version.Version=${VERSION}" \
        -o /out/agentsql ./cmd/agentsql \
    && GOFLAGS= CGO_ENABLED=1 go build -trimpath \
        -ldflags "-s -w -X github.com/cuipengdba/agentsql/internal/version.Version=${VERSION}" \
        -o /out/agentsqlctl ./cmd/agentsqlctl \
    && go version -m /out/agentsql | grep -E 'github\.com/yashan-technologies/yashandb-go[[:space:]]+v1\.4\.4' >/dev/null

FROM debian:bookworm-slim AS runtime

ARG VERSION=v0.5.0
ENV LD_LIBRARY_PATH=/opt/yashandb-client/lib
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
COPY --from=build /opt/yashandb-client/lib /opt/yashandb-client/lib
COPY examples/docker/config.yaml /etc/agentsql/config.yaml
COPY LICENSE /usr/share/licenses/agentsql/LICENSE

USER agentsql
WORKDIR /var/lib/agentsql
EXPOSE 7780

HEALTHCHECK --interval=15s --timeout=3s --retries=3 CMD ["agentsqlctl", "health", "--url", "http://127.0.0.1:7780/healthz"]

ENTRYPOINT ["agentsql"]
CMD ["serve", "--config", "/etc/agentsql/config.yaml"]
