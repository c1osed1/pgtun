#!/bin/sh
# End-to-end smoke test for pgtun.
#
# Starts a throwaway postgres:17 container with the pgtun init script (TLS on,
# hostssl-only pg_hba.conf), builds pgtun, runs the egress server plus a SOCKS5
# client and fetches https://example.com through the tunnel. Exits 0 only if the
# HTTP status is 200. Cleanup always removes the container, volume and processes.
#
# Requirements: docker, go, curl, openssl.
#
# Env overrides:
#   E2E_PG_PORT          host port for PostgreSQL (default 55432)
#   E2E_SOCKS_PORT       local SOCKS5 port (default 11080)
#   E2E_PG_IMAGE         PostgreSQL image (default postgres:17)
#   E2E_PG_PASSWORD      throwaway superuser password
#   E2E_PGTUN_PASSWORD   throwaway tunnel password
#   E2E_URL              URL fetched through the tunnel (default https://example.com)
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

E2E_PG_PORT=${E2E_PG_PORT:-55432}
E2E_SOCKS_PORT=${E2E_SOCKS_PORT:-11080}
E2E_PG_IMAGE=${E2E_PG_IMAGE:-postgres:17}
E2E_PG_PASSWORD=${E2E_PG_PASSWORD:-e2e_superuser_pw}
E2E_PGTUN_PASSWORD=${E2E_PGTUN_PASSWORD:-e2e_tunnel_pw}
E2E_URL=${E2E_URL:-https://example.com}

for tool in docker curl openssl; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "e2e: $tool is required" >&2
        exit 1
    fi
done

TMP=$(mktemp -d "${TMPDIR:-/tmp}/pgtun-e2e.XXXXXX")
NAME="pgtun-e2e-$$"
DB="$NAME-db"
CERTVOL="$NAME-certs"
SERVER_PID=""
CLIENT_PID=""

cleanup() {
    status=$?
    trap - EXIT INT TERM
    if [ -n "$CLIENT_PID" ]; then kill "$CLIENT_PID" >/dev/null 2>&1 || true; fi
    if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" >/dev/null 2>&1 || true; fi
    wait >/dev/null 2>&1 || true
    docker rm -f "$DB" >/dev/null 2>&1 || true
    docker volume rm "$CERTVOL" >/dev/null 2>&1 || true
    rm -rf "$TMP"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "e2e: building pgtun"
( cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -o "$TMP/pgtun" ./cmd/pgtun )

echo "e2e: generating throwaway TLS certs"
docker volume create "$CERTVOL" >/dev/null
docker run --rm -v "$CERTVOL:/certs" --entrypoint sh "$E2E_PG_IMAGE" -c '
set -e
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=pgtun" \
    -keyout /certs/server.key -out /certs/server.crt >/dev/null 2>&1
chown 999:999 /certs/server.key /certs/server.crt
chmod 600 /certs/server.key
chmod 644 /certs/server.crt
'

echo "e2e: starting PostgreSQL ($E2E_PG_IMAGE) on 127.0.0.1:$E2E_PG_PORT"
docker run -d --name "$DB" \
    -e POSTGRES_PASSWORD="$E2E_PG_PASSWORD" \
    -e PGTUN_PASSWORD="$E2E_PGTUN_PASSWORD" \
    -p "127.0.0.1:${E2E_PG_PORT}:5432" \
    -v "$CERTVOL:/certs:ro" \
    -v "$ROOT/docker/init.sh:/docker-entrypoint-initdb.d/10-pgtun.sh:ro" \
    "$E2E_PG_IMAGE" postgres \
    -c ssl=on \
    -c ssl_cert_file=/certs/server.crt \
    -c ssl_key_file=/certs/server.key >/dev/null

echo "e2e: waiting for PostgreSQL (hostssl + pgtun schema)"
i=0
while :; do
    if docker exec -e PGPASSWORD="$E2E_PGTUN_PASSWORD" "$DB" \
        psql "host=127.0.0.1 port=5432 user=pgtun dbname=postgres sslmode=require" \
        -tAc 'select 1 from pgtun.streams limit 1' >/dev/null 2>&1; then
        break
    fi
    if [ "$(docker inspect -f '{{.State.Running}}' "$DB" 2>/dev/null || echo false)" != "true" ]; then
        echo "e2e: PostgreSQL container exited during init" >&2
        docker logs "$DB" >&2 || true
        exit 1
    fi
    i=$((i + 1))
    if [ "$i" -ge 90 ]; then
        echo "e2e: PostgreSQL did not become ready" >&2
        docker logs --tail 50 "$DB" >&2 || true
        exit 1
    fi
    sleep 1
done

DSN="postgres://pgtun:${E2E_PGTUN_PASSWORD}@127.0.0.1:${E2E_PG_PORT}/postgres?sslmode=require"

echo "e2e: starting egress server"
PGTUN_DSN="$DSN" "$TMP/pgtun" server >"$TMP/server.log" 2>&1 &
SERVER_PID=$!

i=0
while ! grep -q 'egress ready' "$TMP/server.log" 2>/dev/null; do
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
        echo "e2e: egress server exited" >&2
        cat "$TMP/server.log" >&2 || true
        exit 1
    fi
    i=$((i + 1))
    if [ "$i" -ge 60 ]; then
        echo "e2e: egress server did not become ready" >&2
        cat "$TMP/server.log" >&2 || true
        exit 1
    fi
    sleep 1
done

echo "e2e: starting client (SOCKS5 127.0.0.1:$E2E_SOCKS_PORT)"
"$TMP/pgtun" client --dsn "$DSN" --socks "127.0.0.1:${E2E_SOCKS_PORT}" --ss "" \
    >"$TMP/client.log" 2>&1 &
CLIENT_PID=$!

i=0
while ! grep -q 'SOCKS5 listening' "$TMP/client.log" 2>/dev/null; do
    if ! kill -0 "$CLIENT_PID" 2>/dev/null; then
        echo "e2e: client exited" >&2
        cat "$TMP/client.log" >&2 || true
        exit 1
    fi
    i=$((i + 1))
    if [ "$i" -ge 30 ]; then
        echo "e2e: SOCKS5 listener did not start" >&2
        cat "$TMP/client.log" >&2 || true
        exit 1
    fi
    sleep 1
done

echo "e2e: fetching $E2E_URL through the tunnel"
code=000
i=0
while [ "$i" -lt 30 ]; do
    code=$(curl --socks5-hostname "127.0.0.1:${E2E_SOCKS_PORT}" -sS -m 20 \
        -o /dev/null -w '%{http_code}' "$E2E_URL" 2>>"$TMP/curl.log" || true)
    if [ "$code" = "200" ]; then
        break
    fi
    i=$((i + 1))
    sleep 1
done

echo "e2e HTTP=$code"
if [ "$code" != "200" ]; then
    echo "e2e: request through the tunnel failed" >&2
    cat "$TMP/curl.log" >&2 || true
    tail -n 40 "$TMP/server.log" >&2 || true
    tail -n 40 "$TMP/client.log" >&2 || true
    exit 1
fi

echo "e2e: OK"
