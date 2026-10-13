# Self-contained Live Demo PostgreSQL (binder) with demo init scripts baked in.
FROM ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.5

# The base image's legacy check requires extension_version=0.4, while the
# current binder reports 0.4-s3m. Our 00_agentsql_binder.sql checks the full
# capability contract before demo data is loaded.
RUN rm /docker-entrypoint-initdb.d/10-agentsql-binder.sql

COPY *.sql /docker-entrypoint-initdb.d/
COPY *.sh /docker-entrypoint-initdb.d/
USER root
RUN chmod 0644 /docker-entrypoint-initdb.d/*.sql \
    && chmod 0755 /docker-entrypoint-initdb.d/*.sh
USER 999
