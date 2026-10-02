# Self-contained Live Demo gateway: demo config + seed manifest baked in.
# Demo-only. Do not use for production.
FROM ghcr.io/cuipengdba/agentsql:v0.5.0-demo

USER root
COPY config.demo.yaml /etc/agentsql/config.demo.yaml
COPY demo-seed.yaml /etc/agentsql/demo-seed.yaml
RUN chown agentsql:agentsql /etc/agentsql/config.demo.yaml /etc/agentsql/demo-seed.yaml \
    && chmod 0644 /etc/agentsql/config.demo.yaml /etc/agentsql/demo-seed.yaml
USER agentsql
