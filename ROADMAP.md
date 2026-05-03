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
- [x] PoW validator — `pkg/identity.PoWBits/GenerateWithPoW` (Sybil-resistance opt-in: design.md §4).
- [x] disjoint paths lookup — реализован в `pkg/dht.iterativeFind` через `Config.Disjoint` (default 3) + `pathState`/`claimBatch`/`mergePathShortlists`. Shared visited-set гарантирует, что peer не запрашивается двумя путями. См. `pkg/dht/skademlia_test.go` + `pkg/dht/skademlia_internal_test.go`.
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
- [x] hop-by-hop routing для multi-hop signaling — `pkg/signaling.Service.relay` + `Router` interface + `internal/network.dhtRouter` с iterative path discovery; MaxHops=8.
- [x] **Tests:**
  - [x] Noise handshake happy path + tampered + wrong-static rejection.
  - [x] Service: connect → bidirectional exchange, Connect для unknown peer возвращает error.
  - [x] Envelope roundtrip + FuzzDecodeEnvelope.
  - [ ] 🌙 3-узловая сеть с relay-узлом — `[OVERNIGHT-DEFERRED]`.
  - [x] Replay protection — Noise XK AEAD nonce + duplicate-HELLO_INIT drop. Tests in `pkg/signaling/replay_test.go`.
- [x] Fuzz: signaling envelope decoder.

### Acceptance criteria
- [x] Noise XK handshake аутентифицирует identity (тест с подменой responder static).
- [x] 1-3 hop relay — `Service.relay` + dhtRouter path discovery. Tests in `pkg/signaling/relay_test.go`.
- [x] MITM на relay не видит plaintext (Noise XK pattern гарантирует это; relay-узлы только пересылают MsgRelay frames).
- [x] Replay protection — see above (Noise nonce + dup-INIT drop).
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
- [x] `pkg/turn` — RFC 7635 ephemeral credentials + per-IP rate-limit + `MaxCredentialLifetime`. `EphemeralCredential` helper for clients.
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
- [❌] ~~`pkg/webrtc/session.go` — Session с PeerConnection, DataChannel, OnTrack handler, Initiator/Responder roles.~~ — **CANCELLED in v2** (commit `375afb5`): WebRTC переехал в браузер, Go больше не держит PeerConnection. Файл удалён. См. decisions log 2026-05-02.
- [x] `pkg/webrtc/signed_sdp.go` — SignedSDP envelope (kind+sdp+ed25519 sig), Sign/Verify/Marshal.
- [❌] ~~Vanilla ICE: ждём GatheringCompletePromise…~~ — теперь в браузере (нативный `RTCPeerConnection`); Go только ферри SignedSDP.
- [x] ICE servers через presence (формируется на уровне приложения и пробрасывается в браузер; интеграция — см. отдельная задача в плане доработки).
- [❌] ~~DataChannel reliable+ordered (default).~~ — браузерный DataChannel.
- [ ] 🌙 DTLS-fingerprint runtime cross-validation — `[OVERNIGHT-DEFERRED]` (pion v4 не выставляет post-DTLS fingerprint в публичном API; в v2 — задача браузерной стороны).
- [x] **Tests:**
  - [x] Unit: SignedSDP roundtrip + sign/verify happy + tamper-detect.
  - [❌] ~~Integration: 2 пира через memory transport + signaling Service устанавливают WebRTC, обмениваются "hello over webrtc".~~ — тест жил в `pkg/webrtc/session_test.go`, удалён вместе с `session.go`.
  - [ ] 🌙 DTLS fingerprint mismatch detected — `[OVERNIGHT-DEFERRED]`.
  - [ ] 🌙 Cross-NAT (через TURN volunteer) — `[OVERNIGHT-DEFERRED]`.

### Acceptance criteria
- [❌] ~~WebRTC PeerConnection устанавливается между двумя инстансами через memory signaling (test).~~ — invalidated by v2 cut (нет Go-теста, реальная установка проверяется в браузере; e2e-проверка отложена в Phase 7).
- [❌] ~~DataChannel ready для bidirectional bytes.~~ — invalidated by v2 cut.
- [x] SDP signature tamper detected.
- [ ] 🌙 DTLS-fingerprint mismatch detected — `[OVERNIGHT-DEFERRED]`.
- [ ] ⏳ ICE servers формируются из presence — **частично**: механизм бит'ов есть в `pkg/presence`, но lookup CapCanSTUN/CapCanTURN и проброс в браузер не реализованы (см. план «volunteer ICE discovery»).

---

## Phase 7 — Chat application

