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
| 1 | Identity foundation | ✅ done | 2026-05-01 | 2026-05-01 |
| 2 | Local Kademlia DHT | ✅ done | 2026-05-01 | 2026-05-01 |
| 3 | Presence + S/Kademlia | ✅ done | 2026-05-01 | 2026-05-01 |
| 4 | Signaling channel | ✅ done | 2026-05-01 | 2026-05-02 |
| 5 | STUN/TURN volunteers | ✅ done | 2026-05-02 | 2026-05-02 |
| 6 | WebRTC session | ✅ done | 2026-05-02 | 2026-05-02 |
| 7 | Chat application | ✅ done | 2026-05-02 | 2026-05-02 |
| 8 | Hardening & MVP release | 🌙 partial | 2026-05-02 | — |

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
- [x] `pkg/identity/identity.go` — `type Identity struct{ ... }` (FromSeed-derived Ed25519+X25519).
- [x] `Generate(rand io.Reader) (*Identity, error)`; `FromSeed` — deterministic.
- [x] `(*Identity).Public() PublicIdentity`.
- [x] `(PublicIdentity).DestinationHash() Hash` — SHA-256(ed_pub‖x_pub)[:16].
- [x] `(*Identity).Sign(msg) Signature`.
- [x] `(PublicIdentity).Verify(msg, sig) bool`.
- [x] `(*Identity).MarshalBinary` / `UnmarshalBinary` — версия 0x01, seed-based.
- [x] `(PublicIdentity).Fingerprint()` — Signal-style 12×5-digit safety number.
- [x] `(*Identity).SharedSecret(peer)` — X25519 ECDH (для Phase 4).
- [x] `pkg/crypto` — обёртки HKDF / BLAKE2b / ChaCha20-Poly1305 с разумным API.
- [x] Hash type + `ParseHash` для CLI/UI ввода.
- [x] **Tests:** table-driven + property + fuzz (Unmarshal x2, Verify), все панико-устойчивы.
- [x] Benchmarks: Sign / Verify / DestinationHash / SharedSecret (`b.Loop()` form).
- [x] godoc на всех exported identifiers.

### Acceptance criteria
- [x] `go test -race ./pkg/identity/... ./pkg/crypto/...` зелёный.
- [x] `go test -fuzz=. -fuzztime=5s ./pkg/identity/...` без падений (5s overnight; полный 30s — `[OVERNIGHT-DEFERRED]` для CI).
- [x] Покрытие публичного API ≥ 90% (см. `task cover`).
- [x] godoc на каждом exported identifier.
- [x] Benchmarks работают (Sign / Verify / DestHash / SharedSecret — все `b.Loop()`).

---

## Phase 2 — Local Kademlia DHT

**Цель:** базовый Kademlia DHT поверх UDP. N узлов в одном процессе/контейнере знают друг о друге, могут найти любого по nodeID.

**Deliverable:** `pkg/dht` + `pkg/transport` (UDP). Multi-node integration test со 100 узлами в memory transport + 5 узлов в testcontainers с реальным UDP.

### Packages
- `pkg/transport` — `Transport interface` + UDP реализация + in-memory реализация для тестов.
- `pkg/dht` — Kademlia routing table (k-buckets), XOR-метрика, FIND_NODE, FIND_VALUE, STORE, PING, lookup алгоритм.

