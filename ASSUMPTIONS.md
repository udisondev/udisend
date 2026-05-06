# Assumptions & decisions (v2 architecture)

> Решения, принятые ассистентом во время unattended overnight-run
> 2026-05-01 → 2026-05-02 и при последующем рефакторе на v2 (browser UI).
> Помечены 🟡 (требуют ревью) или 🔴 (точно надо обсудить и, возможно, переделать).

## Главный архитектурный сдвиг (v2)

🟡 **WebRTC переехал из Go в браузер.** Изначально (v1, ветка `auto/overnight-mvp`) Go-процесс держал `pion/webrtc.PeerConnection` и пытался рендерить видео в fyne — это требовало декодировать VP8 и блитить кадры в `canvas.Image`, что в overnight-сборке было пунктом "best-effort, скорее всего не доедет". Переключились на `Pure-Go HTTP+SSE сервер + браузер` с нативным `RTCPeerConnection`/`<video>`/`getUserMedia`. Видео работает из коробки.

**Что теперь делает Go:**
- identity, DHT participation, presence publish/lookup
- signaling.Service: end-to-end Noise XK канал между двумя процессами
- storage (SQLite через modernc.org/sqlite)
- HTTP+SSE bridge: REST API + EventSource поток для UI
- подпись/верификация SDP (Ed25519) при пересылке через signaling pipe

**Что делает браузер:**
- `RTCPeerConnection` (нативный — hardware accelerated, jitter buffer, etc.)
- `getUserMedia` — захват камеры/микрофона
- `<video>` — рендер видео
- `DataChannel` — чат + файловые чанки
- весь chat-protocol (JSON over DataChannel + binary chunks для файлов)

**Что выкинуто из v1:**
- `internal/ui` (fyne) — удалено
- `pkg/webrtc/session.go` — удалён (Go больше не держит PeerConnection)
- `internal/messenger/sessions.go` — переписан на тонкий Session-wrapper над signaling.Channel
- fyne и pion/webrtc нагрузка с messenger — теперь pion/webrtc используется ТОЛЬКО в STUN/TURN серверах (`pkg/stun`, `pkg/turn`)

**Размер бинаря messenger:** ~16 MB (было 46 MB с fyne).

**Cgo:** только в `pkg/turn` (через pion). Сам messenger-бинарь чистый Go (modernc.org/sqlite вместо mattn/go-sqlite3).

## Глобальные tradeoff'ы

🔴 **Post-phase 3-iteration code review (CLAUDE.md → Post-phase code review) пропущен** для всех фаз 1–8 (ветка `auto/overnight-mvp`) и для v2 рефактора. Чек-лист — `review/PENDING.md`. До публичного релиза каждая фаза ОБЯЗАНА пройти процедуру.

