# Contributing to Clip‑Sync

Thanks for your interest! This guide helps you set up the project and submit high‑quality contributions.

## Project layout

- `server/` — Go module for the backend (HTTP + WebSocket)
- `clients/cli/` — Go module for the CLI client
- `go.work` — Go workspace including both modules

## Getting started

1) Install Go 1.22+ and Git.
2) Clone the repo and sync workspace:

```
go work sync
```

3) Run tests:

```
go -C server test ./...
go -C clients/cli test ./...
```

## Development workflow

- Keep PRs focused and covered by tests (unit or integration where applicable).
- Follow TDD when adding new behavior.
- Use clear commit messages (imperative mood) and descriptive PR titles/bodies.
- Run `go vet` and tests locally before opening a PR.
- The CI runs vet, tests and builds for both modules.

## Coding guidelines

- Prefer readability and simplicity; avoid premature abstractions.
- Keep public APIs minimal; use internal packages for implementation.
- Error messages should be concise and actionable.
- Logs are structured JSON in the server; avoid noisy logs.

## Fuzzing

- A fuzz test for the JSON Envelope exists; feel free to add seeds or invariants.

## Releases

- Tag the repo with `vX.Y.Z` to trigger the `release` workflow.
- It builds and publishes server + CLI binaries for Linux/Windows/macOS (amd64/arm64).

## Auth and protocol

- See `docs/protocol.md` for protocol details and environment flags.
- HMAC token format: `userID:exp_unix:hex(hmac_sha256(secret, userID|exp))`.

## Reporting issues

- Provide steps to reproduce, logs, and environment details.
- Include expected vs actual behavior and any workarounds tried.

## Code of conduct

- Be respectful and constructive. We collaborate to build a small, robust tool.

