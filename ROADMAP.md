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
| 9 | Threat-model audit (protocol-slice) | ✅ done | 2026-05-04 | 2026-05-05 |
| 10 | Inter-node WebRTC mesh | 🚧 in progress | 2026-05-05 | — |

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

## Phase 9 — Threat-model audit — branch `feat/webui-remote-auth`

После публикации feat/webui-remote-auth прошла внутренняя проверка по комбинированной модели угроз: govulncheck (чисто), внешний обзор литературы (P2P/WebRTC/Argon2id/SQLite/Caddy), и три параллельных независимых ревью кода (HTTP/auth, P2P/crypto/storage, privacy/DoS/frontend). HTTP/auth/frontend slice починен в первом коммите Phase 9 (см. decisions log 2026-05-05 «AUDIT»). Этот раздел — **protocol-level slice** Phase 9.

### Done

- [x] **DHT eclipse mitigation: per-/24 bucket cap** — `pkg/dht/bucket.go::subnetOver` ограничивает `MaxContactsPerSubnet=2` контактов в одном bucket из одного /24 IPv4 (или /64 IPv6). Loopback освобождён (тесты/локалка). Закрывает trivial Sybil amplifier — атакующий с одного IP не может больше ассигновать произвольное число NodeID и забить bucket. Tests: `bucket_subnet_test.go`.
- [x] **Relay amplification fix** — `pkg/signaling/service.go::relay` (1) использует `Router.LocalNextHop` (новый метод) — cache-only lookup, drop on miss, без iterative DHT-walk; (2) per-source-IP rate-limit `relayBudgetPerMinute=60`. Закрывает 1 envelope → 24 outbound FIND_NODE amplification.
- [x] **Cold-cache `env.Sender`** — `pkg/signaling/service.go::verifySenderIdentity` теперь активно ретраит resolver lookup в 4-секундном бюджете; на match — accept, на mismatch — reject. На пустой бюджет (presence не реплицировалась) — admit-with-deferred-verification (app-layer SignedSDP — последняя линия). Логирует warn для visibility.
- [x] **STORE signed-only invariant** — `dht.NewNode` теперь warn'ит когда Store=nil (silent default = unvalidated MemoryStore) и идёт через явное `presence.NewRateLimitedStore` в production. `MemoryStore` doc-comment предупреждает что без обёртки публичный bulletin-board.
- [x] **Wire frame size** — `wire.MaxFrameSize` 1 MiB → 64 KiB. Все легитимные DHT-сообщения (включая NODES с K=20) умещаются в ~1 KiB; 64 KiB — естественный UDP-cap. Закрывает allocate-before-validate amplification.
- [x] **TURN username validation** — `pkg/turn::validUsername` отказывает empty user portion (`123:`) и cap'ит overall length (256). Закрывает credential-cache pollution.
- [x] **Outbox TTL + per-peer cap** — `storage::AddOutboxItem` rejects beyond `MaxOutboxItemsPerPeer=1000`; `messenger::outboxRetentionPump` каждые час дропает items older than 7 дней. Закрывает forever-pending growth.
- [x] **Misc protocol hardening** — PutValue parallelism cap `putValueParallelism=8` (был unbounded ~40 concurrent); `NewTxID`/`NewSessionID` теперь panic на crypto/rand failure (был silent zero); envelope v1 (no hops counter) больше не принимается — pre-prod, все peers v2.

### Out-of-this-PR / deferred (Phase 9.5)

- [ ] 🚧 **DHT UDP amplification (BEP-42-style cookie)** — FIND_NODE/FIND_VALUE ~16x amplification на спуфленый IP. Stateless HMAC cookie keyed on `(srcIP, time-window)` — клиент должен echo'ить токен в follow-up запросе, иначе только маленький cookie-only ответ. Wire-format change. Существующий per-IP rate-limit (100 pps) частично смягчает amplification ratio per source, но настоящий fix — cookie protocol. ~3-4 часа на качественную имплементацию.
- [ ] 🚧 **Strict PING-back contact verification** — текущая защита: per-/24 bucket cap + декodeContacts фильтр. Полная защита (контакт добавляется только после успешного PING-back challenge с nonce) требует хранения «candidates pool» и async-verify сценария. Per-/24 cap покрывает базовый Sybil; PING-back добавит резистентность к более продвинутым атакам.
- [ ] 🚧 **TURN per-source allocation quota** — `validUsername` (Phase 9) закрывает credential-cache pollution, но не ограничивает число одновременных allocations с одного source IP. Нужен лиmit (e.g. N=5) с учётом lifecycle через pion's hook API. Без этого аутентифицированный клиент может нагрузить relay неограниченно.

После: 3-iter post-phase ревью по тому же шаблону, что и Phases 7-8.

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
- [x] **Contact removal end-to-end** — `storage.DeleteContact(ctx, hash, opts)` (cascade outbox; optional history wipe) + `Messenger.RemoveContact` (closes active session, then storage) + `POST /api/contacts/delete` + UI: trash button in chat header + confirm modal с чекбоксом "Also delete N messages" (default checked, privacy-first). Tests: `internal/storage.TestStore_DeleteContact{,_Missing}`, `internal/messenger.TestRemoveContact_{CascadesAndClosesSession,KeepsHistory}`, `internal/httpui.TestContactDelete_HTTPRoute`. Browser smoke pass: add bob → seed history → delete with checkbox unchecked → contact gone, outbox cleared, history retained per design. Side-fix: long-standing CSS bug — `#chat-pane` `display:flex` rule overrode `[hidden]` (chat pane stayed visible after deselecting contact); now scoped via `:not([hidden])`.

#### WebUI remote auth (Tier A+B, Caddy-fronted TLS) — branch `feat/webui-remote-auth`

Цель: позволить хостить webui на VPS (или любом не-loopback bind) с защищённым входом. Loopback — без изменений (token-в-URL, как сейчас).

- [x] Argon2id password helper — `internal/httpui/auth/password.go` (encoded `$argon2id$…` формат, OWASP params: 64 MiB / 3 iter / 4 threads). Tests: roundtrip, salt-uniqueness, malformed-input rejection, OWASP-params guard.
- [x] TOTP RFC 6238 inline — `internal/httpui/auth/totp.go` (HMAC-SHA1, 6 digits, 30s, ±1 step skew). Tests: 6 RFC 6238 appendix B vectors + skew window + malformed-code rejection.
- [x] Recovery codes — 10× random base32 (XXXX-XXXX), Argon2id-hash при сохранении, single-use consume. Reuse Argon2 helpers + normalize input (case/whitespace/hyphens). Tests: shape, distinct-across-calls, hash-verify roundtrip, normalization.
- [x] Storage tables — `auth_credentials` (single-row), `auth_recovery_codes`, `auth_sessions`, `auth_log` с retention. Tests in `internal/storage/auth_test.go`.
- [x] Session store + sliding-window rate-limit per-IP — `internal/httpui/auth/session.go`, `…/ratelimit.go`. Sessions in SQLite with rolling-touch throttle. Limiter: 5/min window + 20-fails-in-a-row → 15-min lockout. X-Forwarded-For honored only with `-trust-proxy`.
- [x] Auth middleware + handlers — `GET/POST /login`, `POST /logout`, mode-выбор (loopback ⇒ token, иначе ⇒ session-cookie). Cookie: HttpOnly+Secure+SameSite=Strict + Path=/. Tests: login success/fail/TOTP/recovery, /protected requires session, logout clears.
- [x] CSRF guard — `requireCSRFHeader` middleware checks `X-Requested-With: udisend` на state-changing POST/PUT/DELETE; `app.js` `api()` wrapper sets it; `/login` exempt (Origin-check inside handler instead). Tests: integration POST без header → 403.
- [x] CLI subcommands — `messenger -set-password`, `-setup-totp`, `-reset-totp` через `golang.org/x/term.ReadPassword`. Pure helpers in `auth.SetPassphrase / StartTOTPSetup / FinishTOTPSetup / ResetTOTP`; CLI glue in `cmd/messenger/auth_cli.go`. Tests cover the helpers.
- [x] Startup-guard — non-loop bind ⇒ обязательны passphrase + (-tls-cert+-tls-key OR -trust-proxy), иначе fatal. `resolvePublicMode` + 7 unit tests in `cmd/messenger/public_config_test.go`.
- [x] Public URL — `-public-host` флаг; `URL()` для public mode возвращает `https://<host>/login` без token-параметра. `-public-host` falls back to listen addr.
- [x] Login template — kept inline in `auth_handlers.go` (60 lines, html/template parsed once); logout-кнопка добавлена в `index.html` + `app.js` (видна только в public mode по `auth_mode` в snapshot).
- [x] Audit log writer + retention prune — `h.audit(...)` пишет на каждом login attempt / logout / lockout. `runAuthMaintenance` goroutine раз в час прунит sessions + audit log + rate-limit state.

Acceptance: с `-http 0.0.0.0:9000 -trust-proxy -public-host messenger.example.com`, после `-set-password` + `-setup-totp`, можно зайти по `https://messenger.example.com/` → форма passphrase+TOTP → сессия → доступ к UI; перебор пароля даёт 429 после 5 попыток/мин и 15-минутный lockout после 20 fails. Loopback-режим работает идентично текущему (token-в-URL, никакой формы).

