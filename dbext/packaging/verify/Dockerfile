# syntax=docker/dockerfile:1.7
ARG PG_MAJOR=16
ARG ALPINE_VERSION=3.21
FROM postgres:${PG_MAJOR}-bookworm AS verify-deb
ARG PACKAGE_FILE
COPY ${PACKAGE_FILE} /tmp/agentsql-binder.deb
COPY dbext/packaging/verify/verify-package.sh /usr/local/bin/verify-package
RUN dpkg -i /tmp/agentsql-binder.deb && chmod 0755 /usr/local/bin/verify-package
ENTRYPOINT ["/usr/local/bin/verify-package"]

FROM rockylinux:9 AS verify-rpm-base
ARG TARGETARCH
RUN set -eux; case "$TARGETARCH" in amd64) repo_arch=x86_64;; arm64) repo_arch=aarch64;; *) exit 64;; esac; \
    dnf install -y dnf-plugins-core; dnf config-manager --set-enabled crb; \
    dnf install -y "https://download.postgresql.org/pub/repos/yum/reporpms/EL-9-${repo_arch}/pgdg-redhat-repo-latest.noarch.rpm"; \
    dnf config-manager --set-disabled 'pgdg*'; dnf -qy module disable postgresql

FROM verify-rpm-base AS verify-rpm
ARG PG_MAJOR PACKAGE_FILE
RUN set -eux; \
    dnf config-manager --set-enabled pgdg-common "pgdg${PG_MAJOR}"; \
    dnf install -y "postgresql${PG_MAJOR}-server" util-linux; dnf clean all
COPY ${PACKAGE_FILE} /tmp/agentsql-binder.rpm
COPY dbext/packaging/verify/verify-package.sh /usr/local/bin/verify-package
RUN rpm -ivh /tmp/agentsql-binder.rpm && chmod 0755 /usr/local/bin/verify-package
ENTRYPOINT ["/usr/local/bin/verify-package"]

FROM alpine:${ALPINE_VERSION} AS verify-apk
ARG PG_MAJOR PACKAGE_FILE
RUN apk add --no-cache "postgresql${PG_MAJOR}" su-exec
COPY ${PACKAGE_FILE} /tmp/agentsql-binder.apk
COPY dbext/packaging/verify/verify-package.sh /usr/local/bin/verify-package
RUN apk add --allow-untrusted /tmp/agentsql-binder.apk && chmod 0755 /usr/local/bin/verify-package
ENTRYPOINT ["/usr/local/bin/verify-package"]
