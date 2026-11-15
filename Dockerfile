# syntax=docker/dockerfile:1.7

# ---- build ------------------------------------------------------------------
FROM golang:1.27.4-alpine3.24 AS build

WORKDIR /src

# Dependencies are the slow, rarely changing layer: copy only the module files
# first so `go mod download` is reused until go.mod/go.sum actually change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

# CGO is off so the binary stays static (modernc.org/sqlite is pure Go) and runs
# on the minimal runtime image. Build caches are mounted, not copied, so they
# never end up in a layer.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    if [ -n "${TARGETOS}" ]; then export GOOS="${TARGETOS}"; fi; \
    if [ -n "${TARGETARCH}" ]; then export GOARCH="${TARGETARCH}"; fi; \
    CGO_ENABLED=0 go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
        -o /out/mqttree .

# ---- runtime ----------------------------------------------------------------
FROM alpine:3.24.4

ARG VERSION=dev
ARG COMMIT=none

LABEL org.opencontainers.image.source="https://github.com/dsiebel/mqttree" \
      org.opencontainers.image.url="https://github.com/dsiebel/mqttree" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="Apache-2.0"

# ca-certificates for broker TLS, tzdata for timestamp rendering. The fixed UID
# keeps bind-mounted volume permissions predictable.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 mqttree \
    && adduser -S -D -H -u 10001 -G mqttree -h /data mqttree \
    && mkdir -p /data \
    && chown mqttree:mqttree /data

COPY --from=build /out/mqttree /usr/local/bin/mqttree

USER mqttree
WORKDIR /data

# Inside a container the listener must be reachable through the published port;
# publish it on loopback and put an authenticating proxy in front.
ENV MQTTREE_LISTEN=:8080 \
    MQTTREE_DATA_DIR=/data \
    MQTTREE_SECRET_KEY_FILE=/data/secret.key

VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/mqttree"]
