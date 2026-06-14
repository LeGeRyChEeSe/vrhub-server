# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-06-14

First public release.

### Added
- Chi-based HTTP router with setup-mode middleware.
- VRHub-compatible public API:
  - `GET /meta.7z` (password-protected AES-256 7z archive)
  - `GET /{hash}/` and `GET /{hash}/{packageName}/` HTML directory listings
  - `GET /{hash}/{packageName}/{filename}` file download with Range support
  - `GET /config.json` client configuration bootstrap
- Admin REST API (X-API-Key, SHA-256 hashed):
  - Games CRUD, scan, revalidate, rescan, corruption status
  - Admin settings, change password, API key reveal / regenerate
  - Backup / restore endpoints
  - Update status, apply, reset (GitHub releases)
  - Monitoring SSE stream, stats, network status, API documentation
- Admin Web UI (HTML + session cookies):
  - Dashboard, configuration widget, games, backup, monitoring
  - Login page with secure cookie hardening for LAN access
- Setup wizard endpoints (credentials, archive password, game folders,
  scan review, launch).
- Background workers:
  - File-system watcher with auto-rescan
  - Corruption detection and orphan OBB handling
  - MetaMetadata fetcher with startup + scheduled refresh (conditional
    `Last-Modified` / `ETag` requests)
  - Auto backup on updates, manual export, restore
  - GitHub releases checker with self-apply
  - Real-time monitoring and usage statistics
  - Network status indicator
- SQLite data layer (`modernc.org/sqlite`, pure Go, schema created in code).
- TOML configuration via `BurntSushi/toml`, with first-run detection that
  triggers the setup wizard when `config.toml` is missing.
- Default data dir: Windows `%APPDATA%/vrhub-server`, Unix `$HOME/.vrhub-server`.
- Default listen: `127.0.0.1:39457`.
- Files served from the scanner-discovered `apk_path`, with a legacy
  `{data-dir}/games/{hash}/{packageName}/` fallback and a startup backfill
  for older installs.
- Cross-platform single static binary for Windows (amd64, arm64),
  Linux (amd64, arm64) and macOS (amd64, arm64).
- Docker image and `docker-compose.yml` under `docker/`.
- `README.md`, `docs/` set, `LICENSE` (MIT), `CONTRIBUTING.md`,
  `SECURITY.md`.

### Notes
- Windows binaries ship with a sidecar `vrhub-server.exe.manifest`
  requesting `requireAdministrator` so the firewall helper
  (`internal/firewall`) can call `netsh advfirewall` without manual
  firewall clicks. The helper is a runtime no-op on Linux and macOS.
- The embedded 7z helper binaries are bundled for every supported target.

[Unreleased]: https://github.com/LeGeRyChEeSe/vrhub-server/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/LeGeRyChEeSe/vrhub-server/releases/tag/v0.1.0
