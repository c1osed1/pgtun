# Changelog

## 0.2.0

- Первый публичный PoC: SOCKS5 CONNECT и Shadowsocks AEAD поверх реального
  PostgreSQL (schema `pgtun`, streams/chunks, LISTEN/NOTIFY).
- TLS включён по умолчанию: self-signed сертификат (сервис `certgen`),
  `hostssl` в `pg_hba.conf`, удалённый доступ разрешён только роли `pgtun`.
- Клиентские скрипты установки: `scripts/client-setup.sh` (macOS/Linux,
  launchd/systemd --user) и `scripts/client-setup.ps1` (Windows,
  Scheduled Task).
- Подписка SIP002 `ss://`, base64-подписка и QR-код для HAPP.
- `bootstrap.sh`, `doctor.sh`, docker compose стек (db + egress), сборка
  релизов с инъекцией версии через ldflags.
