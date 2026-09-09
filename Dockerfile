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

# rclone mirrors the cloud folder; tzdata is required because the reporting
# timezone is configured explicitly and looked up by IANA name.
RUN apk add --no-cache ca-certificates tzdata rclone wget \
 && adduser -D -u 10001 tracker \
 && mkdir -p /data \
 && chown -R tracker:tracker /data

COPY --from=build /out/tracker /usr/local/bin/tracker

# Runs as UID 10001. Deployment uses a host bind mount, whose ownership comes
# from the host rather than from this image, so the mounted directory must be
# owned by this UID; see the deployment section of the README.
USER tracker
WORKDIR /data
EXPOSE 8381

ENV DATA_DIR=/data \
    PORT=8381 \
    APP_TZ=UTC

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8381/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/tracker"]
