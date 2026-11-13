#!/bin/sh
# Local deploy helper: bring up db + egress server with docker compose.
set -eu

cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
    cp .env.example .env
    echo "Created .env from .env.example. Edit POSTGRES_PASSWORD/PGTUN_PASSWORD, then re-run." >&2
    exit 1
fi

docker compose up -d --build
