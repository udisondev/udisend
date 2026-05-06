# Phase 10 — post-phase 3-iteration review summary

> Iterations: `phase-10-iter-1.md`, `phase-10-iter-2.md`, `phase-10-iter-3.md`.
> Each was a fresh-context Explore agent over the diff `adca8c7..feat/phase10-rtc-mesh`.

## Aggregation

| ID | Severity | 1 | 2 | 3 | Decision |
|----|----------|---|---|---|----------|
| #1 | BLOCKING | x |   |   | **FIX** — `time.After` in `PeerSession.Send` select leaks goroutines on bufferLow / closed paths |
| #2 | BLOCKING |   |   | x | **FIX** — `fireICE`/`fireSDP` spawn unbounded goroutines under candidate flood |
| #3 | BLOCKING |   |   | x | **FIX** — `PeerSession.inbox` never closes; readers using `range` would hang on shutdown |
| #4 | BLOCKING |   |   | x | **REJECT** — Cap-validation at handshake. Already mitigated by `dhtMeshSelector.hasMeshCapability` (10.8) which skips legacy peers before any Connect attempt; clarify with code comment instead |
| #5 | BLOCKING |   |   | x | **DEFER** — Mesh-eclipse via selector. Subnet-diversity exists in `pkg/dht.MaxContactsPerSubnet=2` at routing-table layer; mesh inherits it transitively because selector pulls from the routing table. Full anti-eclipse cross-validation (UDP↔RTC routing parity) deferred to Phase 10.5 / 11 (already in ROADMAP "Out-of-this-phase / deferred") |
| #6 | SUGGESTION |   | x |   | **DEFER** — `MeshSignaler.channels` unbounded map. Bounded transitively by DHT routing-table size (K-bucket cap × number of buckets). TTL eviction is cleaner; defer to 10.5+ |
| #7 | SUGGESTION |   | x |   | **DEFER** — `PeerManager.Close` does not wait on in-flight dials. Errgroup-propagated ctx-cancel handles correctness; explicit WaitGroup is hardening, defer |
| #8 | SUGGESTION |   | x |   | **DEFER** — `dhtMeshSelector` 250ms lookup budget could skip a slow-cache peer. Mitigated by over-fetch 4×k; document why and move on |
| #9 | SUGGESTION | x |   |   | **FIX** — `flushPending` reads `meshHandler.Load()` once; if handler is set between handshake and flush we drop early frames. Reload per frame |
| #10 | SUGGESTION | x |   |   | **NIT-only** — `capsFor` ModeClient + no PublicIP returns 0 unconditionally. Code is correct; comment for clarity |
| #11 | SUGGESTION | x |   |   | **DEFER** — `peer_manager_test.go` uses `time.Sleep` instead of `testing/synctest`. Tests stable; synctest migration is a polish pass |
| #12 | NIT |   | x |   | **DEFER** — `signaling.Service.acceptInit` two-phase reservation needs a comment. Existing comment is adequate |
| #13 | NIT |   | x |   | **NOTE** — `PeerSession.OnBufferedAmountLow` non-blocking signal can drop. The 30s `time.After` fallback covers it |
| #14 | PRAISE | x | x |   | **NOTE** — `Network.closeServers` shutdown order (mesh→signaling→transport) called out by 2 reviewers as exemplary |

**No 3/3 findings.** Per CLAUDE.md: 3/3 are MUST FIX (none here). 2/3 → fix or rationale-reject (none here either, although #14 PRAISE corroborated by 2). 1/3 → fixable or document.

## What gets fixed in this branch

1. **#1** — `PeerSession.Send` timer leak: replace `time.After` with `time.NewTimer` + `Stop()`.
2. **#2** — `fireICE`/`fireSDP` goroutine flood: bound concurrency via a buffered semaphore.
3. **#3** — `PeerSession.inbox` close: close it in `Close()` after pion's `pc.Close` quiesces senders, signal close via `closed` chan first so OnMessage drops in-flight after that point.
4. **#9** — `flushPending` mesh-handler reload: re-`Load()` per frame inside the loop.

## What gets rejected with rationale

5. **#4** — Cap-validation: already enforced at selector layer, before any Connect attempt. Add an in-line comment in `WebRTCTransport.Connect` pointing at this prior gate.
6. **#10** — `capsFor` zero-return: code is correct; the existing inline comment "ModeClient" already explains.

## What gets deferred (documented for follow-up)

- **#5** Eclipse via mesh — Phase 10.5/11 cross-validation routing.
- **#6** MeshSignaler channel map TTL — Phase 10.5+.
- **#7** PeerManager dial WaitGroup — hardening pass.
- **#8** dhtMeshSelector lookup budget — current 4×k over-fetch is sufficient.
- **#11** PeerManager tests via synctest — polish pass.
- **#12, #13** — pure code-clarity improvements.

## Post-fix re-review (iter 4)

Will run after the four #1/#2/#3/#9 fixes land. Findings → `phase-10-iter-4-postfix.md`.

## Decisions log entry (to be appended)

```
[2026-05-05] [PHASE 10] REVIEW: 3 iterations done.
- MUST FIX (3/3): 0 items.
- 2/3: 0 items.
- 1/3: 14 items, decisions:
  - 4 fixed in branch (#1 timer leak, #2 goroutine flood, #3 inbox close, #9 flushPending reload).
  - 2 rejected with rationale (#4 cap-validation already at selector; #10 capsFor zero is intentional).
  - 6 deferred with rationale (#5–#8, #11–#13).
  - 1 PRAISE noted (#14, 2 reviewers concur).
```