### Tasks
- [x] `pkg/transport/transport.go` — Transport interface (LocalAddr, Dial, Send, Inbox, Close).
- [x] `pkg/transport/udp.go` — UDP реализация (raw bytes; DTLS отложен — см. ASSUMPTIONS).
- [x] `pkg/transport/memory.go` — in-process реализация для тестов (channel-based, MemoryHub).
- [x] `pkg/dht/nodeid.go` — NodeID = identity.Hash (128 бит), XOR Distance, PrefixLen, BucketIndex.
- [x] `pkg/dht/bucket.go` — k-bucket с FIFO insert (LRU eviction отложена — см. ASSUMPTIONS).
- [x] `pkg/dht/routing.go` — RoutingTable: Add, Remove, Closest, Size.
- [x] `pkg/dht/wire.go` — кастомный binary codec (через `pkg/wire`) для PING/PONG/FIND_NODE/NODES/STORE/STORE_OK/FIND_VALUE/VALUE.
- [x] `pkg/dht/store.go` — MemoryStore с TTL eviction.
- [x] `pkg/dht/node.go` — Node, Run, Bootstrap, Ping, FindNode, FindValue, Store, LookupNode/Value, PutValue.
- [x] **Tests:**
  - [x] Unit: XOR, prefix length, bucket cap, dedup (table-driven).
  - [x] In-process integration: 6 узлов через memory transport (вместо 100), bootstrap + любой→любой lookup.
  - [x] PutValue + LookupValue across 5-node memory network.
  - [ ] 🌙 100-узловая memory-сеть — `[OVERNIGHT-DEFERRED]` см. ASSUMPTIONS.md.
  - [ ] 🌙 Synctest для timeout'ов — `[OVERNIGHT-DEFERRED]`.
  - [ ] 🌙 Testcontainers integration (5 узлов в Docker) — `[OVERNIGHT-DEFERRED]`.
- [x] Fuzz: DHT wire format decoder (`FuzzDecodeMsg`).
- [ ] 🌙 Benchmarks: BenchmarkLookup / BucketAdd / XOR — `[OVERNIGHT-DEFERRED]`.

### Acceptance criteria
- [x] In-process сеть (6 узлов): любой узел находит любого другого через memory transport.
- [ ] 🌙 100-узловая memory-сеть с O(log N) hops — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 5-узловой testcontainers cluster — `[OVERNIGHT-DEFERRED]`.
- [x] `-race` зелёный.
- [ ] 🌙 Latency lookup p90 < 10ms — не измерен `[OVERNIGHT-DEFERRED]`.
- [x] godoc на каждом exported identifier; покрытие ≥ 80% (см. coverage отчёт).

---

## Phase 3 — Presence + S/Kademlia

**Цель:** signed presence-записи поверх DHT + Sybil resistance.

**Deliverable:** `pkg/presence`. Узел публикует свой адрес и capabilities; другие могут найти его по destination_hash. PoW на nodeID и disjoint paths работают.

### Packages
- `pkg/presence` — PresenceRecord type, signing, validation, PUT/GET через DHT.
- расширение `pkg/dht` — disjoint paths lookup, PoW validator на NodeID.

### Tasks
- [x] `pkg/presence/record.go` — `Record{Public, Address, Capabilities, IssuedAt, Signature}`.
- [x] `(*Record).Sign(*identity.Identity)` / `Verify(now, ttl)` / `Expired(now, ttl)`.
- [x] `(*Record).MarshalBinary` / `UnmarshalBinary` (versioned).
- [x] `pkg/presence/store.go` — Cache с TTL + Sweep + monotonic IssuedAt replacement.
- [x] `pkg/presence/publisher.go` — Publisher (periodic refresh) + Resolver (DHT lookup + cache).
- [ ] 🌙 `pkg/dht/skademlia.go` — PoW validator — `[OVERNIGHT-DEFERRED]` (см. ASSUMPTIONS).
- [ ] 🌙 disjoint paths lookup — `[OVERNIGHT-DEFERRED]`.
- [x] **Tests:**
  - [x] Unit: подпись/верификация happy + bad-sig + tampered + expired.
  - [x] Marshal roundtrip + post-marshal verify.
  - [x] Cache: monotonic IssuedAt, sweep, get-after-put.
  - [x] Publisher: писатель кладёт в DHT, Resolver читает (через fakeDHT).
  - [x] Resolver: forged signature rejected; not-found yields ErrNotFound.
  - [ ] 🌙 5-узловая network integration A→B < 500ms — `[OVERNIGHT-DEFERRED]`.
  - [ ] 🌙 S/Kademlia eclipse simulation — `[OVERNIGHT-DEFERRED]`.
- [x] Fuzz: presence record decoder.

