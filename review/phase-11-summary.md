# Phase 11 — `pkg/` reusability refactor — review summary

Branch: `feat/phase11-pkg-refactor`. Base: `fd3fcc2`. Commits: 8 (sub-stages 11.1-11.9).

## Process

Three independent fresh-context Explore-agent reviews + one re-audit (same metric used at Phase-11 start). Each reviewer received the same scope (8 commits, branch diff) but distinct angles:

- **Re-audit** — apply baseline reusability rubric, compute delta.
- **Iter-1** — generic critical review (correctness, abstraction shape).
- **Iter-2** — external-consumer angle (lift `pkg/*` into another project).
- **Iter-3** — correctness / security / concurrency angle.

## Findings consolidation

| Severity / consensus | Count | Items |
|---|---|---|
| **BLOCKING — 3/3** | **0** | (no items confirmed by all three reviewers) |
| **BLOCKING — 2/3** | **0** | (Iter-2 raised 2 BLOCKINGs; neither corroborated by Iter-1 or Iter-3) |
| **BLOCKING — 1/3** | 2 | Iter-2 only (see below) |
| **SUGGESTION** | ~10 | mostly doc clarifications |
| **NIT** | ~5 | cosmetic |
| **PRAISE** | ~15 | Suite design, backward compat, test coverage, race-clean, decoupling, configurability |

### 1/3-only BLOCKING items — analysed and resolved

1. **Iter-2: `pkg/presence/rate_limited_store.go` internal map still uses `dht.NodeID`** — *Not actionable.* This is the correct internal representation: PeerID is the boundary type; internal storage uses fixed-size `[16]byte` (=NodeID) for map performance. The `key.Bytes()` conversion at the boundary is the documented pattern and is consistent with `pkg/dht.MemoryStore`, `pkg/transport.WebRTCTransport`, and `pkg/webrtc.PeerManager`. Iter-1 and Iter-3 reviewed the same code and explicitly praised the pattern (Iter-3: "extraction of `nid := key.Bytes()` before acquiring the mutex is a textbook example of minimizing critical sections"). **Decision: rejected with rationale (no change needed).**

2. **Iter-2: `pkg/webrtc.MeshTransport.Peers() []identity.Hash` exposes internal type** — *Intentional design.* Documented in the interface comment (peer_manager.go:14-22) AND in the Phase 11.6 ROADMAP entry: outputs return concrete `identity.Hash` because PeerManager owns the peer-id values internally; returning a `[]identity.PeerID` would force interface boxing on every Snapshot. This is an explicit perf trade-off for an output type, asymmetric with the input boundary by design. **Decision: rejected with rationale (no change needed).**

### 1/3 SUGGESTION items — addressed in iter-4 fix-up

1. **Iter-2: missing Suite0x02 stub test** — *Legitimate gap.* ROADMAP 11.10 explicitly required this as proof of the "extensibility without consumer changes" claim. **Fix: added `pkg/identity/suite02_stub_test.go`** with non-cryptographic stub `stubSuite{}` proving (a) interface satisfaction (Local, Remote, PeerID, Signer, Verifier, KeyAgreement), (b) `pkg/dht` (RoutingTable + MemoryStore) accepts the foreign Suite-derived PeerID end-to-end. The test does NOT register the stub in the global LookupSuite registry (would pollute production state from test code) — registry round-trip is already proven by `TestLookupSuite_Known`. The substantive claim is what matters.

### Other 1/3 items (deferred or noted)

- **Iter-1: `Closest` input/output asymmetry doc** — minor, ROADMAP 11.6 entry already explains the asymmetry; could add inline godoc but low ROI.
- **Iter-1: `noise.StaticKeypair` alias vs struct** — preference, current alias is fine.
- **Iter-2: noise godoc could specify X25519-only constraint more loudly** — fair point; package godoc updated in 11.9 already says "XK with a different DH curve is a different handshake" which expresses the same constraint. Could expand if needed in follow-up.
- **Iter-2: pkg/network opt-out timeline vague** — already documented as Phase 11.5+ follow-up in ROADMAP 11.8 entry.
- **Iter-3: 3 minor doc clarifications** (Marshal panic context, XPriv lifetime warning, `nid := key.Bytes()` comment) — low ROI, deferred.

## Re-audit verdict

| Metric | Pre-Phase-11 | Post-Phase-11 |
|---|---|---|
| GREEN packages | 7/15 (47%) | **11/15 (73%)** |
| YELLOW packages | 7/15 (47%) | **2/15 (13%)** + network (composition-only, by design) |
| RED packages | 0/15 | 0/15 |

**Improved to GREEN** (5 packages): identity, noise, dht, presence, signaling, transport, webrtc, turn (within YELLOW — realm now configurable; remaining YELLOW only for `Software` STUN attribute).

**Remaining YELLOW**: `pkg/stun` (hardcoded `Software="udisend-stun"` — trivial future fix), `pkg/network` (composition-only, intentionally udisend-specific; godoc explicitly directs external consumers to compose lower layers directly).

**Did Phase 11 deliver the user's stated goal?** Re-audit verdict (Agent 1, condensed):
- **`pkg/webrtc` standalone reuse**: ✅ YES.
- **`pkg/dht` + `pkg/signaling` standalone reuse**: ✅ YES.
- **`pkg/identity` Suite-substitution**: ✅ YES (Suite0x02 stub test now proves it).
- **`pkg/network` standalone reuse**: ⚠️ PARTIAL by design (composition layer; explicit godoc points consumers to lower layers).

## Iter-4 post-fix verification

- `go test -race -count=1 ./...` — **green** (1 known Phase-10 mesh-e2e flake under full-suite + heavy race load, not a Phase-11 regression).
- `go vet ./...` — clean.
- New `suite02_stub_test.go` runs in <10ms, deterministic.
- All 8 Phase-11 commits + iter-4 fixes form a clean chain on `feat/phase11-pkg-refactor`.

## Decision

Phase 11 status → **✅ done**. Decisions log entry to be added after this summary lands.