#### WebUI bootstrap management — branch `feat/webui-remote-auth`

Цель: позволить пользователю редактировать список bootstrap-нод прямо из webui — без перезапуска и без CLI-флагов. Bootstrap-список в pkg/bootstrap (community + DNS) и runtime seen-peers cache остаются как fallback.

- [x] Schema + storage CRUD — `bootstrap_overrides (address PK, enabled, note, added_at, last_status, last_status_at)` + `Add/Remove/SetEnabled/MarkStatus/List/EnabledList/Exists` в `internal/storage/bootstrap_overrides.go`. Tests: `internal/storage/bootstrap_overrides_test.go`.
- [x] `pkg/network` — новый optional `BootstrapOverrideStore` интерфейс, консультация **до** seen-peers cache при старте; `Node.Bootstrap(ctx, addr)` для hot re-bootstrap; `bootstrapOne` пишет последний статус в overrides store (silent no-op если адреса нет в overrides).
- [x] REST API — `GET /api/bootstrap` (объединённый список manual + cache + default), `POST /api/bootstrap/add` (с синхронным dial для немедленного `ok`/`fail` badge), `POST /api/bootstrap/remove` (по `source`), `POST /api/bootstrap/toggle`, `POST /api/bootstrap/reconnect` (concurrent re-dial всех enabled). CSRF-guard на mutating endpoints. Tests: `internal/httpui/bootstrap_handlers_test.go`.
- [x] UI — гамбургер `#menu-btn` открывает Settings modal с секцией Bootstrap nodes: список со status-точкой / source-tag / note, toggle-switch для manual entries, trash для manual+cache; форма Add (host:port + note); кнопка Reconnect. Стили — `.bootstrap-row`, `.bootstrap-add-row`, `.toggle-switch` в `app.css` (responsive: < 540px переключается в стопку).

#### WebUI Settings — full rollout — branch `feat/webui-remote-auth`

Цель: вынести из CLI всё что можно безопасно переключать рантайм. Settings теперь tabbed: Security / Network / Privacy / Notifications. Bootstrap живёт под Network.

- [x] Tabbed settings shell — `showSettingsModal` + `SETTINGS_TABS` с `init` per-tab; lazy-load + sessionStorage запоминает активную вкладку. CSS `.settings-tab` / `.settings-section` / `.modal-stack`.
- [x] Security tab — `GET /api/auth/state`, change-passphrase, TOTP enroll/disable (server-side pending-secret store, не round-trip), recovery-code regen, sessions list (по public_id, не cookie value), revoke, audit log tail. Step-up: при TOTP enrolled passphrase ОДНОГО недостаточно — обязателен code или recovery; replay-protection через `last_totp_step` в app_settings.
- [x] Network tab — Bootstrap + STUN/TURN custom servers (`ice_overrides` + `EffectiveICEServers` в `httpui/server.go`) + read-only DHT diagnostics (`network.Node.Stats()`). Disable-fallback toggle честно отключает auto-discovered ICE.
- [x] Privacy tab — history retention auto-prune (`messenger.historyRetentionPump` каждые час), storage usage + vacuum, encrypted identity export (Argon2id v2 формат: header || params || salt || aead с AAD = header+params+salt; параметры в blob, version-byte dispatch для будущих миграций).
- [x] Notifications tab — browser Notification API toggle (срабатывает только при document.hidden), runtime verbose-logs toggle через `*slog.LevelVar` + персист в app_settings.
- [x] Schema additions — `app_settings (key, value)` generic K/V; `ice_overrides (url PK, username, credential, enabled, added_at)`.
- [x] Hardening (post-3-iter-review) — все sensitive POST'ы под `MaxBytesReader(4 KiB)`; `invalidateOtherSessions` через `context.WithoutCancel` + 10s timeout, через single `DELETE … WHERE id != ?`; ICE URL scheme lower-cased и нормализуется на каждом из add/remove/toggle (нет orphan-rows); ICE add/remove аудитируется в public mode; `wipe()` использует `runtime.KeepAlive`; `parsePositiveInt` заменён на `strconv.Atoi`.

#### CLI UX overhaul (public/run/status + autocert) — branch `feat/webui-remote-auth`

Цель: один удобный CLI вместо россыпи флагов. **Default zero-config:** `udisend run` без аргументов и без preconditions поднимает loopback с token-в-URL. Переход в публичный режим — `udisend public -enable <addr>`: IP-литерал → lan-ip (self-signed cert), DNS-имя → public-autocert (Let's Encrypt через embedded autocert), `-via-proxy` → public-proxy (за внешним Caddy/nginx). `public -disable` возвращает в loopback. `public -reset` стирает auth + profile (identity сохраняется). Все флаги опциональные, storage по XDG.

- [x] Подкоманды через `flag.NewFlagSet`: `run`, `public`, `status`. Корневой `udisend` без аргументов печатает usage. Старые флаги `-set-password`/`-setup-totp`/`-reset-totp`/`-public-host`/`-tls-cert`/`-tls-key`/`-trust-proxy` удалены — ветка ещё не релизнута, alias не нужны.
- [x] Storage default — `os.UserConfigDir()/udisend/messenger` (XDG-compliant на Linux, Application Support на macOS, %AppData% на Windows). Все subcommands принимают опциональный `--storage` для override.
- [x] Deployment profile — persisted в `app_settings` (keys `deploy.mode`, `deploy.bind_http`, `deploy.bind_p2p`, `deploy.public_host`, `deploy.tls_cert`, `deploy.tls_key`, `deploy.trust_proxy`). Mode ∈ {loopback, lan-ip, public-autocert, public-proxy}. Теплый load/save через `Storage.SetSetting`/`DeleteSetting`.
- [x] Zero-config `cmd_run.go` — читает profile из storage; **если profile нет — auto-defaults в loopback без всякой ceremony**. Флаги `--bind-http`, `--bind-p2p`, `--public-host`, `--tls-cert`, `--tls-key`, `--trust-proxy` переопределяют profile.
- [x] `cmd_public.go` — `-enable <addr>` (IP→lan-ip / DNS→public-autocert / `-via-proxy`→public-proxy), `-disable` (через `Storage.DeleteSetting`), `-reset` (через `Storage.DeleteAuthCredentials`, удаление tls.{crt,key} + recovery-codes.txt + autocert/ dir). Идемпотентно.
- [x] `cmd_status.go` — печатает: storage path, identity hash, profile mode, auth state (passphrase ✓, TOTP ✓, recovery N/10), TLS cert expiry.
- [x] Embedded autocert TLS — для public-autocert mode `golang.org/x/crypto/acme/autocert.Manager` поверх `httpui.Server`; ACME HTTP-01 challenge handler на :80 в той же errgroup. Серт автоматически выпускается, кэшируется в `<storage>/autocert/`, и продлевается. `httpui.Config.TLSConfig *tls.Config` — новый optional-альтернатива к TLSCert/TLSKey, mutually exclusive validation.
- [x] `cert.go` — генерация self-signed cert через `crypto/x509.CreateCertificate` (RSA 2048, 365 дней, SAN на host из public-host: IP-литералы → IPAddresses, DNS-имена → DNSNames). Stdlib only.
- [x] `qr.go` — терминальный QR через `rsc.io/qr` (BSD-3, нулевые транзитивные deps), Unicode half-blocks (`▀ ▄ █`) для compact rendering.
- [x] `prompts.go` — единый `bufio.Scanner` поверх stdin (фикс бага: старый `auth_cli.go` создавал `bufio.NewReader` дважды и терял данные при piped input). Поддерживает no-echo через `golang.org/x/term`, fallback на line-buffered.
- [x] `profile.go` — load/save deployment profile, validate (мигрирует логику `resolvePublicMode`). Tests: 13 кейсов в `profile_test.go` (все 4 mode валидны/невалидны, isLoopbackBind table-driven, publicMode mapping).
- [x] Recovery codes save — в `<storage>/recovery-codes.txt` (mode 0600) с явным предупреждением и подтверждением Enter.
- [x] Storage helpers — `Storage.DeleteSetting` (TDD, idempotent) для disable; `Storage.DeleteAuthCredentials` (TDD, atomic transaction wiping passphrase + TOTP + recovery codes) для reset. Mirror на ErrCredentialsNotSet после delete.
- [x] Удалить `cmd/messenger/auth_cli.go`, `cmd/messenger/public_config.go` (+ `_test.go`); тесты `public_config_test.go` мигрированы в `profile_test.go`.
- [x] **CLI на cobra** — переход с stdlib `flag` на `github.com/spf13/cobra` (по запросу пользователя). Иерархичные подкоманды: `udisend run`, `udisend public {enable,disable,reset}`, `udisend status`. Auto-generated help, корректная обработка `--bool=false` через `Flags().Changed`, mutually-exclusive flag groups (`--proxy` vs `--tailscale`), позиционный `[addr]` после `enable`. Persistent `--storage` flag на root наследуется всеми subcommands. Бинарь +4 МБ (cobra+pflag).
- [x] **`pkg/tailscale`** — обёртка над `tailscale.com/client/local.Client`: `New()`, `Detect(ctx)` с ясными sentinel ошибками (`ErrNotRunning`/`ErrNotLoggedIn`/`ErrNoMagicDNS`), `Hostname(ctx)` (FQDN без trailing dot), `Cert(ctx, host)` returning `*tls.Certificate`. Tests: 7 кейсов с моком HTTP RoundTripper (Detect/Hostname/Cert success+failure пути).
- [x] **`modePublicTailscale`** — для устройств в Tailscale tailnet'е. Hostname берётся через `tailscale status` (Self.DNSName), cert получается через `tailscale cert` (Tailscale ACME pipeline → Let's Encrypt → device). Профиль forbid'ит TLSCert/TLSKey (tailscaled управляет сам) и TrustProxy. Validate тесты × 3.
- [x] **`udisend public enable` без аргументов = Tailscale** (по умолчанию) — auto-detect tailscaled, прелёт-чек cert до запроса passphrase. Если HTTPS не включён в admin tailnet или daemon не запущен, валим с прямой ссылкой и подсказкой про explicit address. Флаг `--tailscale` удалён за избыточностью.
- [x] **`tailscaleCertRefresher` в `cmd_run.go`** — атомарный `*tls.Certificate` указатель + background goroutine рефрешит каждые 12h. Initial fetch fail-fast при старте. `tls.Config{GetCertificate: ...}` читает из atomic pointer.

