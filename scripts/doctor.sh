#!/bin/sh
# Post-deploy health checks for a running pgtun stack.
# Prints PASS/FAIL per check and exits non-zero if anything fails.
set -u

cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
    echo "error: .env not found; run ./scripts/bootstrap.sh first" >&2
    exit 1
fi

set -a
# shellcheck disable=SC1091
. ./.env
set +a

if [ -z "${PGTUN_PASSWORD:-}" ]; then
    echo "error: PGTUN_PASSWORD is missing from .env" >&2
    exit 1
fi

FAILED=0

pass() { printf 'PASS  %s\n' "$1"; }
fail() {
    printf 'FAIL  %s\n' "$1"
    FAILED=$((FAILED + 1))
}

psql_pgtun() {
    docker compose exec -T -e PGPASSWORD="$PGTUN_PASSWORD" db \
        psql "host=127.0.0.1 dbname=postgres user=pgtun sslmode=require" \
        -v ON_ERROR_STOP=1 -q "$@"
}

echo "== docker compose ps =="
docker compose ps || echo "(docker compose ps failed)"
echo

# --- containers are running -------------------------------------------------
for svc in db pgtun; do
    cid=$(docker compose ps --status running -q "$svc" 2>/dev/null || true)
    if [ -n "$cid" ]; then
        pass "$svc container is running"
    else
        fail "$svc container is not running"
    fi
done

# --- TLS is actually used ---------------------------------------------------
tls=$(psql_pgtun -tAc "select coalesce((select ssl::text from pg_stat_ssl where pid=pg_backend_pid()),'f')" 2>/dev/null | tr -d '[:space:]')
case "$tls" in
    t|true|on|1)
        pass "TLS in use (pg_stat_ssl.ssl = ${tls})"
        ;;
    *)
        fail "TLS not in use (pg_stat_ssl.ssl = '${tls:-<empty>}')"
        ;;
esac

# --- schema is reachable ----------------------------------------------------
count=$(psql_pgtun -tAc "select count(*) from pgtun.streams" 2>/dev/null | tr -d '[:space:]')
case "$count" in
    ''|*[!0-9]*)
        fail "schema check failed (no row count from pgtun.streams)"
        ;;
    *)
        pass "schema pgtun.streams reachable (rows=$count)"
        ;;
esac

# --- write/read/delete round-trip -------------------------------------------
tmp=$(mktemp 2>/dev/null || echo /tmp/pgtun-doctor.$$)
if psql_pgtun >"$tmp" 2>&1 <<'SQL'
DO $do$
DECLARE
    sid uuid := gen_random_uuid();
    got bytea;
BEGIN
    INSERT INTO pgtun.streams (id, target) VALUES (sid, 'doctor');
    INSERT INTO pgtun.chunks (stream_id, dir, data) VALUES (sid, 0, '\xdeadbeef'::bytea);
    SELECT data INTO got FROM pgtun.chunks WHERE stream_id = sid AND dir = 0;
    IF got IS DISTINCT FROM '\xdeadbeef'::bytea THEN
        RAISE EXCEPTION 'round-trip mismatch';
    END IF;
    DELETE FROM pgtun.chunks WHERE stream_id = sid;
    DELETE FROM pgtun.streams WHERE id = sid;
END
$do$;
SQL
then
    pass "chunk insert/select/delete round-trip"
else
    fail "chunk insert/select/delete round-trip"
    sed 's/^/      /' "$tmp"
fi
rm -f "$tmp"

echo
if [ "$FAILED" -eq 0 ]; then
    echo "ALL CHECKS PASSED"
else
    echo "$FAILED check(s) failed"
fi
exit "$FAILED"