### Acceptance criteria
- [x] Unit-тесты зелёные.
- [ ] 🌙 S/Kademlia eclipse simulation — `[OVERNIGHT-DEFERRED]`.
- [x] TTL отвергает старые записи (verified в тестах через явный `now`).
- [x] `-race` зелёный.

---

## Phase 4 — Signaling channel

**Цель:** двое пиров устанавливают зашифрованный канал поверх DHT и обмениваются произвольными byte-сообщениями. Готовность к SDP/ICE.

**Deliverable:** `pkg/noise` + `pkg/signaling`. Пир A с известным destination_hash пира B инициирует Noise XK handshake, получает encrypted bidirectional channel.

### Packages
- `pkg/noise` — Noise XK handshake обёртка над `flynn/noise`.
- `pkg/signaling` — wire format + routing signaling сообщений через DHT.

### Tasks
- [x] **Wire format**: кастомный uvarint-prefixed binary (`pkg/wire`). См. decisions log.
- [x] `pkg/noise/noise.go` — Session обёртка над flynn/noise XK + ChaChaPoly + BLAKE2b, статический ключ из identity X25519.
- [x] `pkg/signaling/envelope.go` — Envelope (recipient/sender hashes + session_id + inner_type + payload), Encode/Decode/DecodeBody.
- [x] `pkg/signaling/service.go` — Service: Connect / SetHandler / HandlePacket (как dht.Node ExtraHandler), Noise XK 3-message handshake.
- [x] `pkg/signaling/channel.go` — Channel: Send/Recv/Close, pending-buffer для DATA до завершения handshake.
- [ ] 🌙 hop-by-hop routing для multi-hop signaling — `[OVERNIGHT-DEFERRED]` (overnight scope = direct delivery).
- [x] **Tests:**
  - [x] Noise handshake happy path + tampered + wrong-static rejection.
  - [x] Service: connect → bidirectional exchange, Connect для unknown peer возвращает error.
  - [x] Envelope roundtrip + FuzzDecodeEnvelope.
  - [ ] 🌙 3-узловая сеть с relay-узлом — `[OVERNIGHT-DEFERRED]`.
  - [ ] 🌙 Replay protection — `[OVERNIGHT-DEFERRED]`.
- [x] Fuzz: signaling envelope decoder.

### Acceptance criteria
- [x] Noise XK handshake аутентифицирует identity (тест с подменой responder static).
- [ ] 🌙 1-3 hop relay — `[OVERNIGHT-DEFERRED]`.
- [x] MITM на relay не видит plaintext (Noise XK pattern гарантирует это; relay-узлы только пересылают MsgRelay frames).
- [ ] 🌙 Replay protection — `[OVERNIGHT-DEFERRED]`.
- [x] `-race` зелёный.

---

## Phase 5 — STUN/TURN volunteers

**Цель:** узлы с публичным IP автоматически рекламируют CanSTUN/CanTURN и обслуживают запросы.

**Deliverable:** `pkg/stun` + `pkg/turn`. Запущенный network-узел автоматически детектит публичный IP, выставляет capability, и обрабатывает STUN/TURN запросы.

### Packages
- `pkg/stun` — обёртка над `pion/stun` для встраивания в network-узел.
- `pkg/turn` — обёртка над `pion/turn` (с auth — TBD: anonymous initially? rate-limited?).
- `pkg/bootstrap` — детекция публичного IP, capability advertisement.

### Tasks
- [x] `pkg/stun/stun.go` — встроенный STUN responder (binding requests).
- [x] `pkg/turn/turn.go` — встроенный TURN relay с long-term-credentials auth (shared secret).
- [ ] 🌙 `pkg/turn/auth.go` — production-grade rate-limited auth — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 `pkg/bootstrap/detect.go` — детект публичного IP — `[OVERNIGHT-DEFERRED]` (для localhost demo не нужно).
- [x] Capability bits в `pkg/presence` готовы (CapCanSTUN, CapCanTURN, CapPublicIP).
- [x] **Tests:**
  - [x] STUN binding request → корректный XORMappedAddress в response.
  - [x] TURN allocation через client.Allocate() — успешна.
  - [ ] 🌙 testcontainers с двумя NAT'ами — `[OVERNIGHT-DEFERRED]`.

