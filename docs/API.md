# API Reference

The server exposes three distinct HTTP surfaces:

1. **Public API** &mdash; no auth, VRHub-compatible.
2. **Admin web UI** &mdash; HTML shell + JSON endpoints, session
   cookie.
3. **Admin REST API** &mdash; programmatic access for scripts and
   bots, `X-API-Key` header.

Setup mode (`{data-dir}/config.toml` missing) returns `503` on every
public route and redirects every `/admin/*` route to
`/admin/setup`.

## Conventions

- All routes are kebab-case (`/admin/api/games`, `/meta.7z`).
- Path parameters use Chi syntax: `/{releaseName}`.
- JSON field names are snake_case (`release_name`, `version_code`).
- All responses are JSON unless explicitly noted (HTML for directory
  listings, binary for `meta.7z` and game files).
- Timestamps are RFC 3339 UTC.
- Sizes are bytes.
- Errors return a JSON envelope:
  ```json
  { "error": "human readable message", "code": "machine_readable_code" }
  ```
  HTTP status is the primary signal; the `code` field is for
  programmatic handling.

## 1. Public API

The public API is the surface the
[VRHub client](https://github.com/LeGeRyChEeSe/VRHub) talks to. It is
**wire-compatible** with the original VRHub service: any client that
worked against the upstream server works against this one without
modification.

### `GET /`

Returns a small JSON banner. Useful for `curl` smoke tests and
uptime monitors.

```http
GET / HTTP/1.1
```

```json
{
  "name": "vrhub-server",
  "version": "0.1.3",
  "mode": "normal"
}
```

### `GET /config.json`

Bootstrap endpoint. Returns the client configuration that the
VRHub client needs to talk to this server: the `baseUri` and the
cleartext `archive_password`. The file is served on every request so
the operator can rotate the password by editing `config.toml` and
restarting the server.

```json
{
  "baseUri": "http://192.0.2.10:39457/",
  "password": "your-cleartext-archive-password"
}
```

### `GET /meta.7z`

Returns the password-protected 7z archive that lists every game
the server is currently exposing. The archive is regenerated on
demand and includes `Last-Modified` / `ETag` headers so the client
can short-circuit downloads when the archive is unchanged.

- `If-None-Match` and `If-Modified-Since` are honoured.
- The archive uses LZMA2 compression and AES-256 encryption, with
  the password from `[admin].archive_password`.
- Format and field names match the original VRHub service.

### `GET /{hash}/`

HTML directory listing of the package subdirectories the server
exposes for the given `{hash}`. The page is a self-contained HTML
document with a single table; the VRHub client does not parse it,
it is here for human operators and the legacy VRHub "browse" UI.

### `GET /{hash}/{packageName}/`

HTML directory listing of the APK and OBB files for the given
`{packageName}`.

### `GET /{hash}/{packageName}/{filename}`

Binary download. Supports the `Range` header (single range, bytes),
conditional requests via `If-Range` and `If-None-Match`, and the
`Accept-Encoding: identity` hint to avoid re-compression by reverse
proxies.

The `Content-Length` matches the file size; `Content-Type` is sniffed
from the extension and falls back to `application/octet-stream`.

## 2. Admin web UI

The admin UI is a single-page app served from
`internal/ui/embed/admin.html`. The router mounts the HTML shell at
`/admin` and the JSON endpoints under `/admin/api/*`.

### Routes (UI shell)

| Path                       | Purpose                                          |
|----------------------------|--------------------------------------------------|
| `GET /admin/setup`         | First-run wizard (setup mode only)               |
| `GET /admin`               | Dashboard                                        |
| `GET /admin/configuration` | Settings widget                                  |
| `GET /admin/games`         | Game library review                              |
| `GET /admin/backup`        | Backup / restore UI                              |
| `GET /admin/monitoring`    | Real-time monitoring (SSE)                       |
| `GET /admin/api-docs`      | Embedded OpenAPI-style documentation             |
| `GET /admin/stats`         | Usage statistics page                            |

### Routes (JSON)

| Method | Path                                              | Purpose                                  |
|--------|---------------------------------------------------|------------------------------------------|
| POST   | `/admin/api/auth/login`                           | Form login (sets the session cookie)     |
| POST   | `/admin/api/auth/logout`                          | Invalidate the session                   |
| GET    | `/admin/api/stats`                                | Usage statistics                         |
| GET    | `/admin/api/network-status`                       | LAN reachability probe                   |
| GET    | `/admin/api/docs`                                 | API documentation payload                |
| GET    | `/admin/api/monitoring`                           | Server-Sent Events stream                |

### Setup wizard endpoints

The wizard exposes a self-contained sub-API under
`/admin/api/setup/*`:

| Method | Path                              | Purpose                              |
|--------|-----------------------------------|--------------------------------------|
| GET    | `/admin/api/setup/state`          | Current wizard step + progress       |
| POST   | `/admin/api/setup/credentials`    | Set admin username + password        |
| POST   | `/admin/api/setup/archive-password` | Set the cleartext archive password |
| POST   | `/admin/api/setup/scan`           | Run a scan against a folder          |
| GET    | `/admin/api/setup/review`         | List the candidates from the scan    |
| POST   | `/admin/api/setup/review`         | Apply the review (include / exclude) |
| POST   | `/admin/api/setup/launch`         | Persist `config.toml` and go live    |

## 3. Admin REST API

Authentication: the `X-API-Key` header. The key is generated by the
setup wizard, returned **once** in plaintext, and stored as a SHA-256
hash in `config.toml`. Regenerate from
`POST /admin/api/admin/api-key/regenerate` (session-authenticated).

All endpoints accept and return JSON. Pagination uses
`?limit=` and `?offset=` (defaults `100` and `0`). Use the
`Link` response header for `next` / `prev` navigation.

### Games

| Method | Path                                                  | Purpose                                  |
|--------|-------------------------------------------------------|------------------------------------------|
| GET    | `/admin/api/games`                                    | List games (paginated)                   |
| POST   | `/admin/api/games`                                    | Create a new game entry                  |
| GET    | `/admin/api/games/{releaseName}`                      | Fetch one game                           |
| PUT    | `/admin/api/games/{releaseName}`                      | Update one game                          |
| DELETE | `/admin/api/games/{releaseName}`                      | Remove one game                          |
| POST   | `/admin/api/games/rescan`                             | Force a re-scan of every game folder     |
| GET    | `/admin/api/games/{releaseName}/corruption-status`    | Corruption diagnostics                   |
| POST   | `/admin/api/games/{releaseName}/revalidate`           | Re-validate a single game                |

### Admin

| Method | Path                                            | Purpose                            |
|--------|-------------------------------------------------|------------------------------------|
| GET    | `/admin/api/admin/settings`                     | Effective configuration            |
| POST   | `/admin/api/admin/change-password`              | Rotate the admin password          |
| GET    | `/admin/api/admin/api-key`                      | Reveal the current API key         |
| POST   | `/admin/api/admin/api-key/regenerate`           | Rotate the API key                 |

### Updates

| Method | Path                                  | Purpose                                                          |
|--------|---------------------------------------|------------------------------------------------------------------|
| GET    | `/admin/api/update/status`            | Current version, latest release, `autoApply`, `autoRestart`, `updateState`, `restartPending` |
| POST   | `/admin/api/update/apply`             | Download and apply the latest release                            |
| POST   | `/admin/api/update/restart`           | Restart into a previously staged binary (releases the TCP listener first) |
| POST   | `/admin/api/update/reset`             | Clear the staging directory                                      |
| GET    | `/admin/api/update/changelog`         | Markdown-rendered release notes (5-minute in-memory TTL)         |

The status response includes a small state machine
(`idle` / `running` / `failed` / `restart-pending`) the UI uses to
drive the update banner and the manual restart button. The changelog
endpoint is cached for 5 minutes to avoid burning the GitHub
unauthenticated rate limit during development.

### Backup &amp; restore

| Method | Path                                | Purpose                                  |
|--------|-------------------------------------|------------------------------------------|
| GET    | `/admin/api/backup`                 | List available backups                   |
| POST   | `/admin/api/backup`                 | Create a new backup                      |
| POST   | `/admin/api/restore`                | Restore from an uploaded backup          |

## 4. Scripts API (compatibility surface)

`/admin/api/scripts/*` is a thin compatibility shim for the
[VRHub](https://github.com/LeGeRyChEeSe/VRHub) automation bot. It
mirrors the field names and the route shapes of the upstream VRHub
service.

| Method | Path                            | Upstream equivalent                   |
|--------|---------------------------------|----------------------------------------|
| GET    | `/admin/api/scripts/_ping`      | Health probe                           |
| GET    | `/admin/api/scripts/status`     | `GET /update/status`                   |
| POST   | `/admin/api/scripts/update/apply` | `POST /update/apply`                 |
| GET    | `/admin/api/scripts/games`      | `GET /games`                           |
| POST   | `/admin/api/scripts/apps`       | `POST /games/rescan`                   |
| GET    | `/admin/api/scripts/config`     | `GET /admin/settings`                  |
| POST   | `/admin/api/scripts/backup`     | `POST /backup`                         |
| POST   | `/admin/api/scripts/restore`    | `POST /restore`                        |

## 5. Error model

| Status | Meaning                                    | Typical `code`                 |
|--------|--------------------------------------------|--------------------------------|
| 400    | Malformed request body or query string     | `bad_request`                  |
| 401    | Missing or invalid auth                    | `unauthorized`                 |
| 403    | Auth OK but the action is not allowed      | `forbidden`                    |
| 404    | Resource not found                         | `not_found`                    |
| 409    | Conflict (e.g. duplicate release name)     | `conflict`                     |
| 413    | Upload too large (body size cap)           | `payload_too_large`            |
| 429    | Rate limited (auth endpoints)              | `rate_limited`                 |
| 500    | Unhandled server error                     | `internal_error`               |
| 503    | Server is in setup mode                    | `setup_mode`                   |

## 6. Versioning

The server does not expose a versioned URL prefix today. The wire
format of `meta.7z` and the `config.json` shape are stable; the JSON
envelopes may gain fields but never rename existing ones. Breaking
changes are signalled via a `Server-Version` response header on
`/`.
