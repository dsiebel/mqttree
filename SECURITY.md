# Security policy

## Supported versions

Security fixes land on the latest release line. Older releases are not patched.

## Reporting a vulnerability

Report privately to **code@dsiebel.de**. Include the version (`mqttree -version`),
a description and, if possible, a reproduction. Please do not open a public
issue before a fix is available.

## Threat model and expectations

mqttree is a single-operator tool that assumes a trusted network segment and an
authenticating reverse proxy in front of it.

- **No authentication or authorization of its own.** Anyone who can reach the
  HTTP port can read stored messages, change connections and publish to the
  configured brokers. Bind it to loopback (the default) or firewall it to the
  proxy.
- **Broker credentials** are encrypted at rest with XChaCha20-Poly1305. The key
  lives in a separate file (`MQTTREE_SECRET_KEY_FILE`, mode 0600 or 0640). Losing
  the key only means re-entering the passwords.
- **Stored payloads are not encrypted.** The SQLite database contains raw
  broker traffic, which may include personal data. Protect the data directory
  accordingly and use retention limits.
- **TLS** verification is on by default when a connection uses TLS. The
  "skip TLS verification" option is an explicit operator decision for
  self-signed brokers.
- The service makes no outbound connections other than to the configured
  brokers and serves no third-party assets.

## Hardening checklist

1. Keep `MQTTREE_LISTEN` on `127.0.0.1` and proxy through an authenticating
   ingress, or restrict the port at the firewall.
2. Run as an unprivileged user with a read-only filesystem except the data
   directory.
3. Back up (or deliberately discard) the data directory and the key file
   together.