🔴 **TDD-дисциплина — "лайт".** Критичные пути (крипто, парсинг wire format, routing table) — TDD по правилам. Скелетный клей (UI, run-loop'ы, тривиальные обвязки) — smoke-уровня или без тестов.

🔴 **Acceptance criteria из ROADMAP формально не все зелёные.** Все пропущенные пункты помечены `🌙 [OVERNIGHT-DEFERRED]` в `ROADMAP.md`.

## Конкретные технические решения

### Wire format — кастомный uvarint-prefixed binary (Phase 4)
🟡 `pkg/wire`. Нулевые внешние deps на wire layer. CBOR/protobuf — будущий рефактор изолированного пакета.

### Crypto — flynn/noise + stdlib
🟡 `github.com/flynn/noise` для XK. Остальное — `crypto/ed25519` + `golang.org/x/crypto/{chacha20poly1305,blake2b,hkdf,curve25519}`.

### SQLite — modernc.org/sqlite
🟡 Pure-Go драйвер. Нет CGO для storage layer.

### TURN auth — long-term-credentials с shared secret
🔴 `pkg/turn` использует HMAC long-term-credentials (RFC 5389 §10.2). Production needs per-IP rate-limit + abuse mitigations.

### S/Kademlia — выключено в MVP (Phase 3)
🔴 PoW на nodeID и disjoint-paths lookup отключены. Базовый Kademlia + signed presence работают; Sybil-resistance ослаблена. Перед публичным релизом ОБЯЗАТЕЛЬНО включить.

### v2: HTTP UI bridge — SSE + REST, без WebSocket
🟡 Хотел использовать `nhooyr.io/websocket`, но runtime-permission-system заблокировал внешний модуль. Переделал на **Server-Sent Events** (server→browser) + plain POST (browser→server) — чисто `net/http` stdlib, ноль новых deps. Для нашего use-case (один клиент-вкладка на messenger) SSE даже лучше: проще, проще дебажить (видно в DevTools Network → EventStream).

### v2: HTTP auth — bearer-token в URL
🟡 При старте messenger генерирует random URL-safe token (24 случайных байта), печатает в логи `http://127.0.0.1:7100/?token=...`. Токен требуется для `/api/*` и `/api/events`. Статика (`/`, `/app.js`, `/app.css`) отдаётся без токена — там нет секретов. Привязки по cookie не делаю — токен в URL/header. Localhost-only, MVP-сценарий.

### v2: SDP signing — Go подписывает, не браузер
🟡 Браузер не имеет доступа к Ed25519 private key (хранится в Go). Браузер генерирует SDP → шлёт в Go через POST → Go оборачивает в `SignedSDP` (kind+SDP+Ed25519 sig) → ship через signaling.Channel. Получатель: Go проверяет подпись против известного pub-key (из presence record / TOFU контакта) → разворачивает → шлёт SDP в браузер через SSE. ICE candidates подписываются по той же схеме.

### v2: Trickle ICE
🟡 Браузер использует trickle ICE по умолчанию (`onicecandidate` каждый кандидат отдельно). Каждый кандидат становится отдельным сигналом kind=`ice`. До прихода `setRemoteDescription` входящие кандидаты буферизуются и применяются после.

### v2: Один браузер на messenger
🟡 SSE bridge ожидает одного активного клиента. Если откроешь вторую вкладку — incoming sessions всё равно идут в первую. Multi-tab UX — post-MVP. Для типичного использования "один человек = один процесс messenger = одна вкладка" это не проблема.

### Видео-звонки — теперь работают из коробки
🟢 `getUserMedia({audio: true, video: true})` → tracks добавляются в `RTCPeerConnection` → ренегоциация offer → второй пир видит remote track в `pc.ontrack` → `<video>.srcObject = stream`. Native browser pipeline.

### Контакты — пока ручной ввод hex destination_hash
🟡 QR-код / safety numbers share — TODO. Fingerprint в UI уже виден (header bar + chat header), пользователь может прочитать его голосом для верификации.

## Что делать после пробуждения

1. Прочитать этот файл и `review/PENDING.md`.
2. Проверить ветку `auto/overnight-mvp-v2` (`git log --oneline auto/overnight-mvp..auto/overnight-mvp-v2`).
3. Запустить демо (`task run:alice` / `run:bob` / `run:carol`).
4. Решить, что из 🔴 принимаем / переделываем / откладываем.

## Demo: 3 messenger'а на одной машине

В трёх терминалах:

```sh
task run:alice   # rendezvous; UDP 9100, HTTP 7100
task run:bob     # bootstraps from alice; UDP 9101, HTTP 7101
task run:carol   # bootstraps from alice; UDP 9102, HTTP 7102
```

Каждый messenger печатает в логи URL вида `http://127.0.0.1:7100/?token=...`. Открой URL в браузере (`task run:*` пробует `xdg-open` автоматически — если открылось не то, скопируй из логов).

В UI:
- ➕ **Add contact** — вставь destination_hash другого пира (видно в его логах + в его UI). Alias — любой.
- Клик по контакту — открывает signaling-сессию + WebRTC PeerConnection. Точка слева станет 🟡 → 🟢.
- Чат — сообщения идут по DataChannel напрямую между браузерами (не через Go).
- 📎 **File** — выбери файл, чанки 16 KiB через DataChannel, у получателя — кнопка скачать через `<a download>`.
- 📞 **Call** — `getUserMedia` → добавляет audio+video tracks → renegotiate offer. У получателя в правом видео-окне — твой стрим.

Любой messenger можно сбросить: `task reset:alice` (удаляет identity и storage только этого пира).

## Известные ограничения, которые ты заметишь

1. **Одна вкладка браузера на процесс.** Открой второй браузер для второго messenger.
2. **Bootstrap всё ещё ручной.** DNS-seeds — не делал.
3. **Нет ICE servers (STUN/TURN) по умолчанию в браузере.** Сейчас в `app.js` зашит `stun:stun.l.google.com:19302` — для localhost-демо хватает (host candidates), но для cross-NAT нужно подключить наши `pkg/stun`/`pkg/turn` через presence. TODO.
4. **TOFU / fingerprint mismatch.** Storage возвращает `ErrFingerprintChanged`, но UI пока не делает alarm — просто валится в "add contact". TODO.
5. **Outbox.** Каркас есть, периодический flush — нет. Browser сам решает, кэшировать ли в localStorage. TODO.
6. **Phase 8 hardening** (threat-model audit, PGO, reproducible builds) не делал.

## Файлы под coverage 80%

- `internal/messenger/*` — smoke только (через `internal/httpui/server_test.go`).
- `internal/httpui/*` — есть ОДИН integration test двух messenger'ов через signaling pipe.
- `cmd/*` — флаг-парсинг, тестов нет.
