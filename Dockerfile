# syntax=docker/dockerfile:1.7
#
# Multi-stage build for the clip-sync server.
# - Static binary (no CGO) for portability.
# - Distroless runtime (no shell, small attack surface).
# - Runs as non-root, listens on :8080, all configuration via env vars.
#
# Dokploy: connect the repo, set env vars in the panel, expose 8080 — done.
#
# Sensible env vars (override in Dokploy / docker run):
#   CLIPSYNC_ADDR              listen address (default :8080)
#   CLIPSYNC_HMAC_SECRET       enable HMAC tokens (recommended in prod)
#   CLIPSYNC_UPLOAD_MAXBYTES   cap blob size (e.g. 5242880 for 5 MiB)
#   CLIPSYNC_UPLOAD_TTL        auto-delete old blobs (e.g. 24h)
#   CLIPSYNC_RATE_LPS          per-device rate limit (default 100, 0 disables)
#   CLIPSYNC_REDIS_URL         optional, multi-instance fan-out
#   CLIPSYNC_LOG_LEVEL         debug|info|error|off

FROM golang:1.24-alpine AS build
WORKDIR /src/server

# Cache module downloads
COPY server/go.mod server/go.sum ./
RUN go mod download

# Build
COPY server/ ./
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/clipsync-server ./cmd/server

# Empty data dir copied into the runtime so the nonroot user can write uploads.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/clipsync-server /clipsync-server
COPY --from=build --chown=nonroot:nonroot /out/data /data

ENV CLIPSYNC_ADDR=:8080 \
    CLIPSYNC_UPLOAD_DIR=/data/uploads

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/clipsync-server"]
