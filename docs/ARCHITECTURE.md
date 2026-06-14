# Architecture

A condensed overview of the project: goals, data flow, package map,
data layer, background workers, the auth model and naming conventions.
This document is the onboarding summary; the package-level GoDoc and the
source itself are the detailed reference.

## Goals

- **Wire compatibility with the VRHub client.** The public API is a
  strict subset of the upstream VRHub service. No client changes.
- **Self-hostable on a single host.** One static binary, one SQLite
  file, one data directory.
- **Operator-friendly.** A web wizard for first-run setup, a web UI
  for day-to-day administration, a JSON REST API for bots.
- **Cross-platform.** Windows amd64 / arm64, Linux amd64 / arm64,
  macOS amd64 / arm64 (Intel and Apple Silicon). Verified locally
  with a `go build` per `GOOS` x `GOARCH` pair.

## High-level data flow

```
                  +----------------------+
                  |   VRHub client       |
                  |   (Meta Quest)       |
                  +----------+-----------+
                             |
                             | HTTP (no auth)
                             v
+----------------------------------------------------+
|  vrhub-server                                       |
|                                                    |
|  +-------------------+    +--------------------+    |
|  |   public API      |    |   admin REST API   |    |
|  | /                 |    | /admin/api/*       |    |
|  | /config.json      |    | (X-API-Key)        |    |
|  | /meta.7z          |    +---------+----------+    |
|  | /{hash}/*         |              |               |
|  +---------+---------+    +---------v----------+    |
|            |              |  admin Web UI     |    |
|            |              |  /admin/*         |    |
|            |              |  (session cookie) |    |
|            |              +---------+----------+    |
|            v                        v               |
|       +----+------------------------+----+          |
|       |            Chi router               |       |
|       +-----------------+-------------------+       |
|                         |                           |
|        +----------------+------------------+        |
|        v                v                  v        |
|   +---------+      +----------+      +----------+   |
|   | archive |      |   game   |      | metadata |   |
|   | (7z +   |      | scanner  |      | fetcher  |   |
|   | AES-256)|      | watcher  |      | cache    |   |
|   +----+----+      +----+-----+      +----+-----+   |
|        |                |                 |         |
|        +--------+ +-----+-----+  +--------+         |
|                 v v           v v                   |
|             +---------+   +-----------+             |
|             | SQLite  |   | fsnotify  |             |
|             | vrhub.db|   | watcher   |             |
|             +---------+   +-----------+             |
|                                                    |
|   background workers: update checker, monitor SSE,  |
|   scheduled metadata refresh, auto backup           |
+----------------------------------------------------+
                            |
                            v
                  +----------------------+
                  |   GitHub releases    |
                  |   MetaMetadata JSON  |
                  +----------------------+
```

## Two-mode operation

| Mode    | Triggered when                                      | Public API       | Admin API                        |
|---------|-----------------------------------------------------|------------------|----------------------------------|
| setup   | `{data-dir}/config.toml` does not exist            | `503` everywhere | redirects to `/admin/setup`      |
| normal  | `config.toml` exists and parses                    | full surface     | full surface                     |

