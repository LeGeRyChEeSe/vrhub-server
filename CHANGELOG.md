# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.5] - 2026-06-27

### Added

- **Trailer resolution cascade (Story 11.1):** A new `[trailer]` config section (`language`,
  optional `youtube_api_key`) drives a three-step resolver: operator override sidecar
  (`{releaseName}.trailer` or `trailer.url`) → OculusDB best-effort → YouTube Data API /
  search fallback. `ResolveMissing` batch-resolves all games with an empty `trailer_url`
  at startup and on demand.
- **Hybrid trailer delivery (Story 11.3):** Every named game now exposes a trailer link via
  `meta.7z` and the directory listing. When a specific video URL is resolved it is served
  directly; otherwise a YouTube search link (`youtube.com/results?search_query=…`) is
  generated from the configured language. The trailer language is folded into the `meta.7z`
  ETag so a language change automatically invalidates client caches.
- **Admin Trailers settings (Story 11.3):** Power-mode admin settings now include a Trailers
  section: a trailer-language dropdown, a write-only YouTube Data API key field (value never
  returned by the server), and a "Resolve trailers now" button (`POST /admin/api/trailers/resolve`)
  that triggers on-demand resolution without a server restart.
- **Multi-folder file watcher (Story 3.5):** `game_folders` now accepts multiple directories.
  The watcher visits every configured folder, skipping missing or inaccessible ones with a
  warning. A new `WatcherManager` serialises Start/Restart/Stop behind a mutex so a
  settings-driven folder change restarts the watcher safely without a server restart.

### Fixed

- **`meta.7z` `Last-Modified` anchor:** The `Last-Modified` response header is now derived
  from `MAX(last_updated)` across all games in the database, not just the subset currently
  exposed. Previously, hiding the most-recently-updated game left `Last-Modified` unchanged,
  causing VRHub clients to short-circuit on the matching header and skip the download.
- **Trailer override on rescan:** The operator override sidecar (`{releaseName}.trailer`) is
  now read and applied when an already-imported APK is rescanned, not only on first import.
  Previously, dropping a sidecar next to an existing APK had no effect until a server restart.

## [0.1.4] - 2026-06-23

### Added

- Games are now automatically enriched with a description and thumbnail fetched from OculusDB. Both are served via new endpoints (`/{hash}/notes.txt`, `/{hash}/thumbnail.jpg`) for VRHub client discovery.

### Fixed

- Metadata image downloads are now scoped to imported games only, instead of processing the entire OculusDB catalog (~3,000 entries) on every startup.
- Pre-existing games (imported before this update) now have their icons backfilled automatically on first startup.
- Fixed manual client setup (baseUri + password fields) being broken due to the archive password being displayed in plaintext in the UI while `/config.json` returns it Base64-encoded. The UI now shows the Base64 value everywhere it is client-facing. The settings form still uses plaintext.

## [0.1.3] - 2026-06-16

### Added
- **In-app update notifications and changelog.** The admin UI now polls
  GitHub releases in the background and surfaces available updates on
  the Michel dashboard and on a new Power `#/updates` page. Release
  notes are rendered as Markdown (headings, bold, italic, code, lists,
  links) and served from a new `GET /admin/api/update/changelog` endpoint
  with a 5-minute in-memory TTL to avoid burning the GitHub rate limit.
- **Manual restart endpoint.** `POST /admin/api/update/restart` accepts an
  operator-initiated restart after a staged update, and the update state
  machine now exposes a new `restart-pending` state.
- **MetaMetadata CDN image pipeline.** Icons and thumbnails are now
  downloaded from the MetaMetadata CDN once per release and re-used by
  the 7z archive generator (keyed by `releaseName`), removing per-request
  network calls during `meta.7z` generation.
- **Mode switch keeps Michel users on the dashboard.** Toggling mode
  from Power into Michel now always lands on the Michel dashboard
  regardless of the previous route. The locked design is now reflected
  in the UI: Michel is a single-page experience.
- **Asset cache-busting on embedded static files.** `admin.css`,
  `admin.js`, `setup.css` and `setup.js` are now served with a `?v=<semver>`
  query string derived from `update.CurrentVersion`, forcing browsers to
  fetch the new build after a server restart even when `Cache-Control:
  no-store` is bypassed by an in-memory cache.
- **Michel "Connecter un client" card.** Populated from `fetchConfig()`,
  removing the previous duplicate `GET /config.json` call.

### Fixed
- **`nil` pointer panic on port rebind.** `liveRebinder.server` and
  `liveRebinder.router` were never assigned after `newLiveRebinder()`,
  so saving a new port in admin settings crashed the server while
  persisting the new value to disk. The fields are now wired in `main()`
  before the listener goroutine starts, and `Rebind()` now performs a
  graceful `Shutdown(ctx)` (10 s timeout) of the old server in a
  goroutine to avoid deadlocking the in-flight handler that triggered
  the rebind.
- **TCP listener not released on Windows update+restart.** The parent
  process held the TCP listener during the 2-second child liveness check,
  causing `EADDRINUSE` and killing both processes on fast machines. The
  restart handler now calls `Shutdown` (100 ms grace) before spawning
  the child, so the port is free when the child binds. Tested live for
  a 0.1.0 → 0.1.2 update+restart.
- **`/config.json` served stale host/port after admin settings save.**
  The public handler held a stale `Config` pointer after a settings
  change. `UpdateConfig` now invokes an `OnConfigUpdated` callback that
  refreshes the pointer used by `HandleClientConfigGET`. The resolved
  listen port is also synced into `cfg.Server.Port` immediately after
  the `-port` flag override.