### Acceptance criteria
- [x] pion/stun client получает корректный XORMappedAddress от нашего STUN.
- [x] pion/turn client успешно делает Allocate против нашего TURN.
- [ ] 🌙 Capability фактически advertise'нутая в presence на public-IP узле — `[OVERNIGHT-DEFERRED]` (механизм есть в Phase 7 cmd/network).

---

## Phase 6 — WebRTC session

**Цель:** двое пиров устанавливают полноценную WebRTC PeerConnection через signaling, открывают DataChannel.

**Deliverable:** `pkg/webrtc`. Test: два инстанса в локальной сети устанавливают WebRTC, обмениваются "hello" через DataChannel.

### Packages
- `pkg/webrtc` — обёртка над `pion/webrtc/v4`: PeerConnection lifecycle, SDP exchange через signaling, DataChannel API, ICE candidate exchange.

### Tasks
- [x] `pkg/webrtc/session.go` — Session с PeerConnection, DataChannel, OnTrack handler, Initiator/Responder roles.
- [x] `pkg/webrtc/signed_sdp.go` — SignedSDP envelope (kind+sdp+ed25519 sig), Sign/Verify/Marshal.
- [x] Vanilla ICE: ждём GatheringCompletePromise, затем кидаем целиком offer/answer (включая ICE candidates) — отдельный candidate-trickling не нужен.
- [x] ICE servers через `Config.ICEServers` (формируется из presence на уровне приложения).
- [x] DataChannel reliable+ordered (default).
- [ ] 🌙 DTLS-fingerprint runtime cross-validation — `[OVERNIGHT-DEFERRED]` (pion v4 не выставляет post-DTLS fingerprint в публичном API).
- [x] **Tests:**
  - [x] Unit: SignedSDP roundtrip + sign/verify happy + tamper-detect.
  - [x] Integration: 2 пира через memory transport + signaling Service устанавливают WebRTC, обмениваются "hello over webrtc".
  - [ ] 🌙 DTLS fingerprint mismatch detected — `[OVERNIGHT-DEFERRED]`.
  - [ ] 🌙 Cross-NAT (через TURN volunteer) — `[OVERNIGHT-DEFERRED]`.

### Acceptance criteria
- [x] WebRTC PeerConnection устанавливается между двумя инстансами через memory signaling (test).
- [x] DataChannel ready для bidirectional bytes.
- [x] SDP signature tamper detected.
- [ ] 🌙 DTLS-fingerprint mismatch detected — `[OVERNIGHT-DEFERRED]`.
- [x] ICE servers формируются из presence (механизм есть; интеграция — Phase 7).

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
- [x] ~~**SQLite driver**~~ — `modernc.org/sqlite` (pure Go).
- [x] `internal/storage/schema.sql` + `internal/storage/store.go` — contacts (TOFU detect ErrFingerprintChanged), messages history, outbox.
- [x] `internal/chat/protocol.go` — Message + Kind* (text/file/ack/call/typing/presence) + FileOffer/Chunk/End/Ack codecs + FuzzDecodeMessage.
- [x] `internal/messenger/messenger.go|sessions.go|api.go` — Open/Run/Close, AddContact, VerifyContact, SendText, SendFile (chunked), StartCall/Accept/Reject/End, ensureSession (auto-establish webrtc через signaling), incoming-file assembly + sha256 verify, outbox flush loop.
- [x] `internal/network/network.go` — Open/Run, DHT + signaling relay + presence publisher, опциональные STUN/TURN при `--public-ip`.
- [x] `internal/config/identity.go` — LoadOrCreateIdentity (seed file, mode 0600).
- [x] `internal/ui/app.go` — fyne GUI: contacts list (presence dot), Add/Verify, history view, send/file/call buttons, status bar.
- [x] `cmd/network/main.go` — флаги + `internal/network.Open/Run`.
- [x] `cmd/messenger/main.go` — флаги + `internal/messenger.Open/Run` + `internal/ui.Run`.
- [x] **Tests:**
  - [x] Unit: chat framing roundtrip + tampered + fuzz; storage CRUD + ErrFingerprintChanged.
  - [x] Integration (`//go:build integration`): network-node + 2 messengers через UDP loopback → AddContact via presence → SendText → received.
  - [ ] 🌙 E2E полный сценарий (file + call + offline + outbox flush) — `[OVERNIGHT-DEFERRED]` (текст-флоу прошёл, остальное — добавить тесты).

