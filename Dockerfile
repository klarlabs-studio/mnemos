FROM alpine:3.21

RUN adduser -D -h /home/mnemos mnemos
# dockers_v2 builds with a per-platform context, so the binary lives under
# its platform directory rather than at the context root.
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/mnemos /usr/local/bin/

USER mnemos
WORKDIR /home/mnemos

ENTRYPOINT ["mnemos"]
