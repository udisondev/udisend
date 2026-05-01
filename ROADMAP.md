# Udisend — Roadmap & Development Tracking

> Дорожная карта реализации. Обновляется по мере прогресса. Архитектура — в `p2p-messenger-design.md`. Правила кодирования — в `CLAUDE.md` и skills под `.claude/skills/`.

**Принципы:**
- Каждая фаза — TDD: failing test → minimal code → refactor.
- Фаза считается завершённой только когда **все acceptance criteria** зелёные, **документация синхронна** с реализацией, и **post-phase code review** (3 итерации, см. `CLAUDE.md` → "Post-phase code review") пройден без необработанных MUST FIX.
- Build tags: `integration` для multi-node, `e2e` для полной системы. Default `go test ./...` гоняет только unit + synctest.
- `-race` обязателен в CI на всех уровнях.

**Status legend:**
⏳ planned · 🚧 in progress · ✅ done · ⏸ blocked · ❌ cancelled · 🌙 overnight-deferred (см. `ASSUMPTIONS.md`)

---

## Overall progress

| Фаза | Название | Status | Started | Completed |
|---|---|---|---|---|
| 0 | Project bootstrap | ✅ done | 2026-05-01 | 2026-05-01 |
| 1 | Identity foundation | ⏳ planned | — | — |
| 2 | Local Kademlia DHT | ⏳ planned | — | — |
| 3 | Presence + S/Kademlia | ⏳ planned | — | — |
| 4 | Signaling channel | ⏳ planned | — | — |
| 5 | STUN/TURN volunteers | ⏳ planned | — | — |
| 6 | WebRTC session | ⏳ planned | — | — |
| 7 | Chat application | ⏳ planned | — | — |
| 8 | Hardening & MVP release | ⏳ planned | — | — |

---

## Phase 0 — Project bootstrap

**Цель:** инициализировать Go-модуль, структуру директорий, CI, lint.

**Deliverable:** `go build ./...` собирает пустой проект без ошибок; `go test ./...` проходит на пустом наборе.