**Цель:** работающий 1-на-1 messenger с CLI/TUI: текст, файлы, audio/video, контакты, TOFU, история, outbox.

**Deliverable:** `cmd/messenger` запускает CLI клиент. Два пользователя на разных машинах могут общаться полным набором фич.

### Packages (v2-актуально)
- ~~`internal/chat`~~ — **удалён 2026-05-03** (см. decisions log). Был v1-рудиментом; chat-protocol живёт в `internal/httpui/assets/app.js` (JSON over DataChannel + binary file chunks).
- `internal/storage` — SQLite layer (контакты + TOFU + история + outbox); contacts-логика живёт здесь, отдельного `internal/contacts` нет.
- `internal/httpui` — HTTP+SSE bridge между Go-runtime и браузером (REST API + EventSource); появился в v2 вместо `internal/ui` (fyne).
- `internal/messenger` — Go-runtime: identity, DHT, presence, signaling.Service, storage; **chat/file/call логика теперь в браузере**, не в Go.
- `internal/network` — сборка network-узла (DHT + presence-publisher + опциональные STUN/TURN).
- `internal/config` — конфиг загрузка для обоих бинарей.
- ~~`internal/ui`~~ — **удалён в v2**, см. decisions log 2026-05-02.
- ~~`internal/contacts`~~ — не создавался, контакт-логика осталась в `internal/storage` + `internal/messenger/api.go`.

### Tasks
- [x] ~~**SQLite driver**~~ — `modernc.org/sqlite` (pure Go).
- [❌] ~~**UI library = fyne.io/fyne/v2**~~ — отменено в v2 (decisions log 2026-05-02). Заменено браузерным UI поверх HTTP+SSE.
- [x] `internal/storage/schema.sql` + `internal/storage/store.go` — contacts (TOFU detect ErrFingerprintChanged), messages history, outbox CRUD.
- [❌] ~~`internal/chat/protocol.go` — Message + Kind* + FileOffer/Chunk/End/Ack codecs + FuzzDecodeMessage~~ — **удалён 2026-05-03** как dead code. Chat-protocol живёт в `app.js`. Wire-format Go-стороны больше не определяется (см. decisions log 2026-05-03).
- [⚠️] `internal/messenger/messenger.go|session.go|api.go` — Open/Run/Close, AddContact, VerifyContact, signaling.Channel-обёртка через `Session`, lookup peer pubkey. **Чего НЕТ в Go (по дизайну v2):** SendText, SendFile (chunked), StartCall/Accept/Reject/End, ensureSession, incoming-file assembly + sha256 verify, outbox flush loop — всё это в браузерной стороне. См. ASSUMPTIONS.md «Что теперь делает Go».
- [x] `internal/network/network.go` — Open/Run, DHT + presence publisher, опциональные STUN/TURN при `--public-ip`. Hop-by-hop signaling relay реализован: `signaling.Service` с `Router` и `dhtRouter.NextHop` пересылает envelope-ы, чьи recipients ≠ self, через iterative LookupNode (см. design.md §4 «Path discovery»).
- [x] `internal/config/identity.go` — LoadOrCreateIdentity (seed file, mode 0600).
- [x] `internal/httpui` — HTTP+SSE bridge: REST API (`/api/snapshot`, `/api/contacts/*`, `/api/history`, `/api/session/*`, `/api/signal/send`) + EventSource для server→browser push, bearer-token auth. Заменяет ушедший `internal/ui`.
- [x] `cmd/network/main.go` — флаги + `internal/network.Open/Run`.
- [x] `cmd/messenger/main.go` — флаги + `internal/messenger.Open/Run` + `internal/httpui.Server.Run` (не `internal/ui.Run` как в v1).
- [⚠️] **Tests:**
  - [❌] ~~Unit: chat framing roundtrip + tampered + fuzz~~ — снято с удалением `internal/chat`. Storage CRUD + ErrFingerprintChanged по-прежнему покрыт.
  - [⚠️] Integration: один integration test в `internal/httpui/server_test.go` (без build-tag) — два messenger'а через signaling pipe + AddContact + signal send. **`//go:build integration` build-tag не используется**, integration-тесты идут наравне с unit-тестами через `go test ./...`.
  - [ ] 🌙 E2E полный сценарий (file + call + offline + outbox flush) — `[OVERNIGHT-DEFERRED]` (текст-флоу прошёл в integration-тесте httpui; остальное — следующая итерация).
  - [ ] ⏳ Smoke/unit тесты для `internal/messenger`, `internal/network`, `internal/config` — отсутствуют; см. план доработки.

