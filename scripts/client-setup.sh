#!/bin/sh
# One-command client setup for macOS and Linux: build pgtun, write a 0600 JSON
# config, install a user-level auto-start service (launchd / systemd --user)
# and print the ss:// link, base64 subscription, QR and the device command.
#
# Usage:
#   ./scripts/client-setup.sh --dsn "<DSN>" [flags]
#
# Flags:
#   --dsn DSN            PostgreSQL DSN (required unless --config has one; env PGTUN_DSN)
#   --socks ADDR         SOCKS5 listen address (default 127.0.0.1:1080)
#   --ss ADDR            Shadowsocks listen address (default 0.0.0.0:8388)
#   --ss-method M        Shadowsocks AEAD method (default chacha20-ietf-poly1305)
#   --ss-password P      Shadowsocks password (auto-generated if absent)
#   --public-host H      host advertised in the subscription (default: detected)
#   --tag T              subscription name (default pgtun)
#   --bin-dir DIR        where the binary is installed (default ~/.local/bin)
#   --config FILE        JSON config path (default ~/.config/pgtun/client.json)
#   --print-only         build + write config + print subscription, then exit
#   --dry-run            print the plan and exit without writing/installing
#   --uninstall          stop and remove the installed service (keep binary/config)
#   -h, --help           show this help
#
# Never requires sudo. Idempotent: safe to re-run.
set -eu

log() { printf '%s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
    sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
}

expand_home() {
    case "$1" in
        "~/"*) printf '%s/%s' "$HOME" "${1#"~/"}" ;;
        "~") printf '%s' "$HOME" ;;
        *) printf '%s' "$1" ;;
    esac
}

# json_string FILE KEY -> first "KEY": "value" string value, empty if absent.
json_string() {
    sed -n 's/.*"'"$2"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$1" 2>/dev/null | head -n 1
}

json_escape() {
    printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

xml_escape() {
    printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}

require_val() {
    [ "$#" -ge 2 ] || die "$1 requires a value"
}

# --- defaults --------------------------------------------------------------

DSN=""
SOCKS="127.0.0.1:1080"
SS="0.0.0.0:8388"
SS_METHOD="chacha20-ietf-poly1305"
SS_PASSWORD=""
PUBLIC_HOST=""
TAG="pgtun"
BIN_DIR="$HOME/.local/bin"
CONFIG="$HOME/.config/pgtun/client.json"
PRINT_ONLY=0
DRY_RUN=0
UNINSTALL=0

SET_DSN=0
SET_SS_PASSWORD=0
SET_PUBLIC_HOST=0
SET_TAG=0

# --- argument parsing ------------------------------------------------------

while [ "$#" -gt 0 ]; do
    case "$1" in
        --dsn=*)         DSN=${1#*=}; SET_DSN=1; shift ;;
        --dsn)           require_val "$@"; DSN=$2; SET_DSN=1; shift 2 ;;
        --socks=*)       SOCKS=${1#*=}; shift ;;
        --socks)         require_val "$@"; SOCKS=$2; shift 2 ;;
        --ss=*)          SS=${1#*=}; shift ;;
        --ss)            require_val "$@"; SS=$2; shift 2 ;;
        --ss-method=*)   SS_METHOD=${1#*=}; shift ;;
        --ss-method)     require_val "$@"; SS_METHOD=$2; shift 2 ;;
        --ss-password=*) SS_PASSWORD=${1#*=}; SET_SS_PASSWORD=1; shift ;;
        --ss-password)   require_val "$@"; SS_PASSWORD=$2; SET_SS_PASSWORD=1; shift 2 ;;
        --public-host=*) PUBLIC_HOST=${1#*=}; SET_PUBLIC_HOST=1; shift ;;
        --public-host)   require_val "$@"; PUBLIC_HOST=$2; SET_PUBLIC_HOST=1; shift 2 ;;
        --tag=*)         TAG=${1#*=}; SET_TAG=1; shift ;;
        --tag)           require_val "$@"; TAG=$2; SET_TAG=1; shift 2 ;;
        --bin-dir=*)     BIN_DIR=${1#*=}; shift ;;
        --bin-dir)       require_val "$@"; BIN_DIR=$2; shift 2 ;;
        --config=*)      CONFIG=${1#*=}; shift ;;
        --config)        require_val "$@"; CONFIG=$2; shift 2 ;;
        --print-only)    PRINT_ONLY=1; shift ;;
        --dry-run)       DRY_RUN=1; shift ;;
        --uninstall)     UNINSTALL=1; shift ;;
        -h|--help)       usage; exit 0 ;;
        *)               die "unknown argument: $1 (see --help)" ;;
    esac
done

BIN_DIR=$(expand_home "$BIN_DIR")
CONFIG=$(expand_home "$CONFIG")
[ -n "$BIN_DIR" ] || die "--bin-dir must not be empty"
[ -n "$CONFIG" ] || die "--config must not be empty"