Acceptance: `udisend run` без preconditions работает (loopback); `udisend public enable 192.168.0.105` ставит lan-ip-режим; `udisend public enable udisend.example.com` собирает autocert profile; `udisend public enable --tailscale` авто-детектит Tailscale, ловит реальный LE cert, **никаких cert warnings в браузере**; `public disable` возвращает в loopback; `public reset` стирает auth (`status` показывает passphrase=not set). Все тесты `-race` зелёные.

#### Topology test suite — branch `feat/webui-remote-auth`

Цель: автотесты разных форм сети и доступности узлов между ними — на DHT/signaling уровне через `transport.MemoryHub`. Без браузера, без Docker, без root. Замыкает строку «100-узловые in-process тесты» из «What does NOT ship overnight». NAT-матрица и реальный e2e DataChannel — отложены до момента, когда появится TUI-клиент с pion (тогда `pion/vnet` сделает их дешёвыми).

- [x] `internal/topotest` harness — `Cluster`, `Spawn(N)`, `Connect(i, j)`, `Bootstrap(i, j)`, `Detach(i)/Reattach(i, prev)`, `WaitFor(predicate)`, `LookupAcross(t)`, `LookupSuccessRate(t, src, targets)`, `KnowsAbout(i, j)`, `AllPairsKnow(idx)`. Sanity tests in `cluster_test.go`.
- [x] Topology builders — `BuildChain`, `BuildRing`, `BuildStar`, `BuildMultiHub(groupSize)`, `BuildFullMesh`, `BuildRandom`, `BuildBridgedClusters`, `BuildKademliaNatural` в `topology.go`.
- [x] §1 form-of-graph tests — `topology_test.go`: chain, ring, star, multi-hub (3×3), full mesh, random (Erdős–Rényi 2N edges), bridged clusters, kademlia-natural. Каждая проверяется через `LookupAcross` (any → any).
- [x] §2 bootstrap tests — `bootstrap_test.go`: thundering herd N=8 в параллель, dead seed → fast-fail, recovery via Detach/Reattach, failover между двумя seed, partial seed set (3 мёртвых из 5).
- [x] §5 churn tests — `churn_test.go`: node leaves в ring → выжившие находят друг друга, lookup ушедшего терминирует; node rejoins; partition + heal в bridged clusters; 50% network replaced + новые bootstrap-ятся → конвергенция.
- [x] §6 adversarial tests — `adversarial_test.go`: per-/24 subnet cap (10 sybil-контактов из одной подсети → ≤ MaxContactsPerSubnet=2 в любом бакете); malicious peer возвращает junk contacts в NodesMsg → lookup терминирует в budget; honest path выживает несмотря на soup malicious peer в shortlist.
- [x] §7 lookup correctness — `lookup_test.go`: lookup несуществующего ID терминирует без выдачи ghost; 10 параллельных lookup того же target от одного peer, все успешны; converged ring → worst-case lookup < 2s; chain success rate = 100%.
- [x] §9 scale — `scale_test.go`: N=10/50 в дефолтной обойме, оба зелёные за ~1s wall clock. `scale_slow_test.go` (`//go:build slow`) — N=100 и N=500, multi-seed (`seedCount = max(5, n/20)`, ~20 клиентов на seed). Первый дизайн с одним seed-узлом упирался в его recv-loop bottleneck — это была структурная проблема теста, не race-overhead, как изначально показалось. N=100 — 5.4s, N=500 — 7.3s, оба 100% success rate под `-race`.
- [x] §8 signaling — `signaling_test.go` + `signaling.go` harness extension (`Options.WithSignaling`, `SigCluster`, `Connect/AcceptOn/SendAndReceive`, `OverrideAddress` для relay). 9 кейсов: direct delivery, both-directions, relay chain (A→relay→B), 2-hop layout, fail-fast на unknown peer, partition блокирует Connect, relay-death кейс, fan-out 1→6, multi-payload session. Все зелёные под `-race` × 3 в ~1s.

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
- ~~100-узловые in-process тесты~~ — реализовано в `internal/topotest` (`go test -race ./internal/topotest/...` гоняет N=10/50/100 в дефолте, N=500 под `-tags=slow`). Docker testcontainers integration остаётся отложенным.
- PGO, reproducible builds, signed releases.

---

## Phase 10 — Inter-node WebRTC mesh — branch `feat/phase10-rtc-mesh`

**Цель:** обеспечить отказоустойчивость графа связности при выпадении значительной части публичных узлов. Сейчас DHT и signaling-relay живут на одном UDP-сокете; в k-bucket попадают только узлы с публичным IP — NAT'нутые принимать входящие не могут. Если публичные ложатся, NAT'нутые между собой не общаются и сеть деградирует. Phase 10 добавляет **persistent WebRTC DataChannel-overlay** между всеми узлами заранее: после коллапса signaling-relay-инфраструктуры уже установленные DC переживают и поддерживают связность для existing-сессий.

**Deliverable:** второй транспорт `pkg/transport.WebRTCTransport` (поверх pion/webrtc/v4) живёт рядом с UDPTransport; PeerManager поддерживает K=8 живых DataChannel'ов на узел; `compositeTransport` в `pkg/network` маршрутизирует Send по типу адреса (UDPAddr → UDP, WebRTCAddr → RTC); resilience-сценарий «убили 2 из 3 публичных узлов в кластере 5» зелёный под `-race`.

**Архитектурные решения** (см. Decisions log от 2026-05-05):
- **Полный peer overlay** — и публичные, и NAT-узлы держат K mesh-links. Mental model единая, граф устойчивее.
- **Strict Send semantics** — `Send` к не-connected пиру → `ErrNoRoute`. PeerManager поддерживает пул проактивно через explicit `Connect(ctx, peer)`. Не блокируем DHT hot path на ICE handshake.
- **pion/webrtc/v4** — единственный mature WebRTC-стек в Go. Уже частично присутствует как indirect (через pion/turn, pion/stun); поднимается в direct.
- **Mesh handshake через существующий `signaling.Channel`** — не открываем параллельный протокол. Новые `InnerType` коды (0x06/0x07/0x08) для Mesh-Offer/Answer/ICE; payload идёт под Noise XK end-to-end как и app-layer SDP.

### Tasks (12 sub-stages, TDD: failing test → minimal code → refactor)