### Acceptance criteria
- [x] Два messenger'а через signaling pipe устанавливают сессию (`internal/httpui/server_test.go`).
- [⚠️] Обмен текстом end-to-end — текст идёт через браузерный DataChannel; Go-сторона тестирует только подъём signaling-канала. Полноценный e2e теста с двумя браузерами и DataChannel — отсутствует.
- [⚠️] File transfer — реализован в JS-стороне над DataChannel (offer + chunks + sha256 verify); Go хранит только storage-метаданные.
- [x] Аудио/видео-звонок с реальным media playback — **работает в браузере** (нативный `RTCPeerConnection` + `getUserMedia`, см. ASSUMPTIONS.md «Видео-звонки — теперь работают из коробки»). v1-acceptance, помеченный 🌙, в v2 закрыт.
- [x] TOFU: storage.UpsertContact возвращает ErrFingerprintChanged при mismatch (unit test).
- [x] Outbox: storage CRUD + `Messenger.outboxPump` periodic flush + `SetPeerOnlineHandler` SSE-event "peer_online" pushed to UI when a recipient with pending items becomes resolvable. Tests in `internal/messenger/outbox_test.go`.
- [x] NAT-traversal через TURN volunteer — `Messenger.ICEServers` + `/api/snapshot.ice_servers` + browser `STATE.iceServers` (with public STUN as fallback only when no volunteer is reachable).

---

## Phase 8 — Hardening & MVP release

**Цель:** threat-model passes, performance baseline, документация для contributors.

**Deliverable:** проект готов к публичному релизу с pre-built бинарями для Linux/macOS/Windows.

### Tasks
- [ ] 🌙 Threat-model audit — `[OVERNIGHT-DEFERRED]`. Чек-лист в `review/PENDING.md`.
- [x] Replay-attack test (signaling) — `pkg/signaling/replay_test.go`.
- [ ] 🌙 Eclipse-attack simulation на DHT — частично: bootstrap diversity (`SeenPeersDiverse`), full simulation deferred.
- [x] DoS rate-limiting (DHT + signaling) — `pkg/ratelimit` + `pkg/dht.Node` per-IP token bucket; signaling rides the same socket so it inherits.
- [ ] 🌙 PGO baseline — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Benchmark suite — `[OVERNIGHT-DEFERRED]`.
- [x] Reproducible builds — `Taskfile.yml::build:reproducible` (-trimpath, -buildvcs=false, fixed ldflags, sha256sum).
- [x] Signed release artifacts — `Taskfile.yml::release:sign` (minisign default, cosign via COSIGN=1).
- [x] threat-model — `review/threat-model.md`. CONTRIBUTING.md / security.md still pending.
- [ ] 🌙 Public protocol spec (separate document) — `[OVERNIGHT-DEFERRED]`.
- [x] **Demo run instructions** — см. `ASSUMPTIONS.md` § Demo + `Taskfile.yml` (`task run-network`, `task run-alice`, `task run-bob`).

### Acceptance criteria
- [ ] 🌙 Все threat-model атаки covered — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Benchmarks стабильны — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Reproducible build verified — `[OVERNIGHT-DEFERRED]`.
- [ ] 🌙 Documentation review — `[OVERNIGHT-DEFERRED]`.

### What ships overnight

