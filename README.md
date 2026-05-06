# udisend

Децентрализованный P2P-мессенджер на Go. Без серверов, без регистрации, без хранения сообщений в сети. Текст, файлы и видеозвонки идут end-to-end зашифрованными напрямую между пирами через WebRTC. Кастомный сетевой слой (DHT + presence + signaling) нужен только для discovery и обмена SDP/ICE.

> **Статус:** MVP, ветка `feat/webui-remote-auth` (в работе). Phases 0–7 закрыты, Phase 8 (Hardening) — частично. См. `ROADMAP.md`. Авторитативный архитектурный документ — `p2p-messenger-design.md`.

---

## Что в коробке

- **End-to-end текстовые сообщения** через WebRTC DataChannel — verified integration-тестом.
- **File transfer** — offer + chunks + sha256 verify, end-to-end в коде.
- **Call signaling** — invite/accept/reject/end через DataChannel; рендеринг media (audio/video) — следующая итерация.
- **TOFU** + Signal-style fingerprint verification на уровне storage.
- **Outbox** для оффлайн-получателей: при следующем PeerOnline сообщения уйдут.
- **WebUI**: одна вкладка в браузере, embedded в `cmd/messenger` (HTML+CSS+JS, без сборщика). Сам WebRTC живёт в браузере; Go-runtime владеет только identity, presence, signaling и storage.
- **Хостинг на VPS** через `feat/webui-remote-auth`: passphrase + TOTP + Argon2id + session-cookie, Caddy как TLS-терминатор.
- **Volunteer STUN/TURN** — узлы с публичным IP автоматически обслуживают NAT traversal.
- **S/Kademlia hardening**: disjoint paths (3 параллельных), sibling lists, опциональный PoW на destination_hash.

Что **НЕ** ships в MVP: групповые чаты, mobile-клиенты, push-уведомления, onion routing для signaling, медиа-rendering на нативной стороне (только browser).

---

## Философия

1. **Нет single point of failure.** Никаких "наших" supernodes. Любой может стать частью инфраструктуры.
2. **Permissionless.** Регистрации нет; адрес = `destination_hash`, выводится из ключа.
3. **Независимость от автора.** После релиза сеть должна жить без обновлений и без меня.
4. **Равноправие узлов.** Различия только технические: публичный IP, аптайм.
5. **End-to-end content.** Содержимое сообщений, файлов, звонков — недоступно никому, кроме отправителя и получателя.

Что **сознательно** отдаём за это: централизованные хотфиксы, модерация/баны, гарантированная доставка оффлайн-сообщений (нет серверного хранилища).

---

## Архитектура

Двухслойная модель — кастомный signaling layer + WebRTC.

```
┌──────────────────────────────────────────────────────────────────┐
│  Application — chat client (JS в браузере)                       │
│  • Message types: text, file_offer/chunk, ack, typing,           │
│    call_signal, presence_ping                                    │
│  • Local SQLite: история, контакты, outbox, fingerprints         │
│  • TOFU + safety numbers                                         │
├──────────────────────────────────────────────────────────────────┤
│  WebRTC P2P session (browser-native)                             │
│  • DataChannel — SCTP-over-DTLS, reliable+ordered                │
│  • MediaStreamTrack — SRTP audio/video                           │
│  • DTLS ephemeral keys → per-session forward secrecy             │
├──────────────────────────────────────────────────────────────────┤
│  Signaling layer (custom, decentralized) — Go                    │
│  • Find peer via DHT presence по destination_hash                │
│  • Encrypted SDP/ICE exchange (Noise XK поверх DHT routing)      │
│  • SDP подписан Ed25519, DTLS-fingerprint cross-validated        │
├──────────────────────────────────────────────────────────────────┤
│  Discovery / DHT / Volunteer NAT services — Go                   │
│  • Kademlia + S/Kademlia (disjoint paths, sibling lists, PoW)    │
│  • Presence records: signed, ephemeral, TTL 60-120 s             │
│  • Capability roles: PublicIP, CanRelay, CanSTUN, CanTURN        │
│  • Bootstrap: curated community list + DNS-seeds + cache         │
├──────────────────────────────────────────────────────────────────┤
│  Identity                                                        │
│  • Ed25519 (sign) + X25519 (KEX)                                 │
│  • destination_hash = first 16 bytes of SHA-256(ed_pub‖x_pub)    │
└──────────────────────────────────────────────────────────────────┘
```