- [x] **10.1 Scaffolding** — Phase 10 в ROADMAP, decisions log entries (4 решения), `go get github.com/pion/webrtc/v4` (snapshot — окончательный pin закрепляется в 10.4 при первом импорте), синхронизация `p2p-messenger-design.md` §10 (pion/webrtc на network-узлах, не на messenger-binary). Commit `phase 10.1: scaffold roadmap, decisions log, design doc sync`.
- [x] **10.2 WebRTCAddr + transport skeleton** — `pkg/transport/webrtc.go`: `WebRTCAddr` с `Network()="rtc"`, `String()="rtc:<hex16>"`, `ParseWebRTCAddr`. `WebRTCTransport` struct + `ErrNotImplemented` stub'ы, sentinel errors (`ErrNoRoute`/`ErrAlreadyConnected`/`ErrBufferFull`/`ErrInvalidAddr`/`ErrNotImplemented`), compile-time assertion. Tests: 4-кейс table-driven roundtrip + 7-кейс error matrix + interface assertion + fuzz `FuzzWebRTCAddr_RoundTrip` (1.5M execs / 5s, no failures). Commit `phase 10.2: WebRTCAddr + WebRTCTransport skeleton`.
- [x] **10.3 Signaler + envelope extension** — `pkg/signaling/envelope.go`: `InnerMeshOffer=0x06`/`InnerMeshAnswer=0x07`/`InnerMeshCandidate=0x08`, `IsMeshInner`. `pkg/signaling/service.go`: `MeshHandler` type + `SetMeshHandler` (nil-disable, atomic). `pkg/signaling/channel.go`: `Channel.SendMesh` (encrypts under sendMu, rejects non-mesh kinds), `handleMesh` (decrypts under recvMu, queues in `pendingMesh` cap=8 if pre-verify, drains in `flushPending` — symmetric to `handleData`). `pkg/transport/webrtc.go`: `Signaler` interface, `MeshSDPKind` enum, `MeshSDPMsg`. `pkg/network/mesh_signaler.go`: per-peer Channel cache, `Connect` on-first-send with race-loser cleanup. Tests (all -race green): 3 envelope-decode cases + Channel.SendMesh end-to-end + 4 invalid-kind rejections + nil-handler disable + 3 mesh-signaler bridge tests (round-trip, channel reuse, close). Commit `phase 10.3: signaler + InnerMesh* envelope extension`.
- [x] **10.4 PeerSession (pion обёртка)** — `pkg/webrtc/peer.go`: `PeerSession` поверх `*pion.PeerConnection`. API: `CreateOffer` (initiator, opens DataChannel + SDP emit), `AcceptOffer`/`AcceptAnswer` (SDP exchange), `AddRemoteCandidate` (trickle ICE), `Send` с `BufferedAmountHighWater=1MiB` через `OnBufferedAmountLow`, `Recv() <-chan []byte`, `Close()`, `RestartICE()` (через `OfferOptions{ICERestart}` — pion v4 не имеет отдельного метода), `Opened()`/`Closed()` chans. `SDPKind` enum + `ICEServer` re-export для изоляции pion API от верхних слоёв. `MaxMessageSize=64KiB`, `BufferedAmountLowThreshold=512KiB`, `inboxBuffer=256`. Tests (-race ~20ms): `TestPeerSession_OfferAnswerLoopback` (E2E через closures), `TestPeerSession_PeerHashStored`, `TestPeerSession_RejectsLargePayload`. pion/webrtc/v4 v4.2.12 закреплён direct; транзитивно ice/v4, sctp, datachannel, sdp/v3, srtp/v3, turn/v5, interceptor, mdns/v2. Commit `phase 10.4: PeerSession pion/webrtc/v4 wrapper`.
- [x] **10.5 WebRTCTransport — полная реализация** — `pkg/transport/webrtc.go`: `WebRTCTransport` теперь полнофункциональный поверх `udwebrtc.PeerSession`. `Connect(ctx, peer)` singleflight через pending map; PeerSession **eagerly install'ится в peers** перед CreateOffer, иначе inbound answer/candidate теряются. `Run(ctx)` pump'ит `signaler.RecvMeshSDP()`, dispatch по kind: offer → handleOffer (responder session + AcceptOffer), answer → AcceptAnswer, candidate → AddRemoteCandidate. `Send` strict (`ErrNoRoute` без неявного dial). `pumpInbox` per-session goroutine fan-in в shared inbox (cap 256). `fireSDP/fireICE` short-lived goroutines tracked через `t.wg` чтобы не блокировать pion-runtime. `Close` idempotent: закрывает PeerSessions, сигналит pending Connect waiters `ErrClosed`, ждёт wg, закрывает inbox. ErrNotImplemented убран (не используется). Tests (-race green): `TestWebRTCTransport_ConnectAndSend` (full E2E + 1KiB), `TestWebRTCTransport_SendWithoutConnectFails`, `TestWebRTCTransport_ConnectIdempotent` (4 concurrent dials share one handshake), `TestWebRTCTransport_DialParsesAddress`. Commit `phase 10.5: WebRTCTransport full implementation`.
- [ ] **10.6 PeerManager** — `pkg/webrtc/peer_manager.go`: K=8 живых линков (configurable), `PeerSelector` interface, FSM (disconnected→connecting→connected→reconnecting), exp-backoff (1s→64s, jitter ±25%), ICE-restart на 10s+ disconnected. Стартер: `TestPeerManager_FillsToK`, `TestPeerManager_BackoffOnFailure`, `TestPeerManager_ICERestart` через `testing/synctest`.
- [ ] **10.7 compositeTransport + multi-transport routing** — `pkg/network/composite.go`: dispatch by addr type, fan-in inbox. `Router.PreferredTransport(target)`. Generify `network.Node` с `*UDPTransport` на `transport.Transport` interface. Стартер: `TestCompositeTransport_RoutesByAddrType`, существующие тесты `pkg/network/*_test.go` зелёные.
- [ ] **10.8 Capability + legacy compat** — `presence.CapCanWebRTCMesh = 1 << 5`; `capsFor` всегда выставляет; PeerSelector skip'ает legacy-контакты без bit'а. Стартер: `TestCapsFor_AdvertisesMeshOnPhase10`, `TestPeerManager_SkipsLegacyContacts`.
- [ ] **10.9 MemoryWebRTCHub (test fake)** — `pkg/transport/memory_webrtc.go`: in-process hub аналог `MemoryHub` для unit-тестов PeerManager / compositeTransport без pion-overhead. Реальный pion остаётся в `pkg/webrtc/peer_test.go` и e2e. Стартер: `TestMemoryWebRTCHub_BasicSendReceive`.
- [ ] **10.10 E2E resilience scenarios** — `internal/topotest/phase10_mesh_resilience_test.go` (build tag `integration`): A) 5 узлов (3 public + 2 NAT), kill 2/3 public, connectivity ≥ 30s; B) cold-start mesh fill ≤ 60s; C) ICE restart на forced disconnect. Стартер: `TestMeshSurvivesPublicNodeLoss`.
- [ ] **10.11 Observability** — `Stats{RTCPeers, RTCBytesSent, RTCBytesRecv, RTCConnectAttempts, RTCConnectFailures, RTCICERestarts}` через `atomic.Int64`. webui Settings → Network: блок «WebRTC mesh». Стартер: `TestStats_RTCFieldsPopulate`.
- [ ] **10.12 Post-phase 3-iteration review** — три независимых итерации (go-review skill / ultrareview / Explore agent), сводная таблица 3/3→2/3→1/3, MUST FIX исправлены отдельными коммитами с TDD-fix'ами, decisions log retrospective.

### Acceptance criteria

- [ ] **WebRTCAddr** реализует `net.Addr`; round-trip покрыт fuzz'ом.
- [ ] **`pkg/transport.WebRTCTransport`** реализует `transport.Transport` поверх pion/webrtc/v4; Send strict-семантика.
- [ ] **`Connect(ctx, identity.Hash) error`** singleflight + idempotent.
- [ ] **PeerManager** держит K=8 (configurable), exp-backoff, subnet-diverse selector.
- [ ] **compositeTransport** маршрутизирует по типу `net.Addr`; UDP primary, RTC preferred при `PreferredTransport == "rtc"`.
- [ ] **Signaling envelope** расширен `InnerMeshOffer/Answer/ICE`; mesh-handshake через `signaling.Channel` (Noise XK).
- [ ] **`presence.CapCanWebRTCMesh = 1 << 5`**; legacy-узлы игнорируются manager'ом.
- [ ] **TDD coverage**: `go test -race ./pkg/transport/... ./pkg/webrtc/... ./pkg/network/...` зелёные. Fuzz: envelope, WebRTCAddr round-trip, PeerManager FSM.
- [ ] **E2E resilience** — kill 2/3 public → mesh держит connectivity ≥ 30s под `-race`.
- [ ] **Observability** — Stats + webui-блок.
- [ ] **ICE restart** на 10s+ disconnected, recovery ≤ 12s в тесте.
- [ ] **Backpressure** — `bufferedAmount > 1MiB` → блок до `OnBufferedAmountLow` или `ErrBufferFull`.
- [ ] **`p2p-messenger-design.md`** синхронизирован: §10 (pion/webrtc на network-узлах), §8 (threat-model «коллапс публичных узлов» 🔴 → 🟡).
- [ ] **3-iteration post-phase review** проведён, MUST FIX закрыты, decisions log дописан.

### Out-of-this-phase / deferred (Phase 10.5)