BIN="$BIN_DIR/pgtun"

# --- repo root and OS ------------------------------------------------------

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

[ -f "$ROOT/go.mod" ] && [ -d "$ROOT/cmd/pgtun" ] \
    || die "cannot find the pgtun repo root at $ROOT (run scripts/client-setup.sh from the repo)"

case "$(uname -s)" in
    Darwin) OS=darwin ;;
    Linux)  OS=linux ;;
    *)      die "unsupported OS: $(uname -s) (use scripts/client-setup.ps1 on Windows)" ;;
esac

# --- existing config values ------------------------------------------------

EXIST_DSN=""
EXIST_PASSWORD=""
EXIST_PUBLIC_HOST=""
EXIST_TAG=""
if [ -f "$CONFIG" ]; then
    EXIST_DSN=$(json_string "$CONFIG" dsn || true)
    EXIST_PASSWORD=$(json_string "$CONFIG" ss_password || true)
    EXIST_PUBLIC_HOST=$(json_string "$CONFIG" public_host || true)
    EXIST_TAG=$(json_string "$CONFIG" tag || true)
fi

if [ "$SET_DSN" -eq 0 ] && [ -z "$DSN" ]; then
    DSN=$EXIST_DSN
fi
if [ -z "$DSN" ]; then
    DSN=${PGTUN_DSN:-}
fi
if [ "$SET_SS_PASSWORD" -eq 0 ] && [ -z "$SS_PASSWORD" ]; then
    SS_PASSWORD=$EXIST_PASSWORD
fi
if [ "$SET_PUBLIC_HOST" -eq 0 ] && [ -z "$PUBLIC_HOST" ]; then
    PUBLIC_HOST=$EXIST_PUBLIC_HOST
fi
if [ "$SET_TAG" -eq 0 ] && [ -n "$EXIST_TAG" ]; then
    TAG=$EXIST_TAG
fi

# --- service helpers -------------------------------------------------------

PLIST="$HOME/Library/LaunchAgents/ru.pgtun.client.plist"
MAC_LOG_DIR="$HOME/Library/Logs/pgtun"
UNIT_DIR="$HOME/.config/systemd/user"
UNIT="$UNIT_DIR/pgtun-client.service"

uninstall_service() {
    case "$OS" in
        darwin)
            launchctl unload "$PLIST" >/dev/null 2>&1 || true
            rm -f "$PLIST"
            log "removed $PLIST (binary and config kept)"
            ;;
        linux)
            if command -v systemctl >/dev/null 2>&1; then
                systemctl --user disable --now pgtun-client >/dev/null 2>&1 || true
            fi
            rm -f "$UNIT"
            if command -v systemctl >/dev/null 2>&1; then
                systemctl --user daemon-reload >/dev/null 2>&1 || true
            fi
            log "removed $UNIT (binary and config kept)"
            ;;
    esac
}

