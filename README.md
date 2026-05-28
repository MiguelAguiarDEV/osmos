# clip-sync

Clip-Sync is a lightweight CLI to sync your clipboard across Windows and Linux, on the same LAN or over the Internet. Small payloads go inline via /ws; large payloads use /upload and are shared by an upload_url. Each user gets a hub with fan-out to devices and no echo to the sender. Built in Go with WebSocket signaling and HTTP for large transfers.

## Table of Contents

* [Introduction](#introduction)
* [Demo](#demo)
* [Quick Start](#quick-start)
  * [Server on Windows](#server-on-windows)
  * [Server on Linux](#server-on-linux)
* [Clients](#clients)
  * [Windows](#windows)
  * [Linux](#linux)
* [Releases](#releases)
* [Configuration](#configuration)
* [Technical Specs](#technical-specs)
* [Repository Layout](#repository-layout)
* [Technical Docs](#technical-docs)

<a id="introduction"></a>

## Introduction

Clip-Sync is a lightweight CLI to sync your clipboard across Windows and Linux, on the same LAN or over the Internet. Small payloads go inline via /ws; large payloads use /upload and are shared by an upload_url. Each user gets a hub with fan-out to devices and no echo to the sender. Built in Go with WebSocket signaling and HTTP for large transfers.

<a id="demo"></a>

## Demo

![Demo](demos/clip-sync-demo.gif)

<a id="quick-start"></a>

## Quick Start

**Requirements:**
* Prebuilt binaries (see [Releases](#releases)), or Go 1.22+ to build from source.
* TCP port `8080` reachable from clients.

<a id="server-on-windows"></a>

### Server on Windows

**Option 1: Using a prebuilt binary**

1) Download `server_windows_amd64.exe` from [Releases](https://github.com/MiguelAguiarDEV/osmos/releases), then run it (PowerShell):

```powershell
.\server_windows_amd64.exe --addr :8080
```

2) Open the firewall (admin):

```powershell
netsh advfirewall firewall add rule name="clip-sync" dir=in action=allow protocol=TCP localport=8080
```

**Option 2: Building from source**

```powershell
go -C server run ./cmd/server --addr :8080
```

<a id="server-on-linux"></a>

### Server on Linux

**Option 1: Using a prebuilt binary**

1) Download `server_linux_amd64` from [Releases](https://github.com/MiguelAguiarDEV/osmos/releases), then run it:

```bash
chmod +x ./server_linux_amd64
./server_linux_amd64 --addr 0.0.0.0:8080
```

2) Open the port (UFW or equivalent):

```bash
sudo ufw allow 8080/tcp
```

**Option 2: Building from source**

```bash
go -C server run ./cmd/server --addr 0.0.0.0:8080
```

<a id="clients"></a>

## Clients

Download the CLI for your platform from [Releases](https://github.com/MiguelAguiarDEV/osmos/releases), or build it with `make build` (binary at `bin/cli`).

**Important:** Use the same `--token` for all devices of the same user, and a unique `--device` ID per machine.

> **Note:** Replace `<SERVER_IP>` with your server's IP address (e.g., `192.168.1.100` for LAN or your public IP for Internet).

<a id="windows"></a>

### Windows

**Bidirectional sync** (recommended for most users):

```powershell
.\cli_windows_amd64.exe --mode sync --addr ws://<SERVER_IP>:8080/ws --token u1 --device W1 -v
```

**Receive only** (apply incoming clipboard changes):

```powershell
.\cli_windows_amd64.exe --mode recv --addr ws://<SERVER_IP>:8080/ws --token u1 --device W1 -v
```

**One-shot send** (text or file):

```powershell
# Send text
.\cli_windows_amd64.exe --mode send --text "hello" --addr ws://<SERVER_IP>:8080/ws --token u1 --device W1

# Send file
.\cli_windows_amd64.exe --mode send --file .\photo.png --mime image/png --addr ws://<SERVER_IP>:8080/ws --token u1 --device W1
```

<a id="linux"></a>

### Linux

**Bidirectional sync** (recommended for most users):

```bash
chmod +x ./cli_linux_amd64
./cli_linux_amd64 --mode sync --addr ws://<SERVER_IP>:8080/ws --token u1 --device L1 -v
```

**Receive only** (apply incoming clipboard changes):

```bash
./cli_linux_amd64 --mode recv --addr ws://<SERVER_IP>:8080/ws --token u1 --device L1 -v
```

**One-shot send** (text or file):

```bash
# Send text from stdin
echo "hello" | ./cli_linux_amd64 --mode send --addr ws://<SERVER_IP>:8080/ws --token u1 --device L1

# Send file
./cli_linux_amd64 --mode send --file ./photo.png --mime image/png --addr ws://<SERVER_IP>:8080/ws --token u1 --device L1
```

<a id="releases"></a>

## Releases

Prebuilt binaries for Linux, Windows and macOS (amd64/arm64) are published on the
[Releases](https://github.com/MiguelAguiarDEV/osmos/releases) page, built by the
release workflow when a `vX.Y.Z` tag is pushed.

To build locally instead:

```bash
make build   # server -> bin/server, cli -> bin/cli
```

<a id="configuration"></a>

## Configuration

* `--addr` (`CLIPSYNC_ADDR`): listen address (default `:8080`).
* `--inline-max-bytes` (`CLIPSYNC_INLINE_MAXBYTES`): inline size limit (default 64 KiB).
* `--upload-dir`, `--upload-max-bytes`, `--upload-allowed`: upload directory, max size, allowed MIME whitelist (supports wildcards like `image/*`).
* `--upload-ttl` (`CLIPSYNC_UPLOAD_TTL`): delete uploaded blobs older than this duration, e.g. `24h` (default `0`, disabled).
* `--rate-lps` (`CLIPSYNC_RATE_LPS`): per‑device clip rate limit per second (default `100`; `0` disables).
* `--redis-url` (`CLIPSYNC_REDIS_URL`): Redis URL (e.g. `redis://host:6379/0`) to fan out across multiple server instances. Empty = single instance. See [Scaling](#scaling).
* `--redis-channel` (`CLIPSYNC_REDIS_CHANNEL`): Redis pub/sub channel (default `clipsync`).
* `--log-level` (`CLIPSYNC_LOG_LEVEL`): `debug|info|error|off`.
* Auth: all of `/ws`, `/upload` and `/d/{id}` require the token. Optional HMAC: set `CLIPSYNC_HMAC_SECRET`; token format `user:exp_unix:hex(hmac_sha256(secret, user|exp))`.
* TLS: use a reverse proxy (e.g., Caddy/Nginx) and connect via `wss://.../ws`.

<a id="scaling"></a>

## Scaling

A single instance keeps connections in memory and fans clips out locally — no
extra setup needed. To run **multiple instances** behind a load balancer (so a
user's devices can land on any instance), point them all at the same Redis:

```bash
./server_linux_amd64 --addr 0.0.0.0:8080 --redis-url redis://10.0.0.5:6379/0
```

Each instance publishes clips to a shared Redis channel and delivers received
clips to its own connected devices. Uploaded blobs are written to `--upload-dir`,
so for multi-instance use put that on shared storage (or a shared object store)
reachable by every instance. If Redis is unreachable at startup the instance
logs an error and falls back to single-instance mode.

<a id="technical-specs"></a>

## Technical Specs

* Stack: Go 1.24, WebSocket (`/ws`) + HTTP (`/upload`, `/d/{id}`, `/health`, `/healthz`).
* Architecture: per‑user fan‑out to devices; no echo to sender. Optional Redis pub/sub for multi‑instance fan‑out.
* Scalability: client exponential backoff; dedup by `msg_id` on server and client; horizontal scale‑out via Redis.
* Clipboard backends:
  * Windows: `clip.exe` or PowerShell (`Get-Clipboard` / `Set-Clipboard`).
  * Linux: Wayland `wl-clipboard` or X11 `xclip` / `xsel`.
* Inline limit: 64 KiB. Large payloads via `/upload` (50 MiB default).
* Observability: `/health` liveness, `/healthz` JSON metrics, optional `/debug/pprof/*` and `/debug/vars`.
* Quality: unit + integration tests; GitHub Actions CI.

<a id="repository-layout"></a>

## Repository Layout

```
.
├─ go.work                 # multi-module workspace
├─ server/                 # Go module: server
│  ├─ cmd/server           # entrypoint
│  ├─ internal/
│  │  ├─ app               # HTTP mux (routes: /health, /healthz, /ws, /upload, /d/{id})
│  │  ├─ httpapi           # upload/download
│  │  └─ ws                # WebSocket handler (per-user fan-out)
│  ├─ pkg/types            # envelopes
│  └─ tests                # integration/E2E
└─ clients/cli/            # Go module: CLI
```

<a id="technical-docs"></a>

## Technical Docs

* Protocol v1: `docs/protocol.md`
* Linux/Windows technical notes (dev/recruiters): `docs/tutorial-linux-windows.md`

