# Install

This guide covers the first-run install of `vrhub-server` on Windows,
Linux, macOS, and Docker. It assumes you have not yet created a
`config.toml` in the data directory &mdash; if you have, jump to
[`docs/CONFIGURATION.md`](CONFIGURATION.md) instead.

## 1. Prerequisites

- A Go **1.26.2** toolchain (for source builds) **or** a pre-built
  binary from the
  [GitHub releases page](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest).
- One or more local folders that contain your legally-owned APK
  and OBB files.
- A modern browser to drive the first-run wizard.

Disk usage is modest: the binary is ~35 MB, the SQLite database is
under 1 MB for thousands of games, and the only large footprint is
your game library itself.

## 2. Build from source

```bash
git clone https://github.com/LeGeRyChEeSe/vrhub-server.git
cd vrhub-server
go vet ./...
go build -o bin/vrhub-server ./cmd/server/
```

On Windows the equivalent `cmd` wrapper also copies the sidecar
manifest that requests `requireAdministrator`:

```cmd
build.cmd
```

## 3. First run &mdash; the setup wizard

Launch the server without any flag to land in setup mode:

```bash
./bin/vrhub-server
```

Output:

```
First run detected — no config file found.
Starting setup wizard at /admin/setup
Listening on 0.0.0.0:39457 (mode=setup)
```

Open <http://127.0.0.1:39457/admin/setup> in your browser. The wizard
walks through five steps:

1. **Credentials** &mdash; set the admin username and password
   (bcrypt-hashed on save).
2. **Archive password** &mdash; the cleartext password used to encrypt
   `meta.7z` with AES-256. The VRHub client needs the original
   password, so it cannot be hashed.
3. **Game folders** &mdash; absolute paths to the directories the
   scanner should walk. Add as many as you need; you can edit the list
   later from the admin UI.
4. **Scan review** &mdash; the scanner inspects each folder, pairs APK
   files with their OBBs, and surfaces the candidates. You can
   exclude individual entries; nothing is deleted.
5. **Launch** &mdash; the server writes `config.toml`, restarts in
   normal mode and exposes the public + admin API.

The wizard also generates a SHA-256-hashed admin API key. You will see
the plaintext exactly once; copy it before you click **Launch**.

## 4. Verify the install

After the wizard completes, the server is in normal mode. Confirm:

```bash
curl -sI http://127.0.0.1:39457/
curl -s   http://127.0.0.1:39457/config.json
curl -sI  http://127.0.0.1:39457/admin/api/stats -H "X-API-Key: <your key>"
```

You should see `200 OK` on the first two requests and a JSON payload
on the third.

## 5. Install paths

### 5.1 Windows

