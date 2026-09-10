# The tracker ships as a single static binary with its templates and front-end
# assets embedded, so the runtime image needs no toolchain and no asset build.

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies are resolved first so that editing source does not invalidate
# the module cache layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tracker ./cmd/tracker

FROM alpine:3.21

# tzdata is required because the reporting timezone is looked up by IANA name,
# ca-certificates to reach the Drive API, and su-exec lets the entrypoint drop
# privileges after preparing the data directory.
RUN apk add --no-cache ca-certificates tzdata wget su-exec \
 && adduser -D -u 10001 tracker \
 && mkdir -p /data \
 && chown -R tracker:tracker /data

COPY --from=build /out/tracker /usr/local/bin/tracker
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# The container starts as root only long enough to hand the data directory to
# uid 10001, which a bind mount from the host will not have done, and then drops
# to that user. The tracker process itself never runs as root.
WORKDIR /data
EXPOSE 8381

ENV DATA_DIR=/data \
    PORT=8381 \
    APP_TZ=UTC

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8381/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/tracker"]