Все user-content (текст, файлы, голос/видео) идут напрямую через WebRTC после handshake. Signaling-сеть — тупая routing fabric, видит только зашифрованные blob'ы.

### Криптография

- **Ed25519** — подписи (presence, SDP).
- **X25519** — ECDH (signaling handshake, Noise XK).
- **ChaCha20-Poly1305** — AEAD для signaling.
- **BLAKE2b** — хеши.
- **HKDF** — key derivation.
- **WebRTC слой** — DTLS 1.3 (AES-GCM или ChaCha20-Poly1305), curve25519/P-256, всё внутри браузера.
- **Argon2id** (OWASP 64 MiB / 3 iter / 4 threads) — passphrase для webui auth.

### Threat model (выдержка)

| Атака | Защита | Статус |
|---|---|---|
| Eclipse на DHT | subnet-diverse bootstrap (/24 IPv4, /64 IPv6), curated community list, DNS-seeds | 🟡 partial |
| Sybil на nodeID | S/Kademlia + opt-in PoW на destination_hash | 🟡 opt-in |
| Sybil на presence | подпись + per-IP rate-limit (16 records/IP) | ✅ |
| MITM на signaling | Ed25519-подписанный SDP, проверяется до setRemoteDescription; канал — Noise XK | ✅ |
| MITM на первом контакте | TOFU + safety numbers в UI | 🟡 user discipline |
| Replay на signaling | Noise XK AEAD nonce + duplicate-HELLO_INIT drop | ✅ |
| DoS на узел | Per-IP token bucket (DHT + signaling делят сокет) | ✅ |
| TURN видит медиа | TURN видит только DTLS-байты, decrypt невозможен | ✅ by design |
| Public webui brute-force | Argon2id + 5/min + lockout 20 fails × 15 min, accumulating | ✅ |

Полная таблица — в `p2p-messenger-design.md` §8.

---

## Технологический стек

