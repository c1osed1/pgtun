#!/bin/sh
# pgtun database bootstrap. Runs as /docker-entrypoint-initdb.d script on the
# first start of a postgres container. Idempotent.
set -eu

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"
: "${PGTUN_PASSWORD:?PGTUN_PASSWORD is required}"

psql -v ON_ERROR_STOP=1 \
     --username "$POSTGRES_USER" \
     --dbname "$POSTGRES_DB" \
     -v pgtun_password="$PGTUN_PASSWORD" <<'EOSQL'
SELECT format('CREATE ROLE pgtun LOGIN PASSWORD %L', :'pgtun_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgtun')\gexec

ALTER ROLE pgtun WITH LOGIN PASSWORD :'pgtun_password';

CREATE SCHEMA IF NOT EXISTS pgtun AUTHORIZATION pgtun;
GRANT ALL ON SCHEMA pgtun TO pgtun;

CREATE TABLE IF NOT EXISTS pgtun.streams (
    id         uuid PRIMARY KEY,
    target     text NOT NULL,
    claimed    boolean NOT NULL DEFAULT false,
    closed     boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS pgtun.chunks (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    stream_id  uuid NOT NULL,
    dir        smallint NOT NULL,
    data       bytea,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS chunks_stream_dir_id_idx
    ON pgtun.chunks (stream_id, dir, id);

GRANT ALL ON ALL TABLES IN SCHEMA pgtun TO pgtun;
GRANT ALL ON ALL SEQUENCES IN SCHEMA pgtun TO pgtun;
EOSQL
