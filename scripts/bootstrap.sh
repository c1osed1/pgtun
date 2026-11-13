#!/bin/sh
# One-command VPS bootstrap: generate secrets, build + start the stack, wait for
# the egress server and print copy-paste connection details.
#
# Usage: ./scripts/bootstrap.sh [--yes] [--port 5432]
#   --yes    install Docker automatically (get.docker.com) if it is missing
#   --port   public PostgreSQL port to open in the firewall (default 5432)
#
# Idempotent: safe to re-run.
set -eu

cd "$(dirname "$0")/.."

YES=0
PORT=5432

while [ $# -gt 0 ]; do
    case "$1" in
        --yes|-y)
            YES=1
            shift
            ;;
        --port)
            if [ $# -lt 2 ]; then
                echo "error: --port requires a value" >&2
                exit 2
            fi
            PORT=$2
            shift 2
            ;;
        --port=*)
            PORT=${1#--port=}
            shift
            ;;
        -h|--help)
            sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            echo "error: unknown argument: $1" >&2
            exit 2
            ;;
    esac
done

case "$PORT" in
    ''|*[!0-9]*)
        echo "error: --port must be a number" >&2
        exit 2
        ;;
esac

log() { printf '%s\n' "$*" >&2; }

have() { command -v "$1" >/dev/null 2>&1; }

install_docker() {
    if [ "$YES" -eq 1 ]; then
        log "Docker not found, installing via https://get.docker.com ..."
        curl -fsSL https://get.docker.com | sh
    else
        cat >&2 <<'EOF'
error: Docker is not installed.

Install it first:
  curl -fsSL https://get.docker.com | sh
or re-run this script with --yes to install automatically.

Docker Compose v2 is required (the `docker compose` subcommand).
EOF
        exit 1
    fi
}

# --- dependencies ----------------------------------------------------------

if ! have docker; then
    install_docker
fi
if ! have docker; then
    echo "error: docker is still not available after installation" >&2
    exit 1
fi

if ! docker compose version >/dev/null 2>&1; then
    if [ "$YES" -eq 1 ] && ! have docker-compose; then
        log "Docker Compose plugin not found, installing docker-compose-plugin ..."
        if have apt-get; then
            apt-get update && apt-get install -y docker-compose-plugin
        elif have dnf; then
            dnf install -y docker-compose-plugin
        elif have yum; then
            yum install -y docker-compose-plugin
        fi
    fi
    if ! docker compose version >/dev/null 2>&1; then
        cat >&2 <<'EOF'
error: `docker compose` is not available.

Install the Compose v2 plugin, e.g.:
  apt-get install docker-compose-plugin
or use Docker Engine >= 20.10 with the compose plugin bundled.
EOF
        exit 1
    fi
fi

# --- secrets ---------------------------------------------------------------

gen_secret() {
    if have openssl; then
        openssl rand -hex 24
    else
        head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
    fi
}

if [ ! -f .env ]; then
    log "Generating .env with strong random passwords ..."
    printf 'POSTGRES_PASSWORD=%s\nPGTUN_PASSWORD=%s\n' "$(gen_secret)" "$(gen_secret)" > .env
    chmod 600 .env
else
    log "Using existing .env"
fi

set -a
# shellcheck disable=SC1091
. ./.env
set +a

if [ -z "${POSTGRES_PASSWORD:-}" ] || [ -z "${PGTUN_PASSWORD:-}" ]; then
    echo "error: .env must define POSTGRES_PASSWORD and PGTUN_PASSWORD" >&2
    exit 1
fi

case "$PGTUN_PASSWORD$POSTGRES_PASSWORD" in
    *change_me*)
        log "warning: .env still contains change_me placeholders; replace them for production"
        ;;
esac

# --- bring the stack up ----------------------------------------------------

log "Building and starting pgtun (db + egress + TLS certs) ..."
docker compose up -d --build

db_healthy() {
    cid=$(docker compose ps -q db 2>/dev/null || true)
    [ -n "$cid" ] || return 1
    status=$(docker inspect --format '{{.State.Health.Status}}' "$cid" 2>/dev/null || true)
    [ "$status" = "healthy" ]
}

log "Waiting for PostgreSQL and the egress server (up to 180s) ..."
i=0
ready=0
while [ "$i" -lt 90 ]; do
    if docker compose logs pgtun 2>&1 | grep -q 'egress ready' && db_healthy; then
        ready=1
        break
    fi
    printf '.' >&2
    sleep 2
    i=$((i + 1))
done
printf '\n' >&2

if [ "$ready" -ne 1 ]; then
    echo "error: stack did not become ready in time" >&2
    docker compose ps >&2 || true
    docker compose logs --tail 50 pgtun >&2 || true
    exit 1
fi
log "Stack is up."

# --- public IP -------------------------------------------------------------

PUBLIC_IP=""
if have curl; then
    PUBLIC_IP=$(curl -fsS -m 5 https://ifconfig.me 2>/dev/null \
        || curl -fsS -m 5 https://api.ipify.org 2>/dev/null \
        || true)
fi
if [ -z "$PUBLIC_IP" ]; then
    PUBLIC_IP="<your-vps-ip>"
fi

# --- summary ---------------------------------------------------------------

cat <<EOF

================================================================
 pgtun is ready
================================================================

DSN (sslmode=require, TLS: on (self-signed)):
  postgres://pgtun:${PGTUN_PASSWORD}@${PUBLIC_IP}:${PORT}/postgres?sslmode=require

Client on this VPS (SOCKS5 + Shadowsocks + subscription/QR):
  pgtun client \\
    --dsn "postgres://pgtun:${PGTUN_PASSWORD}@${PUBLIC_IP}:${PORT}/postgres?sslmode=require" \\
    --socks 127.0.0.1:1080 \\
    --ss 0.0.0.0:8388 \\
    --ss-password '<ss-password>'

Print only the ss:// link + QR (for HAPP):
  pgtun client --print-sub --ss 0.0.0.0:8388 --ss-password '<ss-password>'

Same via helper script (LAN IP of the client host):
  ./scripts/make-sub.sh <lan-ip> 8388 chacha20-ietf-poly1305 '<ss-password>'

Firewall: open TCP ${PORT} (DB port is the only inbound port needed):
  ufw:       ufw allow ${PORT}/tcp
  firewalld: firewall-cmd --permanent --add-port=${PORT}/tcp && firewall-cmd --reload
  iptables:  iptables -A INPUT -p tcp --dport ${PORT} -j ACCEPT

TLS: on (self-signed). Clients use sslmode=require.
Verify the deployment:  ./scripts/doctor.sh
================================================================

EOF

if [ "$PORT" != "5432" ]; then
    log "note: docker-compose.yml publishes 5432; change the port mapping to expose ${PORT}."
fi