print_device_blocks() {
    cat <<EOF

=== КОМАНДА ДЛЯ ЭТОГО УСТРОЙСТВА (SOCKS5) ===
$BIN client --config $CONFIG
=== ПОДПИСКА ДЛЯ HAPP/ТЕЛЕФОНА ===
(ss:// выше — отсканируй QR или вставь base64-подписку)
EOF
}

print_sub() {
    "$BIN" client --config "$CONFIG" --print-sub
}

# --- uninstall -------------------------------------------------------------

if [ "$UNINSTALL" -eq 1 ]; then
    uninstall_service
    exit 0
fi

# --- validation and plan ---------------------------------------------------

[ -n "$DSN" ] || die "--dsn or PGTUN_DSN is required (or an existing --config with \"dsn\")"

NEED_GEN=0
[ -n "$SS_PASSWORD" ] || NEED_GEN=1

if command -v go >/dev/null 2>&1; then
    GO_BIN=$(command -v go)
else
    GO_BIN=""
fi

if [ "$DRY_RUN" -eq 1 ]; then
    if [ -n "$GO_BIN" ]; then
        GO_LINE="$GO_BIN"
    else
        GO_LINE="NOT FOUND (install Go before running without --dry-run)"
    fi
    if [ "$NEED_GEN" -eq 1 ]; then
        PASS_LINE="<auto-generated with openssl rand -hex 16>"
    else
        PASS_LINE="<set>"
    fi
    if [ "$OS" = "darwin" ]; then
        SERVICE_LINE="launchd ru.pgtun.client ($PLIST)"
        STATUS_LINE="launchctl list | grep pgtun"
        LOGS_LINE="$MAC_LOG_DIR/client.log"
    else
        SERVICE_LINE="systemd --user pgtun-client ($UNIT)"
        STATUS_LINE="systemctl --user status --no-pager pgtun-client"
        LOGS_LINE="journalctl --user -u pgtun-client -f"
    fi
    if [ "$PRINT_ONLY" -eq 1 ]; then
        ACTION_LINE="build + write config + print subscription (no service)"
    else
        ACTION_LINE="build + write config + install service + print subscription"
    fi

    cat <<EOF
pgtun client setup plan (--dry-run, nothing written or installed)

  repo root:   $ROOT
  OS:          $OS
  go:          $GO_LINE
  binary:      $BIN
  build:       CGO_ENABLED=0 go build -o "$BIN" ./cmd/pgtun
  config:      $CONFIG
  service:     $SERVICE_LINE
  logs:        $LOGS_LINE
  status:      $STATUS_LINE
  dsn:         $DSN
  socks:       $SOCKS
  ss:          $SS
  ss_method:   $SS_METHOD
  ss_password: $PASS_LINE
  max_streams: 256
  tag:         $TAG
  public_host: ${PUBLIC_HOST:-<auto-detected>}
  action:      $ACTION_LINE

EOF
    exit 0
fi

# --- build -----------------------------------------------------------------

if [ -z "$GO_BIN" ]; then
    cat >&2 <<'EOF'
error: Go toolchain not found; it is required to build pgtun.

Install Go:
  macOS:  brew install go
  Linux:  apt-get install golang-go   (or dnf/yum/pacman equivalent)
  any:    https://go.dev/dl/

Or build on another machine with `make release` and copy the binary.
EOF
    exit 1
fi

if [ "$NEED_GEN" -eq 1 ]; then
    if command -v openssl >/dev/null 2>&1; then
        SS_PASSWORD=$(openssl rand -hex 16)
    else
        SS_PASSWORD=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
    fi
fi

log "Building pgtun -> $BIN"
mkdir -p "$BIN_DIR"
( cd "$ROOT" && CGO_ENABLED=0 go build -o "$BIN" ./cmd/pgtun ) \
    || die "go build failed"

# --- config ----------------------------------------------------------------

log "Writing config -> $CONFIG"
mkdir -p "$(dirname -- "$CONFIG")"
(
    umask 077
    cat > "$CONFIG" <<EOF
{
  "dsn": "$(json_escape "$DSN")",
  "socks": "$(json_escape "$SOCKS")",
  "ss": "$(json_escape "$SS")",
  "ss_method": "$(json_escape "$SS_METHOD")",
  "ss_password": "$(json_escape "$SS_PASSWORD")",
  "max_streams": 256,
  "public_host": "$(json_escape "$PUBLIC_HOST")",
  "tag": "$(json_escape "$TAG")"
}
EOF
)
chmod 600 "$CONFIG" 2>/dev/null || true

# --- print-only ------------------------------------------------------------

if [ "$PRINT_ONLY" -eq 1 ]; then
    print_sub
    print_device_blocks
    exit 0
fi

# --- install auto-start service -------------------------------------------

case "$OS" in
    darwin)
        log "Installing launchd agent -> $PLIST"
        mkdir -p "$HOME/Library/LaunchAgents" "$MAC_LOG_DIR"
        cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>ru.pgtun.client</string>
  <key>ProgramArguments</key>
  <array>
    <string>$(xml_escape "$BIN")</string>
    <string>client</string>
    <string>--config</string>
    <string>$(xml_escape "$CONFIG")</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>$(xml_escape "$MAC_LOG_DIR/client.log")</string>
  <key>StandardErrorPath</key>
  <string>$(xml_escape "$MAC_LOG_DIR/client.log")</string>
</dict>
</plist>
EOF
        chmod 644 "$PLIST" 2>/dev/null || true
        plutil -lint "$PLIST" >/dev/null || die "invalid launchd plist: $PLIST"
        launchctl unload "$PLIST" >/dev/null 2>&1 || true
        launchctl load "$PLIST" || die "launchctl load failed"
        ;;
    linux)
        log "Installing systemd user unit -> $UNIT"
        mkdir -p "$UNIT_DIR"
        cat > "$UNIT" <<EOF
[Unit]
Description=pgtun client (PostgreSQL tunnel, SOCKS5/Shadowsocks)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart="$BIN" client --config "$CONFIG"
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
EOF
        command -v systemctl >/dev/null 2>&1 \
            || die "systemctl not found; run manually: $BIN client --config $CONFIG"
        systemctl --user daemon-reload || die "systemctl --user daemon-reload failed"
        systemctl --user enable --now pgtun-client || die "systemctl --user enable failed"
        ;;
esac

# --- subscription, status, hints ------------------------------------------

print_sub
print_device_blocks

case "$OS" in
    darwin)
        log ""
        log "Status:"
        launchctl list 2>/dev/null | grep pgtun || true
        log "Logs:     tail -f $MAC_LOG_DIR/client.log"
        log "Uninstall: $0 --uninstall"
        ;;
    linux)
        log ""
        log "Status:"
        systemctl --user status --no-pager pgtun-client || true
        log "Logs:     journalctl --user -u pgtun-client -f"
        log "Uninstall: $0 --uninstall"
        ;;
esac
