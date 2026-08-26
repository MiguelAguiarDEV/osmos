# Clip‑Sync Protocol v1

Audience: developers and recruiters. Wire details for the WebSocket + HTTP flows.

## Table of Contents
- [Overview](#overview)
- [Envelopes](#envelopes)
  - [Hello](#hello)
  - [Clip](#clip)
- [HTTP API](#http-api)
  - [POST /upload](#post-upload)
  - [GET /d/{id}](#get-d)
  - [GET /health](#get-health)
  - [GET /healthz](#get-healthz)
- [Server configuration](#server-configuration)
- [CLI behavior](#cli-behavior)
- [Limits](#limits)
- [Observability](#observability)
- [See also](#see-also)

<a id="overview"></a>
## Overview

- Session established over WebSocket at `/ws`.
- Client sends a `hello` envelope to authenticate and identify device.
- Clips are delivered as either inline payloads or via an HTTP upload URL.

<a id="envelopes"></a>
## Envelopes

Top‑level shape:

```json
{
  "type": "hello|clip",
  "from": "<device_id>",
  "hello": { "token": "...", "user_id": "...", "device_id": "..." },
  "clip": { "msg_id": "...", "mime": "...", "size": 0, "data": "...", "upload_url": "..." }
}
```

<a id="hello"></a>
### Hello

- `type`: `"hello"`
- `hello.token`: authentication token. **This is the only source of identity.**
- `hello.user_id`: informational only. The server derives the user from the
  verified token and ignores this field, so a client cannot join another
  user's room by declaring a different `user_id`.
- `hello.device_id`: unique device id within the user namespace.

Validation:
- `device_id` must match `^[A-Za-z0-9_-]{1,64}$`. A malformed id closes the
  connection with `1008 Policy Violation` / `invalid device_id`.
- If HMAC auth is enabled (`CLIPSYNC_HMAC_SECRET`), token must be `userID:exp_unix:hex(hmac_sha256(secret, userID|exp_unix))` and `exp_unix` must be in the future. A bad or expired token closes with `1008` / `unauthorized`.
- A `clip` received before a valid `hello` is dropped and counted.

Duplicate device ids:
- If a second connection sends `hello` with a `device_id` already in use for
  that user, the **older** connection is closed with `1008` / `duplicate_device_id`.
- Clients must treat `1008 Policy Violation` as terminal and **not** reconnect:
  it signals a configuration error, and retrying makes two processes sharing a
  `device_id` evict each other in an endless loop.

<a id="clip"></a>
### Clip

- `type`: `"clip"`
- `clip.msg_id` (optional): used for deduplication.
- `clip.mime`: MIME type. Defaults to `application/octet-stream` when empty.
- `clip.size`: total size in bytes.
- `clip.data` (optional): inline payload. When present:
  - `len(data) == size`
  - `size <= MaxInlineBytes` (64 KiB by default; see `CLIPSYNC_INLINE_MAXBYTES`).
- `clip.upload_url` (optional): HTTP path (e.g., `/d/<id>`) obtained from `/upload` when the clip is too large to send inline. When `data` is absent, `upload_url` must be present and `size > 0`.

Broadcast:
- The server fans out the clip to all other devices of the same user.
- The `from` field is set to the sender `device_id`.
- Each receiving connection has its own bounded outbound queue, so a slow
  device cannot stall delivery to the rest.

Backpressure and rate limits:
- Per‑device token bucket controlled by `CLIPSYNC_RATE_LPS`.
- Drops are counted globally and per device.

Deduplication:
- Optional LRU per user controlled by `CLIPSYNC_DEDUPE` capacity (0 disables).
- When a duplicate `msg_id` is detected, the message is dropped.

Client dedupe (recommended):
- Receivers may drop repeated `msg_id` values locally to avoid reapplying the same clip.
- Senders may choose a stable `msg_id` for clipboard-driven events, e.g., `h-<sha|fnv>` of the text, so transient watchers do not flood duplicates.

<a id="http-api"></a>
## HTTP API

<a id="post-upload"></a>
### POST /upload

Stores a blob and returns a download URL.

Request:
- Body: raw bytes.
- Header: `Content-Type` validated against whitelist when configured.

Env/flags:
- `CLIPSYNC_UPLOAD_DIR` or `--upload-dir` (default `./uploads`)
- `CLIPSYNC_UPLOAD_MAXBYTES` or `--upload-max-bytes` (default `50MiB`)
- `CLIPSYNC_UPLOAD_ALLOWED` or `--upload-allowed` (comma‑separated MIME list, supports wildcards like `image/*`). Empty disables whitelist.

Response:

```json
{ "upload_url": "/d/<id>", "size": 12345 }
```

Status codes:
- 200 OK: stored.
- 413 Payload Too Large: exceeds `MaxBytes`.
- 415 Unsupported Media Type: MIME not in whitelist.
- 5xx: storage or I/O errors.

<a id="get-d"></a>
### GET /d/{id}

Streams the stored blob with `Content-Type: application/octet-stream`.

<a id="get-health"></a>
### GET /health

Returns `200 ok` for liveness checks.

<a id="get-healthz"></a>
### GET /healthz

Returns JSON with basic metrics: `clips_total`, `drops_total`, `conns_current`, and per‑device drops as `drops_device:<user|device>`.

<a id="server-configuration"></a>
## Server configuration

Flags (all have env equivalents):
- `--addr` (`CLIPSYNC_ADDR`): listen address, default `:8080`.
- `--upload-dir`, `--upload-max-bytes`, `--upload-allowed`.
- `--inline-max-bytes` (`CLIPSYNC_INLINE_MAXBYTES`).
- `--log-level` (`CLIPSYNC_LOG_LEVEL`): `debug|info|error|off`.
- `--pprof` (`CLIPSYNC_PPROF`) and `--expvar` (`CLIPSYNC_EXPVAR`).

Auth:
- MVP: `token == user_id` when `CLIPSYNC_HMAC_SECRET` is unset.
- HMAC mode: token format `userID:exp_unix:hex(hmac_sha256(secret, userID|exp))`.

<a id="cli-behavior"></a>
## CLI behavior

- All long-running modes (`listen`, `recv`, `watch`, `sync`) reconnect with
  exponential backoff (500 ms → 5 s), resetting after a successful connect.
  They stop only on `1008 Policy Violation` or on SIGINT/SIGTERM.
- `sync` runs receive and watch over the same connection; if either side
  fails, both are torn down and the connection is re-established.
- Clipboard text is canonicalised (CRLF → LF, no trailing newline) on both
  read and apply. Without this, `Get-Clipboard` on Windows (which appends
  CRLF) and `wl-paste -n` on Linux (which strips the trailing newline)
  disagree about the same content and re-send it to each other forever.
- `send` mode:
  - `--text` inline if ≤ MaxInlineBytes.
  - `--file` uploads to `/upload` with MIME auto‑detected by extension when not provided.
  - Stable pipe: when input is piped to stdin, reads up to MaxInlineBytes inline; otherwise spills to a temp file and uploads; MIME heuristic: valid UTF‑8 → `text/plain`, else `application/octet-stream`.
  - `--text` is sent verbatim; leading/trailing whitespace is preserved.
- The HTTP base for `/upload` and `/d/{id}` is derived from `--addr` by
  replacing the scheme and **dropping the path**: `ws://host:8080/ws` →
  `http://host:8080`.
- Exit codes: usage=2, connect=10, upload=11, send=12, policy=13.

<a id="limits"></a>
## Limits

- `MaxInlineBytes` default 64 KiB. Change via env/flag.
- Upload server `MaxBytes` default 50 MiB.
- The WebSocket read limit is derived from `MaxInlineBytes` (`2×max + 4 KiB`)
  because inline payloads travel base64-encoded inside the JSON envelope and
  therefore expand by ~4/3. Custom clients must raise their own read limit the
  same way; the 32 KiB default of most WebSocket libraries is not enough for a
  64 KiB clip.

<a id="observability"></a>
## Observability
- `/health`: liveness probe (200 OK).
- `/healthz`: JSON metrics snapshot.
- Optional `/debug/pprof/*` and `/debug/vars` (expvar) when enabled.

<a id="see-also"></a>
## See also
- Postman collection: [docs/Clip-Sync.postman_collection.json](Clip-Sync.postman_collection.json)
- README overview: [../README.md](../README.md)
