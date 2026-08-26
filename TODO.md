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

## Future Enhancements

Interface
- [ ] TUI for the client (connection status, device list, clip history, live log)
- [ ] TUI for the server (connected devices, throughput, drops)
- [ ] `osmos` single entry-point command with subcommands instead of `--mode`
- [ ] Config file (`~/.config/osmos/config.toml`) so flags need not be retyped
- [ ] Graphical user interface (GUI) for server and client

Usability
- [ ] Auto-configure server with default settings
- [ ] Display local network IP + port on server startup for easy client connection
- [ ] Auto-generate a unique `--device` id from the hostname
- [ ] `osmos doctor`: check clipboard backend, connectivity and firewall

Sync capabilities
- [ ] Sync non-text clips (images, files) in `recv`/`watch`, not just `text/*`
- [ ] Clipboard history with the ability to re-apply a previous clip
- [ ] Event-driven clipboard watching instead of polling every 400 ms

Server
- [ ] TTL / garbage collection for `/upload` blobs (they accumulate forever)
- [ ] Native TLS (today it requires a reverse proxy)
- [ ] End-to-end encryption so the server never sees clipboard contents
- [ ] Persistence, so a device that was offline receives what it missed
- [ ] Horizontal scale-out (the hub is in-process only)

Packaging
- [ ] Publish binaries as GitHub Releases artifacts instead of committing `dist/`
- [ ] systemd units and a Windows service/startup entry
- [ ] Packages: AUR, Homebrew, winget, Docker image
- [ ] Align the module path and docs with the `osmos` repository name (both still say `clip-sync`)
