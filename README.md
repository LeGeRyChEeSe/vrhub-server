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

<p align="center">
  <a href="#download">Download</a> &nbsp;&middot;&nbsp;
  <a href="#getting-started">Getting Started</a> &nbsp;&middot;&nbsp;
  <a href="#features">Features</a> &nbsp;&middot;&nbsp;
  <a href="#documentation">Documentation</a> &nbsp;&middot;&nbsp;
  <a href="#contributing">Contributing</a>
</p>

---

## What is it?

`vrhub-server` is the backend you run on your computer to serve your VR game library to the [VRHub](https://github.com/LeGeRyChEeSe/VRHub) app on your Meta Quest. It scans your game folders, builds an encrypted game list, and serves everything over your local network.

An admin web interface lets you manage your library, monitor the server, and apply updates — all from your browser. No account, no cloud, no subscription. Everything stays on your machine.

## Download

Pick the file that matches your operating system and machine.

### Windows

| Machine | Download |
|---------|----------|
| Most PCs (64-bit) | [vrhub-server-windows-amd64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-windows-amd64.zip) |
| ARM (Surface Pro X, Snapdragon…) | [vrhub-server-windows-arm64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-windows-arm64.zip) |

### macOS

| Machine | Download |
|---------|----------|
| Apple Silicon (M1 / M2 / M3 / M4) | [vrhub-server-darwin-arm64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-darwin-arm64.zip) |
| Intel Mac | [vrhub-server-darwin-amd64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-darwin-amd64.zip) |

### Linux

| Machine | Download |
|---------|----------|
| Most servers and desktops (64-bit) | [vrhub-server-linux-amd64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-linux-amd64.zip) |
| ARM (Raspberry Pi, etc.) | [vrhub-server-linux-arm64.zip](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-linux-arm64.zip) |

Not sure which to pick? If your machine is a regular PC or laptop bought in the last ten years, the `amd64` version is almost certainly right.

Each release includes a `checksums.txt` file you can use to verify your download. Older versions are available on the [releases page](https://github.com/LeGeRyChEeSe/vrhub-server/releases).

## Getting Started

Setup takes under five minutes:

1. **Download and extract** the binary for your platform (see above).
2. **Run it** — on first launch, the server opens the setup wizard in your browser.
3. **Follow the wizard** — set your admin password, choose an archive password, point the server at your game folder, and confirm the detected games.

Once the wizard completes, your server is live. Then in VRHub on your Quest, select **Manual Entry** or **JSON URL**, enter your server address and the Base64 password shown in the admin UI.

For a detailed walkthrough on every platform (Windows, macOS, Linux, Android, Docker), see **[docs/INSTALL.md](docs/INSTALL.md)**.

## Features

- **Admin web UI** — manage your library, review detected games, monitor server activity and configure everything from your browser. No command line needed after setup.
- **Automatic metadata** — game descriptions and thumbnails are fetched automatically and served alongside your files for VRHub client discovery.
- **Game library management** — add or remove games from your folders, then trigger a rescan from the admin UI to update the library.
- **In-app updates** — the server checks for new versions in the background and lets you review and apply updates directly from the admin UI.
- **Encrypted game list** — the game catalog is served as a password-protected archive that only your VRHub client can read.
- **Single binary** — no dependencies, no Docker required. Runs on Windows, macOS, Linux and Android (Termux).

## Documentation

| Guide | What it covers |
|-------|----------------|
| [Installation](docs/INSTALL.md) | Download, first run, firewall, background service — all platforms |
| [Client setup](docs/CLIENT_INTEGRATION.md) | Connecting the VRHub app to your server |
| [Configuration](docs/CONFIGURATION.md) | All `config.toml` settings explained |
| [API reference](docs/API.md) | HTTP endpoints for developers and automation bots |
| [Architecture](docs/ARCHITECTURE.md) | Code structure, package map, design decisions |
| [Development](docs/DEVELOPMENT.md) | Build, test, and contribute |

## Contributing

Bug reports, feature requests and pull requests are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md) for the coding conventions and PR flow before opening a pull request.

## License

This project is licensed under the **MIT License** — see [LICENSE](LICENSE) for the full text.

---

> `vrhub-server` is a personal library manager. The server does not provide, host or distribute any game content. You are solely responsible for the games you load and must only manage titles you have legitimately purchased.
