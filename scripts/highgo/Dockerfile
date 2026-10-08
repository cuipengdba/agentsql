FROM rockylinux:8

ENV HGDB_HOME=/opt/highgo \
    HGDB_DATA=/var/lib/highgo/data \
    HGDB_PORT=5866 \
    PATH=/opt/highgo/bin:$PATH \
    LD_LIBRARY_PATH=/opt/highgo/lib:/opt/highgo/lib/postgresql

COPY installer.bin /tmp/installer.bin
COPY install-image.sh /tmp/install-image.sh
COPY entrypoint.sh /usr/local/bin/highgo-entrypoint.sh

# The secret is mounted over an empty file at the vendor's confirmed target.
# Its mount is read only, so the vendor's cp cannot put license bytes in a layer.
RUN mkdir -p /opt/highgo/license /var/lib/highgo \
    && : > /opt/highgo/license/license.dat \
    && useradd -u 2000 -m highgo \
    && chown -R highgo:highgo /opt/highgo /var/lib/highgo \
    && chmod 0755 /tmp/install-image.sh /usr/local/bin/highgo-entrypoint.sh
RUN --mount=type=secret,id=highgo_license,target=/opt/highgo/license/license.dat,uid=2000,gid=2000,mode=0400,required=true \
    /tmp/install-image.sh
RUN chown -R highgo:highgo /var/lib/highgo \
    && test ! -s /opt/highgo/license/license.dat \
    && rm -f /tmp/installer.bin /tmp/install-image.sh

USER highgo
EXPOSE 5866
ENTRYPOINT ["/usr/local/bin/highgo-entrypoint.sh"]
