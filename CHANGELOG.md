# Changelog

## Unreleased

- Fixes
  - WebSocket read limit raised on server and client so inline clips up to the
    64 KiB cap are delivered (the 32 KiB library default silently dropped clips
    over ~24 KiB of raw data).
  - HMAC auth now works through the CLI: the server routes by the authenticated
    identity instead of rejecting the client-sent `user_id`.
  - Large clips / `send --file` no longer 404: the HTTP base is derived as
    `scheme://host`, not keeping the `/ws` path.
  - `sync` no longer enters a re-send loop when a clipboard backend is
    non-idempotent on read-back (e.g. Windows `Get-Clipboard` newline).
  - Reconnecting with the same `device_id` no longer orphans the new
    connection; `conns_current` no longer drifts upward.
  - `watch` no longer falsely reconnects (~every 30s) — it drains reads so
    keep-alive PONGs are processed.
- CLI
  - `recv`/`watch`/`sync` reconnect with backoff instead of exiting on a drop;
    permanent rejections (bad token) exit instead of hammering.
  - WebSocket keep-alive ping detects dead/half-open connections.
  - Received `upload_url` downloads are bounded (timeout + size cap).
  - Fails fast with a clear message when no clipboard backend is installed.
  - Line endings normalized to LF (trailing trimmed) so synced content is
    byte-identical across platforms (`--normalize-eol=false` to opt out).
  - macOS clipboard backend (`pbcopy`/`pbpaste`).
- Server
  - `/upload` and `/d/{id}` now require the bearer token, and blobs are isolated
    per user (another user can't fetch a blob by id).
  - Per-connection ping drops dead clients; unauthenticated connections are
    closed on a hello deadline (slowloris guard).
  - Broadcasts fan out in parallel so a slow peer can't stall the room.
  - Per-device rate limit defaults to 100/s (`--rate-lps`, 0 disables).
  - Optional TTL garbage collection of `/upload` blobs (`--upload-ttl`).
  - Graceful shutdown on SIGTERM; `users_current` metric in `/healthz`.
  - Prints LAN client addresses on startup.
  - Removed the unused `hub` package; clip fan-out goes through a pluggable
    broker (Local by default).
  - Horizontal scale-out: optional Redis pub/sub fan-out across instances
    (`--redis-url`).
- Tests & CI
  - Race detector and CLI tests now run in CI; Go version read from `go.work`.
  - Added reconnect, hello-timeout, upload-auth, per-user isolation, churn
    stress, and end-to-end CLI sync tests.

## v1.0.0 (2025-09-27)

- Server
  - WebSocket `/ws` with hello+clip envelopes
  - Fan-out hub per user, no echo to sender
  - Validations: `len(data)==size`, inline size limit, `upload_url` required for large clips
  - Rate limit per device (token bucket) with drop counters and metrics
  - Optional deduplication by `msg_id` (LRU per user)
  - HTTP `/upload` with max bytes and random IDs; `/d/{id}` download
  - MIME whitelist for `/upload` (exact and wildcard `type/*`)
  - `/healthz` metrics, optional `/debug/pprof` and `/debug/vars`
  - Structured logs and graceful shutdown
  - HMAC auth support (token `user:exp:mac`), fallback token==user
  - Flags/env for ports, limits, dirs, log level
- CLI
  - `listen` mode with exponential reconnection backoff
  - `send` mode: `--text` or `--file`; auto-detect MIME by extension
  - Stable pipe mode: stdin inline or auto-upload via temp file; UTF-8 → text/plain
  - Clean stderr output and explicit exit codes
- Tests & CI
  - Unit + integration tests; GitHub Actions vet/test/build
  - Hub fan-out benchmarks; Envelope JSON fuzz test
- Docs
  - Protocol spec, Quickstart, Postman collection

Thanks to all contributors.
