# Installing vrhub-server

> **Step-by-step guide to install and run `vrhub-server` on Windows,
> macOS, Linux and Android (via Termux), with no prior knowledge
> required.**

This document is aimed at a **beginner**: every command is explained,
every path is spelled out, and running the server in the background
is covered for all four supported operating systems.

---

## Table of Contents

1. [Before you start](#1-before-you-start)
   - [1.1 What is `vrhub-server`?](#11-what-is-vrhub-server-)
   - [1.2 What you need](#12-what-you-need)
   - [1.3 Beginner's glossary](#13-beginners-glossary)
2. [Concepts common to all platforms](#2-concepts-common-to-all-platforms)
   - [2.1 The binary](#21-the-binary)
   - [2.2 The data directory](#22-the-data-directory)
   - [2.3 The network port](#23-the-network-port)
   - [2.4 First run: the setup wizard](#24-first-run-the-setup-wizard)
3. [Installing on Windows](#3-installing-on-windows)
   - [3.1 Download the binary](#31-download-the-binary)
   - [3.2 Prepare an install folder](#32-prepare-an-install-folder)
   - [3.3 Verify the download](#33-verify-the-download)
   - [3.4 Run the server for the first time](#34-run-the-server-for-the-first-time)
   - [3.5 The setup wizard](#35-the-setup-wizard)
   - [3.6 Run in the background (Task Scheduler)](#36-run-in-the-background-task-scheduler)
   - [3.7 Run in the background (alternative: .bat script)](#37-run-in-the-background-alternative-bat-script)
   - [3.8 Open the port in the Windows firewall](#38-open-the-port-in-the-windows-firewall)
   - [3.9 Updating](#39-updating)
   - [3.10 Uninstalling](#310-uninstalling)
4. [Installing on macOS](#4-installing-on-macos)
   - [4.1 Identify your Mac's architecture](#41-identify-your-macs-architecture)
   - [4.2 Download the binary](#42-download-the-binary)
   - [4.3 Move the binary to a stable location](#43-move-the-binary-to-a-stable-location)
   - [4.4 Unblock the binary (Gatekeeper)](#44-unblock-the-binary-gatekeeper)
   - [4.5 Run the server for the first time](#45-run-the-server-for-the-first-time)
   - [4.6 The setup wizard](#46-the-setup-wizard)
   - [4.7 Open the port in the macOS firewall](#47-open-the-port-in-the-macos-firewall)
   - [4.8 Run in the background (launchd)](#48-run-in-the-background-launchd)
   - [4.9 Updating](#49-updating)
   - [4.10 Uninstalling](#410-uninstalling)
5. [Installing on Linux](#5-installing-on-linux)
   - [5.1 Identify your distribution](#51-identify-your-distribution)
   - [5.2 Download the binary](#52-download-the-binary)
   - [5.3 Make the binary executable](#53-make-the-binary-executable)
   - [5.4 Quick test](#54-quick-test)
   - [5.5 Run the server for the first time](#55-run-the-server-for-the-first-time)
   - [5.6 The setup wizard](#56-the-setup-wizard)
   - [5.7 Open the port in the firewall](#57-open-the-port-in-the-firewall)
   - [5.8 Run in the background with systemd (Ubuntu, Debian, Fedora, Arch…)](#58-run-in-the-background-with-systemd-ubuntu-debian-fedora-arch)
   - [5.9 Run in the background on a distribution without systemd](#59-run-in-the-background-on-a-distribution-without-systemd)
   - [5.10 Updating](#510-updating)
   - [5.11 Uninstalling](#511-uninstalling)
6. [Installing on Android (via Termux)](#6-installing-on-android-via-termux)
   - [6.1 Install Termux](#61-install-termux)
   - [6.2 Update Termux packages](#62-update-termux-packages)
   - [6.3 Allow Termux to access shared storage](#63-allow-termux-to-access-shared-storage)
   - [6.4 Download the binary](#64-download-the-binary)
   - [6.5 Quick test](#65-quick-test)
   - [6.6 Run the server for the first time](#66-run-the-server-for-the-first-time)
   - [6.7 The setup wizard](#67-the-setup-wizard)
   - [6.8 Stop Android from killing the server in the background](#68-stop-android-from-killing-the-server-in-the-background)
   - [6.9 Run in the background (simple method)](#69-run-in-the-background-simple-method)
   - [6.10 Run in the background with `termux-services`](#610-run-in-the-background-with-termux-services)
   - [6.11 Make the server reachable from the Quest on the same Wi-Fi](#611-make-the-server-reachable-from-the-quest-on-the-same-wi-fi)
   - [6.12 Updating](#612-updating)
   - [6.13 Uninstalling](#613-uninstalling)
7. [Command-line parameters reference](#7-command-line-parameters-reference)
   - [7.1 `-data-dir`](#71--data-dir)
   - [7.2 `-port`](#72--port)
   - [7.3 Useful combinations](#73-useful-combinations)
8. [Data directory layout](#8-data-directory-layout)
9. [Post-install verification](#9-post-install-verification)
10. [Updating (all platforms)](#10-updating-all-platforms)
11. [Uninstalling (all platforms)](#11-uninstalling-all-platforms)
12. [Frequently Asked Questions](#12-frequently-asked-questions)

---

## 1. Before you start

### 1.1 What is `vrhub-server`?

`vrhub-server` is a **small server program** you install on **your**
computer (or Android phone) that acts as a game catalog for the
**VRHub** client running on your Meta Quest. In practice:

- The server **scans** one or more folders on your disk that contain
  `.apk` and `.obb` files (your games);
- It **generates** a password-protected `meta.7z` file that lists
  your games;
- It **serves** the files (HTML, JSON, archives) to your Quest over
  the local network (Wi-Fi);
- It exposes a **web-based admin UI** to configure, monitor and
  update the server.

> **Important**: the server contains no games. You must already
> legally own the games you intend to load; the server only
> organises and exposes them to the Quest.

### 1.2 What you need

| Item                                     | Why                                                                                              |
|------------------------------------------|--------------------------------------------------------------------------------------------------|
| **A recent computer or phone**           | The binary is static and runs on any machine < 10 years old.                                      |
| **~50 MB of free disk space**            | Size of the binary + database. The rest is your games (which can be huge).                       |
| **A local Wi-Fi connection**             | To connect the Quest to your server. Internet is not required (except for metadata updates).     |
| **A modern web browser**                 | For the setup wizard and the admin UI.                                                           |
| **~10 minutes**                          | To follow this guide from start to finish.                                                        |

### 1.3 Beginner's glossary

| Term                       | Definition                                                                                                          |
|----------------------------|---------------------------------------------------------------------------------------------------------------------|
| **Binary**                 | The executable program (a `.exe` file on Windows, a no-extension file on Mac/Linux/Android).                        |
| **Terminal / Console**     | A window where you type text commands. On Windows: `cmd.exe` or PowerShell. On Mac: `Terminal.app`.                 |
| **Shell**                  | The program that interprets commands in a terminal (bash, zsh, PowerShell…).                                        |
| **PATH**                   | A list of directories where the system looks for programs. Adding a directory to `PATH` makes its executables callable without the full path. |
| **Network port**           | A number (here `39457`) that identifies a "channel" on your machine. Two programs cannot use the same port at the same time. |
| **`curl`**                 | A command-line tool for making HTTP requests (testing a web server).                                                |
| **`sudo`**                 | On Mac/Linux/Android, a command that runs what follows with administrator rights.                                   |
| **`systemd`**              | The service manager of modern Linux distributions; it lets you launch a program at boot.                            |
| **launchd**                | The macOS equivalent of systemd.                                                                                    |
| **Task Scheduler**         | The Windows equivalent of systemd/launchd; open it with `win+r` → `taskschd.msc`.                                   |
| **Termux**                 | An Android application that provides a full Linux terminal on a phone.                                             |
| **bcrypt**                 | An algorithm to hash (protect) passwords. The server never stores your password in cleartext.                       |
| **AES-256**                | A very strong encryption algorithm (256-bit) used to protect `meta.7z`.                                              |
| **Daemon / service**       | A program that runs in the background, with no visible window.                                                      |
| **Local IP address**       | Your machine's address on your network (often `192.168.x.x` or `10.x.x.x`).                                         |
| **Cloudflare tunnel**      | A service that exposes your local server on the Internet through an encrypted tunnel. Used by some to reach the server from outside the home. |

---

## 2. Concepts common to all platforms

This section describes shared concepts; the next sections cover each
operating system in detail.

### 2.1 The binary

The server is shipped as **a single executable file**: no
"installation" in the traditional sense, no dependencies to install,
no external database. Everything is inside the binary.

> Download the binary that matches **your** OS and **your**
> architecture from the
> [GitHub Releases page](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest).

| Platform   | Architecture                | File to download                                |
|------------|-----------------------------|-------------------------------------------------|
| Windows    | Intel / AMD 64-bit          | `vrhub-server-windows-amd64.zip`                 |
| Windows    | ARM 64-bit (rare)           | `vrhub-server-windows-arm64.zip`                 |
| macOS      | Apple Silicon (M1/M2/M3/M4) | `vrhub-server-darwin-arm64.zip`                  |
| macOS      | Intel                       | `vrhub-server-darwin-amd64.zip`                  |
| Linux      | Intel / AMD 64-bit          | `vrhub-server-linux-amd64.zip`                   |
| Linux      | ARM 64-bit                  | `vrhub-server-linux-arm64.zip`                   |
| Android    | ARM 64-bit                  | `vrhub-server-linux-arm64.zip` (via Termux)     |

### 2.2 The data directory

This is where the server stores all its persistent files:
configuration, database, metadata cache, backups. By default:

| Platform   | Default path                                |
|------------|---------------------------------------------|
| Windows    | `%APPDATA%\vrhub-server`                    |
| macOS      | `~/.vrhub-server`                           |
| Linux      | `~/.vrhub-server`                           |
| Android    | `~/.vrhub-server` (inside Termux)           |

> **Tip**: `%APPDATA%` is an environment variable. It typically
> points to `C:\Users\<you>\AppData\Roaming`. To open it, hit
> `win+r` → type `%APPDATA%\vrhub-server` → `Enter`.

> **Tip**: `~` (tilde) is a shortcut for "your home directory":
> `/Users/<you>` on macOS, `/home/<you>` on Linux,
> `/data/data/com.termux/files/home` on Android/Termux.

You can **change** this directory with the `-data-dir` parameter
(see [section 7](#7-command-line-parameters-reference)).

### 2.3 The network port

By default, the server listens on port **`39457`**. You can change
this port with the `-port` parameter (see
[section 7](#7-command-line-parameters-reference)).

> **If another program is already using this port**, you will see
> an `EADDRINUSE` error at startup. Switch ports (e.g. `49500`) or
> stop the program that occupies it.

### 2.4 First run: the setup wizard

On the **very first launch**, the server finds no `config.toml` in
the data directory: it switches to **setup mode** and exposes only
the wizard at <http://127.0.0.1:39457/admin/setup>.

The wizard asks you in turn:

1. **Admin credentials** (username + password);
2. **Archive password** (the one that protects `meta.7z` with
   AES-256, to be entered in the VRHub client);
3. **Game folders** to scan (one or more absolute paths);
4. **Detected games review** (paired APK / OBB);
5. **Launch in normal mode**: the server writes `config.toml`,
   restarts in normal mode and exposes the public API.

> **Note**: an **admin API key** is generated and shown **once** at
> the end of the wizard. **Copy it immediately** and keep it
> somewhere safe. It is required for some `/admin/api/*` routes.

---

## 3. Installing on Windows

> **Prerequisite**: Windows 10 or Windows 11 (64-bit). Identify your
> architecture: if you have a regular PC (Intel or AMD), it is
> `amd64`. On a PC with an ARM CPU (rare on PC, common on Apple
> Silicon Macs virtualising Windows), it is `arm64`. If unsure,
> pick `amd64`.

### 3.1 Download the binary

1. Open your browser and go to
   [GitHub Releases](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest).
2. In the **Assets** section, click on
   `vrhub-server-windows-amd64.zip` (or `…-arm64.zip` if applicable).
3. Save the file (it lands in `Downloads` by default).

### 3.2 Prepare an install folder

The binary can stay in `Downloads`, but it's cleaner to place it in
a dedicated folder — for example `C:\Programmes\vrhub-server` or
simply `C:\vrhub-server`.

1. Open **File Explorer**.
2. Navigate to `C:\`.
3. Create a new folder named `vrhub-server` (right-click →
   *New* → *Folder* → type `vrhub-server` → `Enter`).
4. Open the `Downloads` folder, double-click
   `vrhub-server-windows-amd64.zip` to open it in Explorer.
5. **Drag** the `vrhub-server.exe` file from the archive into
   `C:\vrhub-server\`.

> **Note**: the archive usually contains **two** files:
> `vrhub-server.exe` (the program) and `vrhub-server.exe.manifest`
> (a manifest that requests administrator elevation at launch to be
> able to open the port in the firewall automatically). Keep both
> in the same folder.

### 3.3 Verify the download

This step is **optional** but recommended to make sure the file was
not corrupted during download.

1. Open **PowerShell** (Start menu → type `PowerShell` → `Enter`).
2. Move to the Downloads folder:

   ```powershell
   cd $env:USERPROFILE\Downloads
   ```

3. Get the SHA-256 fingerprint of the file:

   ```powershell
   Get-FileHash .\vrhub-server-windows-amd64.zip -Algorithm SHA256
   ```

4. Compare the long hexadecimal string you got with the one
   published next to the file on the GitHub Releases page (see the
   `checksums.txt` field).

> **Note**: if you can't find the checksum, don't worry — the
> server has a recovery mechanism in case of a corrupt file.

### 3.4 Run the server for the first time

1. In File Explorer, open `C:\vrhub-server\`.
2. **Double-click** `vrhub-server.exe`.
3. A **black window** (console) opens. Windows shows a prompt
   *"Do you want to allow this app to make changes to your
   device?"*: click **Yes**. This is the **UAC manifest** asking
   for elevation so it can configure the firewall.
4. The server displays:

   ```text
   First run detected — no config file found.
   Starting setup wizard at /admin/setup
   Listening on 0.0.0.0:39457 (mode=setup)
   ```

5. **Do not close this window**: if you close it, the server stops.

### 3.5 The setup wizard

1. On **the same machine**, open your browser and go to:
   <http://127.0.0.1:39457/admin/setup>
2. Follow the five steps (see
   [section 2.4](#24-first-run-the-setup-wizard)).
3. At the end, **copy the admin API key** displayed on screen.
4. Click **Launch**: the server restarts in normal mode.

> **Tip**: the default port is `39457`. If you changed the port
> with the `-port` parameter (see
> [section 7](#7-command-line-parameters-reference)), adjust the
> URL accordingly.

### 3.6 Run in the background (Task Scheduler)

To make the server run **with no visible window** and **start
automatically** when you turn on your PC, the simplest way on
Windows is to use the **Task Scheduler**.

> **Why not a Windows service?** Creating a real Windows service
> requires an extra program (e.g. `nssm.exe` or `WinSW`). For
> personal use, Task Scheduler is **much simpler** and does
> exactly the same job.

**Step-by-step**:

1. Open **Task Scheduler**: `Win` key → type `taskschd.msc` →
   `Enter`.
2. On the right, click **"Create Task…"** (not "Create Basic
   Task").
3. **General** tab:
   - **Name**: `vrhub-server`
   - **Description**: `VRHub server — auto-start`
   - Tick **"Run whether user is logged on or not"**
   - Tick **"Run with highest privileges"**
4. **Triggers** tab:
   - Click **New…**
   - Begin the task: **At startup**
   - Tick **"Enabled"** → OK
5. **Actions** tab:
   - Click **New…**
   - Action: **Start a program**
   - Program: `C:\vrhub-server\vrhub-server.exe`
   - Add arguments *(optional)*: e.g.
     `-data-dir D:\vrhub-data -port 39457`
   - **Start in**: `C:\vrhub-server\`
   - OK
6. **Conditions** tab:
   - Untick **"Start the task only if the computer is on AC
     power"** (useful for laptops)
7. **Settings** tab:
   - Tick **"Allow task to be run on demand"**
   - Tick **"If the task fails, restart every"**: `1 minute`,
     **3 times**
   - Tick **"Stop the task if it runs longer than"**: leave blank
     (the server does not need to be stopped)
8. Click **OK**. You will be asked for your Windows password (to
   validate the elevation).
9. The task appears in the list. Select it and click
   **"Run"** on the right to start it right away.

> **Verification**: in the *History* tab (at the bottom), tick
> *"Enable All Tasks History"* if you want to see the start and
> stop events.

> **To stop/restart**: select the task → right panel → *End* /
> *Run*.

### 3.7 Run in the background (alternative: .bat script)

If Task Scheduler feels complicated, here is a much simpler method
but it **does not start automatically at boot**.

1. Create a text file `start-vrhub.bat` in `C:\vrhub-server\`
   with this content:

   ```bat
   @echo off
   cd /d C:\vrhub-server
   start "" /B vrhub-server.exe -data-dir "%APPDATA%\vrhub-server" -port 39457
   ```

2. **Script parameters**:
   - `@echo off` : disable command echoing;
   - `cd /d C:\vrhub-server` : move to the binary folder;
   - `start "" /B` : launch the program **in the background,
     without a new console window** (the `/B` is essential).
3. Double-click `start-vrhub.bat` whenever you want to start the
   server.
4. To make it start at boot, drag a **shortcut** to
   `start-vrhub.bat` into the `shell:startup` folder (`Win+r` →
   `shell:startup` → `Enter`).

> **Downside**: if the machine reboots, the server does **not**
> start by itself — you have to double-click the script again. For
> auto-start, prefer the Task Scheduler method (3.6).

### 3.8 Open the port in the Windows firewall

The manifest shipped with the binary automatically opens TCP port
`39457` in the Windows firewall. **You normally have nothing to
do.** If you still need to open it manually (for example after
declining elevation):

1. Open **Windows Defender Firewall with Advanced Security**:
   `Win+r` → `wf.msc` → `Enter`.
2. **Inbound Rules** → *New Rule…*
3. **Port** → **TCP** → `39457` → **Allow the connection** →
   tick *Domain*, *Private*, *Public* → name the rule
   `vrhub-server` → **Finish**.

### 3.9 Updating

See [section 10](#10-updating-all-platforms).

### 3.10 Uninstalling

See [section 11](#11-uninstalling-all-platforms).

---

## 4. Installing on macOS

> **Prerequisite**: macOS 12 (Monterey) or later. Identify your
> Mac's architecture (see 4.1).

### 4.1 Identify your Mac's architecture

1. Click the **Apple menu ()** → **About This Mac**.
2. Look at the **Processor** or **Chip** line:
   - **Intel** → take the `darwin-amd64.zip` archive.
   - **Apple M1 / M2 / M3 / M4 chip** → take `darwin-arm64.zip`.
3. If unsure, open *Terminal* (`Cmd+Space` → type *Terminal* →
   `Enter`) and run:

   ```bash
   uname -m
   ```

   - `x86_64` → Intel → `darwin-amd64`
   - `arm64` → Apple Silicon → `darwin-arm64`

### 4.2 Download the binary

1. Open your browser and go to
   [GitHub Releases](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest).
2. Download `vrhub-server-darwin-arm64.zip` (or
   `darwin-amd64.zip` for Intel).
3. The file lands in `~/Downloads/`.

### 4.3 Move the binary to a stable location

Open *Terminal* (`Cmd+Space` → *Terminal*) and run:

```bash
# Move to the Downloads folder
cd ~/Downloads

# Extract the archive (unzip is preinstalled on macOS)
unzip vrhub-server-darwin-arm64.zip

# The archive produces a binary named
# "vrhub-server-X.Y.Z-darwin-arm64". Rename it to "vrhub-server"
# for simplicity.
mv vrhub-server-*-darwin-arm64 vrhub-server

# Make it executable
chmod +x vrhub-server

# Install it in /usr/local/bin, which is in the default PATH.
# (You will be prompted for your admin password.)
sudo mv vrhub-server /usr/local/bin/
```

> **Note**: on Apple Silicon Macs, if `/usr/local/bin` causes issues
> (rare), you can also use `~/bin` (not in the default PATH); you
> would then need to add `export PATH="$HOME/bin:$PATH"` to your
> `~/.zshrc`.

### 4.4 Unblock the binary (Gatekeeper)

macOS tags binaries downloaded from the Internet with a
"quarantine" attribute that makes Gatekeeper refuse them. If you
launch the binary from the terminal, you won't have this problem,
but it is cleaner to remove the attribute explicitly:

```bash
sudo xattr -d com.apple.quarantine /usr/local/bin/vrhub-server
```

> **If you see "xattr: No such file"**: the attribute does not
> exist, nothing to do. Move on.

### 4.5 Run the server for the first time

1. In the Terminal, run:

   ```bash
   vrhub-server
   ```

2. The server displays:

   ```text
   First run detected — no config file found.
   Starting setup wizard at /admin/setup
   Listening on 0.0.0.0:39457 (mode=setup)
   ```

3. **Leave this Terminal window open** (otherwise the server
   stops).

### 4.6 The setup wizard

1. On the same Mac, open Safari (or your favourite browser) and go
   to <http://127.0.0.1:39457/admin/setup>.
2. Follow the five steps of the wizard (see
   [section 2.4](#24-first-run-the-setup-wizard)).
3. At the end, **copy the admin API key**.
4. Click **Launch**.

### 4.7 Open the port in the macOS firewall

The built-in macOS firewall assistant does not automatically offer
to open port `39457`. For a Quest on the same Wi-Fi to reach your
server, run:

```bash
# Add the binary to the list of allowed applications
sudo /usr/libexec/ApplicationFirewall/socketfilterfw \
  --add /usr/local/bin/vrhub-server

# Lift the block for incoming connections
sudo /usr/libexec/ApplicationFirewall/socketfilterfw \
  --unblock /usr/local/bin/vrhub-server
```

> **If you use a third-party firewall (Little Snitch, LuLu…)**,
> explicitly allow inbound TCP connections on port `39457` for the
> binary.

> **Tip**: to find your Mac's local IP (to give to the VRHub
> client), run `ipconfig getifaddr en0` (Wi-Fi) or
> `ipconfig getifaddr en1` (Ethernet). The setup wizard also shows
> it at step 4.

### 4.8 Run in the background (launchd)

The official way on macOS to run a program as a background
service that starts at login is **launchd**. You create an
"agent" in `~/Library/LaunchAgents/`.

1. Create the file
   `~/Library/LaunchAgents/com.legerycheese.vrhub-server.plist`:

   ```bash
   mkdir -p ~/Library/LaunchAgents
   cat > ~/Library/LaunchAgents/com.legerycheese.vrhub-server.plist <<'PLIST'
   <?xml version="1.0" encoding="UTF-8"?>
   <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
   <plist version="1.0">
   <dict>
       <key>Label</key>
       <string>com.legerycheese.vrhub-server</string>
       <key>ProgramArguments</key>
       <array>
           <string>/usr/local/bin/vrhub-server</string>
           <string>-data-dir</string>
           <string>/Users/you/.vrhub-server</string>
           <string>-port</string>
           <integer>39457</integer>
       </array>
       <key>RunAtLoad</key>
       <true/>
       <key>KeepAlive</key>
       <true/>
       <key>StandardOutPath</key>
       <string>/usr/local/var/log/vrhub-server.log</string>
       <key>StandardErrorPath</key>
       <string>/usr/local/var/log/vrhub-server.err</string>
       <key>WorkingDirectory</key>
       <string>/usr/local/bin</string>
   </dict>
   </plist>
   PLIST
   ```

   > **Edit** `/Users/you/.vrhub-server` so it points to your home
   > directory (use the `whoami` command to print it). If you want
   > to store the data elsewhere (e.g. on an external disk),
   > adjust the path.

2. Create the log folder and reload launchd:

   ```bash
   sudo mkdir -p /usr/local/var/log
   sudo touch /usr/local/var/log/vrhub-server.log /usr/local/var/log/vrhub-server.err
   sudo chown $USER /usr/local/var/log/vrhub-server.log /usr/local/var/log/vrhub-server.err
   launchctl load -w ~/Library/LaunchAgents/com.legerycheese.vrhub-server.plist
   ```

3. The server starts **immediately** and will be relaunched
   automatically at every login / reboot.

> **Useful commands**:
> - `launchctl list | grep vrhub` → see the status
> - `launchctl unload ~/Library/LaunchAgents/com.legerycheese.vrhub-server.plist` → stop
> - `launchctl load -w ~/Library/LaunchAgents/com.legerycheese.vrhub-server.plist` → start
> - `tail -f /usr/local/var/log/vrhub-server.err` → follow the logs

> **Even simpler method**: if you do not need auto-start, just
> run `vrhub-server` in the Terminal and leave the window open.

### 4.9 Updating

See [section 10](#10-updating-all-platforms).

### 4.10 Uninstalling

See [section 11](#11-uninstalling-all-platforms).

---

## 5. Installing on Linux

> **Prerequisite**: any 64-bit (amd64) or 64-bit ARM (arm64) Linux
> distribution. This includes Ubuntu, Debian, Fedora, Arch,
> Manjaro, openSUSE, Mint, Pop!_OS, etc.

### 5.1 Identify your distribution

Open a terminal and run:

```bash
# Architecture
uname -m
# → x86_64 = amd64
# → aarch64 = arm64

# Distribution (and version)
cat /etc/os-release
# → PRETTY_NAME="Ubuntu 24.04 LTS" for example
```

### 5.2 Download the binary

```bash
# Move to your Downloads folder
cd ~/Downloads

# Download the latest release for your architecture
# For amd64:
curl -fL -O https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-linux-amd64.zip
# For arm64:
# curl -fL -O https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-linux-arm64.zip

# Extract
unzip vrhub-server-linux-amd64.zip
# The archive produces a binary "vrhub-server-X.Y.Z-linux-amd64"

# Rename for simplicity
mv vrhub-server-*-linux-amd64 vrhub-server
mv vrhub-server-*-linux-arm64 vrhub-server  # if you are on arm64

# Make executable
chmod +x vrhub-server
```

> **Don't have `unzip`?** Install it with your package manager:
> `sudo apt install unzip` (Debian/Ubuntu/Mint),
> `sudo dnf install unzip` (Fedora), `sudo pacman -S unzip` (Arch).

### 5.3 Make the binary executable

If not already done:

```bash
chmod +x vrhub-server
```

> **If you get `Permission denied`**: you forgot the `chmod +x`.
> Re-run it.

### 5.4 Quick test

```bash
# Prints the version (checks that the binary works)
./vrhub-server --help
```

> **`--help` parameter**: if the command lists the available
> options, the binary works. If you get `command not found`, the
> binary is not in PATH: use `./vrhub-server` instead of
> `vrhub-server`.

### 5.5 Run the server for the first time

```bash
./vrhub-server
```

> **If you want to use a different port or data directory**, see
> [section 7](#7-command-line-parameters-reference).

The server displays:

```text
First run detected — no config file found.
Starting setup wizard at /admin/setup
Listening on 0.0.0.0:39457 (mode=setup)
```

### 5.6 The setup wizard

1. On the same machine, open your browser and go to
   <http://127.0.0.1:39457/admin/setup>.
2. Follow the five steps (see
   [section 2.4](#24-first-run-the-setup-wizard)).
3. **Copy the admin API key** at the end.

### 5.7 Open the port in the firewall

If you are behind a firewall (very likely on a server, less likely
on a desktop PC), you need to open TCP port `39457`.

**ufw** (Ubuntu, Debian, Mint, Pop!_OS):

```bash
sudo ufw allow 39457/tcp
sudo ufw reload
```

**firewalld** (Fedora, RHEL, CentOS):

```bash
sudo firewall-cmd --permanent --add-port=39457/tcp
sudo firewall-cmd --reload
```

**nftables / iptables** (manual configurations):

```bash
sudo iptables -A INPUT -p tcp --dport 39457 -j ACCEPT
# (don't forget to persist the rule — see your distribution's docs)
```

> **On a personal desktop PC**: the firewall is often disabled and
> you have nothing to do. If you still want to check that the port
> is reachable from another device on the network, run
> `ip -4 addr show | grep inet` to get your local IP.

### 5.8 Run in the background with systemd (Ubuntu, Debian, Fedora, Arch…)

systemd is the service manager used by the vast majority of
modern Linux distributions.

1. **Install** the binary (optional but cleaner):

   ```bash
   sudo mv vrhub-server /usr/local/bin/
   ```

2. **Create** a systemd unit file:

   ```bash
   sudo tee /etc/systemd/system/vrhub-server.service > /dev/null <<'UNIT'
   [Unit]
   Description=vrhub-server
   After=network-online.target
   Wants=network-online.target

   [Service]
   Type=simple
   User=your_user
   ExecStart=/usr/local/bin/vrhub-server -data-dir /home/your_user/.vrhub-server -port 39457
   Restart=on-failure
   RestartSec=5s

   [Install]
   WantedBy=multi-user.target
   UNIT
   ```

   > **Edit**:
   > - `User=your_user` → your username (use `whoami` to print
   >   it);
   > - `/home/your_user/.vrhub-server` → your data directory
   >     (default is `~/.vrhub-server`).

3. **Reload** systemd and **enable** the service:

   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable --now vrhub-server
   ```

   - `enable` : start automatically at boot;
   - `--now` : start right now.

4. **Check** that everything works:

   ```bash
   sudo systemctl status vrhub-server
   ```

   You should see `active (running)` in green. To see the logs:

   ```bash
   sudo journalctl -u vrhub-server -f
   ```

   > **`-f` parameter**: follows the logs live (`Ctrl+C` to quit).

5. **Useful commands**:

   ```bash
   sudo systemctl stop vrhub-server      # stop
   sudo systemctl start vrhub-server     # start
   sudo systemctl restart vrhub-server   # restart
   sudo systemctl disable vrhub-server   # do not launch at boot anymore
   sudo systemctl status vrhub-server    # current state
   ```

> **Permissions and data directory**: if the `your_user` user
> cannot write to `/home/your_user/.vrhub-server`, create it
> first: `mkdir -p ~/.vrhub-server && chown $USER:$USER
> ~/.vrhub-server`.

> **Want to expose the server on your LAN?** The server listens by
> default on `0.0.0.0` (all interfaces), so nothing to change on
> the binary side. You still need to open the port
> ([5.7](#57-open-the-port-in-the-firewall)) and to get your local
> IP with `hostname -I` (often `192.168.x.x`).

### 5.9 Run in the background on a distribution without systemd

If you are on a more exotic distribution (Alpine, Void, some
Docker images, WSL, etc.) that does not use systemd, you can use:

- **A shell script + `nohup`** (the simplest):

  ```bash
  #!/bin/sh
  # /usr/local/bin/start-vrhub-server.sh
  nohup /usr/local/bin/vrhub-server \
    -data-dir "$HOME/.vrhub-server" \
    -port 39457 \
    > "$HOME/.vrhub-server/server.log" 2>&1 &
  echo $! > "$HOME/.vrhub-server/server.pid"
  ```

  Make it executable: `chmod +x /usr/local/bin/start-vrhub-server.sh`.
  Start: `/usr/local/bin/start-vrhub-server.sh`.
  Stop: `kill $(cat ~/.vrhub-server/server.pid)`.

  > **Script parameter details**:
  > - `nohup` : prevents the program from being killed when the
  >   terminal closes;
  > - `> … 2>&1` : redirects stdout and stderr to a log file;
  > - `&` : runs in the background;
  > - `echo $!` : saves the PID so you can stop the process
  >   cleanly later.

- **A `@reboot` cron job** (auto-start without systemd):

  ```bash
  crontab -e
  # Add this line:
  @reboot /usr/local/bin/start-vrhub-server.sh
  ```

- **A supervisor** like `runit`, `s6`, `Supervisor` (for advanced
  use: see the documentation of your chosen tool).

### 5.10 Updating

See [section 10](#10-updating-all-platforms).

### 5.11 Uninstalling

See [section 11](#11-uninstalling-all-platforms).

---

## 6. Installing on Android (via Termux)

> **Prerequisite**: a **recent** Android phone (Android 9 or
> later), with at least **200 MB of free space** (binary + Termux
> dependencies). Installation takes about 10 minutes. A Wi-Fi
> connection is recommended for the first install.

> **Why Termux?** Termux is an application that provides a **real
> Linux terminal** on Android. Since `vrhub-server` is a static
> ARM 64-bit Linux binary, it runs perfectly in Termux without
> root.

> **Important — battery and performance**: running a server on
> a phone **drains** the battery. Keep your phone plugged in for
> heavy use. Sections 6.8 to 6.10 explain how to stop Android from
> killing the server.

### 6.1 Install Termux

> **Do NOT install Termux from the Google Play Store**: the Play
> Store version is **abandoned** (no longer maintained since 2020)
> and many packages no longer work on it. Use **F-Droid** or
> **GitHub**.

1. **Recommended method — F-Droid**:
   1. Install [F-Droid](https://f-droid.org/) from their official
      site.
   2. Open F-Droid, search for *Termux* and install the version
      marked *« from F-Droid »* (currently *Termux* and
      *Termux:API* as optional).

2. **Alternative method — GitHub APK**:
   1. Download the latest APK from
      [github.com/termux/termux-app/releases](https://github.com/termux/termux-app/releases)
      (look for `app-universal-debug.apk` or
      `app-arm64-v8a-debug.apk`).
   2. Enable installation from unknown sources for your browser
      (`Settings` → `Security` → tick *Unknown sources*).
   3. Open the APK and install.

3. **Termux:API (optional but useful)**: also install
   [Termux:API](https://github.com/termux/termux-api) from F-Droid
   to be able to control the server from Android shortcuts.

### 6.2 Update Termux packages

> **Note**: `pkg` is Termux's package manager, equivalent to
> `apt` on Ubuntu or `brew` on macOS. `apt` is also available as
> an alias.

Open Termux and run:

```bash
pkg update && pkg upgrade -y
```

> **Details**:
> - `pkg update` : updates the list of available packages;
> - `pkg upgrade` : updates installed packages;
> - `-y` : auto-answer "yes" to all questions.

If Termux offers to install a new version of itself, do it and
relaunch the app.

> **If you see a message *"It seems you have legacy Termux
> installed"***: you are using the Play Store version. Uninstall
> it and install the F-Droid version as described in 6.1.

### 6.3 Allow Termux to access shared storage

For the server to read your games stored outside Termux's private
folder (for example on the SD card or in `/storage/emulated/0/`),
allow storage access:

```bash
termux-setup-storage
```

> **What this command does**: it creates a `$HOME/storage`
> symlink that points to shared storage. After running it, you
> will see a system Android popup asking for permission.

> **Tree details**:
> - `$HOME/storage/shared` → shared internal storage (often your
>   *Downloads*, *DCIM*, etc.)
> - `$HOME/storage/external` → external SD card (if present)
> - `$HOME/storage/dcim`, `$HOME/storage/music`, etc. → standard
>   subfolders

### 6.4 Download the binary

The binary for Android is the same as for ARM 64-bit Linux
(`vrhub-server-linux-arm64.zip`).

```bash
# Make sure you are in your home folder
cd ~

# Install curl and unzip if needed
pkg install -y curl unzip

# Download the latest version
curl -fL -O https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest/download/vrhub-server-linux-arm64.zip

# Extract
unzip vrhub-server-linux-arm64.zip

# Rename
mv vrhub-server-*-linux-arm64 vrhub-server

# Make executable
chmod +x vrhub-server

# Check
./vrhub-server --help
```

> **You see `./vrhub-server: cannot execute: required file not
> found`**: Termux cannot find the required system libraries.
> This happens on old Android (Android 7 or earlier). Update
> Android, or install `pkg install proot` then run with
> `proot-link2 sh -c './vrhub-server'`.

### 6.5 Quick test

```bash
./vrhub-server --help
```

> **`--help` parameter**: if the command lists the available
> options, the binary works. Otherwise, check the result of
> `uname -m`: it should print `aarch64`. If you see `armv7l` or
> `armv8l`, your phone is 32-bit — the 64-bit ARM binary will
> not work (but all Android phones sold since 2018 are 64-bit).

### 6.6 Run the server for the first time

```bash
./vrhub-server
```

> **If you want to use a different port or data directory**, see
> [section 7](#7-command-line-parameters-reference).

The server displays:

```text
First run detected — no config file found.
Starting setup wizard at /admin/setup
Listening on 0.0.0.0:39457 (mode=setup)
```

> **Where is my data directory?** By default
> `/data/data/com.termux/files/home/.vrhub-server`, which is
> **not** visible from another application (it is Termux's
> private folder). If you want it reachable from the Quest, the
> best is to scan a shared folder (see 6.11).

### 6.7 The setup wizard

Open your phone's web browser (Chrome, Firefox…) and go to
<http://127.0.0.1:39457/admin/setup>.

> **Want to open the admin UI from another device (PC, tablet) on
> the same Wi-Fi?** Replace `127.0.0.1` with your phone's local
> IP (findable with `ip -4 addr show wlan0 | grep inet` in
> Termux — usually `192.168.x.x`).

Follow the five steps (see
[section 2.4](#24-first-run-the-setup-wizard)).
At the end, **copy the admin API key**.

### 6.8 Stop Android from killing the server in the background

Android is **aggressive** at saving battery: it regularly kills
background apps, including Termux. To prevent this:

1. **Wake-lock**: stops the phone from sleeping while Termux runs.

   ```bash
   termux-wake-lock
   ```

   > **Details**: `termux-wake-lock` sends a *wake lock* to the
   > system. As long as it is active, Android will not put the
   > CPU into deep sleep. To remove it, run `termux-wake-unlock`.

2. **Persistent notification** (very important): since Android 8,
   a background app **must show a visible notification** to keep
   running. Termux already does so when you launch the server
   with `termux-services` (6.10) or with certain scripts.
   Otherwise, simply keep the Termux session open.

3. **Disable battery optimisation for Termux**:
   Android `Settings` → `Apps` → `Termux` → `Battery` →
   `Unrestricted` (or *« Don't optimise »*).

4. **Disable data saver for Termux** (on Android versions that
   have it): *Settings* → *Network & Internet* → *Data saver* →
   disable for Termux.

5. **Lock Termux in the recents**: on the recent apps carousel,
   long-press on Termux → *Lock*.

6. **Manufacturer-specific**: on Samsung/Xiaomi/Huawei phones
   there is often a "Battery Guardian" or "Phone Manager" that
   *automatically* kills unlisted apps. Add Termux to the
   whitelist.

> **Without wake-lock + persistent notification, the server will
> die after a few minutes** when Android moves Termux to the
> background. Methods 6.9 and 6.10 fix this properly.

### 6.9 Run in the background (simple method)

The simplest method: use `nohup` and keep Termux alive.

```bash
nohup ./vrhub-server \
  -data-dir "$HOME/.vrhub-server" \
  -port 39457 \
  > "$HOME/.vrhub-server/server.log" 2>&1 &
```

> **Parameter details**:
> - `nohup` : ignores the SIGHUP signal (prevents the program
>   from being killed when the shell exits);
> - `> … 2>&1` : redirects the logs to a file;
> - `&` : runs in the background (the terminal stays usable).

Then:

```bash
# Prevent the phone from sleeping
termux-wake-lock
```

> **To stop the server**:
>
> ```bash
> pkill -f vrhub-server
> ```
>
> **To see the logs live**:
>
> ```bash
> tail -f ~/.vrhub-server/server.log
> ```

> **Downside**: you have to re-run the command on every phone
> reboot. For auto-start, see 6.10.

### 6.10 Run in the background with `termux-services`

`termux-services` is a Termux package that lets you manage a
program through `sv` (*runit* commands) — the systemd equivalent
for Termux. The program starts **automatically** at boot (wake)
and is monitored.

1. **Install** the required packages:

   ```bash
   pkg install -y termux-services
   ```

2. **Enable** the service system:

   ```bash
   sv-enable $(echo $PATH | tr ':' '\n' | grep -v '/.')
   ```

   > **Note**: Termux displays the available services. Pick
   > `vrhub-server` (or leave empty to enable all).

3. **Create the service** in
   `$PREFIX/var/service/vrhub-server/run`:

   ```bash
   mkdir -p $PREFIX/var/service/vrhub-server
   cat > $PREFIX/var/service/vrhub-server/run <<'RUN'
   #!/data/data/com.termux/files/usr/bin/sh
   exec /data/data/com.termux/files/home/vrhub-server \
     -data-dir /data/data/com.termux/files/home/.vrhub-server \
     -port 39457 \
     > /data/data/com.termux/files/home/.vrhub-server/server.log 2>&1
   RUN
   chmod +x $PREFIX/var/service/vrhub-server/run
   ```

   > **Adjust the path**: if you installed `vrhub-server`
   > somewhere other than `$HOME/`, modify the first line.

4. **Enable the service**:

   ```bash
   sv-enable vrhub-server
   ```

5. **Useful commands**:

   ```bash
   sv status vrhub-server      # state (run, down, fail…)
   sv restart vrhub-server     # restart
   sv stop vrhub-server        # stop
   sv start vrhub-server       # start
   sv up vrhub-server          # enable auto-start
   sv down vrhub-server        # disable auto-start
   ```

> **Logs**: `tail -f
> /data/data/com.termux/files/home/.vrhub-server/server.log`

### 6.11 Make the server reachable from the Quest on the same Wi-Fi

The server listens by default on `0.0.0.0` (all interfaces), so it
is **already** reachable from the Wi-Fi network. The Quest just
needs to use your phone's local IP as the server address.

To get your phone's local IP in Termux:

```bash
ip -4 addr show wlan0 | grep -oP 'inet \K[\d.]+'
```

> **If the command returns nothing**: your phone is not connected
> to Wi-Fi, or the interface is named differently. Try
> `ip -4 addr show` to see all interfaces.

To get the **hostname** usable from the Quest (if you don't want
to remember the IP):

```bash
hostname
```

Write down the IP (for example `192.168.1.42`). In the VRHub
client, enter:

- **baseUri**: `http://192.168.1.42:39457/`
- **password**: the archive password you chose in the wizard

> **Firewall**: Android has no user-facing firewall enabled by
> default, so you have nothing to open. If you installed
> NetGuard, AFWall+ or another, allow Termux.

> **Wi-Fi power saving**: on some phones, Wi-Fi enters low-power
> mode when the screen turns off, causing disconnects. To
> disable: `Settings` → `Wi-Fi` → menu ⋮ → *Advanced* → *Wi-Fi
> during sleep* → *Never*.

### 6.12 Updating

See [section 10](#10-updating-all-platforms).

### 6.13 Uninstalling

See [section 11](#11-uninstalling-all-platforms).

---

## 7. Command-line parameters reference

The binary accepts two parameters (and only two). Both are
**optional** — without parameters, the server uses the default
values (see [section 2.2](#22-the-data-directory) and
[section 2.3](#23-the-network-port)).

```text
vrhub-server [-data-dir <path>] [-port <number>]
```

### 7.1 `-data-dir`

> **What it does**: tells the server **where to store** its
> configuration file, database, cache and backups (instead of the
> default path).

**Syntax**:

```bash
vrhub-server -data-dir /path/to/folder
```

| Platform   | Example                                       |
|------------|-----------------------------------------------|
| Windows    | `vrhub-server.exe -data-dir "D:\vrhub-data"`  |
| macOS      | `vrhub-server -data-dir /Volumes/External/vrhub` |
| Linux      | `vrhub-server -data-dir /mnt/nas/vrhub`       |
| Android    | `vrhub-server -data-dir /sdcard/vrhub`        |

> **Windows — quotes**: if the path contains spaces
> (`C:\My Documents\vrhub`), **wrap it in double quotes**:
> `-data-dir "C:\My Documents\vrhub"`. Otherwise the server will
> only see the first word.

> **Android — access rights**: to use a folder under `/sdcard/`,
> you must first run `termux-setup-storage` (see 6.3) **and** grant
> the READ permission to Termux in Android settings.

> **Why change the default folder?** To store the data on a
> bigger disk, on a NAS, on an SD card, or simply to separate it
> from your documents.

### 7.2 `-port`

> **What it does**: changes the TCP port on which the server
> listens. Useful if port `39457` is already in use, or if you
> want to pick a more memorable one.

**Syntax**:

```bash
vrhub-server -port 49500
```

> **Valid port range**: 1 to 65535. Avoid ports < 1024 (reserved
> for system services, require root/admin) and ports already used
> by other common programs:
> - 80 (HTTP)
> - 443 (HTTPS)
> - 22 (SSH)
> - 3306 (MySQL)
> - 5432 (PostgreSQL)
> - 8080, 8000 (web development)

> **Don't forget**: if you change the port, you also need to
> change the URL in your browser
> (`http://127.0.0.1:49500/admin/setup`) and the one entered in
> the VRHub client.

### 7.3 Useful combinations

| Use case                                | Command                                                                |
|-----------------------------------------|------------------------------------------------------------------------|
| Default values                          | `vrhub-server`                                                         |
| Change the port                         | `vrhub-server -port 49500`                                             |
| Custom data directory                   | `vrhub-server -data-dir /my/folder`                                    |
| Custom port + data directory            | `vrhub-server -port 49500 -data-dir /my/folder`                        |
| Load a specific game folder             | configured in the wizard (`/admin/configuration` afterwards)           |
| Test another instance (different port)  | `vrhub-server -port 39500 -data-dir /tmp/vrhub-test`                   |

---

## 8. Data directory layout

After the first launch, the data directory contains:

```
{data-dir}/
├── config.toml              # TOML configuration (created by the wizard)
├── vrhub.db                 # SQLite database
├── games/                   # Legacy installations only
│   └── {hash}/
│       └── {packageName}/
│           ├── *.apk
│           └── *.obb
├── metadata/                # MetaMetadata cache
├── backups/                 # Automatic and manual backups
└── .updating/               # Temporary directory during updates
```

> **Note**: since version 4.x, APK/OBB files are **no longer
> copied** into the data directory: the server only stores their
> original path. You can therefore keep your games anywhere on
> your machine.

> **Backups**: to restore a backup, go to the admin UI →
> *Backup/Restore* → pick the `.zip` file you want.

---

## 9. Post-install verification

Once the wizard is done and the server is in normal mode, run
these commands to check that everything works (on the same
machine as the server):

```bash
# 1. The server responds
curl -sI http://127.0.0.1:39457/
# → should print "200 OK"

# 2. The client config is served
curl -s http://127.0.0.1:39457/config.json
# → should print JSON with "baseUri" and "password"

# 3. The admin API responds (replace YOUR_KEY with the displayed key)
curl -sI http://127.0.0.1:39457/admin/api/stats -H "X-API-Key: YOUR_KEY"
# → should print "200 OK"
```

**On Windows PowerShell**:

```powershell
curl.exe -sI http://127.0.0.1:39457/
curl.exe -s http://127.0.0.1:39457/config.json
curl.exe -sI http://127.0.0.1:39457/admin/api/stats -H "X-API-Key: YOUR_KEY"
```

> **You get "Connection refused"**: the server is not running, or
> it listens on another port/interface. Check the `Listening on …`
> line in the startup console.

> **You get a "timeout"**: the firewall is blocking the
> connection. See the OS-specific sections (3.8, 4.7, 5.7, 6.11).

> **Want to test from the Quest (or another device)?** Replace
> `127.0.0.1` with the server's local IP (see the OS-specific
> sections to find it).

---

## 10. Updating (all platforms)

Three methods, from the simplest to the most manual:

### 10.1 Update through the admin UI (recommended)

1. Log into the admin UI (`http://127.0.0.1:39457/admin`).
2. Go to *Configuration* → *Updates*.
3. If an update is available, click **Apply**.
4. The admin UI tells you when the server has restarted on the
   new version.

> **Prerequisite**: `[update].enabled = true` in `config.toml`
> (this is the default). The server checks for new versions every
> 6 hours by default.

### 10.2 Update from the command line

```bash
# Windows (PowerShell)
curl.exe -fL -X POST http://127.0.0.1:39457/admin/api/update/apply -H "X-API-Key: YOUR_KEY"

# macOS / Linux / Android
curl -fL -X POST http://127.0.0.1:39457/admin/api/update/apply -H "X-API-Key: YOUR_KEY"
```

### 10.3 Manual update (if the auto-update does not work)

1. Download the latest version from
   [GitHub Releases](https://github.com/LeGeRyChEeSe/vrhub-server/releases/latest).
2. **Stop** the server (Ctrl+C in the console, or
   `systemctl stop vrhub-server` / `launchctl unload …` /
   `sv stop vrhub-server` / `pkill -f vrhub-server`).
3. **Replace** the old binary with the new one (at the same
   location).
4. **Restart** the server (your usual command or restart the
   service).

> **The database and configuration are not lost**: they live in
> the data directory, not in the binary.

---

## 11. Uninstalling (all platforms)

Three steps, common to all platforms:

1. **Stop** the server.
2. **Delete the binary** and everything attached to it (systemd
   units, launchd agents, Termux services, scheduled tasks, .bat
   scripts, etc.).
3. **Delete the data directory** if you do not want to keep the
   database, configuration, cache and backups.

> **The server stores nothing else** than what is in the data
> directory. Once the binary and that folder are gone, no trace
> remains.

### 11.1 Windows

```powershell
# 1. Stop the scheduled task
schtasks /End /TN "vrhub-server"
schtasks /Delete /TN "vrhub-server" /F

# 2. Delete the binary
Remove-Item -Recurse -Force C:\vrhub-server

# 3. Delete the data directory
Remove-Item -Recurse -Force "$env:APPDATA\vrhub-server"

# 4. (Optional) Remove the firewall rule
Remove-NetFirewallRule -DisplayName "vrhub-server"
```

### 11.2 macOS

```bash
# 1. Stop and disable the launchd agent
launchctl unload ~/Library/LaunchAgents/com.legerycheese.vrhub-server.plist
rm -f ~/Library/LaunchAgents/com.legerycheese.vrhub-server.plist

# 2. Delete the binary
sudo rm -f /usr/local/bin/vrhub-server

# 3. Delete the data directory
rm -rf ~/.vrhub-server

# 4. (Optional) Remove the firewall rule
sudo /usr/libexec/ApplicationFirewall/socketfilterfw \
  --remove /usr/local/bin/vrhub-server 2>/dev/null

# 5. (Optional) Delete the logs
sudo rm -f /usr/local/var/log/vrhub-server.log /usr/local/var/log/vrhub-server.err
```

### 11.3 Linux

```bash
# 1. Stop and disable the systemd service
sudo systemctl disable --now vrhub-server
sudo rm -f /etc/systemd/system/vrhub-server.service
sudo systemctl daemon-reload

# 2. Delete the binary
sudo rm -f /usr/local/bin/vrhub-server

# 3. Delete the data directory
rm -rf ~/.vrhub-server

# 4. (Optional) Remove the firewall rule
sudo ufw delete allow 39457/tcp 2>/dev/null
```

### 11.4 Android (Termux)

```bash
# 1. Stop the service
sv down vrhub-server
sv stop vrhub-server
rm -rf $PREFIX/var/service/vrhub-server

# 2. Kill any remaining process
pkill -f vrhub-server

# 3. Release the wake-lock
termux-wake-unlock

# 4. Delete the binary
rm -f ~/vrhub-server ~/vrhub-server-*-linux-arm64 ~/vrhub-server-linux-arm64.zip

# 5. Delete the data directory
rm -rf ~/.vrhub-server

# 6. (Optional) Uninstall Termux
# → Uninstall Termux via Android settings
```

---

## 12. Frequently Asked Questions

### 12.1 The server does not start and shows "EADDRINUSE"

**Cause**: another program uses port `39457` (or the port you
chose).

**Fix**:

1. **Change the port**: restart with `-port 49500` (or any other
   free port).
2. **Identify the program that holds the port**:

   ```bash
   # Windows PowerShell
   Get-NetTCPConnection -LocalPort 39457 | Select-Object OwningProcess

   # macOS / Linux
   lsof -i :39457
   # or
   ss -tlnp | grep 39457
   ```

3. **Stop the program** that holds the port, or change the port.

### 12.2 The wizard is not reachable from the browser

**Check**:

1. You typed `http://` (not `https://`).
2. The port is correct (default `39457`).
3. The server's console displays
   `Listening on 0.0.0.0:39457 (mode=setup)`. If it displays
   `127.0.0.1:39457`, the server only listens on the local
   interface; change `[server].host` to `0.0.0.0` in
   `config.toml` or launch with `-data-dir …` then edit the
   config.

### 12.3 The Quest cannot see the server on Wi-Fi

**Check**:

1. The phone (or PC) running the server is **on the same Wi-Fi
   network** as the Quest.
2. The local IP entered in the VRHub client is correct:
   - **Windows**: `ipconfig` (look for *Default Gateway* and
     *IPv4 Address*)
   - **macOS**: `ipconfig getifaddr en0`
   - **Linux**: `hostname -I` or `ip -4 addr show`
   - **Android**: `ip -4 addr show wlan0`
3. The port is open in the firewall (see sections 3.8, 4.7, 5.7).
4. You entered `http://` (not `https://`).
5. If you use a *guest Wi-Fi* or *AP isolation*, the Quest won't
   see the server. Connect to the main Wi-Fi.

### 12.4 The server stops by itself (Android)

See [section 6.8](#68-stop-android-from-killing-the-server-in-the-background).
In short:

1. `termux-wake-lock` in the Termux session.
2. Persistent notification displayed (keep Termux in the
   foreground or use `termux-services`).
3. Disable battery optimisation for Termux in Android settings.
4. Add Termux to the whitelist of the manufacturer's "Battery
   Guardian" (Samsung, Xiaomi, Huawei…).

### 12.5 The wizard asks to reuse an existing configuration

If you reinstall the server **on the same machine** with a data
directory that already contains a `config.toml`, the wizard will
not appear: the server starts directly in normal mode with the
existing configuration.

To **start over from scratch**:

1. Stop the server.
2. Back up (or delete) the data directory.
3. Restart the server: the wizard will appear again.

### 12.6 I forgot my admin API key

1. Log into the admin UI with your **username and password** (not
   the API key).
2. Go to *Configuration* → *API Key* → *Regenerate*.
3. A new key is generated and shown **once**: copy it
   immediately.

### 12.7 I forgot my admin password

There is **no** automatic recovery procedure. You must:

1. Stop the server.
2. Edit `{data-dir}/config.toml`.
3. Replace `[admin].password_hash` with a new value (use the
   `cmd/test-config` script or an online bcrypt tool — but
   beware, the hash must be in the right bcrypt format).
4. Restart the server.

> **Tip**: better make a **backup** of your `config.toml` (and
> of the API key) in a password manager, right from the first
> install.

### 12.8 The download is blocked by the antivirus (Windows)

**Rare cause**: some heuristic antivirus engines mark freshly
downloaded Go binaries as suspicious.

**Fix**:

1. Verify the SHA-256 hash of the archive (see
   [section 3.3](#33-verify-the-download)) to confirm it is
   intact.
2. Add the install folder to your antivirus whitelist.

### 12.9 I want to use the server from outside home (Cloudflare tunnel)

The `vrhub-group` project uses a Cloudflare tunnel. Refer to the
specific documentation of that tunnel to set up remote access.

> **Security note**: exposing a personal server on the Internet
> adds risk. Use a strong admin password, do not share your API
> key, and keep the server up to date.

### 12.10 The server crashes on startup with "no such file or directory"

**Cause**: the `-data-dir` path points to a folder that does not
exist and the server has no rights to create it.

**Fix**:

1. Create the folder manually:
   - Windows: `mkdir D:\vrhub-data`
   - macOS/Linux: `mkdir -p ~/.vrhub-server`
   - Android: `mkdir -p ~/storage/shared/vrhub` (after
     `termux-setup-storage`)
2. Restart the server.

---

> For any question not covered by this guide, open an *issue* on
> [github.com/LeGeRyChEeSe/vrhub-server/issues](https://github.com/LeGeRyChEeSe/vrhub-server/issues).
> Feel free to write in any language you are comfortable with.