### Tasks
- [x] `go mod init github.com/udisondev/udisend`.
- [x] `go.mod` с `go 1.26` (директива `tool` будет добавлена при появлении первого пинуемого инструмента; пока линтер ставится локально/в CI отдельно).
- [x] Скаффолд директорий: `cmd/network/`, `cmd/messenger/`, `pkg/`, `internal/`, `test/`.
- [x] `cmd/network/main.go`, `cmd/messenger/main.go` — минимальные stubs (печатают "TODO").
- [x] `.gitignore` (бинарники, `*.pgo`, `coverage.out`, и т.д.).
- [x] `Taskfile.yml` ([go-task](https://taskfile.dev)) с целями: `test`, `test-race`, `test-integration`, `test-e2e`, `lint`, `build`, `bench`.
- [x] CI настройка (GitHub Actions): `go vet`, `go test -race`, `golangci-lint`, build.
- [x] `golangci-lint` конфиг с разумным набором (`govet`, `staticcheck`, `errcheck`, `gosec`, `revive`).
- [x] Первая зелёная сборка в CI. *(branch `phase-0/bootstrap` запушен, CI на push активирован)*

### Acceptance criteria
- `go build ./...` зелёный.
- `go test -race ./...` зелёный (на пустом наборе).
- CI на push/PR в `main` зелёный.
- `golangci-lint run ./...` без warnings.

---

## Phase 1 — Identity foundation

**Цель:** криптографическая идентичность пользователя — keypair, destination_hash, sign/verify, fingerprint.

**Deliverable:** `pkg/identity` + `pkg/crypto` готовы. Возможно сгенерировать identity, сохранить в файл, загрузить, подписать blob, проверить подпись.

### Packages
- `pkg/identity` — Identity type, generation, serialization, destination_hash, fingerprint string.
- `pkg/crypto` — обёртки: `BLAKE2b`, `HKDF`, `ChaCha20-Poly1305`.

### Tasks
- [ ] `pkg/identity/identity.go` — `type Identity struct{ EdPriv ed25519.PrivateKey; XPriv ... }`.
- [ ] `Generate(rand io.Reader) (*Identity, error)` — детерминированно при тесте через фиксированный reader.
- [ ] `(*Identity).PublicKey() PublicIdentity` — pub-only тип для безопасной передачи.
- [ ] `(*Identity).DestinationHash() Hash` — `[16]byte` = SHA-256(ed_pub‖x_pub)[:16].
- [ ] `(*Identity).Sign(msg []byte) Signature`.
- [ ] `(PublicIdentity).Verify(msg, sig) bool`.
- [ ] `(*Identity).MarshalBinary` / `UnmarshalBinary` — стабильный формат сериализации.
- [ ] `(PublicIdentity).Fingerprint() string` — человекочитаемая форма (Signal-style numbers).
- [ ] `pkg/crypto` — обёртки HKDF / BLAKE2b / ChaCha20-Poly1305 с разумным API.
- [ ] **Tests (TDD throughout):**
  - [ ] table-driven для destination_hash, sign/verify happy + sad paths.
  - [ ] property test: верификация после подписи всегда true для своего ключа.
  - [ ] property test: верификация подписи чужим ключом всегда false.
  - [ ] fuzz `UnmarshalBinary` — никогда не должен паниковать.
  - [ ] fuzz `Verify(msg, sig)` — никогда не должен паниковать.
- [ ] Benchmarks: `BenchmarkSign`, `BenchmarkVerify`, `BenchmarkDestinationHash` (`b.Loop()` form).
- [ ] godoc на каждом exported identifier.

### Acceptance criteria
- `go test -race ./pkg/identity/... ./pkg/crypto/...` зелёный.
- `go test -fuzz=. -fuzztime=30s ./pkg/identity/...` без падений.
- Покрытие публичного API ≥ 90% (через `go test -cover`).
- godoc проходит `go doc` без warnings.
- Benchmarks работают, выводят разумные числа (Sign < 100µs на современном x86_64).

---

## Phase 2 — Local Kademlia DHT

**Цель:** базовый Kademlia DHT поверх UDP. N узлов в одном процессе/контейнере знают друг о друге, могут найти любого по nodeID.

**Deliverable:** `pkg/dht` + `pkg/transport` (UDP). Multi-node integration test со 100 узлами в memory transport + 5 узлов в testcontainers с реальным UDP.

### Packages
- `pkg/transport` — `Transport interface` + UDP реализация + in-memory реализация для тестов.
- `pkg/dht` — Kademlia routing table (k-buckets), XOR-метрика, FIND_NODE, FIND_VALUE, STORE, PING, lookup алгоритм.

### Tasks
- [ ] `pkg/transport/transport.go` — interface.
- [ ] `pkg/transport/udp.go` — UDP реализация (raw bytes, без DTLS пока).
- [ ] `pkg/transport/memory.go` — in-process реализация для тестов (channel-based).
- [ ] `pkg/dht/nodeid.go` — NodeID type, XOR, prefix length.
- [ ] `pkg/dht/bucket.go` — k-bucket с LRU eviction.
- [ ] `pkg/dht/routing.go` — RoutingTable: add, remove, find K closest.
- [ ] `pkg/dht/wire.go` — wire format (TBD: бинарный или CBOR — обсудить в фазе 4).
- [ ] `pkg/dht/rpc.go` — FIND_NODE, FIND_VALUE, STORE, PING handlers.
- [ ] `pkg/dht/lookup.go` — iterative lookup алгоритм.
- [ ] `pkg/dht/node.go` — Node struct, Run() method, lifecycle.
- [ ] **Tests:**
  - [ ] Unit: XOR, prefix length, bucket eviction (table-driven).
  - [ ] Synctest: lookup протокол (виртуальное время для timeout'ов).
  - [ ] In-process integration: 100 узлов через memory transport, проверка connectivity.
  - [ ] Testcontainers integration (`//go:build integration`): 5 узлов в Docker, реальный UDP.
- [ ] Benchmarks: `BenchmarkLookup`, `BenchmarkBucketAdd`, `BenchmarkXOR`.
- [ ] Fuzz: wire format decoder.

### Acceptance criteria
- 100-узловая memory-сеть: любой узел находит любого другого за O(log N) hops.
- 5-узловой testcontainers cluster: 100% success rate на 1000 случайных lookups.
- `-race` зелёный на всём.
- Latency lookup на in-process сети < 10ms (90-й percentile).
- godoc + покрытие ≥ 80%.

---

## Phase 3 — Presence + S/Kademlia

**Цель:** signed presence-записи поверх DHT + Sybil resistance.

**Deliverable:** `pkg/presence`. Узел публикует свой адрес и capabilities; другие могут найти его по destination_hash. PoW на nodeID и disjoint paths работают.

### Packages
- `pkg/presence` — PresenceRecord type, signing, validation, PUT/GET через DHT.
- расширение `pkg/dht` — disjoint paths lookup, PoW validator на NodeID.

### Tasks
- [ ] `pkg/presence/record.go` — `type PresenceRecord struct{ Address; Capabilities; Timestamp; Signature }`.
- [ ] `(*PresenceRecord).Sign(*identity.Identity)`.
- [ ] `(*PresenceRecord).Verify(identity.PublicIdentity) error`.
- [ ] `(*PresenceRecord).IsExpired(now time.Time) bool`.
- [ ] `pkg/presence/store.go` — local presence cache с TTL eviction.
- [ ] `pkg/presence/publisher.go` — periodic refresh в DHT.
- [ ] `pkg/dht/skademlia.go` — PoW validator (адаптивная сложность).
- [ ] `pkg/dht/lookup.go` — disjoint paths option.
- [ ] **Tests:**
  - [ ] Unit: подпись/верификация, expiration (synctest для virtual time).
  - [ ] Property: presence-запись с подделанной подписью отвергается.
  - [ ] Integration: 5-узловая сеть, A публикует presence, B находит за < 500ms.
  - [ ] S/Kademlia attack simulation: атакующий не может eclipse target несмотря на K Sybil-узлов.
- [ ] Fuzz: presence record decoder.

### Acceptance criteria
- Все unit + integration tests зелёные.
- S/Kademlia eclipse simulation: target всегда findable при ≤ N/3 Sybil-узлов.
- TTL правильно работает (synctest verifies).
- `-race` зелёный.

---

## Phase 4 — Signaling channel

**Цель:** двое пиров устанавливают зашифрованный канал поверх DHT и обмениваются произвольными byte-сообщениями. Готовность к SDP/ICE.

**Deliverable:** `pkg/noise` + `pkg/signaling`. Пир A с известным destination_hash пира B инициирует Noise XK handshake, получает encrypted bidirectional channel.

### Packages
- `pkg/noise` — Noise XK handshake обёртка над `flynn/noise`.
- `pkg/signaling` — wire format + routing signaling сообщений через DHT.

### Tasks
- [ ] **Решить wire format** signaling-сообщений (protobuf / CBOR / custom binary). Документировать решение в `p2p-messenger-design.md`.
- [ ] `pkg/noise/handshake.go` — initiator/responder XK pattern, integration с identity keys.
- [ ] `pkg/noise/session.go` — encrypted bidirectional session после handshake.
- [ ] `pkg/signaling/envelope.go` — wire format для signaling blob'ов (recipient_id, sender_id, encrypted payload, signature).
- [ ] `pkg/signaling/router.go` — hop-by-hop routing через DHT.
- [ ] `pkg/signaling/dialer.go` — `Connect(ctx, peer Hash) (Channel, error)`.
- [ ] `pkg/signaling/listener.go` — `Listen(handler func(peer Hash, ch Channel))`.
- [ ] **Tests:**
  - [ ] Unit: handshake happy path, mismatched identity, replay protection.
  - [ ] Integration: 3-узловая сеть, A↔B через relay-узел, обмен 100 сообщениями обоими направлениями.
  - [ ] Failure: relay убивается посреди сессии — обе стороны получают error в разумное время.
- [ ] Fuzz: signaling envelope decoder.

### Acceptance criteria
- Noise XK handshake работает с правильной аутентификацией identity.
- Сообщения через 1-3 hop signaling-relay'я приходят intact.
- MITM на relay не может расшифровать payload (cryptographic test).
- Replay не возможен (sequence numbers / nonces in protocol).
- `-race` зелёный.

---

## Phase 5 — STUN/TURN volunteers

**Цель:** узлы с публичным IP автоматически рекламируют CanSTUN/CanTURN и обслуживают запросы.

**Deliverable:** `pkg/stun` + `pkg/turn`. Запущенный network-узел автоматически детектит публичный IP, выставляет capability, и обрабатывает STUN/TURN запросы.

### Packages
- `pkg/stun` — обёртка над `pion/stun` для встраивания в network-узел.
- `pkg/turn` — обёртка над `pion/turn` (с auth — TBD: anonymous initially? rate-limited?).
- `pkg/bootstrap` — детекция публичного IP, capability advertisement.

### Tasks
- [ ] `pkg/stun/server.go` — встроенный STUN responder.
- [ ] `pkg/turn/server.go` — встроенный TURN relay.
- [ ] `pkg/turn/auth.go` — auth strategy (TBD; начать с rate-limited anonymous).
- [ ] `pkg/bootstrap/detect.go` — детект публичного IP через сторонние STUN или peer-introspection.
- [ ] Integration с `pkg/presence` — capability advertisement.
- [ ] **Tests:**
  - [ ] STUN binding request → правильный srflx ответ.
  - [ ] TURN allocation → правильный allocated address.
  - [ ] testcontainers: 2 узла за разными "NAT'ами" (через Docker network isolation), TURN-relay между ними.

### Acceptance criteria
- pion/webrtc может использовать наш STUN endpoint и получает правильный srflx candidate.
- pion/webrtc может использовать наш TURN endpoint и устанавливает relay candidate.
- Capability `CanSTUN`/`CanTURN` корректно появляется в presence записях узлов с публичным IP.

---

## Phase 6 — WebRTC session

**Цель:** двое пиров устанавливают полноценную WebRTC PeerConnection через signaling, открывают DataChannel.

**Deliverable:** `pkg/webrtc`. Test: два инстанса в локальной сети устанавливают WebRTC, обмениваются "hello" через DataChannel.

### Packages
- `pkg/webrtc` — обёртка над `pion/webrtc/v4`: PeerConnection lifecycle, SDP exchange через signaling, DataChannel API, ICE candidate exchange.

### Tasks
- [ ] `pkg/webrtc/session.go` — `type Session struct{ pc *webrtc.PeerConnection; ... }`.
- [ ] `pkg/webrtc/offer.go` — initiator: create offer, sign, send via signaling, await answer.
- [ ] `pkg/webrtc/answer.go` — responder: receive offer, validate signature, validate DTLS-fingerprint binding, create answer, send.
- [ ] `pkg/webrtc/ice.go` — ICE candidate gathering + exchange.
- [ ] `pkg/webrtc/iceservers.go` — выбор STUN/TURN из presence записей с capability.
- [ ] `pkg/webrtc/datachannel.go` — DataChannel wrapper с reliable+ordered defaults.
- [ ] **SDP↔Identity binding** — критическая проверка fingerprint после DTLS handshake.
- [ ] **Tests:**
  - [ ] Unit: SDP подпись + валидация.
  - [ ] Unit: подмена SDP detected (signature fail).
  - [ ] Unit: подмена DTLS-fingerprint detected (cross-validation fail).
  - [ ] Integration: 2 узла в одной Docker network устанавливают WebRTC, обмениваются "hello".
  - [ ] Integration: 2 узла за разными NAT (через TURN volunteer) устанавливают WebRTC.

### Acceptance criteria
- WebRTC PeerConnection устанавливается между двумя инстансами.
- DataChannel ready для bidirectional bytes.
- SDP подделка / fingerprint подмена detected и сессия rejected.
- ICE servers пригодны для использования из presence записей.

---

## Phase 7 — Chat application

**Цель:** работающий 1-на-1 messenger с CLI/TUI: текст, файлы, audio/video, контакты, TOFU, история, outbox.

**Deliverable:** `cmd/messenger` запускает CLI клиент. Два пользователя на разных машинах могут общаться полным набором фич.

### Packages
- `internal/chat` — application protocol (типы сообщений, framing, ACK).
- `internal/contacts` — контакты, TOFU store, fingerprint verification.
- `internal/storage` — SQLite layer для истории, контактов, outbox.
- `internal/ui` — desktop GUI на `fyne.io/fyne/v2` (см. decisions log 2026-05-01).
- `internal/messenger` — сборка messenger клиента.
- `internal/network` — сборка network-узла.
- `internal/config` — конфиг загрузка для обоих бинарей.

### Tasks
- [ ] **Решить SQLite driver** (`modernc.org/sqlite` vs `mattn/go-sqlite3`).
- [x] ~~**Решить UI library**~~ — `fyne.io/fyne/v2` (см. decisions log 2026-05-01).
- [ ] `internal/storage/schema.sql` — схема для контактов, истории, outbox.
- [ ] `internal/storage/store.go` — DB layer с интерфейсом для тестов.
- [ ] `internal/contacts/store.go` — TOFU + fingerprint verification flow.
- [ ] `internal/chat/protocol.go` — типы сообщений (text, file_offer, file_chunk, ack, typing, call_signal, presence_ping).
- [ ] `internal/chat/framing.go` — encoding/decoding ApplicationMessage.
- [ ] `internal/chat/file.go` — file chunking, per-chunk ACK, resume support.
- [ ] `internal/chat/call.go` — audio/video call setup через media tracks.
- [ ] `internal/chat/outbox.go` — periodic outbox flush при появлении peer в presence.
- [ ] `internal/messenger/run.go` — main entry: load identity → join DHT → start signaling listener → CLI/TUI loop.
- [ ] `internal/network/run.go` — main entry: join DHT → start signaling-relay + STUN/TURN if public IP.
- [ ] `internal/ui/app.go` — fyne app (главное окно, контакт-лист, чат, кнопки call/file/verify, история).
- [ ] `cmd/messenger/main.go` — flag parsing → `internal/messenger.Run`.
- [ ] `cmd/network/main.go` — flag parsing → `internal/network.Run`.
- [ ] **Tests:**
  - [ ] Unit: framing, ACK matching, outbox semantics, TOFU flow.
  - [ ] Integration: 2 messenger клиента + 1 network-узел в testcontainers, обмен текстом и файлом.
  - [ ] E2E (`//go:build e2e`): полный сценарий — TOFU → message → file → call → offline → outbox flush.

### Acceptance criteria
- Два пользователя на разных машинах за разными NAT'ами устанавливают сессию через volunteer-узел.
- Обмениваются текстом, файлом ≥ 10 MB, аудио/видео-звонком.
- TOFU работает: первый контакт сохраняет pubkey, второй ругается при mismatch.
- Outbox: один уходит оффлайн, другой шлёт сообщение → лежит в outbox → доставляется когда первый возвращается.
- E2E тесты зелёные.

---

## Phase 8 — Hardening & MVP release

**Цель:** threat-model passes, performance baseline, документация для contributors.

**Deliverable:** проект готов к публичному релизу с pre-built бинарями для Linux/macOS/Windows.

### Tasks
- [ ] Threat-model audit (по `p2p-messenger-design.md` §8) — каждая атака покрыта тестом или документирована как accepted risk.
- [ ] Replay-attack test: signaling и chat layer.
- [ ] Eclipse-attack simulation на DHT.
- [ ] DoS rate-limiting на DHT и signaling.
- [ ] PGO baseline: capture production profile, commit `default.pgo`.
- [ ] Benchmark suite: fix regressions, document expected numbers.
- [ ] Reproducible builds (с GOFLAGS, fixed timestamps).
- [ ] Signed release artifacts.
- [ ] CONTRIBUTING.md, threat-model.md, security.md.
- [ ] Public spec document (отдельно от Go-кода) для multi-implementation future.
- [ ] Release notes + установка инструкции.

### Acceptance criteria
- Все threat-model атаки covered.
- Benchmarks стабильны (variance ≤ 5% по `benchstat`).
- Reproducible build verified by independent rebuild.
- Documentation review by an outside reader (готовность к contributors).

---

## Decisions log

Запись принципиальных архитектурных решений по ходу реализации. Формат: `[DATE] [PHASE] DECISION: ... — RATIONALE: ...`.

- `[2026-05-01] [PHASE 0] DECISION: task runner — go-task (Taskfile.yml), не Make/just.` — RATIONALE: кросс-платформенный YAML-формат, явные deps между задачами, watch-режим, нативная Go-экосистема (single static binary, `go install`); Make избыточен для проекта без C-сборки, just не даёт deps между задачами.
- `[2026-05-01] [PHASE 0] RETROSPECTIVE: Phase 0 done.` — bootstrap прошёл по плану. Выявленные нюансы: (1) parent-директория `~/Projects/go/` оказалась случайным git-репо без коммитов — пришлось инициализировать отдельный repo внутри `udisend/`; (2) workflow проекта — feature branches без PR, push в `main` запрещён; (3) module path = `github.com/udisondev/udisend`.
- `[2026-05-01] [PHASE 7] DECISION: UI = fyne (fyne.io/fyne/v2), не CLI/TUI.` — RATIONALE: пользователь явно попросил desktop-клиент с fyne для overnight-run. Фаза 7 в части `internal/ui` пересматривается под fyne; bubbletea/tview — снимаются. Trade-off: cgo-зависимость в messenger-бинаре (libgl/xorg-dev на Linux); pure-Go выбор для остального стека (modernc.org/sqlite) сохраняется.
- `[2026-05-01] [PHASE 4] DECISION: wire format для DHT/signaling — кастомный uvarint-prefixed binary в pkg/wire.` — RATIONALE: одна реализация (Go), нулевые внешние deps на wire layer, простой fuzz-таргет. CBOR/protobuf — будущий рефактор, изолирован одним пакетом.
- `[2026-05-01] [OVERNIGHT] CONTEXT: unattended overnight run на ветке auto/overnight-mvp.` — пользователь оставил auto-mode, потребовал runnable MVP (cmd/network + cmd/messenger fyne) с text/files/video. Все одиночные решения и cuts документированы в `ASSUMPTIONS.md`. Post-phase 3-iteration ревью пропущено для всех фаз 1–8, чек-лист — в `review/PENDING.md`. Не сделанные пункты ROADMAP помечены `[OVERNIGHT-DEFERRED]`.

---

## Out-of-MVP — последующие версии

Эти задачи **намеренно отложены** и не входят в MVP scope. Документируются здесь, чтобы случайно не закатать в фазу.

- **Группы** (mesh для маленьких, SFU для больших).
- **Browser/WASM клиент** + WebSocket signaling bridge на public-IP узлах.
- **Multi-device** для одного пользователя (sync между устройствами).
- **Double Ratchet** поверх DataChannel (post-compromise security).
- **Pluggable transports** (anti-censorship, bridge nodes как в Tor).
- **Mobile clients** (iOS/Android, push-уведомления через APNS/FCM — компромисс).
- **Onion routing** для signaling traffic-analysis resistance.
- **Reputation/karma system** для приоритизации полезных узлов.
- **GUI клиенты** (Linux/macOS/Windows).

---

## How to use this document

1. **Перед началом фазы** — открыть соответствующую секцию, перечитать tasks, проверить что предыдущая фаза в статусе ✅.
2. **Во время фазы** — отмечать tasks `[x]` по мере завершения, обновить status фазы на 🚧 в начале и ✅ в конце.
3. **При архитектурном решении** — добавить запись в "Decisions log" + обновить `p2p-messenger-design.md`.
4. **В конце фазы** — заполнить даты в "Overall progress" таблице, сделать short retrospective comment в decisions log.