- Default data dir: `%APPDATA%\vrhub-server`
- Default listen: `127.0.0.1:39457`
- The `build.cmd` wrapper places the binary and the sidecar manifest
  in `bin\`. The manifest causes Windows to ask for UAC elevation
  every time the binary launches &mdash; this is intentional: the
  embedded firewall helper invokes `netsh advfirewall firewall add
  rule` to open the listening port.
- For a "no UAC prompt" install, run the server with an existing
  firewall rule, or compile the binary with
  `cmd/server/vrhub-server.exe.manifest` removed. The helper will
  silently fall back to "firewall rule not created" log entries.

### 5.2 macOS

- Default data dir: `$HOME/.vrhub-server`
- Default listen: `127.0.0.1:39457`
- Download the `vrhub-server-darwin-amd64.zip` (Intel) or
  `vrhub-server-darwin-arm64.zip` (Apple Silicon) archive, extract it,
  mark the binary executable and (recommended) move it to a stable
  location on `PATH`:

  ```bash
  # Pick the right asset for your Mac
  curl -fL -o vrhub-server.zip \
    https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-darwin-arm64.zip
  unzip vrhub-server.zip
  mv vrhub-server-*-darwin-arm64 vrhub-server   # the archive holds a versioned binary
  chmod +x vrhub-server
  xattr -d com.apple.quarantine vrhub-server    # removes the Gatekeeper flag added by browsers
  sudo mv vrhub-server /usr/local/bin/
  ```

- The first launch opens the setup wizard on
  <http://127.0.0.1:39457/admin/setup>. The embedded firewall helper
  is a runtime no-op on macOS: open TCP `39457` in
  **System Settings &rarr; Network &rarr; Firewall &rarr; Options**
  if you bind to `0.0.0.0` and need LAN reachability, or use the
  `socketfilterfw` CLI:

  ```bash
  # Allow inbound TCP/39457 (LAN only)
  sudo /usr/libexec/ApplicationFirewall/socketfilterfw \
    --add /usr/local/bin/vrhub-server
  sudo /usr/libexec/ApplicationFirewall/socketfilterfw \
    --unblock /usr/local/bin/vrhub-server
  ```

- A `launchd` plist for autostart at login is the macOS equivalent of
  the Linux systemd unit. Save the snippet below to
  `~/Library/LaunchAgents/com.legerycheese.vrhub-server.plist` and
  load it with `launchctl load -w ~/Library/LaunchAgents/com.legerycheese.vrhub-server.plist`:

  ```xml
  <?xml version="1.0" encoding="UTF-8"?>
  <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
  <plist version="1.0">
  <dict>
      <key>Label</key><string>com.legerycheese.vrhub-server</string>
      <key>ProgramArguments</key>
      <array>
          <string>/usr/local/bin/vrhub-server</string>
      </array>
      <key>RunAtLoad</key><true/>
      <key>KeepAlive</key><true/>
      <key>StandardOutPath</key><string>/usr/local/var/log/vrhub-server.log</string>
      <key>StandardErrorPath</key><string>/usr/local/var/log/vrhub-server.err</string>
  </dict>
  </plist>
  ```

### 5.3 Linux

- Default data dir: `$HOME/.vrhub-server`
- Default listen: `127.0.0.1:39457`
- Recommended systemd unit (replace `youruser` with your user):

  ```ini
  [Unit]
  Description=vrhub-server
  After=network-online.target

  [Service]
  Type=simple
  User=youruser
  ExecStart=/usr/local/bin/vrhub-server -data-dir %h/.vrhub-server
  Restart=on-failure
  RestartSec=5s

  [Install]
  WantedBy=multi-user.target
  ```

- If you want LAN reachability, either bind to `0.0.0.0` via
  `[server].host` in `config.toml` and open TCP `39457` in your
  firewall, or put a reverse proxy (Caddy, nginx) in front of the
  service for HTTPS termination.

### 5.4 Docker

A `docker/Dockerfile` and `docker/docker-compose.yml` are provided.
The image is a static binary on a distroless base; the compose file
mounts `./data` under `/data` so the database, config, metadata cache
and backups survive container restarts, and mounts your game library
read-only under `/games`.

```bash
cd docker
# Edit docker-compose.yml: set the /games host path to your library.
docker compose up -d --build
docker compose logs -f vrhub-server
```

Open the setup wizard at <http://127.0.0.1:39457/admin/setup>. Add the
in-container path (`/games`) as a scanned folder during the wizard.
Because the server runs inside a container, set `[server].host` to
`0.0.0.0` (the setup wizard already binds all interfaces) so the
published port is reachable from the host.

## 6. Updating

- The update checker (when `[update].enabled = true`) polls GitHub
  releases on a configurable interval and surfaces a banner in the
  admin UI.
- Apply an update from the admin UI (**Configuration &rarr;
  Updates**) or via the REST API
  (`POST /admin/api/update/apply` with `X-API-Key`).
- The update is staged into `{data-dir}/.updating/`, verified, then
  atomically swapped. A pre-launch recovery step handles an
  interrupted previous update.

## 7. Uninstall

Removing the server is a three-step affair:

1. Stop the binary (Ctrl+C, `systemctl stop vrhub-server`, or
   `docker compose down`).
2. Delete the binary.
3. Delete the data directory if you no longer need the database,
   configuration, cached metadata, and backups.

The server keeps no state outside the data directory you pass via
`-data-dir` (or the platform default).
