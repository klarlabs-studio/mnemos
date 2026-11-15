FROM alpine:3.21@sha256:bc21487101ae47172509130ee1092cc273a831968d64b617776d1f69c81e42ab

LABEL org.opencontainers.image.authors="Felix Geelhaar"

# zlib in the pinned base predates the ALPINE-CVE-2026-4738 fix (1.4-r0).
RUN apk upgrade --no-cache zlib \
 && adduser -D -h /home/mnemos mnemos
# dockers_v2 builds with a per-platform context, so the binary lives under
# its platform directory rather than at the context root.
ARG TARGETPLATFORM
COPY --chown=root:root $TARGETPLATFORM/mnemos /usr/local/bin/

USER mnemos
WORKDIR /home/mnemos

# Only meaningful for `serve`; one-shot CLI invocations exit before the first probe.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["sh", "-c", "wget -qO- http://127.0.0.1:${MNEMOS_SERVE_PORT:-7777}/health >/dev/null"]

ENTRYPOINT ["mnemos"]
