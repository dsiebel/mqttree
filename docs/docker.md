# Running mqttree in Docker

mqttree ships as a single static binary in a small Alpine image and keeps all
state in `/data`: the SQLite database and the key file that seals broker
passwords. The image runs as the unprivileged user `mqttree` (UID/GID `10001`).

## Quick start with Compose

The repository contains a `docker-compose.yaml` that starts mqttree together
with a throwaway Mosquitto broker for local testing:

```sh
docker compose up -d --build
```

Then:

1. Open <http://127.0.0.1:8080>.
2. Open **Settings → Connections → Add connection**.
3. Use `mosquitto` as the host and `1883` as the port, keep `#` as the topic
   filter and enable the connection.
4. Publish a message and watch it appear in the tree:

```sh
docker compose exec mosquitto mosquitto_pub -t demo/hello -m '{"hello":"world"}'
```

The bundled broker accepts anonymous clients and is published on
`127.0.0.1:1883` only. It is meant for local testing; remove it and point
mqttree at your real broker for anything else.

`docker compose down` stops the stack and keeps the database in the
`mqttree_mqttree-data` volume. `docker compose down -v` also deletes it.

## Run the image without Compose

```sh
docker build -t mqttree:1.0.0 .
docker run -d --name mqttree \
  -p 127.0.0.1:8080:8080 \
  -v mqttree-data:/data \
  mqttree:1.0.0
```

Open <http://127.0.0.1:8080> and add a broker connection. To use a bind mount
instead of a named volume, make the host directory writable by UID `10001`.

Build args `VERSION`, `COMMIT` and `DATE` override the version reported by
`mqttree -version`. For a multi-arch image:

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t mqttree:1.0.0 .
```

## Published images

Every push and pull request builds the image and smoke-tests it without pushing
anything. Release tags publish the image to the Forgejo container registry and
to the GitHub container registry under the tag name, for `linux/amd64` and
`linux/arm64`:

```sh
docker pull forgejo.home.dsiebel.de/oss/mqttree:v1.0.0
docker pull ghcr.io/dsiebel/mqttree:v1.0.0
```

The GitHub image is a registry-to-registry copy of the tested Forgejo image, so
both tags refer to the same manifest.

The build steps use `woodpeckerci/plugin-docker-buildx:6.1.2`, which runs a
nested Docker daemon and therefore needs privileged mode. Allow that plugin on
the Woodpecker server instead of trusting the repository:

```
WOODPECKER_PLUGINS_PRIVILEGED=woodpeckerci/plugin-docker-buildx
```

Without a tag the entry accepts every plugin version, so Renovate can bump the
image without a matching server change. Add a tag to the entry to require an
exact version instead.

The publish step authenticates to the registry with the existing `forgejo_token`
secret, which needs the `write:package` scope. No username secret is needed:
Forgejo resolves the token owner from the password field of the basic-auth
credentials and ignores the supplied username, so the workflow passes
`${CI_REPO_OWNER}` to satisfy the plugin's non-empty username check.

## Configuration

The image sets container-appropriate defaults. Override them with `-e` or the
Compose `environment` section.

| Variable | Image default | Purpose |
| --- | --- | --- |
| `MQTTREE_LISTEN` | `:8080` | HTTP listen address. |
| `MQTTREE_DATA_DIR` | `/data` | SQLite database directory. |
| `MQTTREE_SECRET_KEY_FILE` | `/data/secret.key` | Key sealing broker passwords, generated on first start. |
| `MQTTREE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |

The default listen address inside the image is `:8080` so the port can be
published; the container port should still be bound to loopback on the host
(`-p 127.0.0.1:8080:8080`), because mqttree has no authentication of its own.

## Data and backups

Everything persistent lives in `/data`. Back up the database **and** the key
file together — without the key, stored broker passwords cannot be decrypted:

```sh
docker run --rm -v mqttree-data:/data -v "$PWD:/backup" alpine \
  tar czf /backup/mqttree-backup.tar.gz -C /data .
```

Restore by extracting the archive back into the volume before starting the
container.

## Health check

The image defines a `HEALTHCHECK` against `GET /healthz`. `docker ps` shows
`healthy` once the HTTP server is up, and Compose can gate other services on it.
