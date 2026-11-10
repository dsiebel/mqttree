#!/usr/bin/env bash
# Local end-to-end run: throwaway mosquitto, a freshly built binary and the
# Playwright suite. Requires Docker and Node/npm.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
broker_port="${MQTT_BROKER_PORT:-1883}"
container="mqttree-e2e-broker-$RANDOM"
data_dir="$(mktemp -d)"
app_pid=""

cleanup() {
    docker rm -f "$container" >/dev/null 2>&1 || true
    if [[ -n "$app_pid" ]]; then kill "$app_pid" >/dev/null 2>&1 || true; fi
    rm -rf "$data_dir"
}
trap cleanup EXIT

docker run -d --rm --name "$container" -p "127.0.0.1:${broker_port}:1883" eclipse-mosquitto:2 \
    sh -c 'printf "listener 1883\nallow_anonymous true\n" > /tmp/mosquitto.conf && exec mosquitto -c /tmp/mosquitto.conf' >/dev/null

(cd "$root" && go build -trimpath -o /tmp/mqttree-e2e .)
MQTTREE_DATA_DIR="$data_dir" MQTTREE_SECRET_KEY_FILE="$data_dir/secret.key" \
    /tmp/mqttree-e2e >"$data_dir/mqttree.log" 2>&1 &
app_pid=$!

for _ in $(seq 1 60); do
    curl -fsS http://127.0.0.1:8080/healthz >/dev/null && break
    sleep 0.2
done

cd "$root/e2e"
npm install --silent
MQTT_BROKER_PORT="$broker_port" npx playwright test
