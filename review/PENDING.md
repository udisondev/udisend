# Pending review tasks (post-overnight)

> Per `CLAUDE.md` § Post-phase code review: каждая фаза 1–8 ДОЛЖНА пройти 3 итерации независимого ревью + цикл фиксов до перевода в ✅ и до мержа в `main`. Overnight-run эту процедуру **пропустил** ради runnable MVP. Этот файл — чек-лист для дальнейшего прогона.

## Процедура для каждой фазы

1. Чек-аут на коммит, помечающий завершение фазы.
2. Запустить **три независимые итерации** ревью, каждая в свежем контексте, без подсказок. Использовать:
   - `/ultrareview` (multi-agent cloud review), или
   - `Agent(subagent_type: "code-reviewer")`, или
   - skill `go-review`.
3. Сводка по severity (BLOCKING/SUGGESTION/NIT/PRAISE), пересечения по итерациям.
4. Все MUST FIX (3/3) — обязательны к фиксу. Тесты на каждый фикс (TDD).
5. Минимум 1 повторная итерация ревью на изменённые места.
6. Запись в decisions log: `[DATE] [PHASE N] REVIEW: 3 iterations done. — MUST FIX (3/3): ...`.

## Чек-лист по фазам

- [ ] Phase 1 — Identity foundation
- [ ] Phase 2 — Local Kademlia DHT
- [ ] Phase 3 — Presence
- [ ] Phase 4 — Signaling channel
- [ ] Phase 5 — STUN/TURN volunteers
- [ ] Phase 6 — WebRTC session
- [ ] Phase 7 — Chat application (v2: browser UI поверх HTTP+SSE; fyne dropped 2026-05-02)

## Известные технические долги

Закрыто в текущем raunde архитектурного ревью:

- [x] **S/Kademlia hardening — PoW** (`pkg/identity.PoWBits/GenerateWithPoW`).
- [x] **Replay protection в signaling** — Noise XK AEAD nonce + dup-INIT drop. Tests in `pkg/signaling/replay_test.go`.
- [x] **TURN production-grade auth** — RFC 7635 ephemeral credentials + per-IP rate-limit (`pkg/turn`).
- [x] **Threat-model audit** — `review/threat-model.md`.
- [x] **DoS rate-limiting (DHT)** — `pkg/ratelimit` + `pkg/dht.Node`.
- [x] **Reproducible builds** — `Taskfile.yml::build:reproducible`.
- [x] **Signed release artifacts** — `Taskfile.yml::release:sign`.
- [x] **Sybil presence rate-limit per IP** — `pkg/presence.RateLimitedStore`.
- [x] **Eclipse mitigation (bootstrap diversity)** — `internal/storage.SeenPeersDiverse`.
- [x] **Hop-by-hop signaling relay** — `pkg/signaling.Service.relay` + dhtRouter path discovery.
- [x] **TLV-tolerant decoders** (forward compat) — `pkg/wire.SkipUnknownTLVs`.
- [x] **Volunteer ICE discovery** — `Messenger.ICEServers`.
- [x] **Outbox flush pump** — `Messenger.outboxPump` + SSE peer_online event.
- [x] **TOFU verification UI flow** — modal showing both safety numbers.
- [x] **Bootstrap persistence cache** — `internal/storage` seen_peers.
- [x] **Bootstrap infrastructure (community list + DNS-seeds)** — `pkg/bootstrap` с пустым default'ом и ldflag-override, подключён в `cmd/messenger` и `cmd/network`. Реальные адреса нужно добавить пред-релизом.

Остаётся:

- [ ] **S/Kademlia: disjoint paths + sibling lists** — требуют расширения iterativeFind.
- [ ] **DHT integration tests:** 100-узловая memory-сеть + 5-узловой testcontainers cluster.
- [ ] **PGO baseline:** capture production profile, commit `default.pgo`.
- [ ] **Eclipse simulation** (a/b test on the bootstrap-diversity mitigation).
- [ ] **Fuzz-тесты прогнаны 30s+ в CI** для каждого decoder'а.
- [ ] **Покрытие** ≥ 80% для `pkg/*`.
- [ ] **WebRTC NAT integration:** 2 узла за разными NAT через TURN volunteer.
- [ ] **E2E-тест полного сценария:** TOFU → message → file → call → offline → outbox flush.
- [ ] **godoc:** все exported идентификаторы в `pkg/*` имеют godoc-комментарии.
- [ ] **Реальные адреса в `pkg/bootstrap.CommunityList` и `DNSSeeds`** — нужны операторы публичных узлов; infrastructure (см. выше) уже на месте.
- [ ] **CONTRIBUTING.md / security.md / public protocol spec.**