- [ ] NAT type detection — pion fallback на UDP при `Connect → ErrICEFailed` хватит для MVP. NAT-aware PeerSelector — Phase 10.5.
- [ ] Eclipse через mesh — subnet-diversity ослабляет; полное решение (cross-validation routing UDP↔RTC) — Phase 10.5 / 11.
- [ ] Onion-routing для mesh-traffic — out of MVP, see general roadmap.

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
- `[2026-05-03] [PHASE 3+8] DECISION: реализовать S-Kademlia disjoint paths + sibling lists.` — RATIONALE: design.md §4 явно требует оба механизма как защиту от Sybil. Без них Sybil-кластер вокруг target'а может успешно подавить как lookup (single path), так и хранение (только K closest реплицируются). **Disjoint paths**: `pkg/dht.iterativeFind` запускает `Config.Disjoint=3` параллельных путей с round-robin partition initial top-K и shared visited-set — peer, опрошенный одним путём, недоступен остальным. Стоимость Sybil-атаки растёт линейно по d. **Sibling lists**: `pkg/dht.RoutingTable.Siblings(s)` отдаёт s ближайших к self; `PutValue` реплицирует на K closest ∪ own siblings (dedup'd) — Sybil-кластер вокруг key должен дополнительно захватить наш собственный neighborhood, чтобы стереть запись. Тесты: `pkg/dht/skademlia_test.go` (convergence + sibling replication) + `pkg/dht/skademlia_internal_test.go` (disjoint property + alpha bound + merge dedup). Closes deferred items в `review/PENDING.md`, `review/threat-model.md`.
- `[2026-05-03] [PHASE 8] DECISION: contact removal cascades outbox always, history opt-in.` — RATIONALE: Outbox содержит ещё не доставленные сообщения к удалённому контакту. Если оставить — при следующем resolve того же hash они уйдут получателю; пользователь воспримет это как утечку и сюрприз. Поэтому outbox при удалении контакта вычищается всегда. История — это запись разговора пользователя, удалять её принудительно нельзя. UI показывает чекбокс "Also delete N messages" в confirm-модалке, default checked (privacy-first, как в Signal). Active signaling session закрывается через существующий `Messenger.removeSession` путь до удаления storage row, чтобы не остался зомби-handler. API: `storage.DeleteContact(ctx, hash, DeleteContactOptions{WipeHistory bool})`, `Messenger.RemoveContact(ctx, hash, opts)`, `POST /api/contacts/delete`.
- `[2026-05-04] [PHASE 8] REVIEW: WebUI remote auth, 3 iterations done.`
  - **Iter 1, 2, 3 unanimous (3/3) — fixed:**
    1. `cmd/messenger/auth_cli.go` — second printed line under label "Secret" was `auth.OTPAuthURL(...)` again, not the base32-secret. Manual-entry TOTP path was broken end-to-end. Fixed: print `base32.StdEncoding.WithPadding(NoPadding).EncodeToString(secret)`.
    2. `/login` body unbounded — Argon2id at ~64 MiB peak per attempt, attacker could pin CPU/RAM with a megabyte passphrase. Fixed: `http.MaxBytesReader(w, r.Body, 4*1024)` at top of `handleLoginPOST`.
  - **Iter 2/3 BLOCKING (2/3) — fixed (functional defect, fix unconditional):**
    3. `app.js boot()/api()/EventSource` aborted in public mode because TOKEN was null/empty (cookie auth path). After successful login the SPA never loaded. Fixed: removed the token-required guard in boot, made Authorization header conditional on TOKEN truthy, EventSource omits `?token=` when empty, added `credentials: 'same-origin'` and `withCredentials: true` so the session cookie reaches both fetches and SSE.
  - **Iter 1 BLOCKING + iter 3 SUGGESTION (2/3) — fixed:**
    4. `URL()` in public mode without `-public-host` printed `https://0.0.0.0:9000/login`. Fixed: `resolvePublicMode` now requires non-empty `PublicHost`; `URL()` no longer falls back to listener addr in public mode.
    5. `handleLogout` not wrapped in `requireCSRFHeader`. Fixed: route registered as `s.requireCSRFHeader(s.authH.handleLogout)`; logout button JS already sends `X-Requested-With`.
  - **Iter 1 BLOCKING + iter 2 SUGGESTION (2/3) — fixed:**
    6. `RecordFail` reset `st.fails = 0` after firing lockout, giving attacker a fresh 20-fails-per-15-min budget perpetually (~1900 attempts/day per IP). Fixed: don't reset `fails` — only `attempts`; only `RecordSuccess` clears state. Each post-lockout fail re-arms the lockout, asymptote drops to 1 attempt per LockDuration. New test `TestRateLimiter_FailsAccumulateAcrossLockouts`.
  - **Iter 2/3 SUGGESTION (2/3) — fixed:**
    7. `auth_log` / `auth_sessions` accept unbounded UA strings. Fixed: `truncateUA` caps at 256 bytes before persistence.
  - **Iter 3 only (1/3) — fixed (real correctness bug):**
    8. `tryRecoveryCode` TOCTOU race: two concurrent requests with the same recovery code both hash-verify, both call `MarkRecoveryCodeConsumed`, both return success → "single-use" violated. Fixed: `MarkRecoveryCodeConsumed` is now an atomic conditional UPDATE (`SET consumed_at=? WHERE id=? AND consumed_at=0`) returning `RowsAffected==1`; `tryRecoveryCode` loops to next candidate on a lost race. New double-consume regression check in `auth_test.go`.
  - **Iter 1 only (1/3) — fixed (cheap signal improvement):**
    9. Recovery-code logins were audited as plain `login_success`, indistinguishable from TOTP logins. Fixed: emit `login_success_recovery` when the second factor was satisfied via a recovery code, so the operator can spot break-glass usage.
  - **Test gaps closed:** `TestSameOriginIfPresent` (5 cases), `TestLoginPOST_CookieAttributes` (HttpOnly+Secure+SameSite=Strict+Path+MaxAge), `TestLoginPOST_BodyTooLargeRejected`, `TestRateLimiter_FailsAccumulateAcrossLockouts`, recovery-code double-consume regression in `auth_test.go`.
  - **Deferred (1/3, accepted as follow-up):**
    - RateLimiter zero-value trap (iter 1) — production wiring always sets fields; defer `NewRateLimiter` constructor with validation.
    - `readPassphrase` pipe-input bug from double `bufio.NewReader` (iter 3) — production CLI path is TTY; pipe path is dev-only and not on the documented workflow.
    - Passphrase-change-invalidates-all-sessions (iter 2) — security improvement, defer to follow-up; current behaviour is documented.
    - 80-bit recovery codes vs current 40-bit (iter 3) — Argon2id + per-IP rate-limit make 40-bit defensible; revisit if the verify path ever bypasses rate-limit.
    - Absolute session `MaxLifetime` cap (iter 2 SUGGESTION) — current rolling 30d Idle is acceptable for MVP; revisit when first compromise drill prompts it.
    - TOTP step-replay protection (iter 2) — 30 s reuse window vs intercepted code is a documented gap; closing requires storing last-consumed step per credential.
    - `runAuthMaintenance` synctest coverage (iter 2/3) — wire when synctest layout for the package settles.
    - `randomToken()` swallows `crypto/rand` error (iter 1/2/3) — pre-existing, broader than this PR.
