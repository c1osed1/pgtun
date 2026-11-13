# pgtun

Туннель, который переносит SOCKS5/Shadowsocks-трафик через настоящую базу
PostgreSQL. База используется как почтовый ящик: клиент и egress-сервер
подключаются к одному и тому же PostgreSQL по штатному протоколу, складывают
данные в таблицы, а о новых порциях узнают через LISTEN/NOTIFY.

## Быстрый старт: 3 шага

### 1. Развернуть на VPS

Скопировать репозиторий на VPS и запустить bootstrap (Docker поставится сам):

```sh
scp -r pgtun root@vps:/opt/pgtun && ssh root@vps 'cd /opt/pgtun && ./scripts/bootstrap.sh --yes'
```

Скрипт поднимет `db` + egress + self-signed TLS, дождётся готовности и напечатает
DSN вида `postgres://pgtun:...@IP:5432/postgres?sslmode=require`. Этот DSN нужен
на следующем шаге. Единственный входящий порт — TCP 5432.

### 2. Подключить компьютер одним скриптом

| ОС | Команда |
|---|---|
| macOS / Linux | `./scripts/client-setup.sh --dsn "<DSN>"` |
| Windows (PowerShell) | `powershell -ExecutionPolicy Bypass -File .\scripts\client-setup.ps1 -Dsn "<DSN>"` |

Команды выполняются из корня репозитория на компьютере. macOS/Linux:

```sh
./scripts/client-setup.sh --dsn "<DSN>"
```

Windows:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\client-setup.ps1 -Dsn "<DSN>"
```

Скрипт собирает клиент (`~/.local/bin/pgtun` или
`%LOCALAPPDATA%\pgtun\pgtun.exe`), пишет конфиг с правами 600
(`~/.config/pgtun/client.json` или `%APPDATA%\pgtun\client.json`), ставит
автозапуск при входе в систему (launchd / systemd --user / Scheduled Task),
поднимает SOCKS5 на `127.0.0.1:1080` и Shadowsocks на `0.0.0.0:8388`, и сам
генерирует пароль Shadowsocks, если он не задан.

### 3. Подписка и команда для устройства

Скрипт печатает три вещи:

- `ss://`-ссылку;
- base64-подписку (одна строка);
- QR-код — отсканировать в HAPP (или вставить base64-подписку).

Там же будет готовая команда для этого устройства:

```sh
pgtun client --config <config>
```

Для macOS/Linux это `~/.local/bin/pgtun client --config ~/.config/pgtun/client.json`,
для Windows — `%LOCALAPPDATA%\pgtun\pgtun.exe client --config %APPDATA%\pgtun\client.json`.

Альтернатива без установки сервиса — `./scripts/make-sub.sh <host> 8388 chacha20-ietf-poly1305 '<password>' home`.
Полезные флаги `client-setup`:

- `--print-only` — собрать, записать конфиг и напечатать подписку, не ставя сервис;
- `--dry-run` — показать план (пути, менеджер сервиса, команды) и ничего не менять;
- `--uninstall` — остановить и удалить сервис, сохранив бинарник и конфиг.

## Disclaimer

Обход блокировок регулируется по-разному в разных странах (в том числе в РФ) —
убедитесь, что ваше использование законно. Ответственность за использование
лежит на пользователе. Проект не предназначен для незаконной деятельности и
любого абьюза. Не выставляйте PostgreSQL в интернет без пароля и без
необходимости. Учитывайте ToS облачного провайдера: нестандартный трафик к СУБД
может нарушать правила хостинга. Это PoC: код не аудирован, используйте на свой
риск.

## What it is

- `pgtun client` поднимает локальный SOCKS5 (и опционально Shadowsocks AEAD)
  listener. На каждое TCP-соединение создаётся строка в `pgtun.streams`, байты
  пишутся в `pgtun.chunks` (dir=0 — от клиента, dir=1 — от сервера).
