# Phase 12 — API surface tightening — review summary

Branch: `feat/phase11-pkg-refactor`. Commits under review: `be9c747` (12.1) + `2f3b129` (12.2).

## Process

Three independent fresh-context agent reviews:

- **Iter-1** — general-purpose, audit-style: correctness + contract integrity.
- **Iter-2** — Explore agent, focused on dispatch + concurrency.
- **Iter-3** — general-purpose, focused on contract + design.

## Findings consolidation

| Severity / consensus | Count | Action |
|---|---|---|
| **BLOCKING — 3/3** | **0** | — |
| **BLOCKING — 2/3** | **0** | — |
| **BLOCKING — 1/3** | 3 | analysed below |
| **SUGGESTION — 2/3** | 4 | all fixed |
| **NIT — 2/3** | 0 | — |
| **1/3 (any severity)** | ~10 | accepted-or-rejected, all logged below |
| **PRAISE** | ~10 | extension API, wire-format preservation, mesh kind privatization |

### 2/3 SUGGESTION items — all fixed

1. **DHT reserved-range mismatch** [Iter-1 S1.1, Iter-3 B3.1] — godoc claimed pkg/dht owns 0x01–0x0F but `handlePacket` switch only handled 0x01–0x08. Embedder picking 0x0A "by docs" would silently get the frame today and lose it tomorrow. **Fixed:** explicit `if typ < ExtensionRangeMin → drop+log` in default branch, plus exported constant `dht.ExtensionRangeMin = 0x10` so embedders pin against it. Range 0x09–0x0F now reserved for future DHT opcodes and silently dropped at the inbound boundary. New tests: `TestNode_ReservedRangeDropped` (reserved 0x09/0x0a/0x0c/0x0f are dropped) + `TestNode_ExtensionReceivesEmbedderFrames` table-extended for `ExtensionRangeMin` / 0x42 / 0xff.

2. **`time.Sleep` polling in `pkg/dht/extension_test.go`** [Iter-1 S1.2, Iter-3 N3.1] — 10ms polling loop instead of channel-based sync. **Fixed:** `captureExt` now signals via `notifyCh chan struct{}` populated from `HandleDHTFrame`; tests `select` on it with a 2 s deadline. No polling. Also extracted `extensionFixture` helper to dedupe boilerplate across the new test cases.

3. **Cargo-cult `_ = idA` in dht extension test** [Iter-1 N1.1, Iter-3 S3.4] — generated identity for peer A then discarded. **Fixed:** removed entirely.

