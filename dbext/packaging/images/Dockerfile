# syntax=docker/dockerfile:1.7
ARG PG_MAJOR=16
FROM postgres:${PG_MAJOR}-bookworm
ARG PG_MAJOR
ARG TARGETARCH
ARG SOURCE_REVISION=unknown
LABEL org.opencontainers.image.title="AgentSQL PostgreSQL binder" \
      org.opencontainers.image.version="0.4" \
      org.opencontainers.image.revision="$SOURCE_REVISION" \
      dev.agentsql.postgresql.major="$PG_MAJOR" \
      dev.agentsql.release-gate="DEV-ONLY-NOT-PUBLISHED"
COPY dist/binder/deb/pg${PG_MAJOR}/${TARGETARCH}/agentsql-binder-pg${PG_MAJOR}_0.5.0-1_${TARGETARCH}.deb /tmp/agentsql-binder.deb
COPY dbext/packaging/images/10-agentsql-binder.sql /docker-entrypoint-initdb.d/10-agentsql-binder.sql
COPY dbext/packaging/images/agentsql-binder-selftest.sh /usr/local/bin/agentsql-binder-selftest
RUN dpkg -i /tmp/agentsql-binder.deb && rm -f /tmp/agentsql-binder.deb && \
    chmod 0755 /usr/local/bin/agentsql-binder-selftest && \
    test -L "$(pg_config --pkglibdir)/agentsql_binder.so" && \
    test -f "$(pg_config --pkglibdir)/agentsql_binder-0.4.so" && \
    test -f "$(pg_config --sharedir)/extension/agentsql_binder.control"
