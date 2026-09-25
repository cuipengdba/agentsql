FROM debian:bookworm-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates findutils gnupg && rm -rf /var/lib/apt/lists/*
COPY dbext/packaging/signing/sign-dev.sh /usr/local/bin/sign-dev
COPY dbext/packaging/signing/test-tamper.sh /usr/local/bin/test-signature-tamper
RUN chmod 0755 /usr/local/bin/sign-dev /usr/local/bin/test-signature-tamper
ENTRYPOINT ["/usr/local/bin/sign-dev"]