- `pgtun server` (egress) опрашивает `pgtun.streams`, атомарно забирает новые
  стримы (`FOR UPDATE SKIP LOCKED`), делает `net.Dial` на целевой адрес и
  перекладывает байты между сокетом и таблицей.
- Пробуждение потребителя — `pg_notify('pgtun', <uuid>)` в той же транзакции,
  что и вставка чанка; LISTEN держит отдельное соединение pgx.
- NULL в `chunks.data` — EOF-маркер (half-close).

## Architecture

```
  приложение / HAPP
         |
         | SOCKS5 или Shadowsocks (локально)
         v
  +--------------+         PostgreSQL wire protocol + TLS
  | pgtun client |<--------------------------------------+
  +--------------+                                       |
                                                         v
                                            +------------------------+
                                            |  реальный PostgreSQL   |
                                            |  schema pgtun          |
                                            |  streams / chunks      |
                                            |  LISTEN/NOTIFY         |
                                            +------------------------+
                                                         ^
                                                         |
                                            +------------------------+
                                            |  pgtun server (egress) |
                                            +------------------------+
                                                         |
                                                         | TCP
                                                         v
                                                    интернет
```

## Why real PG

Это не имитация: клиент и сервер реально говорят с PostgreSQL, база реально
хранит данные, на активные пробы отвечает настоящий postgres (версия, ошибки,
TLS, аутентификация). DPI видит легитимный трафик к СУБД, а не самописный
протокол. Цена — накладные расходы и нестандартная модель угроз.

## Deploy on VPS

Быстрый путь — `./scripts/bootstrap.sh --yes` (см. шаг 1). Вручную:

```sh
cp .env.example .env
# отредактировать POSTGRES_PASSWORD и PGTUN_PASSWORD
docker compose up -d --build
```

Compose поднимает три сервиса:

- `certgen` — один раз генерирует self-signed сертификат в volume `pgcerts`;
- `db` — postgres:17 с включённым `ssl=on`;
- `pgtun` — egress-сервер.

Открыть в firewall TCP 5432 (единственный входящий порт). Проверить:

```sh
docker compose ps
docker compose logs -f pgtun
make doctor
```

Клиент подключается к `postgres://pgtun:<PGTUN_PASSWORD>@<vps>:5432/postgres?sslmode=require`.

## TLS

TLS включён по умолчанию: сервис `certgen` создаёт self-signed сертификат
(`CN=pgtun`, 10 лет) в volume `pgcerts`, PostgreSQL стартует с `ssl=on`.
Клиенты используют `sslmode=require` — трафик шифруется, сертификат не
проверяется.

Варианты строже:

- `sslmode=verify-full` + раздать клиентам `server.crt` из volume `pgcerts`
  как доверенный корень (`sslrootcert=...`), либо
- Let's Encrypt с реальным доменом и сертификатом для PostgreSQL.

## Config file

Флаги имеют приоритет над файлом; файл — над env/defaults. `client-setup`
создаёт такой файл автоматически. Пример `~/.config/pgtun/client.json`:

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

`"socks": ""` отключает SOCKS-листенер. `public_host` (или флаг
`--public-host`) переопределяет LAN/public IP, попадающий в подписку (по
умолчанию определяется автоматически), `tag` (или `--tag`) задаёт имя профиля.
Подписку можно напечатать без подключения к базе:

```sh
pgtun client --config ~/.config/pgtun/client.json --print-sub
```

## Client

Вручную, без установки сервиса:

```sh
go build -o pgtun ./cmd/pgtun
./pgtun client \
  --dsn "postgres://pgtun:tunnel_pass@VPS:5432/postgres?sslmode=require" \
  --socks 127.0.0.1:1080
```

Проверка:

```sh
curl --socks5-hostname 127.0.0.1:1080 -sS -o /dev/null -w '%{http_code}\n' https://example.com
```

## HAPP

HAPP (и любой клиент с поддержкой Shadowsocks) не умеет PostgreSQL, поэтому
клиент pgtun запускается на всегда включённом хосте в той же LAN, что и
телефон. `client-setup` делает это автоматически; вручную:

```sh
./pgtun client --dsn "postgres://pgtun:PASS@VPS:5432/postgres?sslmode=require" \
  --socks "" --ss 0.0.0.0:8388 --ss-password 'ss_password'
```

В STDOUT появятся `ss://`-ссылка, base64 подписки и QR — импортировать в HAPP.
Телефон должен иметь сетевой доступ к LAN-IP хоста на порту 8388.

## systemd: клиент как LAN-шлюз

`client-setup` на Linux ставит user-юнит в
`~/.config/systemd/user/pgtun-client.service` (без sudo). Для системного
сервиса есть шаблон `deploy/pgtun-client.service`:

```sh
install -m 0755 pgtun /usr/local/bin/pgtun
install -d -m 0755 /etc/pgtun
install -m 0600 client.json /etc/pgtun/client.json
cp deploy/pgtun-client.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now pgtun-client
journalctl -u pgtun-client -f
```

`ExecStart=/usr/local/bin/pgtun client --config /etc/pgtun/client.json`,
`Restart=always`, `RestartSec=3`, старт после `network-online.target`. В юните
есть комментарии по установке и опциональному hardening.

## Diagnostics

```sh
make doctor          # или ./scripts/doctor.sh
```

Проверяет: контейнеры `db`/`pgtun` запущены, соединение реально идёт по TLS
(`pg_stat_ssl`), схема `pgtun` доступна, чанк пишется/читается/удаляется.
Печатает PASS/FAIL по каждому пункту, exit code ≠ 0 при провале.

## Troubleshooting

- **Порт 5432 закрыт или занят.** Проверьте `docker compose ps`, firewall
  (`ufw status`, `firewall-cmd --list-ports`) и что порт не занят другим
  процессом (`lsof -i :5432`). На VPS откройте только TCP 5432.
- **`sslmode=require` обязателен.** `pg_hba.conf` разрешает удалённые
  подключения только через `hostssl`; без TLS (`sslmode=disable`) сервер
  ответит `no pg_hba.conf entry`. Клиент всегда должен использовать
  `?sslmode=require` (или `verify-full` с `server.crt`).
- **Неверный LAN IP в подписке.** Если автоопределение выбрало неправильный
  адрес, задайте его явно: `pgtun client --public-host 192.168.1.10` (или
  `"public_host"` в конфиге / `--public-host` в `client-setup`).
- **Телефон не подключается по Shadowsocks.** Windows: разрешите TCP 8388 во
  входящих правилах брандмауэра. macOS: разрешите входящие для бинарника в
  System Settings → Network → Firewall. Телефон должен быть в той же LAN и
  видеть IP хоста на порту 8388.
- **Что проверить в первую очередь.** `make doctor` — проверки контейнеров,
  TLS и схемы; `./scripts/e2e.sh` — сквозной прогон туннеля на временном
  PostgreSQL.
- **Логи сервиса клиента.** macOS: `tail -f ~/Library/Logs/pgtun/client.log`
  или `launchctl list | grep pgtun`. Linux:
  `journalctl --user -u pgtun-client -f` (для системного юнита —
  `journalctl -u pgtun-client -f`).
- **База не стартует после смены пароля.** Init-скрипты выполняются только на
  пустом volume. Для чистого пересоздания: `docker compose down -v` (удалит
  данные) и `docker compose up -d --build`.

## Сборка релизов

```sh
make release   # dist/pgtun-{darwin,linux}-{amd64,arm64}, pgtun-windows-amd64.exe
make fmt
```

## Security warnings

- Публичный PostgreSQL — это публичная СУБД. Обязательно сильные пароли
  (`POSTGRES_PASSWORD`, `PGTUN_PASSWORD`); `bootstrap.sh` генерирует их сам.
  Не выставляйте 5432 в интернет без необходимости.
