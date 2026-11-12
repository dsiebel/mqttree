#!/bin/sh
# Smoke test an image filesystem exported by `docker build --output type=local`.
#
# Usage: dev/smoke-image.sh [root]
#
# Verifies that the runtime stage contains the static binary, the unprivileged
# user and the data directory, then runs the exported binary, checks that it
# creates its key and database and that it serves the UI and health endpoint.
set -eu

root=${1:-.ci-image}
bin="$root/usr/local/bin/mqttree"
port=18080
workdir=$(mktemp -d)

cleanup() {
    if [ -n "${pid:-}" ]; then
        kill "$pid" 2>/dev/null || true
    fi
    rm -rf "$workdir"
}
trap cleanup EXIT INT TERM

fail() {
    echo "smoke test failed: $*" >&2
    if [ -f "$workdir/mqttree.log" ]; then
        cat "$workdir/mqttree.log" >&2
    fi
    exit 1
}

test -x "$bin" || fail "$bin is missing or not executable"
test -d "$root/data" || fail "$root/data is missing"
grep -q '^mqttree:' "$root/etc/passwd" || fail "mqttree user is missing"

version=$("$bin" -version)
echo "$version"
test "$version" = 'mqttree dev (none, unknown)' || fail "unexpected version output: $version"

MQTTREE_LISTEN="127.0.0.1:$port" \
    MQTTREE_DATA_DIR="$workdir" \
    MQTTREE_SECRET_KEY_FILE="$workdir/secret.key" \
    "$bin" >"$workdir/mqttree.log" 2>&1 &
pid=$!

i=0
while [ "$i" -lt 60 ]; do
    if wget -qO- "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
        break
    fi
    i=$((i + 1))
    sleep 0.5
done

health=$(wget -qO- "http://127.0.0.1:$port/healthz" 2>/dev/null || true)
test "$health" = ok || fail "health endpoint returned '$health'"
wget -qO- "http://127.0.0.1:$port/" | grep -q '<title>mqttree</title>' || fail "UI is not served"
test -s "$workdir/secret.key" || fail "secret key was not created"
test -f "$workdir/mqttree.db" || fail "database was not created"

cat "$workdir/mqttree.log"
