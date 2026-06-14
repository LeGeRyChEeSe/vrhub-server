# Security Policy

## Supported Versions

| Version | Supported          |
|---------|--------------------|
| 0.1.x   | Active development  |
| < 0.1.0 | End of life        |

Only the latest minor release line receives security fixes. Older lines
are not patched; please upgrade.

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Send a private report to the maintainer via one of these channels:

- **Email:** open a security advisory draft on
  [the security tab](https://github.com/LeGeRyChEeSe/vrhub-server/security/advisories/new)
  (GitHub will keep it private until we publish a fix).
- **Direct contact:** reach out to the maintainer through the contact
  details on the GitHub profile page.

When filing the report, please include:

1. A clear description of the vulnerability and its impact.
2. Reproduction steps, ideally with a minimal `curl` or HTTP transcript.
3. The exact version affected (`vrhub-server --version` or the
   `Server-Version` response header on `/`).
4. Your assessment of the severity (informational / low / medium / high /
   critical).

We will acknowledge receipt within **5 business days** and aim to ship a
fix or mitigation within **30 days** for high and critical issues. The
disclosure timeline is coordinated with the reporter; we credit them in
the release notes unless they prefer to stay anonymous.

## Hardening Notes for Operators

- The admin Web UI sends session cookies with the `Secure` flag; deploy
  behind HTTPS in production. See
  [`docs/CLIENT_INTEGRATION.md`](docs/CLIENT_INTEGRATION.md) for the
  recommended reverse-proxy setup.
- The admin password and archive password are stored in cleartext in
  `config.toml` because the VRHub client needs the original archive
  password to decrypt `meta.7z`. Restrict the file system permissions
  on the data directory (`chmod 700` on Unix; restrict ACLs on
  Windows). See [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) for the
  full list of secrets.
- The admin API key is stored as a SHA-256 hash in `config.toml`. The
  plaintext is held in process memory only.
- The Windows build (`build.cmd`) emits a sidecar `.manifest` requesting
  `requireAdministrator` so the embedded firewall helper can call
  `netsh advfirewall`. This is the expected behaviour; do not strip the
  manifest.

## Scope

In scope:

- Authentication, session management, and password handling
- File serving path traversal, range handling, MIME sniffing
- Archive generation (7z / AES-256) and the archive password lifecycle
- API key generation, hashing, storage, and revocation
- Server-side request forgery in outbound metadata / update calls
- Dependency vulnerabilities (we accept reports even when a patch is
  upstream only)

Out of scope:

- Vulnerabilities in third-party VR game installers managed by the
  operator
- Issues that require the operator's workstation to already be
  compromised
- Theoretical denial-of-service against an unauthenticated
  LAN-discoverable instance (run the server behind a firewall; see
  the warning in [`README.md`](README.md))