- `pg_hba.conf` жёстко форсирует TLS: удалённые подключения разрешены только
  через `hostssl` и только роли `pgtun`, всё остальное (включая удалённого
  superuser) отклоняется. Не ослабляйте эти правила.
- TLS включён, но сертификат self-signed: `sslmode=require` шифрует канал, но
  **не защищает от MITM** (сертификат не проверяется). Для защиты от MITM
  используйте `sslmode=verify-full` с `server.crt` из volume `pgcerts` как
  `sslrootcert` или Let's Encrypt с реальным доменом.
- Shadowsocks-листенер в клиентских скриптах слушает `0.0.0.0:8388`, то есть
  доступен всей LAN и защищён только паролем SS. Держите порт за firewall или
  переключите на `127.0.0.1` с пробросом.
- Ограничьте `pg_hba.conf` (конкретные адреса клиентов, `scram-sha-256`),
  закройте 5432 от всего интернета, если клиентов можно перечислить.
- Если автоопределение LAN IP неверно, задайте адрес явно флагом
  `--public-host` (или `public_host` в конфиге), чтобы в подписку не попал
  чужой/недоступный адрес.
- Данные туннеля лежат в таблицах открытым текстом (TLS защищает только
  транспорт). Это PoC, не средство защиты данных.
- Роль `pgtun` не является superuser и не может создавать БД/роли; не выдавайте
  ей лишних прав.
- Конфиг клиента содержит DSN и пароль Shadowsocks: `client-setup` пишет его с
  правами 600 (на Windows ограничивает ACL через `icacls`).

## Сравнение

| Решение | Маскировка | Латентность | Зрелость |
|---|---|---|---|
| pgtun | под реальный PostgreSQL (штатный протокол, TLS, схема, LISTEN/NOTIFY) | высокая: RTT до базы + polling 40 мс, TCP-over-TCP | PoC, не аудирован |
| AmneziaWG | обфускация WireGuard (UDP, мусорные пакеты, параметры рукопожатия) | низкая | зрелое, популярно в РФ |
| XHTTP/Reality | маскировка под TLS/HTTP реального сайта | ниже pgtun | зрелое, активно развивается |

Честно: pgtun проигрывает AmneziaWG и XHTTP/Reality по latency и устойчивости
к анализу объёмов. Его ниша — камуфляж под легитимную SQL-нагрузку к реальной
СУБД, а не максимальная скорость.

## Detection & limitations

Честно:

- Редкость направления: клиент шлёт много трафика на IP:5432, которого нет в
  типичных профилях домашних сетей. Это заметно по flow-метрикам.
- Объём и форма поведения: множество коротких запросов/ответов, одинаковая
  периодичность polling, аномальные размеры чанков. По базе — рост таблиц,
  нагрузка на WAL и autovacuum.
- TCP-over-TCP: туннель несёт TCP внутри PostgreSQL-запросов; при потерях
  получается двойная ретрансмиссия и рост задержки.
- Задержка: даже с NOTIFY остаётся RTT до базы плюс 40 мс polling-тик; для
  интерактивных игр и голоса не подходит.
- Не магия: не даёт неотличимости от обычного пользователя БД, не скрывает
  SNI у внешнего трафика egress-сервера. TLS лишь делает поток похожим на
  шифрованное подключение к СУБД.
- Сравнение: AmneziaWG маскирует UDP-протокол, XHTTP/Reality маскируют TLS под
  реальный сайт. pgtun маскируется под SQL-нагрузку к конкретной СУБД и
  проигрывает им по latency и устойчивости к анализу объёмов.

## Status

PoC. Работает: SOCKS5 CONNECT, Shadowsocks AEAD, мультиплексирование стримов,
EOF/half-close, reconnection LISTEN, janitor, TLS-on-by-default, подписка/QR.
Не реализовано: UDP/QUIC, повторная выдача потерянных стримов, шифрование на
уровне приложения, метрики.

## License

MIT. Полный текст — в файле [LICENSE](LICENSE).
