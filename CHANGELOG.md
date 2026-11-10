# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] - 2026-11-10

First release.

### Added

- Topic tree over MQTT v5: subscribes to a filter and renders every topic as a
  collapsible tree with last value, retained marker and activity highlighting.
- Per-topic message history with paging, JSON pretty-printing and syntax
  highlighting, base64 rendering for binary payloads and copy buttons.
- Retention: MQTT v5 message-expiry interval first, then a configurable
  retention window, then a database size cap that prunes oldest messages.
- Saved broker connections with TLS and custom CA support; passwords sealed with
  XChaCha20-Poly1305 using a key file separate from the database.
- Publish to any topic with QoS and retain.
- Settings UI for retention and connections, including clearing stored data for
  a connection.
- Dark and light themes, resizable tree pane, compact mobile layout.
- Single static binary, embedded UI, SQLite storage, `/healthz` endpoint.

### Security

- Loopback listen address by default, with a startup warning when bound to a
  non-loopback address, because the API has no authentication and is meant to
  sit behind an authenticating reverse proxy.