- `[2026-05-04] [PHASE 8] DECISION: WebUI remote auth = passphrase + TOTP (Tier A+B), Argon2id, SQLite-backed sessions, Caddy-fronted TLS.` — RATIONALE: до сих пор httpui биндился только на loopback и юзал random URL-token. Хостинг на VPS требует защиты от интернет-перебора. Form login + Argon2id (OWASP 64 MiB/3/4) — стандарт; TOTP RFC 6238 inline (без новых deps) удваивает прочность — украденный пароль ≠ доступ. Session — server-side в SQLite (не JWT: revoke важнее performance, single-user, переживает рестарт). Cookie HttpOnly+Secure+SameSite=Strict + custom-header CSRF (`X-Requested-With: udisend`) поверх — defense in depth. TLS делегируем Caddy (auto Let's Encrypt) — `golang.org/x/crypto/acme/autocert` в бинаре поддерживать дороже, чем `Caddyfile` из 5 строк. Startup-guard: non-loop bind без `-tls-cert/-key` или `-trust-proxy` — fatal, чтобы случайно не выкатить пароль в plaintext. Loopback режим не меняется (token-в-URL остаётся zero-friction для локалки). Trade-off: identity-приватник теперь живёт на VPS — root-on-VPS = identity-compromise; принимаем как осознанный выбор пользователя ("VPS — это моя машина"). Дополнительно: passphrase-only (без username, default `admin`-эквивалент не добавляет security при single-user; миграция на multi-user тривиальна, если понадобится). Зависимости: 0 новых third-party — argon2 из `x/crypto`, term из `x/term` (justified: read-no-echo password в TTY), TOTP реализуем inline (~30 LOC). Branch: `feat/webui-remote-auth`.
- `[2026-05-05] [PHASE 9] REVIEW: protocol-slice — 3 iterations done.` Inputs: 3 independent reviewers covering DHT/wire, signaling/relay/cold-cache, storage/messenger/turn. **Fixed in this branch** (post-review): signaling cold-cache verification dispatched OFF the dispatcher goroutine into `Channel.verifyAndAdmit` (was: blocked DHT recv for 4s); admit-with-deferred-verification removed — failed verification now hard-rejects (was: silent admission with non-existent `unverified` flag); `Channel.verified atomic.Bool` gates `handleData` so frames stay in `pending` until verification succeeds; `Channel.recvMu` serialises `noise.Decrypt` across `handleData`/`flushPending`/BYE-validate (race-detector found this). decodeContacts now applies `MaxContactsPerSubnet` to RETURNED slice as well as bucket — closes iterativeFind 20-FIND_NODE amplification through one /24. storage.Open `SetMaxOpenConns(1)` so PRAGMAs (synchronous, foreign_keys, busy_timeout) actually apply globally (was: only on first pool conn). AddOutboxItem becomes `INSERT ... WHERE (SELECT COUNT < cap)` — atomic, no TOCTOU. `allowRelayFrom` sweeps map only when threshold (256) exceeded — O(1) amortised on legitimate traffic; `relayHostKey` strips IPv6 zone-id and uses `net.UDPAddr.IP.String()` directly. `verifySenderIdentity` retry loop uses `time.NewTimer.Reset` instead of leaking `time.After` channels. ROADMAP Phase 9.5 now lists "TURN per-source allocation quota" explicitly. Tests added: bucket /24 IPv6 + IPv4-mapped-IPv6 + bigger ipv6 set; outbox cap (1000 inserts → ErrOutboxFull); storage.PruneOutboxOlderThan; pkg/turn validUsername table-driven 11 cases. context.Canceled filtered from outbox/history prune Warn logs. **Deferred to Phase 9.5**: BEP-42 cookie (UDP amp), strict PING-back contact verify, TURN allocation cap (silently dropped pre-review — now explicitly listed). All 21 packages green under -race.
- `[2026-05-05] [PHASE 9] FIXES: protocol-level slice.` Closed: per-/24 bucket cap (`pkg/dht/bucket.go`, `MaxContactsPerSubnet=2`) — trivial Sybil amplifier; relay drops on cache-miss + per-source-IP rate-limit `relayBudgetPerMinute=60` (`pkg/signaling/service.go`) — 1 envelope→24 FIND_NODE amplification gone; envelope v1 rejected (was relay-loop vector); `verifySenderIdentity` retries presence lookup in 4-s budget, fails closed on mismatch, deferred-mark on miss (was: returned ok=true on resolver error); STORE signed-only contract loud-default in `dht.NewNode`; wire MaxFrameSize 1 MiB → 64 KiB; TURN `validUsername` rejects empty user / overlong (256); outbox per-peer cap (1000) + 7-day TTL pump; PutValue parallelism capped at 8; NewTxID/NewSessionID panic-on-crypto/rand failure (was silent zero); presence Resolver cache-respects bool from Put. Tests added for /24 cap. **Deferred to Phase 9.5**: BEP-42-style stateless HMAC cookie for FIND_NODE/FIND_VALUE (UDP source-validation against spoofed IP amplification — needs wire-format change, ~3-4 hours); strict PING-back contact verification (per-/24 cap covers basic Sybil for now). All 21 packages green under `-race`.
- `[2026-05-05] [PHASE 9] AUDIT: combined threat-model pass.` Inputs: (1) govulncheck across module — 0 known CVEs in deps; (2) external literature survey — IPFS Sybil 2024, WebRTC IP-leak/CSP-bypass 2025/26, OWASP Argon2id 2024, RFC 6238 TOTP, Caddy/SQLite advisories; (3) three independent code-review agents (HTTP/auth, P2P/crypto/storage, privacy/DoS/frontend). **Fixed in this branch:** step-up auth makes passphrase mandatory and second-factor mandatory when enrolled (was: passphrase OR 2fa, both could be skipped); first-time TOTP enrollment requires step-up (was: session cookie alone could plant attacker secret); TOTP replay-store DB-error fails closed (was: silent bypass); session-revoke requires step-up + uses `public_id` (SHA256-derived) so listing sessions does not leak cookie values; pendingTOTPStore capped at 16 entries; NewServer self-validates public-mode invariant (loopback ↔ public, TLS or trust-proxy); http.Server gets ReadTimeout + IdleTimeout (slowloris); ServeTLS uses MinVersion=TLS1.2 + curve preferences; security-headers middleware (CSP default-src 'self', X-Frame-Options DENY, X-Content-Type-Options, Referrer-Policy no-referrer, Permissions-Policy disabling camera/mic/geo, HSTS in public+TLS); Cache-Control: no-store on auth-bearing GET responses; URL token stripped via `history.replaceState` after first read; MaxBytesReader on every Argon2id-fronting and JSON POST endpoint (4 KiB sensitive, 64 KiB general, 256 KiB signal, 1 MiB append-history); step-up endpoints share the /login per-IP rate-limiter (Argon2id-DoS defence); rate-limiter map capped at 50k IPs (XFF-spoof defence); clientIP uses **leftmost** XFF (was: rightmost — wrong for RFC 7239); ListAuthSessions LIMIT 200 in SQL; SQLite PRAGMA hardening (WAL/synchronous=NORMAL/foreign_keys/busy_timeout=5000); presence Resolver respects `cache.Put` rejected boolean (was: silent downgrade on replayed older record); JS-side hard caps on per-peer incoming files (8) / per-file bytes (100 MiB) / pending ICE candidates (64) — peer cannot OOM the tab. **Deferred to Phase 9** (protocol-level — see Phase 9 section): DHT eclipse via unverified k-bucket inserts, FIND_NODE/FIND_VALUE amplification, signaling relay → iterative-lookup amplification, cold-cache env.Sender, TURN allocation cap, outbox TTL.
- `[2026-05-05] [PHASE 8] REVIEW: webui Settings full-rollout — 3 iterations done.` MUST FIX (3/3): step-up аутентификация требует TOTP при TOTP-enrolled (был один из 3 reviewers'ов? — нет, это было 3/3 единогласно: passphrase-only collapse 2FA), TOTP code replay protection, parsePositiveInt → strconv.Atoi, invalidateOtherSessions ctx + single DELETE, wipe() ineffective. **Все 5 исправлены коммитами этой ветки.** 2/3: handleSessionsList утечка cookie value (только rev2, но критично — auth bypass — починено: SHA256(id) → public_id, revoke принимает public_id с constant-time лукапом), TOTP enroll round-trip secret (rev1+rev3 — починено: server-side pendingTOTPStore + 5-мин TTL по nonce), MaxBytesReader на Argon2id endpoints (rev1+rev2 — починено: 4 KiB cap), identity export step-up (rev1+rev2 — починено: confirmStepUp + AAD = header||params||salt, version byte v2, params в blob), AAD identity export (rev1+rev2 — починено: см. выше). 1/3 (но functional bugs, починено): EffectiveICEServers игнорировал disable_default_fallback (rev3 — toggle был мёртв, теперь честно дропает discovered), ICE URL case-mismatch (rev3 — нормализация scheme на add/remove/toggle), ICE not audited (rev1 — добавлены auditIfPublic). Тесты обновлены под новые контракты (enroll_id вместо secret_b32 round-trip, public_id вместо id, passphrase-alone reject); добавлены TestICE_NormalizesScheme + TestSnapshot_HonorsDisableFallback. -race ./... зелёные.
- `[2026-05-05] [PHASE 8] DECISION: bootstrap nodes — runtime-editable из webui (Settings → Bootstrap).` — RATIONALE: до этого bootstrap-список фиксировался при старте (`-bootstrap` флаг → seen-peers cache → community defaults). Это плохо для VPS-установок: смена точек входа требовала рестарта. Новая таблица `bootstrap_overrides` (manual list с enable-toggle и last-status) даёт пользователю прямой контроль. Источник №1 при старте теперь — user overrides, потом seen-peers cache, потом community defaults; `-bootstrap` CLI остаётся высшим приоритетом-операторским override'ом. `pkg/network` получает optional `BootstrapOverrideStore` интерфейс (DI seam, как у `SeenPeerStore`); `Node.Bootstrap(ctx, addr)` для hot re-bootstrap без рестарта (используется UI-кнопкой Reconnect и при добавлении новой записи). UX: гамбургер `#menu-btn` (раньше декорация) → Settings modal → unified list manual+cache+default с status-точкой ●ok/●fail/●— и timestamp. Trade-off: ещё одна SQLite-таблица + 5 REST-эндпоинтов; альтернатива — конфиг-файл — была отвергнута, т.к. для public-mode (VPS) у пользователя обычно нет shell-доступа в момент необходимости поправить bootstrap. Без новых deps. Tests: storage CRUD, REST-handlers (add/list/toggle/remove/reconnect c успехом+ошибкой), CSRF-guard, source-aware delete (manual vs cache vs default).
- `[2026-05-05] [PHASE 9.5] AUDIT: combined web-research + code-audit pass.` Inputs: (1) external literature survey across 25+ targeted searches (USENIX/CCS/PoPETs 2024–2026, NVD, MITRE, Briar/Matrix/Signal advisories) — top-15 threats compiled; (2) two independent agent passes covering all `pkg/`, `internal/`, `cmd/` files; (3) govulncheck — 0 known CVEs in deps. **Closed in this branch (12 BLOCKING/HIGH fixes):**
  - **F1 DNS-rebinding** — `internal/httpui.checkHost` middleware allowlists `Host` header (127.0.0.1/localhost/::1/publicHost), 421 on mismatch. Closes CVE-2024-28224 (Ollama) / CVE-2025-66414 (MCP TS SDK) class.
  - **F2 SignedSDP channel binding** — payload now covers domain-separator + version + kind + Recipient + SessionID + IssuedAt + sdp; Verify rejects cross-recipient/cross-session/stale replay; envelope version bumped to v2 (pre-MVP, no wire-compat needed).
  - **F3 unbounded maps capped** — `MemoryStore.MaxEntries` (default 16K, evict nearest-expiry), `RateLimitedStore.MaxTrackedIPs` (default 100K), `ratelimit.Limiter.MaxKeys` (default 100K, LRU evict). Closes the 1M-IP-spoof memory amplifier.
  - **F4 HELLO_INIT half-open cap** — `signaling.MaxHalfOpenPerIP=8`; counter increment on accept, decrement on handshake-complete or removal. Bounds responder allocation cost under HELLO_INIT spray.
  - **F5 Noise nonce-budget** — `noise.MaxMessagesPerKey=2^32`; Encrypt/Decrypt return `ErrSessionExhausted` past the bound. Defence-in-depth without full Noise-spec rekey (deferred — MVP traffic never reaches 2^32).
  - **F6 auth ratelimit fail-closed** — `internal/httpui/auth.RateLimiter` refuses unknown IPs once `MaxTrackedIPs` is hit (was: returned transient state, swallowing fail counters → unlimited brute force).
  - **F7 login TOTP step-replay** — `/login` now uses `VerifyTOTPStep` + persisted `auth.last_totp_step`. Same code can no longer be replayed within its 30 s window.
  - **F8 Argon2 PHC bounds** — `decodePHC` clamps `m`/`t`/`p` (8 MiB ≤ m ≤ 1 GiB, 1 ≤ t ≤ 16, 1 ≤ p ≤ 16) before handing to argon2.IDKey. DB-poisoning OOM closed.
  - **F9 token leak in logs** — `cmd/messenger` logs `address` instead of full URL. Token still printed to stdout for the operator.
  - **F10 TURN secret out of CLI** — `cmd/network` reads from `-turn-secret-file` or `UDISEND_TURN_SECRET` env. CLI flag removed (was reachable via `ps -ef` / `/proc/<pid>/cmdline`).
  - **F11 STUN per-IP cap** — `pkg/stun` token bucket on binding-success responses (rate=30 tok/s, burst=60). Closes the 4× reflective-amplification class flagged in NETSCOUT 2024.
  - **F12 pkg/ panic → error** — `dht.NewTxID` / `signaling.NewSessionID` return `(_, error)` on RNG failure (CLAUDE.md forbids panic in pkg/).
  - **F16 SQLite file mode** — `storage.Open` chmods DB to 0600; `cmd/messenger` chmods storage dir to 0700.
  - **F17 wire decoder hardening** — `Buffer.ReadBytes`/`ReadString` reject negative maxLen (`ErrInvalidLength`); `ReadUvarint` distinguishes overflow (`ErrUvarintOverflow`) from short-buffer.
  - **F18 bootstrap DNS** — `bootstrap.Defaults` filters loopback / RFC1918 / link-local / documentation IPs from DNS-seed responses. Closes the local-resolver-poisoning vector.
  
  **Closed in follow-up pass (2026-05-05 evening):**
  - **F13 DHT routing-table poisoning** — `pkg/dht.Node.handlePacket` admits contacts on response paths only; request paths schedule `maybeProbe` (async PING with in-flight dedup) and admit only on PONG-back. Closes passive replay/spoof; live attackers must maintain bidirectional UDP + per-/24 bucket cap. `RoutingTable.GetContact` added for probe-state lookup. Test: `TestRouting_DoesNotAdmitSilentRequester`.
  - **F14 ICEServers reachability probe** — `pkg/network.probeSTUNReachable` sends a STUN BindingRequest to every advertised STUN responder before returning it as an ICE candidate; non-responders are dropped. Closes capability spoofing where a peer signs a record with `CapCanSTUN` pointing at a victim's IP. Tests: `TestProbeSTUNReachable_AcceptsRealResponder`, `RejectsSilentTarget`, `RejectsClosedPort`.
  
  **Deferred / out-of-scope this branch (documented in `review/threat-model.md` follow-ups):**
  - F15 At-rest encryption (TOTP / history / identity.key) — re-scoped: AEAD-wrap of TOTP alone is security theater without also encrypting identity.key + history, and the proper fix needs OS-keyring or required-passphrase-per-startup. SQLCipher rejected (project is CGO-free). Operator-control: full-disk encryption is the canonical mitigation. Tracked as out-of-MVP.
  - F19 UDP transport sync.Pool / Packet.Release API — performance; closes memory-feedback violation but requires API touchpoints across all consumers.
  
  **Threat-model.md statuses updated** to reflect the new mitigations: "MITM на signaling" includes channel-binding; "DoS на узел" lists cardinality caps; "Replay" includes SignedSDP binding; new sections under "Web UI (HTTP + auth) layer" cover DNS-rebinding, login-bruteforce-via-saturation, login TOTP replay, Argon2 PHC bounds, token-log leak, TURN-secret CLI exposure, SQLite at-rest. "Forward secrecy" downgraded 🟡 partial (no Noise rekey, message-budget backstop).
  
  **Test deltas:** all 12 fixes have at least one regression test (TDD: failing-test-first). Specific tests: `TestHostHeader_RejectsForeignHost`, `TestRateLimiter_FailClosedWhenSaturated`, `TestLoginPOST_TOTPCodeNotReusableSameStep`, `TestVerifyPassphrase_RejectsAbsurdParams`, `TestOpen_ChmodsDBFile_OwnerOnly`, `TestBuffer_RejectsNegativeMaxLen`, `TestBuffer_RejectsUvarintOverflow`, `TestMemoryStore_BoundedSize_EvictsNearestExpiry`, `TestRateLimitedStore_TrackedIPsBounded`, `TestLimiter_BoundedKeyCardinality_EvictsLRU`, `TestSignedSDP_RejectsCrossRecipientReplay`, `TestSignedSDP_RejectsCrossSessionReplay`, `TestSignedSDP_RejectsStaleReplay`, `TestSignedSDP_AcceptsWithinSkew`, `TestEncrypt_RefusesPastMessageBudget`, `TestDefaults_FiltersUnroutableDNSResults`. All packages green under `go test ./...`.
- `[2026-05-05] [PHASE 8] DECISION: CLI = run/public/status; default zero-config; embedded autocert вместо ручной установки Caddy.` — RATIONALE: первая итерация дизайна (init wizard + 7 подкоманд) была правильной по форме, но требовала `udisend init` даже для localhost-сценария — лишний шаг. Финальный surface: (1) `udisend run` без preconditions поднимает loopback с token-в-URL — ровно как до всего рефакторинга. (2) Переход в публичный режим — `udisend public -enable <addr>`: IP → lan-ip (self-signed), DNS → public-autocert (`golang.org/x/crypto/acme/autocert` issues+rotates Let's Encrypt серт сам), `-via-proxy` → public-proxy (Caddy/nginx терминирует TLS снаружи). (3) `public -disable`/`-reset` симметричны и идемпотентны через новые `Storage.DeleteSetting`/`DeleteAuthCredentials`. **Auto-install Caddy отвергнут:** требует root, ломает кросс-платформенность, конфликтует с уже стоящим nginx, увеличивает attack surface. Вместо этого embedded autocert — один бинарь, ноль внешних процессов, тот же ACME-механизм. Trade-off: автокерт требует port 80 reachable for HTTP-01 challenge (для VPS — норм, для дома за NAT — нужен port-forward или escape-hatch `-via-proxy` с DNS-01 на стороне прокси). DNS-01 в самом messenger пока не делаем — добавит API-key management для DNS-провайдера, скоуп больше. Старые флаги (`-set-password`, `-setup-totp`, `-public-host`, `-tls-cert`/`-tls-key`, `-trust-proxy`, `-listen`, `-http`) удалены целиком, ветка не релизнута. Storage default — XDG (`os.UserConfigDir()/udisend/messenger`). Cobra отвергнут (5-MB dep дерево; stdlib `flag` + ручной dispatch достаточен для 3 подкоманд). New deps: `rsc.io/qr` (BSD-3, ~1200 LOC, ноль транзитивных) для терминального QR; `golang.org/x/crypto/acme/autocert` (уже было в x/crypto). TDD: `Storage.DeleteSetting`/`DeleteAuthCredentials` написаны failing-test-first; `profile.validate` covered 13 кейсами. Branch `feat/webui-remote-auth`.
- `[2026-05-05] [PHASE 8] DECISION: Tailscale mode + cobra CLI — путь к «нормальному UX» без cert warnings.` — RATIONALE: пользователь спросил «как сделать нормально, и без действий вне приложения». Self-signed на LAN-IP фундаментально ломается в браузерах из-за opaque-origin поведения. Реальные альтернативы — (a) public DNS + autocert (нужен публичный IP/домен), (b) Tailscale (private mesh, но cert настоящий LE), (c) custom CA + install-profile (большая UX-боль на iOS). Выбран **Tailscale** как наименее frictional: пользователь раз ставит app на каждое устройство, дальше — `udisend public enable --tailscale` авто-детектит daemon, получает реальный LE cert через Tailscale ACME pipeline (`tailscaled` cache + auto-rotate), браузеры trust'ят cert ⇒ opaque-origin не возникает ⇒ строгая Origin-CSRF проверка возвращается без компромиссов. Реализация: `pkg/tailscale` (~110 LOC обёртка над `tailscale.com/client/local`), `modePublicTailscale` profile + validate, `--tailscale` flag в enable, `tailscaleCertRefresher` (atomic.Pointer[tls.Certificate] + 12h-tick rotation). Trade-off: dep `tailscale.com v1.96.5` (24MB src, ~4MB бинарь) — приемлемо за полностью устранённую категорию UX-проблем. Mock-тесты для wrapper'а через httptest+RoundTripper. **Cobra-переход:** изначальный stdlib `flag` не поддерживал mutually-exclusive флаги и положительные/отрицательные bool overrides чисто; cobra даёт `MarkFlagsMutuallyExclusive`, `Flags().Changed`, иерархичные subcommand'ы (`udisend public {enable,disable,reset}` вместо `-enable/-disable/-reset`), auto-help. Дeп `github.com/spf13/cobra v1.10` + `pflag` — ещё несколько MB, но UX-выигрыш ощутим. Старый stdlib flag dispatch удалён.
- `[2026-05-05] [PHASE 8] FIX: login-CSRF defence — conditional Origin check (allow `null` in lan-ip mode).` — RATIONALE: пользовательский баг-репорт: `udisend public -enable 192.168.0.105` + accept self-signed → POST `/login` отдавал 403 «bad origin». Лог показал `Origin: null` от браузера — документированное поведение Chromium/Safari: страница с self-signed cert override становится **opaque origin**, и POST'ы несут `Origin: null`. **Первая итерация фикса (SameSite=Strict CSRF cookie) тоже не сработала** — браузер не отправляет SameSite-cookie с opaque-origin страницы (cookie не виден на сервере при POST). Финальное решение: вернули Origin-check, но добавили исключение `Origin == "null"` → accept в режимах с self-signed cert (`Mode == lan-ip`); для других режимов (loopback / public-tls / public-autocert / public-proxy) `null` остаётся CSRF-сигналом. Реализация: `httpui.Config.AllowOpaqueOrigin bool` → `authHandlers.allowOpaqueOrigin`; `cmd_run.go` устанавливает `prof.Mode == modeLANIP`; `checkLoginOrigin` метод заменил функцию `sameOriginIfPresent`. **Trade-off в lan-ip mode:** теряем строгую CSRF защиту на /login. Mitigation: (a) udisend single-tenant — нет «чужих аккаунтов» для login-fixation; (b) rate-limiter (5/min, lockout 20 fails); (c) Argon2id passphrase hashing (slow brute force); (d) audit log. Для threat model самохостящегося LAN-мессенджера это приемлемая деградация. Тесты: `TestCheckLoginOrigin` × 7 кейсов (включая `null rejected in trusted-cert mode` + `null accepted in self-signed mode`), удалён `TestSameOriginIfPresent`. Под `-race` зелёные.
- `[2026-05-05] [PHASE 8] REVIEW: CLI UX overhaul — 3 iterations done.` — review files в `review/cli-ux-overhaul-iter-{1,2,3}.md`, post-fix verify в `iter-4-postfix.md`. **MUST FIX (3/3): 1 item.** A: `Storage.DeleteAuthCredentials` не чистил `auth_sessions` — после `public -reset` старая сессионная cookie выживала и валидировалась против новой passphrase, что превращало reset в security non-event. Fix: добавлен `DELETE FROM auth_sessions` в ту же транзакцию; регрессионный тест `TestAuthCredentials_Delete` создаёт сессию до reset и ассертит её отсутствие после. **2/3: 2 items, оба fixed.** B (security regression): HSTS-гейт `s.tlsCert != ""` пропускал autocert-mode (где TLS живёт в `TLSConfig.GetCertificate`, а не в файлах); добавлен флаг `Server.hasTLS bool`, тест `TestSecureHeaders_HSTSGate` × 6 кейсов. C (bool-flag override): `fs.Lookup("trust-proxy").Value.String() == "true"` всегда читал текущее значение (после default), так что `-trust-proxy=false` не мог отменить persisted true; заменено на `fs.Visit` — корректный go-идиоматический паттерн для "был ли флаг установлен пользователем". **1/3: 3 items, все fixed (real bugs, не controversial).** D: `validatePublicModeConfig` отклонял loopback bind безусловно, что ломало `public-proxy` mode (`127.0.0.1:port` за Caddy/nginx) — добавлено исключение для `TrustProxy=true`, тест `TestValidatePublicModeConfig` × 7 кейсов. E: IPv6 LAN literal mishandled — `host + ":" + port` для `fe80::1` давал `fe80::1:8443`, который `net.ParseIP` интерпретирует как другой адрес; заменено на `net.JoinHostPort`, `splitHostPort` дополнен веткой для bare IPv6, тест `TestSplitHostPort` × 7 кейсов включая `[fe80::1]:8443`/`fe80::1`/`::1`. F: `runPublic`/`runStatus` использовали `context.Background()`, Ctrl-C во время Argon2id (~1s) мог оставить torn SQLite-tx; перенесено в общий `openStore` через `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` с возвратом `cancel`. **Post-fix re-review (iter 4):** APPROVE, 0 новых BLOCKING; 1 nit (cancel discarded в openStore — закрыт там же, returning cancel callers defer'ят). **TDD coverage gap (iter 1):** только `profile_test.go` имел unit-тесты до review; теперь добавлены `TestSecureHeaders_HSTSGate`, `TestValidatePublicModeConfig`, `TestSplitHostPort`, `TestAuthCredentials_Delete` (с session wipe). Полное покрытие cert.go/qr.go/prompts.go отложено — это passthrough к stdlib без условной логики, юнит-тесты дали бы 0 информации. **All packages green under `go test -race`**. Acceptance criteria всех 14 пунктов CLI UX overhaul секции: ✅.
- `[2026-05-05] [PHASE 10] DECISION: Phase 10 «Inter-node WebRTC mesh» добавлена в roadmap.` — RATIONALE: текущая архитектура зависит от наличия достаточного числа узлов с публичным IP — DHT-таблицы и signaling-relay живут только на них; NAT-узлы между собой друг друга не достигают. При координированном выпадении публичных (отказ инфраструктуры, цензура, takedown) сеть деградирует до неработоспособности. Phase 10 строит **persistent WebRTC DataChannel-overlay поверх UDP**: каждый узел заранее держит K=8 живых линков к выбранным пирам через сегодняшние публичные узлы как signaling-relay; после коллапса signaling-инфраструктуры уже установленные DC переживают и поддерживают связность для existing-сессий. Новых соединений в degraded-режиме не появится (signaling не работает), но граф между уже-paired узлами остаётся живым. См. `ROADMAP.md` § Phase 10.
- `[2026-05-05] [PHASE 10] DECISION: pion/webrtc/v4 для inter-node mesh.` — RATIONALE: единственный mature WebRTC-стек в Go-экосистеме. Уже частично присутствует как indirect через `pion/turn/v4` и `pion/stun/v3`; поднимается в direct без новых cgo-зависимостей. Альтернативы: `gortc/webrtc` (мёртв с 2020), свой WebRTC-стек (out of scope). Это **override** прежнего тезиса в design.md §10 («pion/webrtc НЕ используется на стороне messenger-бинаря, v2-сдвиг 2026-05-02») — теперь ограничение действует только для messenger-binary; на network-узле pion/webrtc используется для inter-node mesh. Браузерная сторона messenger по-прежнему держит `RTCPeerConnection` сама.
- `[2026-05-05] [PHASE 10] DECISION: strict Send semantics + explicit Connect API.` — RATIONALE: альтернатива — lazy Send, который сам бы поднимал DC при отсутствии. Lazy-семантика блокирует hot path DHT-RPC (iterativeFind, FIND_NODE) на ICE/DTLS handshake (0.5–3 секунды), что превращает DHT-операции в нестабильные. Strict-семантика разделяет lifecycle: PeerManager заранее поддерживает пул из K=8 живых DataChannel'ов, `Send` к не-connected пиру → `ErrNoRoute` (вызывающий слой делает UDP-fallback). Чище lifecycle, предсказуемая latency.
- `[2026-05-05] [PHASE 10] DECISION: K=8 mesh-links, full peer overlay (не NAT-only).` — RATIONALE: альтернатива — overlay только между NAT-узлами, чтобы не нагружать публичные. Но: (а) единая mental model «каждый узел держит K линков» проще для реализации и аудита; (б) граф устойчивее, если публичные тоже — full peers (их падение не выбивает целые сегменты overlay'я); (в) публичные всё равно держат DHT-state на UDP — добавочная нагрузка от 8 PeerConnection (~80MB RAM, ~400ms на handshake) приемлема. K=8 выбрано как баланс между connectivity-redundancy (K-2 потери допустимы) и resource-cost; configurable через `Config.MaxMeshLinks`.
- `[2026-05-03] [REFACTOR] DECISION: lift internal/network → pkg/network as opaque Node; rewrite messenger to consume it agnostically; delete cmd/network.` — RATIONALE: до рефакторинга `internal/network` и `internal/messenger` параллельно собирали один и тот же `pkg/{transport,dht,signaling,presence,bootstrap}`-стек, дублируя bootstrap-логику и присутствие — package-oriented design нарушался. После: `pkg/network.Node` инкапсулирует DHT/signaling/transport/STUN/TURN/presence; messenger импортирует только `pkg/{identity,network,webrtc}` (5 сетевых пакетов схлопнулись в 1). Низкоуровневое API — единый `Node.Income() <-chan *Income` (peer + sessionID + PeerPublic + payload + final) + outbound `Connect/Send/CloseSession`; messenger демуксит по sessionID и оборачивает в `SignedSDP`. errgroup поверх `Run(ctx) error` для propagate-on-error. `*Income` идёт через `sync.Pool` (0 allocs/op в `BenchmarkIncomePool`); `Session`/`PeerInfo` — value-types, без лишних heap-escape. `cmd/network` удалён — passive relay use-case покрывает отдельный binary позже. Тесты: `pkg/network/api_test.go` (Connect/Income/Lookup/SeenPeerStore), `pkg/network/income_internal_test.go` (pool), `internal/messenger/messenger_test.go` + `outbox_test.go` переписаны под новую `Open(cfg Config{Network, Storage})`. Branch `refactor/pkg-network-agnostic-messenger`. Out-of-scope этого PR: переименование `cmd/messenger`→`cmd/messenger-web`, `internal/httpui`→`internal/webui`, вычистка boilerplate в `cmd/*`, превращение `pkg/dht|presence|signaling.Run` в `Run(ctx) error`.

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