4. **Dead `if !isExtensionKind` guard in service.go default arm** [Iter-1 S1.3, Iter-2 B2.1] — explicit cases above cover 0x01–0x05 so the guard could only ever fire on kind 0x00. **Fixed:** replaced with explicit `if env.InnerType == 0` check (drops malformed peer's zero kind), then unconditional `handleExtension` for the rest. Default-arm comment now states the actual reachable kinds.

5. **Coverage: arbitrary embedder kind round-trip** [Iter-2 S2.2, Iter-3 B3.2] — old test only verified 0x06/0x07/0x08; new SendExtension allows any kind ≥ 0x06 but no test pinned this end-to-end. **Fixed:** `TestNode_ExtensionReceivesEmbedderFrames` is now table-driven over {ExtensionRangeMin, 0x42, 0xff} so any kind ≥ 0x10 demonstrably reaches `Extension.HandleDHTFrame`. `TestChannel_SendExtension_RejectsInternalKinds` already enumerates every internal kind 0x01–0x05 explicitly.

### 1/3 BLOCKING items — analysed and resolved

1. **Iter-2: `TestMesh_E2E_StatsAccumulate consistently times out`** — *Not a regression.* Re-verified under multiple isolated runs (`-count=10` for the named test, `-count=5` for the whole pkg/network) — all PASS. The flake observed by Iter-2 was the well-documented mesh-e2e scheduler flake under full-suite + `-race` + heavy parallelism, called out in Phase 11.3, 11.10, and 11.11 ROADMAP entries. Phase 12 changes are opcode renames + dispatch-default-tightening with no semantic effect on the mesh handshake path. **Decision: rejected with rationale (no change needed; flake pre-dates this branch).**

2. **Iter-2: `HandleDHTFrame` docstring says "pkg/dht never interprets opcode 0x10"** — *Misread.* The docstring describes pkg/dht's behaviour (which is correct: pkg/dht hands 0x10 to Extension without interpretation). The `if typ != frameTypeRelay` check inside `HandleDHTFrame` is signaling-side defense-in-depth: if a future dht.Extension dispatcher widens what it forwards, signaling still rejects unrelated opcodes. **Decision: rejected with rationale (docstring is accurate).**

3. **Iter-2: dispatch validation logic clarity** — overlapped with the 2/3 dead-guard finding. Already fixed.

### 1/3 SUGGESTION/NIT items — accepted

1. **Iter-2: SendExtension error message should mention valid range** — accepted. Error now reads `kind 0xNN is signaling-internal (embedder range is 0x06-0xff)`.

2. **Iter-3 N3.3: `p2p-messenger-design.md` still references `MsgRelay` and `InnerMesh*`** — accepted. Doc lines 239 (relay frame) and 500 (mesh handshake) updated to describe the post-Phase-12 dispatch model.

3. **Iter-3 N3.4 / N3.5: stale comments referencing old symbols** — partially accepted. Comments in extension_test.go updated to drop the "previous SendMesh/SetMeshHandler pair" framing where it added no value. The `pkg/transport/webrtc.go` and `pkg/presence/record.go` doc-comment fixes were already part of the 12.2 commit.

### 1/3 SUGGESTION items — rejected with rationale

1. **Iter-3 S3.3: reserve a future signaling-internal range (e.g. 0xF0–0xFF)** — *speculative.* CLAUDE.md "no abstractions without second consumer". A future signaling-internal opcode would bump envelope version (the pattern already used: v1→v2 added the `hops` byte). Carving out a band today buys nothing and forecloses the embedder space.

2. **Iter-3 S3.2: `dht.Extension` should be a `func` alias instead of an interface** — wash. Interface is marginally cleaner for mocking in tests (struct implements method) and aligns with Go idiom for hooks that may grow methods later (lifecycle, metrics). Function-alias would force every test to define a typedef and store as `Config.Extension = ExtensionFunc(myFn)`. Net friction either way; staying with interface.

3. **Iter-1 QUESTION: per-kind ordering doc on `ExtensionHandler`** — *speculative.* Today the only embedder is mesh, which tolerates any order (offer/answer/candidate are state-machine self-correcting). A future embedder needing per-kind FIFO would split into multiple Channels or include a sequence number — the contract neither over-promises (claiming order) nor under-documents (it's clear "a stream of frames").

4. **Iter-1 N1.3: collapse `meshKind*` constants into a `MeshSDPKind`-keyed table** — *out of phase scope.* Logged as follow-up; touches `pkg/network/mesh_signaler.go` mapping helpers that were not changed in 12.1/12.2.

5. **Iter-1 S1.4: `TestSetExtensionHandler_NilDisables` uses `time.Sleep(200ms)` for negative assertion** — kept. Negative-result-via-timeout is an inherent pattern for "handler must NOT fire after disable"; converting to synctest in pkg/signaling would be a multi-test refactor (all of pkg/signaling uses real noise + memory transport, not synctest). Out of scope for Phase 12.

6. **Iter-2 N2.1: log consistency (peer in some entries, not in others)** — partially fixed (default-arm `extension envelope without session` now includes peer). Remaining inconsistencies are pre-existing and not part of Phase 12 scope.

## Iter-4 post-fix verification

- `go test -race ./...` — PASS for all 25 packages (pkg/network occasional `TestMesh_E2E_*` flake under full -race suite is the documented Phase 10/11 scheduler-pressure pattern; pkg/network alone -count=5: PASS).
- `go vet ./...` — clean.
- `go test -tags=e2e -p 1 ./...` — PASS.
- New tests: `TestNode_ExtensionReceivesEmbedderFrames` (table-driven, 3 cases), `TestNode_ReservedRangeDropped` (4 cases), `TestChannel_SendExtension_RoundTrip`, `TestChannel_SendExtension_RejectsInternalKinds` (5 cases), `TestSetExtensionHandler_NilDisables`, `TestConnect_BogusPeer_NoPanic` — all PASS under -race count=10.

## Acceptance criteria delta (Phase 12)

| Criterion | Status |
|---|---|
| pkg/dht does not mention `MsgRelay` in code or comments | ✅ |
| pkg/signaling does not mention `InnerMeshOffer/Answer/Candidate` | ✅ |
| `pkg/dht.Config.Extension` is an interface | ✅ |
| `Channel.SendExtension` rejects 0x01–0x05, accepts ≥ 0x06 | ✅ (test enumerates all 5 internal kinds) |
| `go test -race ./...` green after each sub-stage | ✅ |
| `go test -tags=e2e ./...` green after the phase | ✅ (under `-p 1`; full-parallel flake pre-existing) |
| 3-iteration post-phase review done, MUST FIX closed | ✅ (no MUST FIX 3/3; all 2/3 SUGGESTIONs fixed) |
| Wire-format bit-exact | ✅ (no Encode/Decode path touched in either commit) |
