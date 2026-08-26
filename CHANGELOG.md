# Changelog

## Unreleased

Bug fixes and hardening. No new features.

- Server
  - **Inline clips up to 64 KiB now actually work.** The WebSocket read limit
    was left at the library default of 32 KiB while `MaxInlineBytes` was
    64 KiB, so any large clip silently killed the connection instead of being
    delivered. The limit is now derived from `MaxInlineBytes` with room for
    base64 expansion.
  - **HMAC auth was unusable.** The user id is now taken from the verified
    token instead of the client-supplied `hello.user_id`, which also prevents a
    client from joining another user's room by declaring someone else's id.
  - Reconnecting with a `device_id` already in use no longer lets the old
    connection's teardown deregister the new one; the old session is closed
    with `1008 duplicate_device_id`.
  - Fan-out moved off the reader goroutine to a bounded per-connection queue:
    a slow or stalled device no longer blocks delivery to the others for up to
    a second each.
  - Removed the unconditional 50 ms sleep on every broadcast; the join grace
    period now only applies during the first 300 ms of a connection.
  - Fixed a data race on the per-device rate limiter and on the dedupe cache.
  - `clip` envelopes received before a valid `hello` are dropped instead of
    being broadcast to an empty room.
  - SIGTERM now triggers the graceful shutdown path (systemd and `docker stop`
    send SIGTERM, not SIGINT).
  - Added `ReadHeaderTimeout`; the listener is opened before announcing so a
    busy port fails immediately instead of after printing "listening".
- CLI
  - **`--file` and all large clips were broken end to end**: the HTTP base was
    derived from the WebSocket URL without dropping its path, so uploads went
    to `http://host/ws/upload` and got a 404. This affected `--mode send
    --file`, uploads in `watch` and downloads in `recv`.
  - **`recv`, `watch` and `sync` never reconnected.** They connected once and
    died on the first server restart; in `sync` the receiving goroutine died
    silently while the watcher kept writing to a dead socket forever. All
    long-running modes now share one supervisor with exponential backoff.
  - A close with `1008 Policy Violation` (bad token, invalid or duplicate
    `device_id`) is terminal and exits with code 13, instead of retrying in an
    endless loop.
  - Client-side WebSocket read limit raised to match a 64 KiB inline clip.
  - Clipboard text is canonicalised (CRLF → LF, no trailing newline), fixing an
    endless re-send loop between Windows and Linux caused by `Get-Clipboard`
    appending CRLF while `wl-paste -n` strips the trailing newline.
  - `wl-copy`/`xclip`/`xsel` writes no longer risk hanging forever: those tools
    daemonise and inherit the pipes that `CombinedOutput` waits on.
  - Clipboard reads use `Output()` so a backend warning on stderr can no longer
    end up pasted into the clipboard content, and errors now name the backend
    and include its stderr instead of always claiming no backend was found.
  - `--text` is sent verbatim; it used to be silently trimmed.
  - A transient clipboard read failure no longer tears down the session
    (10 consecutive failures still do).
  - SIGINT/SIGTERM shut down cleanly; added `--version`; `--poll-ms` is validated.
- Build & CI
  - `make test` and `make lint` were broken: `go test ./...` cannot run from the
    root of a multi-module workspace. Added per-module `test`, `race`, `vet`,
    `fmt-check` and `dist` targets.
  - CI now checks gofmt, runs the CLI test suite (it never did) and runs
    everything under `-race`; Go version raised to match `go.work`.
  - `gofmt` applied across the tree (16 files were unformatted).
- Docs
  - README no longer points at `dist/*windows*` binaries and a `scripts/`
    directory that do not exist.
  - Protocol spec documents the identity rules, the duplicate-device behaviour,
    the read-limit requirement and the clipboard canonicalisation.

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
