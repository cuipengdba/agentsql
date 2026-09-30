# Self-contained Live Demo PostgreSQL (binder) with demo init scripts baked in.
FROM ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.4

COPY *.sql /docker-entrypoint-initdb.d/
COPY *.sh /docker-entrypoint-initdb.d/
USER root
RUN chmod 0644 /docker-entrypoint-initdb.d/*.sql \
    && chmod 0755 /docker-entrypoint-initdb.d/*.sh
USER 999
