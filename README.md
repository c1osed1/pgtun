# pgtun

**Carry SOCKS5 / Shadowsocks traffic through a real PostgreSQL database.**

[![CI](https://github.com/c1osed1/pgtun/actions/workflows/ci.yml/badge.svg)](https://github.com/c1osed1/pgtun/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go 1.27+](https://img.shields.io/badge/go-1.27%2B-00ADD8.svg)](go.mod)
[![status: PoC](https://img.shields.io/badge/status-PoC-orange.svg)](#status)

**English** · [Русский](README.ru.md)

> Proof of concept, not audited. Read [Disclaimer](#disclaimer) and [Security](#security) before exposing anything to the internet.

---

## Why

DPI engines fingerprint custom VPN protocols from the first packet. `pgtun` does
not invent a protocol. It uses a **real, stock PostgreSQL** as the transport: the
database answers active probes with a genuine PG handshake (TLS,
`scram-sha-256`, real error messages), while your traffic rides inside ordinary
SQL table operations and `LISTEN/NOTIFY`. On the wire it looks like a TLS
connection to a database — because it is one.

It is **not magic**. Latency is high by design and behavioural detection is
still possible — see [Limitations](#limitations).

## Architecture

```
   app / HAPP
       │  SOCKS5 / Shadowsocks (localhost)
       ▼
 ┌──────────────┐                          ┌──────────────────────────┐
 │ pgtun client │ ─── PG wire + TLS ─────▶ │  real PostgreSQL (:5432) │
 └──────────────┘                          │  schema pgtun            │
                                           │  streams / chunks        │
 ┌──────────────┐                          │  LISTEN / NOTIFY         │
 │ pgtun server │ ◀── PG wire + TLS ─────▶ │                          │
 │   (egress)   │                          └──────────────────────────┘
 └──────┬───────┘
        │ TCP
        ▼
     internet
```

The database is a mailbox. Both the client and the egress server connect to the
**same** PostgreSQL over the normal protocol; they exchange bytes through
tables and wake each other with `pg_notify`.

## Quickstart (3 steps)

### 1. Deploy on a VPS

Copy the repo and run bootstrap (installs Docker if missing):

```sh
scp -r pgtun root@vps:/opt/pgtun && ssh root@vps 'cd /opt/pgtun && ./scripts/bootstrap.sh --yes'
```

It brings up `db` + egress + self-signed TLS, waits for readiness and prints a
DSN such as `postgres://pgtun:...@IP:5432/postgres?sslmode=require`. The only
inbound port is TCP `5432`.

### 2. Connect a computer with one script

| OS | Command |
|---|---|
| macOS / Linux | `./scripts/client-setup.sh --dsn "<DSN>"` |
| Windows (PowerShell) | `powershell -ExecutionPolicy Bypass -File .\scripts\client-setup.ps1 -Dsn "<DSN>"` |

Run from the repository root on the computer that should act as the gateway.
The script builds the client, writes a `0600` config, installs an autostart
service (launchd / `systemd --user` / Scheduled Task), exposes SOCKS5 on
`127.0.0.1:1080` and Shadowsocks on `0.0.0.0:8388`, and generates the SS
password for you.

### 3. Subscription and device command

The script prints:

- an `ss://` link;
- a one-line base64 subscription;
- a QR code to scan in HAPP (or paste the subscription).

It also prints the ready-to-use device command:

```sh
pgtun client --config <config>
```

Useful `client-setup` flags:

| Flag | Meaning |
|---|---|
| `--print-only` | build, write config, print subscription, do not install a service |
| `--dry-run` | print the plan, change nothing |
| `--uninstall` | stop and remove the service (keeps binary and config) |
| `--public-host <ip>` | override the LAN/public IP placed into the subscription |

Or generate a subscription manually:

```sh
./scripts/make-sub.sh 192.168.1.10 8388 chacha20-ietf-poly1305 'ss_password' home
```

## Features

- **Real protocol camouflage** — stock PostgreSQL, real TLS, real auth and
  schema; no custom wire format.
- **SOCKS5 and Shadowsocks AEAD** front-ends on the client.
- **Multiplexed streams** with half-close (`NULL` chunk = EOF).
- **Wakeups via `LISTEN/NOTIFY`** with a polling fallback.
- **TLS on by default** — `certgen` creates a self-signed cert, PostgreSQL runs
  with `ssl=on`, and `pg_hba.conf` enforces `hostssl` only.
- **One-command deploy** with auto-generated secrets and a health `doctor`.
- **Subscription + QR** for HAPP / any Shadowsocks client.
- **CI** on Linux and macOS, unit tests and a self-contained TLS e2e.

## How it works

- `pgtun client` opens a local SOCKS5 (and optional Shadowsocks) listener. Each
  TCP connection becomes a row in `pgtun.streams`; bytes are written to
  `pgtun.chunks` (`dir=0` client→server, `dir=1` server→client).
- `pgtun server` (egress) polls `pgtun.streams`, atomically claims new streams
  (`FOR UPDATE SKIP LOCKED`), dials the target and shuttles bytes between the
  socket and the table.
- A consumer is woken by `pg_notify('pgtun', <uuid>)` in the same transaction as
  the chunk insert; a dedicated pgx connection holds `LISTEN`.
- `NULL` in `chunks.data` is the EOF marker (half-close).

## Client

Manual run, without a service:

```sh
go build -o pgtun ./cmd/pgtun
./pgtun client \
  --dsn "postgres://pgtun:PASS@VPS:5432/postgres?sslmode=require" \
  --socks 127.0.0.1:1080
```

Verify:

```sh
curl --socks5-hostname 127.0.0.1:1080 -sS -o /dev/null -w '%{http_code}\n' https://example.com
```

Once the repository is published you can also install with Go:

```sh
go install github.com/c1osed1/pgtun/cmd/pgtun@latest
```

## HAPP / phone

HAPP and other Shadowsocks clients cannot speak PostgreSQL, so run the `pgtun`
client on an always-on host in the same LAN as the phone. `client-setup` does
this automatically. Manually:

```sh
./pgtun client --dsn "postgres://pgtun:PASS@VPS:5432/postgres?sslmode=require" \
  --socks "" --ss 0.0.0.0:8388 --ss-password 'ss_password'
```

STDOUT prints the `ss://` link, the base64 subscription and a QR code. The phone
must be able to reach the host's LAN IP on port `8388`.

## Config file

Flags override the file; the file overrides env/defaults. `client-setup` writes
this automatically. Example at `~/.config/pgtun/client.json`:

```json
{
  "dsn": "postgres://pgtun:PASS@VPS:5432/postgres?sslmode=require",
  "socks": "127.0.0.1:1080",
  "ss": "0.0.0.0:8388",
  "ss_method": "chacha20-ietf-poly1305",
  "ss_password": "ss_password",
  "max_streams": 256,
  "public_host": "",
  "tag": "pgtun"
}
```

```sh
pgtun client --config ~/.config/pgtun/client.json
```

`"socks": ""` disables the SOCKS listener. `public_host` (or `--public-host`)
overrides the LAN/public IP placed into the subscription; `tag` (or `--tag`)
sets the profile name. Print the subscription without touching the database:

```sh
pgtun client --config ~/.config/pgtun/client.json --print-sub
```

## Self-hosting details

The fast path is `./scripts/bootstrap.sh --yes` (step 1). Manual:

```sh
cp .env.example .env
# edit POSTGRES_PASSWORD and PGTUN_PASSWORD
docker compose up -d --build
```

Compose runs three services:

- `certgen` — generates a self-signed certificate once into the `pgcerts` volume;
- `db` — `postgres:17` with `ssl=on`;
- `pgtun` — the egress server.

Open TCP `5432` in the firewall (the only inbound port). Check:

```sh
docker compose ps
docker compose logs -f pgtun
make doctor
```

The client connects to
`postgres://pgtun:<PGTUN_PASSWORD>@<vps>:5432/postgres?sslmode=require`.

## TLS

TLS is on by default: `certgen` creates a self-signed certificate (`CN=pgtun`,
10 years) in the `pgcerts` volume and PostgreSQL starts with `ssl=on`. Clients
use `sslmode=require` — traffic is encrypted, the certificate is not verified.

For stricter setups:

- `sslmode=verify-full` and distribute `server.crt` from the `pgcerts` volume
  to clients as a trusted root (`sslrootcert=...`); or
- Let's Encrypt with a real domain and a certificate for PostgreSQL.

## Security

- **Public PostgreSQL is a public database.** Use strong passwords
  (`POSTGRES_PASSWORD`, `PGTUN_PASSWORD`); `bootstrap.sh` generates them. Do not
  expose `5432` unless you need to.
- `pg_hba.conf` is locked down: remote connections are `hostssl` only, only for
  role `pgtun`, and everything else (including a remote superuser) is rejected.
  Do not weaken these rules.
- The TLS certificate is self-signed: `sslmode=require` encrypts but does **not**
  protect against MITM. Use `sslmode=verify-full` with `server.crt`, or Let's
  Encrypt.
- The Shadowsocks listener in the client scripts binds `0.0.0.0:8388`, i.e. it is
  reachable from the whole LAN and protected by the SS password only. Keep it
  behind a firewall or bind `127.0.0.1`.
- Tunnel data is stored in tables as plain bytes (TLS protects only transport).
  This is a PoC, not a data-protection tool.
- The client config contains the DSN and SS password; `client-setup` writes it
  `0600` (Windows restricts access with `icacls`).

## Troubleshooting

- **Port 5432 closed or busy.** Check `docker compose ps`, the firewall
  (`ufw status`, `firewall-cmd --list-ports`) and that nothing else listens on
  `5432` (`lsof -i :5432`). On a VPS open only TCP `5432`.
- **`sslmode=require` is required.** `pg_hba.conf` allows remote connections via
  `hostssl` only; without TLS (`sslmode=disable`) the server replies
  `no pg_hba.conf entry`. Always use `?sslmode=require` (or `verify-full`).
- **Wrong LAN IP in the subscription.** Set it explicitly:
  `pgtun client --public-host 192.168.1.10` (or `"public_host"` in the config /
  `--public-host` in `client-setup`).
- **Phone cannot connect over Shadowsocks.** Windows: allow inbound TCP `8388`.
  macOS: allow incoming connections for the binary in System Settings → Network
  → Firewall. The phone must be on the same LAN and see the host's IP on `8388`.
- **What to check first.** `make doctor` (containers, TLS, schema) and
  `./scripts/e2e.sh` (end-to-end on a throwaway PostgreSQL).
- **Client logs.** macOS: `tail -f ~/Library/Logs/pgtun/client.log` or
  `launchctl list | grep pgtun`. Linux: `journalctl --user -u pgtun-client -f`.
- **Database does not start after a password change.** Init scripts run only on
  an empty volume. For a clean rebuild: `docker compose down -v` (drops data)
  then `docker compose up -d --build`.

## Comparison

| Solution | Camouflage | Latency | Maturity |
|---|---|---|---|
| **pgtun** | real PostgreSQL (native protocol, TLS, schema, `LISTEN/NOTIFY`) | high: DB RTT + 40 ms polling, TCP-over-TCP | PoC, not audited |
| **AmneziaWG** | obfuscated WireGuard (UDP, junk packets, handshake params) | low | mature, popular in RU |
| **XHTTP / Reality** | TLS/HTTP of a real website | lower than pgtun | mature, actively developed |

Honestly: `pgtun` loses to AmneziaWG and XHTTP/Reality on latency and
resistance to volume analysis. Its niche is camouflage as legitimate SQL
workload against a real database — not maximum speed.

## Limitations

- **Rare direction.** The client sends a lot of traffic to `IP:5432`, which is
  unusual for a home network. This shows up in flow metrics.
- **Volume and shape.** Many short request/response pairs, a fixed polling
  cadence and unusual chunk sizes; on the DB side, table growth, WAL and
  autovacuum load.
- **TCP-over-TCP.** The tunnel carries TCP inside SQL; packet loss causes double
  retransmission and growing latency.
- **Latency.** Even with `NOTIFY` there is DB RTT plus a 40 ms polling tick; not
  suitable for interactive games or voice.
- **Not invisible.** It does not make you indistinguishable from a normal DB
  user and does not hide the SNI of the egress server's outbound traffic. TLS
  only makes the flow look like an encrypted DB connection.

## Status

PoC. Working: SOCKS5 CONNECT, Shadowsocks AEAD, stream multiplexing,
EOF/half-close, `LISTEN` reconnect, janitor, TLS on by default, subscription/QR.
Not implemented: UDP/QUIC, reclaiming lost streams, application-layer
encryption, metrics.

## Disclaimer

Laws on circumvention differ by country (including Russia) — make sure your use
is legal. You are responsible for how you use this software. It is not intended
for illegal activity or abuse. Do not expose PostgreSQL to the internet without
a password or a reason. Respect your hosting provider's ToS: unusual traffic to
a DBMS may violate it. This is a PoC: the code is not audited, use at your own
risk.

## License

MIT — see [LICENSE](LICENSE).
