# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

First release.

### Added

- Topic tree over MQTT v5: subscribes to a filter and renders every topic as a
  collapsible tree with last value, retained marker and activity highlighting.
- Per-topic message history with paging, JSON pretty-printing and syntax
  highlighting, base64 rendering for binary payloads and copy buttons.
- Message history collapses by default and loads ten entries per page; live
  payload updates are diffed line by line with a red/green highlight that fades
  out.
- Bottom status bar with stored topic and message counts plus database size; the
  topic filter moved into the sidebar header.
- Retention: MQTT v5 message-expiry interval first, then a configurable
  retention window, then a database size cap that prunes oldest messages.
- Saved broker connections with TLS and custom CA support; passwords sealed with
  XChaCha20-Poly1305 using a key file separate from the database.
- Publish to any topic with QoS and retain.
- Settings UI for retention and connections, including clearing stored data for
  a connection.
- Dark and light themes, resizable tree pane, compact mobile layout.
- Single static binary, embedded UI, SQLite storage, `/healthz` endpoint.
- Multi-stage Docker image running as an unprivileged user, plus a Compose
  stack with a local Mosquitto broker and Docker getting-started documentation.
- CI builds and smoke-tests the Docker image on every push and pull request, and
  publishes release tags to the Forgejo container registry.
- Renovate workflow and configuration, covering the CI images, Dockerfile,
  Compose file, Go modules and E2E dependencies.

### Changed

- Pipeline configuration moved from `.woodpecker.yml` into `.woodpecker/` so a
  separate scheduled Renovate workflow can live next to it.
- Toolchain refresh: Go 1.27.3, Alpine 3.24.4, current Go module
  dependencies, Playwright 1.65, Goreleaser 2.20.1 and Mosquitto 2.1.2.

### Security

- Loopback listen address by default, with a startup warning when bound to a
  non-loopback address, because the API has no authentication and is meant to
  sit behind an authenticating reverse proxy.