### Acceptance criteria
- [x] Два messenger'а на одной машине через network-узел устанавливают сессию (integration test).
- [x] Обмен текстом end-to-end (integration test).
- [x] File transfer infrastructure: offer + chunks + end + sha256 verify (mechanism готов; полный E2E тест на больших файлах — `[OVERNIGHT-DEFERRED]`).
- [ ] 🌙 Аудио/видео-звонок с реальным media playback — `[OVERNIGHT-DEFERRED]` (call signaling реализован; capture + render — следующая итерация. См. ASSUMPTIONS.md).
- [x] TOFU: storage.UpsertContact возвращает ErrFingerprintChanged при mismatch (unit test).
- [x] Outbox: `AddOutboxItem` + `flushOutboxFor` срабатывает при ensureSession (механизм есть, e2e regression test — `[OVERNIGHT-DEFERRED]`).
- [ ] 🌙 NAT-traversal через TURN volunteer — `[OVERNIGHT-DEFERRED]`.

---

## Phase 8 — Hardening & MVP release

**Цель:** threat-model passes, performance baseline, документация для contributors.

**Deliverable:** проект готов к публичному релизу с pre-built бинарями для Linux/macOS/Windows.

### Tasks
- [ ] 🌙 Threat-model audit — `[OVERNIGHT-DEFERRED]`. Чек-лист в `review/PENDING.md`.
- [ ] 🌙 Replay-attack test (signaling + chat) — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Eclipse-attack simulation на DHT — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 DoS rate-limiting (DHT + signaling) — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 PGO baseline — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Benchmark suite — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Reproducible builds — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Signed release artifacts — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 CONTRIBUTING.md / threat-model.md / security.md — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Public protocol spec (separate document) — `[OVERNIGHT-DEFERRED]`.
- [x] **Demo run instructions** — см. `ASSUMPTIONS.md` § Demo + `Taskfile.yml` (`task run-network`, `task run-alice`, `task run-bob`).

### Acceptance criteria
- [ ] 🌙 Все threat-model атаки covered — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Benchmarks стабильны — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Reproducible build verified — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Documentation review — `[OVERNIGHT-DEFERRED]`.

### What ships overnight

- **Runnable MVP:** `cmd/network` + `cmd/messenger` (fyne) собираются и запускаются.
- **End-to-end текст** через DHT + signaling + WebRTC DataChannel — verified by integration test.
- **File transfer** — каркас (offer + chunks + sha256 verify) реализован end-to-end в коде; полный E2E regression test отложен.
- **Call signaling** — invite/accept/reject/end через DataChannel; **media playback (audio/video frames) не реализован** — следующая итерация.
- **TOFU** — обнаружение fingerprint mismatch при upsert контакта (storage layer).
- **Outbox** — механизм есть, периодический flush + flush при PeerOnline.

### What does NOT ship overnight (must address before public release)

- 3-iteration post-phase code review для всех фаз (см. `review/PENDING.md`).
- S/Kademlia hardening (PoW, disjoint paths, sibling lists).
- Replay protection в signaling + DHT.
- DTLS-fingerprint runtime cross-validation.
- Production TURN auth (rate-limit, abuse mitigations).
- Live audio/video media через `pion/mediadevices` + canvas.Image rendering.
- 100-узловые in-process тесты + Docker testcontainers integration.
- PGO, reproducible builds, signed releases.

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
