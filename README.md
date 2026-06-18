<p align="center">
  <img src="docs/assets/logo.svg" width="128" alt="vrhub-server logo">
</p>

<p align="center">
  <strong>vrhub-server</strong> &mdash; self-hosted backend for the VRHub Meta Quest client.
</p>

<p align="center">
  <a href="https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest"><img src="https://img.shields.io/github/v/release/LeGeRyChEeSe/vrhub-server?style=for-the-badge&sort=semver" alt="Latest release"></a>
  <a href="https://github.com/LeGeRyChEeSe/vrhub-server/blob/main/LICENSE"><img src="https://img.shields.io/github/license/LeGeRyChEeSe/vrhub-server?style=for-the-badge" alt="License: MIT"></a>
  <a href="https://github.com/LeGeRyChEeSe/vrhub-server/actions"><img src="https://img.shields.io/github/actions/workflow/status/LeGeRyChEeSe/vrhub-server/ci.yml?style=for-the-badge" alt="CI status"></a>
  <a href="https://github.com/LeGeRyChEeSe/VRHub"><img src="https://img.shields.io/badge/client-VRHub-2ea44f?style=for-the-badge" alt="Client: VRHub"></a>
</p>

---

## Table of Contents

- [Overview](#overview)
- [Download](#download)
- [Relationship to the VRHub Client](#relationship-to-the-vrhub-client)
- [Key Features](#key-features)
- [Quick Start](#quick-start)
- [Build &amp; Run](#build--run)
- [Project Layout](#project-layout)
- [Data Directory](#data-directory)
- [Configuration](#configuration)
- [API Summary](#api-summary)
- [Development](#development)
- [Documentation](#documentation)
- [Contributing](#contributing)
- [License](#license)
- [Acknowledgements](#acknowledgements)

---

## Overview

`vrhub-server` is the self-hosted backend that pairs with the
[VRHub](https://github.com/LeGeRyChEeSe/VRHub) Meta Quest client. It scans
local game directories, enriches metadata from
[MetaMetadata](https://github.com/nicnacnic/MetaMetadata), generates
password-protected `meta.7z` archives and serves the files over a
VRHub-compatible HTTP API. An embedded admin web UI handles first-run
setup, game review, monitoring and configuration.

The project is written in **Go 1.26** and ships as a single static
binary. The server has no external database dependency: everything lives
in a local SQLite file inside the data directory.

## Download

Latest release:
[![](https://img.shields.io/github/v/release/LeGeRyChEeSe/vrhub-server?include_prereleases&sort=semver&style=for-the-badge&label=Latest)](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest)

Pre-built static binaries are published for every supported
`GOOS` x `GOARCH` combination as a per-platform `.zip` archive. Pick
the row that matches your machine, download, extract, and follow
[`docs/INSTALL.md`](docs/INSTALL.md) for the first-run wizard.

### Windows

| Architecture  | Download                                                                                                                                                            |
|---------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| amd64         | [vrhub-server-windows-amd64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-windows-amd64.zip)                               |
| arm64         | [vrhub-server-windows-arm64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-windows-arm64.zip)                               |

### macOS

| Architecture  | Download                                                                                                                                                            |
|---------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Apple Silicon (M1 / M2 / M3 / M4) | [vrhub-server-darwin-arm64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-darwin-arm64.zip)                            |
| Intel         | [vrhub-server-darwin-amd64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-darwin-amd64.zip)                                         |

### Linux

| Architecture  | Download                                                                                                                                                            |
|---------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| amd64         | [vrhub-server-linux-amd64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-linux-amd64.zip)                                           |
| arm64         | [vrhub-server-linux-arm64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-linux-arm64.zip)                                           |

Each release ships a `checksums.txt` with SHA-256 sums for every
archive; verify your download with `sha256sum -c checksums.txt`
(or `Get-FileHash` on Windows PowerShell) before launching. Need an
older build? Browse
[all releases](https://github.com/LeGeRyChEeSe/vrhub-server/releases).

## Relationship to the VRHub Client

| Concern              | vrhub-server (this repo)                | VRHub client                         |
|----------------------|------------------------------------------|--------------------------------------|
| Role                 | Self-hosted backend                      | Meta Quest sideloader                |
| Language / runtime   | Go 1.26, single static binary            | Kotlin, Jetpack Compose              |
| Public API           | `meta.7z`, file serving, `config.json`   | Reads the public API                 |
| Auth                 | Admin session + API key                  | Consumes the public API anonymously  |
| Distribution         | GitHub releases, Docker                  | SideQuest, browser install, `adb`    |
| Repository           | `github.com/LeGeRyChEeSe/vrhub-server`   | `github.com/LeGeRyChESe/VRHub`       |

> The public API is **wire-compatible** with the VRHub client: end users
> can swap the client-side `baseUri` + `password` to point at a
> `vrhub-server` instance and the experience is identical. See
> [`docs/CLIENT_INTEGRATION.md`](docs/CLIENT_INTEGRATION.md) for the
> step-by-step.

## Key Features

- **Two-mode operation.** Normal mode exposes the full public + admin
  surface; setup mode (no `config.toml`) only serves the first-run wizard
  and returns `503` on every other public route.
- **VRHub-compatible public API** out of the box: `meta.7z`, HTML
  directory listings, Range-aware file downloads, `config.json`
  bootstrap endpoint.
- **AES-256 encrypted `meta.7z`.** The archive is regenerated on demand
  with the operator's archive password.
- **MetaMetadata enrichment.** Background worker fetches
  [MetaMetadata](https://github.com/nicnacnic/MetaMetadata) at startup
  and on a configurable schedule using `Last-Modified` / `ETag`
  conditional requests.
- **File-system watcher.** New APK / OBB files added to a scanned
  folder are detected automatically; corrupt files are flagged for
  operator review.
- **Admin web UI.** Dashboard, configuration widget, games review,
  backup / restore, real-time monitoring via SSE, embedded API
  documentation.
- **Self-update.** On startup the server polls GitHub releases; the
  operator can review and apply updates from the admin UI. Available
  updates surface on the Michel dashboard and on a Power `#/updates`
  page; release notes are rendered as Markdown from a 5-minute
  in-memory cache. After staging, an explicit
  `POST /admin/api/update/restart` triggers a graceful port release
  before the new binary binds, avoiding `EADDRINUSE` on Windows.
- **Single static binary.** No external services, no Docker required.
  SQLite is compiled in (`modernc.org/sqlite`).
- **Cross-platform builds.** Windows (amd64, arm64), Linux (amd64, arm64),
  macOS (amd64, arm64); binaries are published on every GitHub release.

## Quick Start

```bash
# 1. Clone
git clone https://github.com/LeGeRyChEeSe/vrhub-server.git
cd vrhub-server

# 2. Build
go build -o bin/vrhub-server ./cmd/server/

# 3. Launch — first run enters the setup wizard
./bin/vrhub-server

# 4. Open the admin UI at http://127.0.0.1:39457/admin/setup
#    Follow the wizard to:
#      - set the admin username and password
#      - set the archive password (used to encrypt meta.7z)
#      - pick the game folders to scan
#      - review the detected APK / OBB pairs
#      - launch the server in normal mode

# 5. Point the VRHub client at the server
#    baseUri : http://<your-host>:39457/
#    password: <the archive password you set during setup>
```

A Windows-friendly alternative that also requests the UAC elevation the
firewall helper needs:

```cmd
build.cmd
run-with-logs.bat
```

See [`docs/INSTALL.md`](docs/INSTALL.md) for the full walkthrough
(Windows, Linux, macOS, Android via Termux, Docker, and a manual
recovery path).

## Build &amp; Run

### Prerequisites

- **Go 1.26.2** (or newer in the 1.26.x series)
- A POSIX shell or PowerShell for the helper scripts
- 50 MB of free disk space for the build output

### Standard build

```bash
go vet ./...
go build -o bin/vrhub-server ./cmd/server/
./bin/vrhub-server -data-dir "$HOME/.vrhub-server" -port 39457
```

CLI flags:

| Flag        | Default (Windows / Unix)              | Description                                  |
|-------------|----------------------------------------|----------------------------------------------|
| `-data-dir` | `%APPDATA%/vrhub-server` / `~/.vrhub-server` | Override the data directory.                |
| `-port`     | `39457`                                | Override the listen port (otherwise from `config.toml`). |

The default listen address is `127.0.0.1`; change it via
`[server].host` in `config.toml` to expose the server on your LAN.

### Windows: sidecar manifest

`build.cmd` is a thin wrapper that compiles the binary **and** copies
the sidecar `cmd/server/vrhub-server.exe.manifest` into `bin/`. The
manifest requests `requireAdministrator` at launch, which lets the
embedded firewall helper invoke `netsh advfirewall firewall add rule`
and open the TCP port without manual clicks.

### Cross-compilation examples

The same six combinations published in the [Download](#download)
section can be reproduced locally with plain `go build` (no CGo, no
cross toolchain required):

```bash
GOOS=windows GOARCH=amd64 go build -o bin/vrhub-server-windows-amd64.exe ./cmd/server/
GOOS=windows GOARCH=arm64 go build -o bin/vrhub-server-windows-arm64.exe ./cmd/server/
GOOS=linux   GOARCH=amd64 go build -o bin/vrhub-server-linux-amd64     ./cmd/server/
GOOS=linux   GOARCH=arm64 go build -o bin/vrhub-server-linux-arm64     ./cmd/server/
GOOS=darwin  GOARCH=amd64 go build -o bin/vrhub-server-darwin-amd64    ./cmd/server/
GOOS=darwin  GOARCH=arm64 go build -o bin/vrhub-server-darwin-arm64    ./cmd/server/
```

## Project Layout

```
.
├── cmd/
│   ├── server/              Main binary (CLI flags, lifecycle, setup mode)
│   ├── inspect/             Offline config / DB inspector
│   ├── refresh-metadata/    One-shot MetaMetadata refresh
│   └── test-config/         Smoke test for config.toml
├── internal/
│   ├── api/                 Chi router, public + admin handlers
│   ├── archive/             7z + AES-256 meta.7z generator
│   ├── auth/                Session cookies, API key, rate limit
│   ├── config/              TOML loader/saver, first-run detection
│   ├── db/                  SQLite layer (modernc.org/sqlite)
│   ├── firewall/            netsh advfirewall helper (Windows)
│   ├── game/                Scanner, watcher, importer
│   ├── log/                 zerolog initialisation
│   ├── metadata/            MetaMetadata fetcher + cache
│   ├── monitor/             Real-time monitoring publisher
│   ├── network/             Network status helper
│   ├── update/              GitHub releases checker + self-apply
│   └── ui/                  Embedded admin UI assets
├── pkg/types/               Shared types (Config, ServerMode, GameEntry)
├── docker/                  Dockerfile + docker-compose.yml
├── docs/                    Project documentation (see Documentation)
└── build.cmd                Windows build wrapper
```

`internal/` packages are private to this binary; `pkg/types/` is the
only importable surface for external tools.

## Data Directory

The server creates the data directory on first run. Layout:

```
{v data-dir}/
├── config.toml              # TOML configuration
├── vrhub.db                 # SQLite database
├── games/                   # Legacy storage layout (older installs, kept for backwards compatibility)
│   └── {hash}/
│       └── {packageName}/
│           ├── *.apk
│           └── *.obb
├── metadata/                # MetaMetadata cache
└── backups/                 # Auto + manual backups
```

The server stores the on-disk APK path in an `apk_path` column so
files can be served directly from their original location, removing
the need to copy large binaries into the data directory. Older
installs are still served from the legacy `games/{hash}/{packageName}/`
layout and are backfilled by the startup scan.

## Configuration

All configuration lives in `{data-dir}/config.toml`. See
[`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) for the full key
reference; the most important keys are:

| Key                          | Description                                       |
|------------------------------|---------------------------------------------------|
| `[server].host` / `.port`    | Listen address (default `127.0.0.1:39457`)        |
| `[server].mode`              | `normal` or `setup` (forces setup on first run)  |
| `game_folders`               | Absolute paths scanned for APK / OBB files       |
| `[database].path`            | SQLite file path                                  |
| `[metadata].url`             | MetaMetadata dataset URL                         |
| `[metadata].refresh_interval`| Background refresh cadence (Go duration string)  |
| `[update].enabled`           | Toggle the GitHub releases checker                |
| `[update].check-interval`    | Polling cadence (Go duration string)             |
| `[update].auto-apply`        | Apply updates automatically (default false)      |
| `[admin].username`           | Admin web UI login                                |
| `[admin].password_hash`      | bcrypt hash of the admin password                 |
| `[admin].archive_password`   | Cleartext password for `meta.7z` (AES-256)        |
| `[admin].api_key_hash`       | SHA-256 hash of the admin API key                 |

## API Summary

### Public API (no auth)

| Method | Path                                        | Purpose                                  |
|--------|---------------------------------------------|------------------------------------------|
| GET    | `/`                                         | Server banner + version                  |
| GET    | `/config.json`                              | Client configuration bootstrap            |
| GET    | `/meta.7z`                                  | Password-protected game list (AES-256)   |
| GET    | `/{hash}/`                                  | HTML directory listing                   |
| GET    | `/{hash}/{packageName}/`                    | HTML directory listing                   |
| GET    | `/{hash}/{packageName}/{filename}`          | File download with Range support         |

### Admin Web UI (`/admin/*`, session cookie)

| Path                       | Purpose                                       |
|----------------------------|-----------------------------------------------|
| `/admin/setup`             | First-run wizard (only in setup mode)        |
| `/admin`                   | Dashboard                                     |
| `/admin/configuration`     | Settings widget                               |
| `/admin/games`             | Game library review                           |
| `/admin/backup`            | Backup / restore UI                           |
| `/admin/monitoring`        | Real-time monitoring (SSE)                    |
| `/admin/api-docs`          | Embedded OpenAPI-style documentation          |
| `/admin/api/stats`         | Usage statistics                               |
| `/admin/api/network-status`| LAN reachability probe                        |

### Admin REST API (`/admin/api/*`, `X-API-Key` header)

`POST /admin/api/auth/login`, `POST /admin/api/auth/logout`,
`GET/POST /admin/api/games`, `POST /admin/api/games/rescan`,
`GET /admin/api/games/{releaseName}/corruption-status`,
`POST /admin/api/games/{releaseName}/revalidate`,
`GET /admin/api/admin/settings`,
`POST /admin/api/admin/change-password`,
`GET /admin/api/admin/api-key`,
`POST /admin/api/admin/api-key/regenerate`,
`GET /admin/api/update/status`, `POST /admin/api/update/apply`,
`POST /admin/api/update/restart`, `POST /admin/api/update/reset`,
`GET /admin/api/update/changelog`, plus the
`/admin/api/scripts/*` compatibility surface for the
[VRHub](https://github.com/LeGeRyChEeSe/VRHub) bot.

See [`docs/API.md`](docs/API.md) for the full reference including
request and response shapes, error model and the scripts-API
compatibility table.

## Development

```bash
# Run the focused test suite
go test ./internal/api/ ./internal/config/

# Run the full test suite
go test ./...

# Static analysis
go vet ./...

# Build the Windows binary with the sidecar manifest
build.cmd
```

See [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) for the full
development workflow.

## Documentation

- [`docs/INSTALL.md`](docs/INSTALL.md) &mdash; full install guide
  (Windows, Linux, Docker).
- [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) &mdash; every
  `config.toml` key, sourced from `pkg/types/types.go`.
- [`docs/API.md`](docs/API.md) &mdash; public + admin REST API
  reference.
- [`docs/CLIENT_INTEGRATION.md`](docs/CLIENT_INTEGRATION.md) &mdash;
  connecting the [VRHub](https://github.com/LeGeRyChEeSe/VRHub) client
  to this server, including the `baseUri` + `password` JSON shape.
- [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) &mdash; dev build, test,
  lint workflow.
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) &mdash; high-level
  architecture, package map, data flow, naming conventions.

## Contributing

We welcome bug reports, feature requests and pull requests. Please read
[`CONTRIBUTING.md`](CONTRIBUTING.md) for the coding conventions, commit
naming rules (Conventional Commits) and PR flow.

## License

This project is licensed under the **MIT License**. See
[`LICENSE`](LICENSE) for the full text.

## Acknowledgements

- [VRHub](https://github.com/LeGeRyChEeSe/VRHub) &mdash; the Meta Quest
  client that this server is designed to serve.
- [MetaMetadata](https://github.com/nicnacnic/MetaMetadata) &mdash; the
  community dataset that powers game metadata enrichment.
- [go-chi/chi](https://github.com/go-chi/chi) &mdash; the HTTP router
  used throughout the API surface.
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) &mdash;
  the pure-Go SQLite driver that keeps the binary self-contained.
- [BurntSushi/toml](https://github.com/BurntSushi/toml) &mdash; the
  TOML parser used for `config.toml`.
- [rs/zerolog](https://github.com/rs/zerolog) &mdash; the structured
  logger that powers the JSON console output.

---

> vrhub-server is a personal library manager. The server does not
> provide, host or distribute any game content. You are solely
> responsible for the games you load and must only manage titles you
> have legitimately purchased.
