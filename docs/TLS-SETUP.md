# TLS/HTTPS Setup Guide

## Overview

vrhub-server supports TLS/HTTPS via operator-provided certificate and key files. When both `tls_cert` and `tls_key` are set in the TOML config (or via the admin settings UI), the server starts with `http.ListenAndServeTLS` instead of the plain `ListenAndServe`.

## Configuration

### TOML Config (`config.toml`)

```toml
[server]
host = "0.0.0.0"
port = 39457
tls_cert = "C:/path/to/fullchain.pem"
tls_key  = "C:/path/to/privkey.pem"
```

Both fields must be set together — providing only one will cause a startup error.

### Admin Settings UI

The TLS fields appear in the server settings section of the admin dashboard. Changing them triggers a live rebind (graceful shutdown + restart) with the new TLS configuration.

## Generating Self-Signed Certificates (Development)

### OpenSSL

```bash
# Generate a self-signed certificate valid for localhost and 127.0.0.1
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -keyout privkey.pem -out cert.pem -days 365 -nodes \
  -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
```

This produces:
- `privkey.pem` — private key (set as `tls_key`)
- `cert.pem` — certificate (set as `tls_cert`)

### Windows PowerShell (OpenSSL available)

```powershell
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 `
  -keyout privkey.pem -out cert.pem -days 365 -nodes `
  -subj "/CN=localhost" `
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
```

## Production: Let's Encrypt (Certbot)

### Automatic renewal

```bash
# Install certbot (Linux example)
sudo apt install certbot

# Obtain certificate for your domain
sudo certbot certonly --standalone -d vrhub.yourdomain.com

# Certificate files are placed at:
#   /etc/letsencrypt/live/vrhub.yourdomain.com/fullchain.pem
#   /etc/letsencrypt/live/vrhub.yourdomain.com/privkey.pem
```

### Config for Let's Encrypt

```toml
[server]
host = "0.0.0.0"
port = 39457
tls_cert = "/etc/letsencrypt/live/vrhub.yourdomain.com/fullchain.pem"
tls_key  = "/etc/letsencrypt/live/vrhub.yourdomain.com/privkey.pem"
```

### Renewal hook

Add a cron job or systemd timer to reload the server after certbot renews:

```bash
# /etc/cron.d/certbot-reload
0 3 * * 1 root certbot renew --quiet && systemctl reload vrhub-server
```

Or use the `--deploy-hook`:

```bash
certbot renew --deploy-hook "systemctl reload vrhub-server"
```

## Security Considerations

### Key Protection

- The private key file (`tls_key`) should be readable only by the user running vrhub-server:
  ```bash
  chmod 600 privkey.pem
  ```
- On Windows, restrict the file ACL to the service account.

### Certificate Validity

- Self-signed certificates expire and must be regenerated.
- Let's Encrypt certificates expire every 90 days — automate renewal.
- The server logs the certificate subject and issuer at startup for operator visibility.

### Network Exposure

- TLS does not replace the need for a firewall rule. The built-in firewall helper (Windows) or your firewall configuration should still restrict access to the server port.
- If the server is behind a reverse proxy (nginx, Caddy, Cloudflare Tunnel), TLS termination can happen at the proxy and the server can continue using plain HTTP internally.

### Cipher Suites

The server uses Go's default TLS configuration, which provides strong cipher suites:
- TLS 1.2+ only (TLS 1.0/1.1 are disabled)
- AEAD ciphers (AES-GCM, ChaCha20-Poly1305)
- Forward secrecy via ECDHE key exchange

No additional TLS configuration is required for a secure setup.

## Troubleshooting

### "tls: failed to find certificate/PEM data"

The certificate file does not contain valid PEM-encoded X.509 certificate data. Verify:
```bash
openssl x509 -in cert.pem -noout -text
```

### "tls: failed to parse private key"

The key file is malformed or encrypted (password-protected). vrhub-server requires an unencrypted key. Convert if needed:
```bash
openssl rsa -in encrypted-key.pem -out privkey.pem
```

### "no such file or directory"

The file path in `tls_cert` or `tls_key` is incorrect or the file does not exist. Verify:
```bash
ls -la /path/to/cert.pem /path/to/privkey.pem
```

### Live rebind fails with TLS

If the new TLS configuration is invalid, the rebind will fail and the server continues running with the old configuration. Check the logs for the specific error (certificate parse failure, key mismatch, etc.).

## Logging

At startup, the server logs the certificate details:

```
{"level":"info","cert_subject":"localhost","cert_issuer":"localhost","cert_expiry":"2027-09-12T08:57:21+02:00","event":"tls_enabled","message":"TLS enabled"}
```

This helps operators verify the correct certificate is loaded.