- **Go 1.26.** Используем generics, `iter.Seq`, `slices`/`maps`/`cmp`, `log/slog`, `errors.Join`, `atomic.Pointer[T]`, `testing/synctest`.
- **`modernc.org/sqlite`** — pure-Go SQLite. Бинарь без cgo.
- **`pion/stun`, `pion/turn`** — embedded NAT-traversal volunteers на узлах с публичным IP.
- **`flynn/noise`** — Noise XK для signaling-канала.
- **`golang.org/x/crypto`** — Ed25519, X25519, Argon2id.
- **HTTP bridge: SSE + REST** между браузером и Go (никакого WebSocket — `nhooyr.io/websocket` был заблокирован runtime-permission'ами).
- **Никакого WebRTC в Go** для messenger-бинаря (был `pion/webrtc` в v1, ушёл в v2 ради уменьшения бинаря на ~30 MB и нормального video render).
- **Caddy** (внешний бинарь, опционально) — TLS-терминатор для VPS-хостинга. См. ниже.

---

## Сборка

Go 1.26+ обязателен.

```bash
git clone https://github.com/udisondev/udisend
cd udisend
go build ./cmd/messenger ./cmd/network    # или: task build → ./bin/messenger, ./bin/network
```

Проект использует [go-task](https://taskfile.dev) для dev-команд. Установка не обязательна — все цели тривиально воспроизводятся через `go build` / `go test`. С task-ом:

```bash
task               # vet + race tests + lint (default цель)
task build         # сборка обоих бинарей в ./bin/
task test          # юниты
task test:race     # под -race
task test:integration   # с тегом integration
task test:e2e      # с тегом e2e
task test:fuzz -- ./pkg/identity   # fuzz одной пакета на 30 секунд
task lint          # golangci-lint
task cover         # coverage report
task bench         # все бенчмарки
task build:reproducible OS=linux ARCH=amd64 VERSION=v0.1.0
task release:sign                   # minisign / cosign
```

CI (`.github/workflows/`) гоняет vet + race + lint на push/PR.

---

## Quick start: трёх-узловая локальная демка

Запусти три мессенджера на одной машине; они найдут друг друга через локальный DHT.

```bash
task run:alice    # rendezvous: UDP :9100, HTTP :7100
task run:bob      # bootstraps from alice: UDP :9101, HTTP :7101
task run:carol    # bootstraps from alice: UDP :9102, HTTP :7102
```

Без `task`:

```bash
mkdir -p ~/.config/udisend/alice ~/.config/udisend/bob

./messenger -identity ~/.config/udisend/alice.key \
            -storage ~/.config/udisend/alice \
            -listen 127.0.0.1:9100 -http 127.0.0.1:7100 -v &

./messenger -identity ~/.config/udisend/bob.key \
            -storage ~/.config/udisend/bob \
            -listen 127.0.0.1:9101 -http 127.0.0.1:7101 \
            -bootstrap 127.0.0.1:9100 -v &
```

Каждый мессенджер на старте печатает баннер с URL вида `http://127.0.0.1:7100/?token=...`. Открой его в браузере. Открой второй URL во второй вкладке/браузере. В первой вкладке Alice добавляет Bob по его `destination_hash` (виден в баннере / в шапке UI второго мессенджера). Через несколько секунд presence сходится, и сообщения летают.

Сброс одного пира:

```bash
task reset:bob          # удаляет identity + storage этого пира
task reset:all          # удаляет всё под ~/.config/udisend
```

---

## Self-hosting на VPS

Сценарий: ты хочешь зайти в свой мессенджер с любого устройства, не таская SSH-туннель. Identity-ключ при этом живёт на VPS — это сознательный tradeoff: root на VPS = identity скомпрометирован.

Полная инструкция и алтернативы (sslip.io вместо домена, self-signed cert, SSH-tunnel, Tailscale) — в decisions log `ROADMAP.md` и в коммит-сообщениях ветки `feat/webui-remote-auth`. Краткая версия:

### 1. Поставь passphrase

```bash
messenger -set-password
# Passphrase: ········  (≥ 12 chars; argon2id-hash в SQLite)
# Confirm:    ········
```

### 2. (Рекомендую) Включи TOTP

```bash
messenger -setup-totp
# Печатает otpauth://-URL и base32-secret для manual entry в твою
# authenticator app (Google Authenticator / Authy / 1Password / etc).
# Введи 6-значный код для подтверждения, получи 10 recovery-кодов.
# СОХРАНИ ИХ — они показываются только один раз.
```

`messenger -reset-totp` снимает TOTP, passphrase остаётся.

### 3. Поставь Caddy на VPS

`/etc/caddy/Caddyfile`:

```
messenger.example.com {
    reverse_proxy localhost:9000
}
```

`sudo systemctl reload caddy`. Caddy сам выпускает Let's Encrypt cert и продлевает.

### 4. Запусти messenger в public mode

```bash
messenger \
  -http 127.0.0.1:9000 \
  -listen 0.0.0.0:9001 \
  -trust-proxy \
  -public-host messenger.example.com
```

- `-http 127.0.0.1:9000` — webui слушает только loopback; Caddy ходит к нему через localhost.
- `-listen 0.0.0.0:9001` — UDP-порт DHT/signaling, открой его в firewall.
- `-trust-proxy` — доверяем `X-Forwarded-For` от Caddy для honest client-IP в rate-limit/audit.
- `-public-host` — обязателен в public mode; используется для same-origin-check на /login и для печати URL.

Альтернатива Caddy: `-tls-cert path -tls-key path` (in-process TLS, без прокси).

### 5. Зайди

`https://messenger.example.com/` → редирект на `/login` → форма passphrase (+TOTP) → cookie `udisend_session` (HttpOnly+Secure+SameSite=Strict, 30 дней rolling) → UI.

systemd-юнит-пример — см. инструкцию ниже на странице, либо в чате с автором.

### Что произойдёт, если...

| Сценарий | Реакция |
|---|---|
| Не запустил `-set-password` перед public mode | Fail-fast при старте: "non-loopback HTTP bind requires an existing passphrase" |
| Не задал ни `-tls-cert`, ни `-trust-proxy` | Fail-fast: "non-loopback HTTP bind requires TLS" |
| Не задал `-public-host` в public mode | Fail-fast: "non-loopback HTTP bind requires -public-host" |
| 5 неудачных попыток за минуту | 429 Too Many Requests + `Retry-After` |
| 20 неудач подряд | IP заблокирован 15 минут; каждая дополнительная неудача продлевает lockout (асимптот ≈1 попытка / lockout duration) |
| Потерял phone | Recovery-код вместо TOTP. Использованный помечается consumed_at, второй раз не пройдёт. После входа — `messenger -setup-totp` для нового набора. |
| Забыл passphrase | SSH на VPS → `messenger -set-password`. TOTP остаётся. |

---

## CLI reference

### `messenger` — клиент-мессенджер

```
Auth subcommands (выполняются и выходят):
  -set-password            интерактивный prompt для passphrase (Argon2id)
  -setup-totp              сгенерировать TOTP secret + recovery codes
  -reset-totp              удалить TOTP secret и recovery codes

Network/storage:
  -identity <path>         путь к seed identity (default ~/.config/udisend/messenger.key)
  -storage  <dir>          директория SQLite + downloads
  -listen   <host:port>    UDP listen для DHT/signaling (default 127.0.0.1:0)
  -bootstrap <host:port>   peer для bootstrap (можно несколько; пусто = community defaults)

WebUI:
  -http <host:port>        HTTP listen (default 127.0.0.1:0; loopback = token-mode)
  -open                    открыть UI в браузере на старте (default true; в public mode игнорируется)

Public-mode (требуется при -http != loopback):
  -public-host <hostname>  внешний hostname (REQUIRED)
  -trust-proxy             доверять X-Forwarded-For от reverse proxy (нужен один из:
                           -trust-proxy ИЛИ -tls-cert+-tls-key)
  -tls-cert <path>         PEM сертификат для in-process TLS
  -tls-key  <path>         PEM приватный ключ

Misc:
  -v                       verbose logs (slog Debug)
```

### `network` — публичный узел (relay + bootstrap, опционально STUN/TURN)

Без чат-логики, без webui, без storage. Сидит в DHT, маршрутизирует signaling, опционально обслуживает NAT traversal.

```
  -identity <path>         путь к seed identity (default ~/.config/udisend/network.key)
  -listen <host:port>      UDP listen (default :9000)
  -bootstrap <host:port>   peer для seed routing table (можно несколько)

Volunteer NAT services (только при заданном -public-ip):
  -public-ip <addr>        реклама STUN/TURN на этом IP
  -stun <host:port>        STUN listen (default :3478)
  -turn <host:port>        TURN listen (default :3479)
  -turn-secret <hex>       shared secret для RFC 7635 ephemeral credentials
                           (omit = TURN отключён)

  -v                       verbose logs
```

Пример VPS-узла с STUN+TURN:

```bash
network -listen :9000 \
        -public-ip 203.0.113.7 \
        -turn-secret $(openssl rand -hex 32) \
        -v
```

---

## Project layout

```
udisend/
├── cmd/
│   ├── messenger/        # бинарь клиента (identity + DHT + signaling + webui)
│   └── network/          # бинарь публичного узла (relay/bootstrap, без UI)
│
├── pkg/                  # переиспользуемая логика; не импортирует internal/
│   ├── identity/         # Ed25519 + X25519, destination_hash, PoW
│   ├── crypto/           # AEAD/KDF/hash обёртки над std lib
│   ├── transport/        # UDP transport
│   ├── wire/             # uvarint-prefixed binary wire format + fuzz
│   ├── noise/            # Noise XK для signaling
│   ├── dht/              # Kademlia + S/Kademlia (disjoint paths, siblings)
│   ├── presence/         # подписанные эфемерные records + per-IP rate-limit
│   ├── signaling/        # encrypted SDP/ICE exchange поверх DHT routing
│   ├── webrtc/           # SignedSDP (Sign/Verify Ed25519 над SDP)
│   ├── stun/             # pion/stun обёртка для volunteer
│   ├── turn/             # pion/turn обёртка + RFC 7635 ephemeral creds
│   ├── ratelimit/        # per-IP token bucket
│   ├── bootstrap/        # community list + DNS-seeds resolver
│   └── network/          # opaque Node — собирает DHT/signaling/transport/STUN/TURN
│
├── internal/             # клей и проектная специфика; импортирует pkg/ свободно
│   ├── config/           # LoadOrCreateIdentity, defaults
│   ├── messenger/        # Messenger high-level API над pkg/network
│   ├── storage/          # SQLite (contacts, history, outbox, seen_peers, auth_*)
│   └── httpui/           # HTTP+SSE bridge + embedded HTML/CSS/JS
│       ├── assets/       # index.html, app.js, app.css (embedded via go:embed)
│       └── auth/         # Argon2id, TOTP, sessions, rate-limit, recovery codes
│
├── review/               # post-phase code review артефакты
├── p2p-messenger-design.md   # AUTHORITATIVE архитектурный документ
├── ROADMAP.md            # фазовый трекер + decisions log
├── CLAUDE.md             # правила для Claude Code (TDD, Go style, layout)
├── ASSUMPTIONS.md        # overnight-run cuts и known gaps
└── Taskfile.yml          # dev-команды (go-task)
```

### Layering rules (см. `CLAUDE.md`)

- `cmd/*` — минимально: парсинг флагов + вызов `internal/<app>.Run(ctx, cfg)`.
- `pkg/*` — самодостаточно, переиспользуемо, не импортирует `internal/*`, не упоминает "messenger"/"udisend" в API.
- `internal/*` — клей + проектная специфика; свободно импортирует `pkg/*` и другие `internal/*`.
- Никаких cyclic deps.

---

## Testing & quality

TDD обязателен — каждая фича пишется начиная с failing-теста (см. `CLAUDE.md`).

- **Unit-тесты:** table-driven, `t.Parallel()` везде где нет shared mutable state, `t.Cleanup` вместо defer для фикстур, `t.Helper()` на хелперах.
- **Integration / E2E:** build-теги `integration` / `e2e`, `testing/synctest` для детерминизированной concurrency, `testcontainers-go` где реально нужно (in-memory fakes — предпочтительно).
- **Fuzz:** wire format, identity, парсинг — `task test:fuzz -- ./pkg/wire`.
- **`-race`** обязателен в CI на concurrency-критичный код.
- **Покрытие:** не гонимся за %, но каждая публичная функция в `pkg/` имеет тесты.

Stylistic invariants (`CLAUDE.md`):

- Sentinel errors (`var ErrFoo = errors.New(...)`); никаких string-сравнений сообщений.
- `slog` для логов, не `log` / `zap` / `zerolog`.
- `context.Context` везде где есть IO или cancel.
- Никаких `panic` в библиотечном коде кроме программерских ошибок.
- Минимум комментариев — только когда WHY неочевиден; godoc-стиль на публичных API.

Post-phase code review — обязательно перед закрытием фазы (3 независимых итерации, MUST FIX = unanimous, см. `CLAUDE.md`). Артефакты в `review/`.

---

## Поучаствовать

См. `ROADMAP.md` — текущая фаза и acceptance criteria. `CLAUDE.md` — правила кодинга. `p2p-messenger-design.md` — архитектурные инварианты.

Workflow: feature branches без PR в этом репо, push в `main` запрещён, авторство в коммитах — только пользователь (никаких `Co-Authored-By: assistant` подписей).

---

## Лицензия

TBD. План — пермиссивная (MIT / Apache 2.0 / BSD-3) специально чтобы не ограничивать форки и независимые реализации (см. `p2p-messenger-design.md` §7).
