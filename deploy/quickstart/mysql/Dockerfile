# Self-contained Live Demo MySQL with demo init scripts baked in.
FROM mysql:8

COPY *.sql /docker-entrypoint-initdb.d/
COPY *.sh /docker-entrypoint-initdb.d/
COPY *.inc /docker-entrypoint-initdb.d/
USER root
RUN chmod 0644 /docker-entrypoint-initdb.d/*.sql /docker-entrypoint-initdb.d/*.inc \
    && chmod 0755 /docker-entrypoint-initdb.d/*.sh
USER mysql
