# pgtun

**Проводит трафик SOCKS5 / Shadowsocks через настоящую базу PostgreSQL.**

[![CI](https://github.com/c1osed1/pgtun/actions/workflows/ci.yml/badge.svg)](https://github.com/c1osed1/pgtun/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go 1.27+](https://img.shields.io/badge/go-1.27%2B-00ADD8.svg)](go.mod)
[![status: PoC](https://img.shields.io/badge/status-PoC-orange.svg)](#статус)

[English](README.md) · **Русский**

> Proof of concept, код не аудирован. Перед публикацией в интернет прочитайте
> [Дисклеймер](#дисклеймер) и [Безопасность](#безопасность).

---

## Зачем

DPI опознаёт самописные VPN-протоколы с первого пакета. `pgtun` не изобретает
протокол, а использует **настоящий штатный PostgreSQL** как транспорт: база
отвечает на активные пробы подлинным PG-рукопожатием (TLS, `scram-sha-256`,
реальные ошибки), а трафик едет внутри обычных SQL-операций и
`LISTEN/NOTIFY`. На проводе это похоже на TLS-подключение к базе — потому что
это оно и есть.

Это **не магия**. Латентность высокая by design, поведенческая детекция всё ещё
возможна — см. [Ограничения](#ограничения).

## Архитектура

```
   приложение / HAPP
        │  SOCKS5 / Shadowsocks (локально)
        ▼
 ┌──────────────┐                          ┌──────────────────────────┐
 │ pgtun client │ ─── PG wire + TLS ─────▶ │  реальный PostgreSQL     │
 └──────────────┘                          │  schema pgtun            │
                                           │  streams / chunks        │
 ┌──────────────┐                          │  LISTEN / NOTIFY         │
 │ pgtun server │ ◀── PG wire + TLS ─────▶ │                          │
 │   (egress)   │                          └──────────────────────────┘
 └──────┬───────┘
        │ TCP
        ▼
     интернет
```

База — это почтовый ящик. Клиент и egress-сервер подключаются к **одному и тому
же** PostgreSQL по штатному протоколу, обмениваются байтами через таблицы и
будят друг друга через `pg_notify`.

## Быстрый старт: 3 шага

### 1. Развернуть на VPS

Скопировать репозиторий и запустить bootstrap (Docker поставится сам):

```sh
scp -r pgtun root@vps:/opt/pgtun && ssh root@vps 'cd /opt/pgtun && ./scripts/bootstrap.sh --yes'
```

Скрипт поднимет `db` + egress + self-signed TLS, дождётся готовности и напечатает
DSN вида `postgres://pgtun:...@IP:5432/postgres?sslmode=require`. Единственный
входящий порт — TCP `5432`.

### 2. Подключить компьютер одним скриптом

| ОС | Команда |
|---|---|
| macOS / Linux | `./scripts/client-setup.sh --dsn "<DSN>"` |
| Windows (PowerShell) | `powershell -ExecutionPolicy Bypass -File .\scripts\client-setup.ps1 -Dsn "<DSN>"` |

Запускать из корня репозитория на компьютере, который будет шлюзом. Скрипт
собирает клиент, пишет конфиг `0600`, ставит автозапуск (launchd /
`systemd --user` / Scheduled Task), поднимает SOCKS5 на `127.0.0.1:1080` и
Shadowsocks на `0.0.0.0:8388`, и сам генерирует пароль SS.

### 3. Подписка и команда для устройства

Скрипт печатает:

- `ss://`-ссылку;
- base64-подписку одной строкой;
- QR-код для сканирования в HAPP (или вставки подписки).

Там же — готовая команда для этого устройства:

```sh
pgtun client --config <config>
```

Полезные флаги `client-setup`:

| Флаг | Что делает |
|---|---|
| `--print-only` | собрать, записать конфиг, напечатать подписку, не ставя сервис |
| `--dry-run` | показать план, ничего не менять |
| `--uninstall` | остановить и удалить сервис (бинарник и конфиг остаются) |
| `--public-host <ip>` | переопределить LAN/public IP в подписке |

Или сгенерировать подписку вручную:

```sh
./scripts/make-sub.sh 192.168.1.10 8388 chacha20-ietf-poly1305 'ss_password' home
```

## Возможности

- **Настоящая маскировка** — штатный PostgreSQL, реальный TLS, реальная
  аутентификация и схема; никакого самописного формата.
- **Фронтенды SOCKS5 и Shadowsocks AEAD** на клиенте.
- **Мультиплексирование стримов** с half-close (`NULL`-чанк = EOF).
- **Пробуждение через `LISTEN/NOTIFY`** с polling-фолбэком.
- **TLS по умолчанию** — `certgen` создаёт self-signed серт, PostgreSQL
  стартует с `ssl=on`, а `pg_hba.conf` разрешает только `hostssl`.
- **Деплой одной командой** с автогенерацией секретов и health-проверкой `doctor`.
- **Подписка + QR** для HAPP и любого Shadowsocks-клиента.
- **CI** на Linux и macOS, unit-тесты и самоподнимающийся TLS-e2e.

## Как это работает

- `pgtun client` поднимает локальный SOCKS5 (и опционально Shadowsocks) listener.
  На каждое TCP-соединение создаётся строка в `pgtun.streams`, байты пишутся в
  `pgtun.chunks` (`dir=0` — клиент→сервер, `dir=1` — сервер→клиент).
- `pgtun server` (egress) опрашивает `pgtun.streams`, атомарно забирает новые
  стримы (`FOR UPDATE SKIP LOCKED`), делает `net.Dial` на цель и перекладывает
  байты между сокетом и таблицей.
- Потребитель просыпается по `pg_notify('pgtun', <uuid>)` в той же транзакции,
  что и вставка чанка; отдельное pgx-соединение держит `LISTEN`.
- `NULL` в `chunks.data` — EOF-маркер (half-close).

## Клиент

Вручную, без установки сервиса:

```sh
go build -o pgtun ./cmd/pgtun
./pgtun client \
  --dsn "postgres://pgtun:PASS@VPS:5432/postgres?sslmode=require" \
  --socks 127.0.0.1:1080
```

Проверка:

```sh
curl --socks5-hostname 127.0.0.1:1080 -sS -o /dev/null -w '%{http_code}\n' https://example.com
```

После публикации репозитория можно ставить через Go:

```sh
go install github.com/c1osed1/pgtun/cmd/pgtun@latest
```

## HAPP / телефон

HAPP и другие Shadowsocks-клиенты не умеют PostgreSQL, поэтому клиент `pgtun`
запускается на всегда включённом хосте в той же LAN, что и телефон.
`client-setup` делает это автоматически. Вручную:

```sh
./pgtun client --dsn "postgres://pgtun:PASS@VPS:5432/postgres?sslmode=require" \
  --socks "" --ss 0.0.0.0:8388 --ss-password 'ss_password'
```

В STDOUT появятся `ss://`-ссылка, base64-подписка и QR. Телефон должен видеть
LAN-IP хоста на порту `8388`.

## Конфиг

Флаги приоритетнее файла; файл — приоритетнее env/defaults. `client-setup`
создаёт его автоматически. Пример `~/.config/pgtun/client.json`:

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

`"socks": ""` отключает SOCKS-листенер. `public_host` (или `--public-host`)
переопределяет LAN/public IP в подписке; `tag` (или `--tag`) задаёт имя профиля.
Подписку можно напечатать без подключения к базе:

```sh
pgtun client --config ~/.config/pgtun/client.json --print-sub
```

## Self-hosting

Быстрый путь — `./scripts/bootstrap.sh --yes` (шаг 1). Вручную:

```sh
cp .env.example .env
# отредактировать POSTGRES_PASSWORD и PGTUN_PASSWORD
docker compose up -d --build
```

Compose поднимает три сервиса:

- `certgen` — один раз генерирует self-signed сертификат в volume `pgcerts`;
- `db` — `postgres:17` с включённым `ssl=on`;
- `pgtun` — egress-сервер.

Открыть в firewall TCP `5432` (единственный входящий порт). Проверить:

```sh
docker compose ps
docker compose logs -f pgtun
make doctor
```

Клиент подключается к
`postgres://pgtun:<PGTUN_PASSWORD>@<vps>:5432/postgres?sslmode=require`.

## TLS

TLS включён по умолчанию: `certgen` создаёт self-signed сертификат (`CN=pgtun`,
10 лет) в volume `pgcerts`, PostgreSQL стартует с `ssl=on`. Клиенты используют
`sslmode=require` — трафик шифруется, сертификат не проверяется.

Варианты строже:

- `sslmode=verify-full` и раздать клиентам `server.crt` из volume `pgcerts`
  как доверенный корень (`sslrootcert=...`); либо
- Let's Encrypt с реальным доменом и сертификатом для PostgreSQL.

## Безопасность

- **Публичный PostgreSQL — это публичная СУБД.** Обязательно сильные пароли
  (`POSTGRES_PASSWORD`, `PGTUN_PASSWORD`); `bootstrap.sh` генерирует их сам. Не
  выставляйте `5432` в интернет без необходимости.
- `pg_hba.conf` жёстко закрыт: удалённые подключения — только `hostssl` и только
  для роли `pgtun`, всё остальное (включая удалённого superuser) отклоняется.
  Не ослабляйте эти правила.
- Сертификат self-signed: `sslmode=require` шифрует, но **не защищает от MITM**.
  Для защиты используйте `sslmode=verify-full` с `server.crt` или Let's Encrypt.
- Shadowsocks-листенер в клиентских скриптах слушает `0.0.0.0:8388`, то есть
  доступен всей LAN и защищён только паролем SS. Держите его за firewall или
  переключите на `127.0.0.1`.
- Данные туннеля лежат в таблицах открытым текстом (TLS защищает только
  транспорт). Это PoC, не средство защиты данных.
- Конфиг клиента содержит DSN и пароль SS: `client-setup` пишет его `0600` (на
  Windows доступ ограничивается через `icacls`).

## Troubleshooting

- **Порт 5432 закрыт или занят.** Проверьте `docker compose ps`, firewall
  (`ufw status`, `firewall-cmd --list-ports`) и что порт не занят другим
  процессом (`lsof -i :5432`). На VPS откройте только TCP `5432`.
- **`sslmode=require` обязателен.** `pg_hba.conf` разрешает удалённые
  подключения только через `hostssl`; без TLS (`sslmode=disable`) сервер
  ответит `no pg_hba.conf entry`. Всегда используйте `?sslmode=require` (или
  `verify-full`).
- **Неверный LAN IP в подписке.** Задайте явно:
  `pgtun client --public-host 192.168.1.10` (или `"public_host"` в конфиге /
  `--public-host` в `client-setup`).
- **Телефон не подключается по Shadowsocks.** Windows: разрешите входящий TCP
  `8388`. macOS: разрешите входящие для бинарника в System Settings → Network →
  Firewall. Телефон должен быть в той же LAN и видеть IP хоста на `8388`.
- **Что проверить первым.** `make doctor` (контейнеры, TLS, схема) и
  `./scripts/e2e.sh` (сквозной прогон на временном PostgreSQL).
- **Логи клиента.** macOS: `tail -f ~/Library/Logs/pgtun/client.log` или
  `launchctl list | grep pgtun`. Linux: `journalctl --user -u pgtun-client -f`.
- **База не стартует после смены пароля.** Init-скрипты выполняются только на
  пустом volume. Для чистого пересоздания: `docker compose down -v` (удалит
  данные), затем `docker compose up -d --build`.

## Сравнение

| Решение | Маскировка | Латентность | Зрелость |
|---|---|---|---|
| **pgtun** | под реальный PostgreSQL (штатный протокол, TLS, схема, `LISTEN/NOTIFY`) | высокая: RTT до базы + polling 40 мс, TCP-over-TCP | PoC, не аудирован |
| **AmneziaWG** | обфускация WireGuard (UDP, мусорные пакеты, параметры рукопожатия) | низкая | зрелое, популярно в РФ |
| **XHTTP / Reality** | маскировка под TLS/HTTP реального сайта | ниже pgtun | зрелое, активно развивается |

Честно: `pgtun` проигрывает AmneziaWG и XHTTP/Reality по latency и устойчивости
к анализу объёмов. Его ниша — камуфляж под легитимную SQL-нагрузку к реальной
СУБД, а не максимальная скорость.

## Ограничения

- **Редкость направления.** Клиент шлёт много трафика на `IP:5432`, чего нет в
  типичных профилях домашних сетей. Это заметно по flow-метрикам.
- **Объём и форма.** Множество коротких запросов/ответов, одинаковая
  периодичность polling, аномальные размеры чанков; по базе — рост таблиц,
  нагрузка на WAL и autovacuum.
- **TCP-over-TCP.** Туннель несёт TCP внутри SQL; при потерях — двойная
  ретрансмиссия и рост задержки.
- **Задержка.** Даже с `NOTIFY` остаётся RTT до базы плюс 40 мс polling-тик; для
  интерактивных игр и голоса не подходит.
- **Не невидимка.** Не даёт неотличимости от обычного пользователя БД и не
  скрывает SNI исходящего трафика egress-сервера. TLS лишь делает поток похожим
  на шифрованное подключение к СУБД.

## Статус

PoC. Работает: SOCKS5 CONNECT, Shadowsocks AEAD, мультиплексирование стримов,
EOF/half-close, reconnect `LISTEN`, janitor, TLS по умолчанию, подписка/QR.
Не реализовано: UDP/QUIC, перевыдача потерянных стримов, шифрование на уровне
приложения, метрики.

## Дисклеймер

Обход блокировок регулируется по-разному в разных странах (в том числе в РФ) —
убедитесь, что ваше использование законно. Ответственность за использование
лежит на пользователе. Проект не предназначен для незаконной деятельности и
абьюза. Не выставляйте PostgreSQL в интернет без пароля и необходимости.
Учитывайте ToS облачного провайдера: нестандартный трафик к СУБД может нарушать
правила хостинга. Это PoC: код не аудирован, используйте на свой риск.

## Лицензия

MIT — см. [LICENSE](LICENSE).
