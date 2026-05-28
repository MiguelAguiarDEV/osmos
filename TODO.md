# Clip‑Sync — Checklist v1

- [X] Initial repo and Go workspace configured
- [X] Basic HTTP mux with `/health`
- [X] Fan‑out hub implemented
- [X] WS server registered at `/ws`
- [X] MVP auth (token == userID)
- [X] WS keep‑alive (ping/pong + timeout)

- [X] `/upload` endpoint with `MaxBytes` limit and random IDs
- [X] `/upload` unit tests passing
- [X] `/upload` route registered in mux
- [X] E2E tests: basic WS + upload+signal (UploadAndSignal)

- [X] Negative WS tests: inconsistent size, empty clip
- [X] WS validations: `len(Data)==Size`, `Size<=MaxInlineBytes`, `UploadURL` when `Data` is empty
- [X] Default `mime` assignment when missing
- [X] Per‑device rate limit
- [X] Metrics: clips, drops, connections counters
- [X] Structured logs: request id, device, user
- [X] `/healthz` with minimal metrics
- [X] Graceful shutdown: `http.Server` + hub drain
- [X] Optional dedupe by `msg_id` (small LRU)
- [X] Visible backpressure: per‑device drop counters
- [X] HMAC auth → `userID` + expiry

- [X] Validate `device_id` (format; optional registry)
- [X] `/upload`: allow only accepted types
- [X] Flags/env: ports, limits, upload dir, log level
- [X] `/debug/pprof` and optional `expvar`

- [X] Makefile for build/test/run
- [X] CI (GitHub Actions): lint, `go test ./...`, build server+cli

- [X] CLI: exponential reconnection
- [X] CLI: stable pipe mode (`echo hello | clip-sync --mode send`)
- [X] CLI: simple MIME detection by extension
- [X] CLI: clean stderr and consistent exit codes

- [X] Lightweight hub benchmarks
- [X] Envelope JSON fuzzing (small corpus)

- [X] Protocol docs (hello, inline clip, `upload_url`)
- [X] Quickstart README (server + 2 clients)
- [X] Postman collection
- [X] v1 changelog

- [X] CLI: `watch` monitors clipboard and publishes over WS
- [X] CLI: `recv` applies incoming clips to clipboard
- [X] Large clips: upload in `watch`, download in `recv`
- [X] Dedupe by `msg_id` in server and client
- [X] Binaries for Windows, macOS and Linux (release workflow)

## v1.1 — Reliability & cross-platform

- [X] Raise WS read limit so inline clips up to 64 KiB are delivered
- [X] Make HMAC auth work through the CLI (route by authenticated identity)
- [X] Fix `sync` re-send loop with non-idempotent clipboard backends (Windows newline)
- [X] Reconnect with backoff in `recv`/`watch`/`sync` (not just `listen`)
- [X] WS keep-alive ping to detect dead/half-open connections (client + server)
- [X] macOS clipboard backend (`pbcopy`/`pbpaste`)
- [X] TTL garbage collection for `/upload` blobs
- [X] Remove unused `hub` package
- [X] Large clips / `send --file` HTTP base fix (was 404 with `/ws` address)
- [X] Connection-lifecycle correctness (identity-checked removal, no metric drift, map cleanup)
- [X] Hello deadline (slowloris guard) + parallel broadcast fan-out
- [X] Authenticate `/upload` and `/d/{id}`; per-user blob isolation
- [X] Default per-device rate limit; graceful shutdown on SIGTERM
- [X] CI: race detector, CLI tests, Go version from `go.work`

## Future Enhancements

- [ ] Normalize line endings on apply so clipboard content is byte-identical across platforms (product decision: changes user content)
- [ ] Sync images/binary clipboard content (not just text)
- [ ] Horizontal scale-out (shared pub/sub instead of in-memory per-instance fan-out)
- [ ] Auto-configure server with default settings
- [ ] Display local network IP + port on server startup for easy client connection
- [ ] Graphical user interface (GUI) for server and client
