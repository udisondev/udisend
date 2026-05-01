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
- [ ] Phase 7 — Chat application + fyne UI

## Известные технические долги (overnight-deferred)

- [ ] **S/Kademlia hardening:** PoW на nodeID, disjoint paths, sibling lists. Без этого Sybil-resistance ослаблена.
- [ ] **DHT integration tests:** 100-узловая memory-сеть + 5-узловой testcontainers cluster. Сейчас — только 3-5 узлов через memory transport.
- [ ] **Replay protection** в signaling (sequence numbers / nonce window) — каркас есть, тесты на replay не написаны.
- [ ] **TURN production-grade auth:** rate-limiting per-IP, abuse mitigations. Сейчас — anonymous + short-term HMAC, не production.
- [ ] **Threat-model audit** (`p2p-messenger-design.md` §8) — каждая атака должна иметь либо тест, либо явно accepted risk.
- [ ] **Eclipse-attack simulation** на DHT.
- [ ] **DoS rate-limiting** на DHT и signaling.
- [ ] **PGO baseline:** capture production profile, commit `default.pgo`.
- [ ] **Reproducible builds** (с GOFLAGS, fixed timestamps).
- [ ] **Signed release artifacts.**
- [ ] **Fuzz-тесты прогнаны 30s+ в CI** для каждого decoder'а.
- [ ] **Покрытие** ≥ 80% для `pkg/*` (фактическое — см. отчёт coverage в финальном коммите).
- [ ] **WebRTC NAT integration:** 2 узла за разными NAT через TURN volunteer.
- [ ] **E2E-тест полного сценария:** TOFU → message → file → call → offline → outbox flush.
- [ ] **godoc:** все exported идентификаторы в `pkg/*` имеют godoc-комментарии.
