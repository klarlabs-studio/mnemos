# One Dockerfile, two binary sources (#508).
#
# A release builds from GoReleaser's per-platform context, where the binary
# already sits at $TARGETPLATFORM/mnemos; .goreleaser.yaml selects that with
# --build-arg BINARY_SOURCE=release. Any other build (the shared CI smoke job,
# a developer's `docker build .`) gets the default and compiles from source.
# BuildKit builds only the stage the final image copies from, so neither path
# needs the other's inputs.
ARG BINARY_SOURCE=source

FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS binary-source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/mnemos ./cmd/mnemos

FROM scratch AS binary-release
# dockers_v2 builds with a per-platform context, so the binary lives under
# its platform directory rather than at the context root.
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/mnemos /out/mnemos

FROM binary-${BINARY_SOURCE} AS binary

FROM alpine:3.21@sha256:bc21487101ae47172509130ee1092cc273a831968d64b617776d1f69c81e42ab

LABEL org.opencontainers.image.authors="Felix Geelhaar"

# Pinning the digest surfaced ALPINE-CVE-2026-4738 in the base image's zlib,
# fixed in zlib 1.4-r0.
RUN apk upgrade --no-cache zlib && adduser -D -h /home/mnemos mnemos
COPY --from=binary /out/mnemos /usr/local/bin/mnemos

USER mnemos
WORKDIR /home/mnemos

# Probes `mnemos serve` on its default port. /health is unauthenticated by
# design; a container started for a one-off CLI command exits before the
# first probe.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:7777/health || exit 1

ENTRYPOINT ["mnemos"]
