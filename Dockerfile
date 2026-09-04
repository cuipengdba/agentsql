# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS build

ARG VERSION=dev
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/agentsql ./cmd/agentsql

FROM alpine:3.21

RUN addgroup -S agentsql \
    && adduser -S -G agentsql agentsql \
    && mkdir -p /var/lib/agentsql \
    && chown -R agentsql:agentsql /var/lib/agentsql

COPY --from=build /out/agentsql /usr/local/bin/agentsql
COPY examples/config.example.yaml /etc/agentsql/config.yaml

USER agentsql
WORKDIR /var/lib/agentsql
EXPOSE 7780

ENTRYPOINT ["agentsql"]
CMD ["serve", "--config", "/etc/agentsql/config.yaml"]
