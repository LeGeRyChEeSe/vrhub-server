# Client Integration

This guide shows how to connect the
[VRHub client](https://github.com/LeGeRyChEeSe/VRHub) for Meta Quest
to a `vrhub-server` instance. The two projects are designed to be
interchangeable: any client that talks to the upstream VRHub service
talks to this server without code changes.

## Prerequisites

- A running `vrhub-server` reachable from the Quest over your local
  network.
- The **archive password** you set during the setup wizard
  ([`admin.archive_password`](CONFIGURATION.md)).
- The base URL of the server in the form
  `http://<host>:<port>/`. The trailing slash is required.

## How the client gets the configuration

The VRHub client needs two pieces of information: the `baseUri` of
the server and the cleartext `archive_password` (so it can decrypt
`meta.7z`). There are two ways to deliver them.

### Option A &mdash; JSON URL (recommended)

Host a static JSON file with the following shape anywhere the
Quest can reach:

```json
{
  "baseUri": "http://192.0.2.10:39457/",
  "password": "your-cleartext-archive-password"
}
```

In the VRHub client, pick **JSON URL** mode and paste the URL of
that file. The client downloads it, validates the keys and shows
the **TEST** button.

### Option B &mdash; Manual entry

Pick **Manual Entry** mode in the client and add the two keys
yourself:

| Key       | Value                                                                       |
|-----------|-----------------------------------------------------------------------------|
| `baseUri` | `http://192.0.2.10:39457/` (your server URL)                                |
| `password`| The **Base64-encoded** archive password shown in the admin UI               |

The admin UI displays the Base64 value in all client-facing cards
(dashboard chip, client setup card). Do **not** paste the raw
password from `config.toml` — the client expects the Base64 form
that `/config.json` returns.

Press **ADD KEY** after each entry, then **TEST** to verify the
connection.

## Server-side: expose `config.json`

The server ships its own bootstrap file at
`GET http://<host>:<port>/config.json`. It is regenerated on every
request from `config.toml` and always reflects the current
`archive_password`. You can either:

- Use the server's own `/config.json` directly (only safe on trusted
  networks &mdash; the password is in the body), **or**
- Host your own JSON file and point the client at it. This lets you
  share a file without revealing the live admin endpoint to anyone
  who has read access to the URL.

## Network reachability

### LAN (most common)

1. Bind the server to the LAN address: edit `[server].host` in
   `config.toml` to `0.0.0.0` (or the specific interface IP).
2. Open TCP `39457` (or your chosen port) on the host firewall.
   - **Windows:** the embedded firewall helper does this for you
     when the binary is launched with the sidecar manifest.
   - **Linux:** `sudo ufw allow 39457/tcp` or equivalent
     (`firewalld`, `iptables`, `nftables` work the same way).
   - **macOS:** the embedded helper is a no-op; allow the binary
     via the Firewall pane in System Settings, or from the CLI:
     ```bash
     sudo /usr/libexec/ApplicationFirewall/socketfilterfw \
       --add /usr/local/bin/vrhub-server
     sudo /usr/libexec/ApplicationFirewall/socketfilterfw \
       --unblock /usr/local/bin/vrhub-server
     ```
3. Verify from another device on the same network:
   ```bash
   curl -sI http://<host-ip>:39457/
   ```

### WAN (advanced)

Exposing `vrhub-server` to the public internet is **strongly
discouraged**: the admin UI does not implement CSRF tokens, the
session cookie is single-factor, and the public API is unauthenticated
by design. If you must:

- Put the server behind a reverse proxy (Caddy, nginx, Traefik) that
  terminates TLS and exposes only `/`, `/config.json`, `/meta.7z`,
  and `/{hash}/*` to the public.
- Restrict the admin UI to an internal VPN or an SSH tunnel.
- Rotate the archive password before going live and after any
  exposure incident.

## Troubleshooting

### "Game not found" on every install

The base URL must end with a trailing slash and match the on-disk
hash the server assigned. Confirm with:

```bash
curl -s http://<host>:<port>/config.json
curl -sI http://<host>:<port>/meta.7z
```

If the first request succeeds and the second returns
`401 Unauthorized`, the archive password you pasted into the client
does not match `[admin].archive_password` in `config.toml`.

### Connection times out

- Confirm the server is bound to a reachable interface
  (`[server].host` in `config.toml`).
- Confirm the firewall is open on the listening port
  (`netsh advfirewall firewall show rule name=vrhub-server`
  on Windows).
- Confirm the Quest is on the same network (or your VPN is up).

### `meta.7z` is rejected by the client

The client expects the **Base64-encoded** form of the archive
password, not the raw value from `config.toml`. Copy the password
from the admin UI (dashboard chip or client setup card) and
re-add the key in the client.

### Setup mode is stuck

`meta.7z` is the only public route that works in setup mode (it
returns `503`). If the client shows "server in setup mode", complete
the wizard in the browser at `/admin/setup`.

## Sample `config.json` for a home server

```json
{
  "baseUri": "http://vrhub.lan:39457/",
  "password": "pick-a-long-random-string"
}
```

Save this file on a personal web host (a static S3 bucket, a GitHub
Gist, an internal wiki), then point the VRHub client at the URL.

## Further reading

- [`docs/CONFIGURATION.md`](CONFIGURATION.md) &mdash; full
  `config.toml` reference, including the rationale for storing the
  archive password in cleartext.
- [`docs/API.md`](API.md) &mdash; the wire-level API surface, in
  case you are building a non-VRHub client against the same
  backend.
- [`docs/SECURITY.md`](../SECURITY.md) &mdash; hardening notes for
  operators.