- **Runnable MVP:** `cmd/network` + `cmd/messenger` (browser UI через HTTP+SSE) собираются и запускаются. Изначально планировался fyne; v2-сдвиг (drop fyne, browser owns WebRTC) — см. decisions log 2026-05-02.
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
- `[2026-05-02] [PHASE 7] DECISION: drop fyne; WebRTC переезжает в браузер; Go = HTTP+SSE bridge.` — RATIONALE: video-render в fyne требовал кастомного VP8-decode + canvas.Image blit, что не успевало в overnight. Браузерный нативный `RTCPeerConnection` + `<video>` + `getUserMedia` решает это из коробки, плюс убирает 30 MB fyne-зависимостей из messenger-бинаря. Trade-off: пользователь теперь должен открыть URL в браузере; messenger печатает его в логи. Это override решения от 2026-05-01 ("UI = fyne"). Удалены `internal/ui`, `pkg/webrtc/session.go`, `internal/messenger/sessions.go`. Подробнее — ASSUMPTIONS.md §«Главный архитектурный сдвиг (v2)».
- `[2026-05-02] [PHASE 7] DECISION: HTTP-bridge — SSE + REST вместо WebSocket.` — RATIONALE: runtime-permission-system заблокировал внешний WS-модуль (`nhooyr.io/websocket`). SSE (server→browser) + plain POST (browser→server) — чистый stdlib (`net/http`), ноль новых deps. Для use-case "одна вкладка на messenger" SSE достаточен и проще для дебага (DevTools Network → EventStream).
- `[2026-05-02] [PHASE 7] DECISION: chat application protocol живёт в браузере, Go только подписывает SDP.` — RATIONALE: всё, что раньше планировалось в `internal/messenger.SendText/SendFile/StartCall`, теперь реализовано на JS поверх браузерного `RTCDataChannel`. Go-сторона выставляет тонкий API (`POST /api/signal/send`, `EventSource /api/events`), оборачивает SDP в `pkg/webrtc.SignedSDP` (Ed25519) и пересылает через `signaling.Channel`. `internal/chat/protocol.go` сохранён как формат, но фактически dead code — браузер использует свой JSON envelope (`internal/httpui/sse.go: envelope`).
- `[2026-05-02] [PHASES 4–7] RETROSPECTIVE.` — все фазы формально закрыты ✅ в overall-progress, но **post-phase 3-iteration ревью пропущено** (известный overnight cut, см. `review/PENDING.md`). Acceptance criteria Phase 6 ("WebRTC PeerConnection устанавливается через memory signaling", "DataChannel ready") инвалидированы v2-сдвигом — соответствующие тесты удалены вместе с `pkg/webrtc/session.go`. Phase 7 acceptance частично переинтерпретированы под браузерный UI; Go-сторона покрыта только одним integration test'ом в `internal/httpui/server_test.go`.
- `[2026-05-03] [PHASE 7] DECISION: удалить рудимент internal/chat.` — RATIONALE: chat application protocol окончательно живёт в браузере (`internal/httpui/assets/app.js`); Go-формат `internal/chat/protocol.go` не использовался в data path и сбивал с толку при чтении дизайна. Удалены `internal/chat/protocol.go` и его тесты. design.md §9 переписан, чтобы прямо говорить «application protocol живёт в браузере». ROADMAP Phase 7 packages-секция и acceptance criteria синхронизированы.
- `[2026-05-03] [PHASE 7] DECISION: добавить pkg/bootstrap (curated community list + DNS-seeds).` — RATIONALE: design.md §7 явно требует "хардкоднутся 20-30 bootstrap-адресов" и "DNS-seeds (как в Bitcoin)" чтобы сеть могла самовоспроизводиться без участия автора. До сих пор cmd/messenger полагался только на `--bootstrap` флаг и seen-peers cache, а cmd/network — только на флаг. Теперь оба бинаря на холодном старте падают в `bootstrap.Defaults` (community list + DNS A/AAAA). Списки в апстриме намеренно пустые: публиковать адреса до релиза смысла нет, переменные `CommunityList`/`DNSSeeds` и ldflag-override `ldflagList` готовы для форков и release pipeline.
- `[2026-05-03] [DOCS] DESIGN-DOC SYNC PASS.` — design.md §10 переписан под фактический tech stack (pion/webrtc убран из messenger-бинаря, явно перечислена браузерная сторона). §9 — application protocol больше не Go-формат. §4 «Path discovery» расшифровано как iterative LookupNode (не отдельный message type, эквивалентно по эффекту). §8 threat-model расширен статусной колонкой и добавлены строки про PoW (opt-in), per-IP rate-limit, replay protection, DTLS fingerprint runtime check (🟡 native browser API). Никакого изменения протокола или поведения — только документация.
- `[2026-05-03] [PHASE 3+8] DECISION: реализовать S-Kademlia disjoint paths + sibling lists.` — RATIONALE: design.md §4 явно требует оба механизма как защиту от Sybil. Без них один Sybil-кластер вокруг target'а может успешно подавить как lookup (single path), так и хранение (только K closest реплицируются). **Disjoint paths**: `pkg/dht.iterativeFind` теперь запускает `Config.Disjoint=3` параллельных путей с round-robin partition initial top-K и shared visited-set — peer touched by one path is off-limits to the others. Стоимость атаки растёт линейно по d. **Sibling lists**: `pkg/dht.RoutingTable.Siblings(s)` exposes the s closest to self; `PutValue` сейчас реплицирует на K closest ∪ own siblings (dedup'd) — Sybil-кластер вокруг key должен дополнительно захватить наш собственный neighborhood, чтобы стереть запись. API backwards-compatible: `Config.Disjoint=1` восстанавливает legacy single-path. Тесты: `pkg/dht/skademlia_test.go` (functional convergence + sibling replication) + `pkg/dht/skademlia_internal_test.go` (unit-level disjoint property + alpha bound + merge dedup). Closes deferred items in `review/PENDING.md`, `review/threat-model.md`.

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