- **Games silently removed from the DB on metadata parse failure.**
  Two related fixes: (a) the APK metadata extractor now always retries
  with `extractViaManifestOnly` when the primary `apk.OpenFile` call
  fails, so valid APKs are no longer skipped on every scan; (b) the
  file-system watcher now records every seen APK path in a `seenPaths`
  map and refuses to delete a DB entry whose `apk_path` is still
  present on disk.
- **Multi-section visible during mode toggle.** `body.mode-michel
  #section-dashboard` (specificity 1-1-1) was overriding the base
  `[data-route] { display: none }` rule, leaving the previous section
  visible when switching to Michel. The defensive CSS rule was removed
  (the HTML template already sets the correct `data-route`), then
  re-added in a more targeted form (`body.mode-michel:not([data-route])
  #section-dashboard`) to keep Michel defensive styling without the
  bleed. A short debug `setTimeout`/`console.log` block in the
  `modechange` handler was removed.
- **Michel mode lands on dashboard on toggle.** The `modechange` handler
  used to send Michel users to the `power-required` page whenever they
  toggled mode from a Power-only route, and was leaking shared-mode
  pages (configuration, client-setup, updates) into the Michel UX.
  Both behaviours are now aligned with the locked design: Michel is a
  single-page experience and any switch into Michel lands on the
  dashboard.
- **Frontend fetches hanging on a stalled server.** `AbortController`
  with a 10 s timeout was added to `fetchServerStatus`, `populateHeader`,
  `handleRouteClientSetup`, `fetchPowerConfig`, `handleRouteConfiguration`,
  `fetchConfig`, the `loadMichelClientSetupCard` safety-net and
  `fetchChangelog`, so a hung request no longer freezes the UI.
- **Empty-state on the Power updates page.** The "Aucune mise à jour
  disponible." message is now toggled on/off inside `renderUpdateCard()`
  based on `data.versionAvailable`, instead of being always-visible
  HTML.
- **i18n coverage on the updates flow.** Hardcoded English strings on
  the update banner, modal and restart page are now bound through
  `data-i18n`. Nine new FR keys were added to `I18N_MICHEL` and nine
  matching EN keys to `I18N_POWER`.
- **MetaMetadata URL.** The default dataset URL now points to
  `threethan/MetaMetadata` (the canonical community fork).
- **Card tilt proportionality and rescan button freeze.** The 3D tilt
  angle is now scaled inversely to element size, and tilt handlers
  inside tiltable cards are no longer attached to inner buttons, so
  the rescan button no longer freezes on first hover.
- **Changelog scroll cap on the dashboard.** Changelog content divs
  are now capped at 280 px with `overflow-y: auto` so very long
  release notes no longer push the rest of the page.
- **Update checker config not overridden by stale `Enabled=false`.**
  The router now applies per-field overrides (`CheckInterval`,
  `GithubToken`, `Owner`, `Repo`, `AutoApply`, `AutoRestart`) only when
  the config has a non-zero value, so `DefaultConfig()` fallbacks are
  preserved. The periodic check is always active.
- **Copy buttons failing on plain-HTTP admin pages.** The copy
  buttons now use a `copyToClipboard()` helper that prefers the async
  Clipboard API and falls back to `document.execCommand('copy')` for
  non-secure-context admin pages. Copy button labels now use the
  `copy_btn` i18n key instead of the `header_copied` confirmation text.
- **`Rescan` did not persist `DataDir` to `game_folders` when using the
  fallback path.** Rescan now writes the current `DataDir` back to
  `game_folders` so the next scan keeps the working set.
- **`game_folders` not exposed in `/admin/api/admin/settings`.** The
  effective configuration endpoint now reports the scanned folders.
- **`responseRecorder` did not flush, blocking the SSE monitor
  pipeline.** A `Flush()` method was added to the recorder and
  `MonitorMiddleware` is now wired on the router.

## [0.1.2] - 2026-06-15

### Fixed
- **"Scan failed: Unexpected token '<'" on session expiry:** All `fetch()` calls in the
  admin UI now send `Accept: application/json`. Previously, when a session expired the
  auth middleware returned an HTML 302 redirect (no `Accept` header → not a JSON client),
  `fetch()` followed it silently, received an HTML login page with HTTP 200, and the
  caller's `.json()` parse threw "Unexpected token '<'…". Sending the header ensures the
  middleware returns 401 JSON instead, and the UI displays a proper error message.

## [0.1.1] - 2026-06-15

### Fixed
- **Game URL hash regression:** The file-server hash was changed to SHA-256(filepath) in
  story 9.10, breaking VRHub URL compatibility. The VRHub client always derives the URL
  hash as MD5(releaseName + "\n"); the importer now uses the same algorithm so download
  URLs resolve correctly after re-import.
- **OBB not downloaded for games with subdirectory layout:** When the OBB file lives in a
  subdirectory of the release folder (e.g. `com.Package/main.N.com.Package.obb`) the
  file listing at `/{hash}/{packageName}/` omitted it, causing the client to download
  only the APK. The listing now injects the OBB basename from `game.OBBPath` when it
  resides outside the APK directory. The download handler already used `OBBPath` directly
  so no change was needed there.

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

[Unreleased]: https://github.com/LeGeRyChEeSe/vrhub-server/compare/v0.1.4...HEAD
[0.1.4]: https://github.com/LeGeRyChEeSe/vrhub-server/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/LeGeRyChEeSe/vrhub-server/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/LeGeRyChEeSe/vrhub-server/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/LeGeRyChEeSe/vrhub-server/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/LeGeRyChEeSe/vrhub-server/releases/tag/v0.1.0
