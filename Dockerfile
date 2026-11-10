# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/mqttree .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S mqttree \
    && adduser -S -G mqttree -h /data mqttree \
    && mkdir -p /data \
    && chown mqttree:mqttree /data

COPY --from=build /out/mqttree /usr/local/bin/mqttree

USER mqttree
# Inside a container the listener must be reachable through the published port;
# publish it on loopback and put an authenticating proxy in front.
ENV MQTTREE_LISTEN=:8080 \
    MQTTREE_DATA_DIR=/data \
    MQTTREE_SECRET_KEY_FILE=/data/secret.key

VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/mqttree"]
