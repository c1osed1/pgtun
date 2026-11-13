#!/bin/sh
# Generate a SIP002 ss:// URI and a base64 subscription for it.
# Usage: make-sub.sh HOST PORT [METHOD] [PASSWORD] [TAG]
set -eu

if [ $# -lt 2 ]; then
    echo "usage: $0 HOST PORT [METHOD] [PASSWORD] [TAG]" >&2
    exit 2
fi

HOST=$1
PORT=$2
METHOD=${3:-chacha20-ietf-poly1305}
PASSWORD=${4:-}
TAG=${5:-pgtun}

b64url() {
    base64 | tr -d '\n' | tr '+/' '-_' | tr -d '='
}

CRED=$(printf '%s:%s' "$METHOD" "$PASSWORD" | b64url)
URI="ss://${CRED}@${HOST}:${PORT}#${TAG}"

printf '%s\n' "$URI"
printf '%s\n' "$URI" | base64 | tr -d '\n'
printf '\n'
