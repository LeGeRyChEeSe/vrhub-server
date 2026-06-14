# Development

How to set up a working development environment, run the tests, and
make focused changes. Most of the routine commands live in
[`CONTRIBUTING.md`](../CONTRIBUTING.md); this document covers the
workflow details.

## Toolchain

- **Go 1.26.2** (or newer 1.26.x). Verify with `go version`.
- A POSIX shell on Linux / macOS, PowerShell on Windows.

## First checkout

```bash
git clone https://github.com/LeGeRyChEeSe/vrhub-server.git
cd vrhub-server
go vet ./...
go build -o bin/vrhub-server ./cmd/server/
```

The build is self-contained; no `go generate` step is required.

## Day-to-day commands

```bash
# Lint
go vet ./...

# Build
go build -o bin/vrhub-server ./cmd/server/

# Run the server
./bin/vrhub-server -data-dir ./tmp/data -port 39457

# Focused test run
go test ./internal/api/ ./internal/config/

# Full test run
go test ./...

# Race detector (slow)
go test -race ./internal/api/

# Verbose single test
go test -v -run TestHandleRescanPOST ./internal/api/
```

The test suite uses standard `testing` with `httptest` for handler
tests and a temp directory per test for the data dir. No
testcontainers, no network calls &mdash; the `internal/metadata`
fetch is stubbed in tests.

## Project layout

```
cmd/
  server/              main entry; CLI flags; setup mode bootstrap
  inspect/             offline inspector (config / DB sanity)
  refresh-metadata/    one-shot MetaMetadata refresh
  test-config/         smoke test for config.toml
internal/
  api/                 Chi router, public + admin handlers
  archive/             7z + AES-256 meta.7z generator
  auth/                session cookies, API key, rate limit
  config/              TOML loader/saver
  db/                  SQLite layer
  firewall/            netsh advfirewall helper (Windows)
  game/                scanner, watcher, importer
  log/                 zerolog initialisation
  metadata/            MetaMetadata fetcher + cache
  monitor/             real-time monitoring publisher
  network/             network status helper
  update/              GitHub releases checker
  ui/                  embedded admin UI assets
pkg/types/             shared types (Config, ServerMode, GameEntry)
docker/                Dockerfile + docker-compose.yml
build.cmd              Windows build wrapper
```

`internal/` is private to this binary. `pkg/types/` is the only
importable surface; external tools (the VRHub automation bot, the
inspector CLI) can depend on it.

## Data directory for development

The setup wizard writes a real `config.toml` and a real SQLite
database. For local development, point the binary at a throwaway
directory:

```bash
mkdir -p ./tmp/data
./bin/vrhub-server -data-dir ./tmp/data -port 39457
```

To reset, stop the binary and `rm -rf ./tmp/data`. The wizard will
run again on the next launch.

## Hot reload (optional)

The Go binary does not have hot-reload support out of the box. If
you want one, the typical pattern is:

```bash
go install github.com/cosmtrek/air@latest
air -c .air.toml
```

No `.air.toml` is shipped; copy the default and tweak as needed.

## Cross-compilation

The same six combinations used for the published release assets can
be reproduced locally with plain `go build`. No CGo, no cross
toolchain required.

```bash
GOOS=windows GOARCH=amd64 go build -o bin/vrhub-server-windows-amd64.exe ./cmd/server/
GOOS=windows GOARCH=arm64 go build -o bin/vrhub-server-windows-arm64.exe ./cmd/server/
GOOS=linux   GOARCH=amd64 go build -o bin/vrhub-server-linux-amd64     ./cmd/server/
GOOS=linux   GOARCH=arm64 go build -o bin/vrhub-server-linux-arm64     ./cmd/server/
GOOS=darwin  GOARCH=amd64 go build -o bin/vrhub-server-darwin-amd64    ./cmd/server/
GOOS=darwin  GOARCH=arm64 go build -o bin/vrhub-server-darwin-arm64    ./cmd/server/
```

Note: the `internal/firewall` package is a runtime no-op outside
Windows (it returns `nil` immediately when `runtime.GOOS != "windows"`),
so cross-compiling for Linux or macOS produces a binary that does
not touch `netsh advfirewall`. The embedded 7z helper binary is
selected at build time per `GOOS` and is already present for all
six targets.

## Code conventions

- **Formatting:** `gofmt -w .` before committing. The CI pipeline
  will reject unformatted code.
- **Imports:** standard library, then third-party, then `internal/`
  and `pkg/`. `goimports` is your friend.
- **Errors:** wrap with `fmt.Errorf("context: %w", err)`. Use
  `errors.Is` / `errors.As` to inspect them.
- **Logging:** `vlog.Get().Info().Msg("...")` from
  `internal/log`. No `fmt.Println` outside the setup banner.
- **Concurrency:** prefer channels for cross-goroutine signalling,
  `sync.Mutex` for shared state, `atomic.Value` for config-style
  snapshots. The setup-to-normal mode transition uses
  `atomic.Value` &mdash; see `cmd/server/main.go` and the
  `SetupModeMiddleware` for the canonical pattern.

See [`docs/ARCHITECTURE.md`](ARCHITECTURE.md) for the package map
and the naming conventions table.