The mode is held in an `atomic.Value` and reloaded on the
setup-to-normal transition (the wizard's **Launch** step). The
`SetupModeMiddleware` in `internal/api/router.go` is the single
source of truth for the active mode at request time.

## Package map

| Package                                | Responsibility                                            |
|----------------------------------------|-----------------------------------------------------------|
| `cmd/server`                           | Entry point, CLI flags, mode bootstrap, signal handling    |
| `cmd/inspect`                          | Offline config / DB inspector                              |
| `cmd/refresh-metadata`                 | One-shot MetaMetadata refresh                              |
| `cmd/test-config`                      | Smoke test for a `config.toml`                             |
| `internal/api`                         | Chi router, public + admin handlers, middleware            |
| `internal/api/admin_api.go`            | JSON endpoints under `/admin/api/*`                       |
| `internal/api/public.go`               | VRHub-compatible public endpoints                          |
| `internal/api/router.go`               | Route table, mode middleware, mode transitions             |
| `internal/api/setup*.go`               | First-run wizard endpoints                                 |
| `internal/archive`                     | 7z + AES-256 `meta.7z` generator                          |
| `internal/auth`                        | Session cookies, API key, rate limit                       |
| `internal/config`                      | TOML loader / saver, first-run detection                  |
| `internal/db`                          | SQLite layer (modernc.org/sqlite)                          |
| `internal/firewall`                    | `netsh advfirewall` helper on Windows; runtime no-op on Linux and macOS |
| `internal/game`                        | Scanner, watcher, importer, manager                       |
| `internal/log`                         | zerolog initialisation                                     |
| `internal/metadata`                    | MetaMetadata fetcher + cache                              |
| `internal/monitor`                     | Real-time monitoring publisher (SSE feed)                  |
| `internal/network`                     | Network reachability helper                                |
| `internal/update`                      | GitHub releases checker, self-apply, recovery             |
| `internal/ui/embed/`                   | Embedded admin UI assets (`go:embed`)                      |
| `pkg/types`                            | Shared types (`Config`, `ServerMode`, `GameEntry`)         |

## Data layer

- **SQLite** via `modernc.org/sqlite` (pure Go, no CGo).
- The schema is created and migrated in code by `internal/db`.
- Single `games` table + a small set of auxiliary tables
  (`api_keys`, `sessions`, `backups`).
- Snippet from `pkg/types/types.go` (the canonical GameEntry):
  ```go
  type GameEntry struct {
      ID          int64     `json:"id"`
      ReleaseName string    `json:"release_name"`
      GameName    string    `json:"game_name"`
      PackageName string    `json:"package_name"`
      VersionCode int64     `json:"version_code"`
      SizeBytes   int64     `json:"size_bytes"`
      Hash        string    `json:"hash"`
      Corrupted   bool      `json:"corrupted"`
      Exposed     bool      `json:"exposed"`
      ApkPath     string    `json:"apk_path,omitempty"`
      // ...
  }
  ```

## Background workers

| Worker                | Triggered by                       | Output                              |
|-----------------------|------------------------------------|-------------------------------------|
| File watcher          | fsnotify events on game folders   | re-scan + DB write                  |
| Metadata fetcher      | startup + `[metadata].refresh_interval` | DB enrichment, conditional GET |
| Update checker        | startup + `[update].check-interval` | `/admin/api/update/status`       |
| Auto backup           | pre-update + manual schedule       | zip under `{data-dir}/backups/`     |
| Monitor publisher     | every request to `/admin/monitoring` | SSE stream                      |
| Stats collector       | request to `/admin/api/stats`     | aggregated counters                 |
| Network status probe  | request to `/admin/api/network-status` | LAN reachability info           |

All workers are goroutines started in `cmd/server/main.go` after the
mode transitions to `normal`. They respect `context.Context`
cancellation so a clean shutdown drains them.

## Auth model

| Surface             | Mechanism                                                              |
|---------------------|------------------------------------------------------------------------|
| Public API          | none (the VRHub client is anonymous by design)                         |
| Admin web UI        | username + password form, bcrypt-hashed, HTTP-only session cookie      |
| Admin REST API      | `X-API-Key` header, SHA-256 hash stored in `config.toml`               |
| Setup wizard        | only reachable in setup mode; no auth on the wizard routes themselves  |

The session cookie carries the `Secure` flag and is hardened for
LAN access, so plan for HTTPS termination when exposing the admin
UI.

## Naming conventions

| Context      | Convention                              | Example                          |
|--------------|-----------------------------------------|----------------------------------|
| DB tables    | snake_case, plural                      | `games`, `api_keys`              |
| DB columns   | snake_case                              | `release_name`, `version_code`   |
| JSON fields  | snake_case                              | `release_name`, `game_name`      |
| Go exported  | MixedCase                               | `GetGame`, `GameEntry`           |
| Go local     | mixedCase                               | `releaseName`, `versionCode`     |
| API paths    | kebab-case                              | `/admin/api/games`               |
| Path params  | `:paramName` (Chi)                      | `/:releaseName`                  |
| Error wrap   | `fmt.Errorf("context: %w", err)`        | --                               |

## Cross-cutting concerns

- **Logging** &mdash; structured JSON via zerolog; every log line
  carries `data_dir` and `mode` context after `vlog.Init()`.
- **Configuration** &mdash; `config.toml` for everything that
  persists; CLI flags only for `-data-dir` and `-port`.
- **Error handling** &mdash; `internal/api` handlers return a JSON
  envelope with a stable `code` field for programmatic clients.
- **Concurrency** &mdash; `atomic.Value` for the live mode and the
  live config snapshot; channels for cross-goroutine teardown
  signals; `sync.Mutex` only for the few stateful caches that
  really need it.

## Further reading

- [`docs/API.md`](API.md) &mdash; HTTP surface reference.
- [`docs/CONFIGURATION.md`](CONFIGURATION.md) &mdash; runtime
  configuration.
- [`docs/CLIENT_INTEGRATION.md`](CLIENT_INTEGRATION.md) &mdash; how
  the VRHub client talks to this server.
- [`docs/DEVELOPMENT.md`](DEVELOPMENT.md) &mdash; dev build, test,
  lint workflow.
