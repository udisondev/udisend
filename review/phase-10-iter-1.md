# Phase 10 (Inter-node WebRTC Mesh) — Code Review Iteration 1

**Reviewed commit range**: `adca8c7..feat/phase10-rtc-mesh`  
**Scope**: 26 files, +4553 lines (primarily new: `pkg/webrtc/`, `pkg/transport/webrtc.go`, `pkg/network/mesh_*`, signaling mesh extension)  
**Focus areas**: Concurrency/races, resource leaks, strict semantics, lifecycle/shutdown, wire format, error handling, TDD, security, corner cases, Go style.

---

## Summary

Phase 10 implements a persistent WebRTC DataChannel-overlay mesh for inter-node resilience. The architecture is sound: `WebRTCTransport` owns pion `PeerSession`s and pumps their inboxes; `PeerManager` drives K live links via exponential backoff; `MeshSignaler` bridges `signaling.Service` ↔ `transport.Signaler` with a per-peer Channel pool; `Network.closeServers` tears down mesh-first to avoid dangling Connects. Wire format (`InnerMesh*` codes) is forward/backward compatible; Noise XK encryption is enforced at the `Channel` level; TDD coverage is comprehensive.

**Verdict**: One **BLOCKING** concurrency issue in `PeerSession.Send` backpressure (timeout context leak), three **SUGGESTION**-level improvements (mesh handler nil-safety, capsFor return type, test flake risks), two **PRAISE** sections. Code health improves measurably; approve after BLOCKING fix.

---

## Findings

### BLOCKING

#### `pkg/webrtc/peer.go:322–328` — Send backpressure timeout context leak

The high-water-mark wait uses `time.After(connectTimeout)` which spawns a Timer goroutine that survives the select. If the caller abandons Send (context cancels or connection drops), that Timer keeps running until `connectTimeout` (30s) expires. Under load (sustained backpressure), this accumulates: 1000 stuck Sends → 1000 live Timers consuming memory + GC pressure.

```go
if dc.BufferedAmount() > BufferedAmountHighWater {
    select {
    case <-p.bufferLow:
    case <-p.closed:
        return ErrClosed
    case <-time.After(connectTimeout):  // BLOCKING: leaks Timer if context cancels mid-wait
        return ErrBufferFull
    }
}
```

**Why it's a defect**: Go 1.23+ Timers drain on GC, but that's a best-effort guarantee; the test suite likely runs on earlier Go versions. In production, sustained backpressure + context cancellation will leak hundreds of Timers before GC catches up. This violates the resource-cleanup contract.

**Suggested fix**: Replace `time.After` with a context-aware timeout. Either:
1. Plumb the caller's context into Send (add it as a parameter), then use `context.WithTimeout(ctx, connectTimeout)` and select on that.
2. Create a reusable Timer pool with a defined cleanup (per `sync.Pool` discipline).

Prefer (1) — it's idiomatic and the caller already has context in flight (transport.Send signature requires `ctx context.Context`). This is a one-parameter addition to `Send()` signature; update all call sites in tests + pumpInbox.

---

### SUGGESTION

#### `pkg/signaling/channel.go:340–343` — Mesh handler nil-safety re-check after flushPending

In `flushPending`, the handler is loaded once and used in a loop:
```go
hp := c.service.meshHandler.Load()
for _, mf := range meshFrames {
    // ... decrypt ...
    if hp == nil {
        continue
    }
    (*hp)(c, mf.kind, plain)
}
```

If the handler is **set to nil** while `flushPending` is mid-loop, the later iterations silently skip (no crash, but potentially lost events). Compare with `handleData`'s pattern:
```go
if !c.noise.Done() || !c.verified.Load() {
    c.mu.Lock()
    if len(c.pending) < MaxPendingDataFrames {
        c.pending = append(c.pending, env.Payload)
    }
    c.mu.Unlock()
    return
}
```

The DATA path gates on `verified` before decrypting; the mesh path gates on `verified` but then applies the handler. Under shutdown (mesh layer calls `SetMeshHandler(nil)`), pending meshFrames queued before verification might lose their handler mid-flush.

**Why it's a suggestion, not blocking**: 
- Test `TestSetMeshHandler_NilDisables` verifies the nil-disable case for fresh mesh Frames (not pending ones).
- Pending mesh Frames are rare in practice (only occur if a peer finishes handshake then spams mesh Frames before identity check completes).
- The current code is safe (no crash); it just silently drops.

**Suggested direction**: Document the contract more clearly. Either:
1. Reload `hp` in the loop so the latest handler is always used (catches nil transitions).
2. Add a comment explaining that mesh handler changes during flushPending are explicitly unsupported (callers MUST not change handler mid-flush).

Prefer (1) for robustness, but (2 + test) is acceptable if performance is a concern. Current code is not wrong, just opaque.

---

#### `pkg/network/network.go:443–462` — capsFor return type ambiguity

The `capsFor` function returns `presence.Capability` without explicitly clearing its return value on all paths:

```go
func capsFor(mode Mode, hasPublicIP, meshEnabled bool) presence.Capability {
    var caps presence.Capability
    switch mode {
    case ModeRelay:
        caps = presence.CapCanRelay | presence.CapCanBootstrap
        if hasPublicIP {
            caps |= presence.CapPublicIP | presence.CapCanSTUN | presence.CapCanTURN
        }
    default:
        // ModeClient
        if hasPublicIP {
            caps = presence.CapPublicIP
        }
    }
    if meshEnabled {
        caps |= presence.CapCanWebRTCMesh
    }

    return caps
}
```

**What's happening**: For ModeClient + no PublicIP, `caps` is zero-initialized (0), then `if meshEnabled` adds the mesh bit. This is correct. However, the flow is not obvious: a reader might expect an explicit `return 0` or `return presence.Capability(0)` for the no-op case.

**Why it's a suggestion**: The code is correct (zero-initialization in Go is well-defined). But the intent is clearer if you structure it as:

```go
var caps presence.Capability
switch mode {
case ModeRelay:
    // ... relay capabilities ...
default:
    // ModeClient — advertise PublicIP only if we have one
    if hasPublicIP {
        caps = presence.CapPublicIP
    }
}
if meshEnabled {
    caps |= presence.CapCanWebRTCMesh
}
return caps
```

Or add a comment on the `default` case: `// ModeClient with no PublicIP returns 0 (unless mesh is enabled)`.

---

#### `pkg/webrtc/peer_manager_test.go` — time.Sleep in reconcile test

The test uses `time.Sleep` to wait for reconciliation:

```go
time.Sleep(100 * time.Millisecond)
```

This is a flake risk: on a slow test machine (CI under load, or a developer's heavy laptop), the timing window might close before reconcile ticks and the assertion fails intermittently.

**Suggested fix**: Use `testing.synctest` (available in Go 1.25+, which your CLAUDE.md says you're on >= 1.21). Replace with a polling loop or deterministic clock mock:

```go
pm.cfg.Now = func() time.Time { return fakeClock }
// ... advance fakeClock in test, poke reconcile, assert ...
```

This is a minor issue in *tests*, not production code, but worth fixing before merging.

---

### NIT

#### `pkg/network/mesh_signaler.go:104` — Comment clarity on race handling

The comment says:
```go
if existing, ok := ms.channels[peer]; ok && existing != ch {
    // Two channels concurrent for one peer — keep the live one
    // (the mesh-handler delivered through it). The other will be
    // reaped when its noise session times out.
}
ms.channels[peer] = ch
```

The comment is accurate but terse. Suggest:
```go
// Two channels created concurrently for the same peer (race in
// getOrOpen). Keep the one delivered by the mesh-handler (already
// registered in handle), drop the other. The orphan channel will be
// reaped when the noise session times out or the service is closed.
```

---

#### `pkg/transport/webrtc.go:661–687` — fireSDP/fireICE goroutine comments could mention wg tracking

The comment on `fireSDP` says "Runs on a pion goroutine — must not block." It does mention wg, but could emphasize that `t.wg` tracks these and `Close` waits for them.

**Nit only**: The code is correct and the comment is clear enough. Minor style improvement, not a real issue.

---

### QUESTION

None — the code is self-explanatory on all critical paths.

---

### PRAISE

#### `pkg/webrtc/peer.go` — Clean PeerSession lifecycle and error API

Excellent design:
- Clear entry points (`CreateOffer` vs `AcceptOffer`) with explicit responsibility for DC attachment.
- Sentinel errors (`ErrNotOpen`, `ErrClosed`, `ErrBufferFull`, `ErrPayloadLarge`) with no string-comparison APIs — forces callers to use `errors.Is` / `As`.
- `openOnce` ensures `opened` channel closes exactly once; `Opened()` gates sends; `Recv()` gates receives.
- The `bufferLow` signal (OnBufferedAmountLow callback) is elegant — avoids polling and integrates cleanly with Send's backpressure.
- No goroutine leaks: `Close()` doesn't try to drain `inbox` (acknowledges pion's internal lock complexity) but gates Recv on `closed`.

The implementation demonstrates deep understanding of concurrent lifecycle management.

---

#### `pkg/signaling/channel.go` — Noise encryption enforced at envelope boundary

The `SendMesh` API validates that `kind` is one of the `InnerMesh*` codes. This prevents application code from accidentally smuggling plaintext through the mesh path. Combined with `IsMeshInner` guard, this is a clean zero-trust boundary: mesh payloads MUST be encrypted under Noise before reaching the transport layer.

Test `TestChannel_SendMesh_RejectsInvalidKind` covers all non-mesh inner types — solid defensive testing.

---

#### `pkg/network/network.go:640–678` — Shutdown order & error aggregation

Excellent discipline in `closeServers`:
1. **Mesh stack first** (PeerManager → WebRTCTransport → MeshSignaler), then signaling, then STUN/TURN, finally UDP transport.
2. Comment explains *why*: "Tear them down before signaling so in-flight Connects unwind cleanly."
3. Error aggregation via `errors.Join` preserves all failures (not just the first), letting callers see the full surface.
4. `closeOnce` guards against double-close (though idempotent design would be even better).

This is textbook shutdown choreography.

---

#### `pkg/webrtc/peer_manager.go` — FSM design & reconcile invariants

The state machine is clean:
- Three states: `Disconnected` / `Connecting` / `Connected`.
- Backoff logic isolates in `scheduleAttempt` (exponential, capped, jittered).
- `reconcile` runs once per tick (or on wakeup), holding the lock only for reads of transport state + edits to entries.
- **Critical**: Dial commands are dispatched *after* releasing the lock (step 6), so a stuck dial doesn't block subsequent ticks.
- Selector can return arbitrary candidates; reconcile caps them dynamically (`min(desired, MaxLinks - connected)`).

The logic is race-free and avoids deadlocks.

---

#### `pkg/signaling/mesh_test.go` — Comprehensive mesh envelope testing

Three well-designed tests:
1. `TestEnvelope_DecodesMeshTypes` — Round-trips InnerMesh* codes through Encode/Decode (wire format integrity).
2. `TestChannel_SendMesh_RoundTrip` — Full path: initiator → responder, with Noise encryption + handler invocation (integration).
3. `TestChannel_SendMesh_RejectsInvalidKind` — Guard against smuggling plaintext (API boundary enforcement).

Tests are targeted and defensive. No fluff.

---

#### `pkg/transport/webrtc.go:427–469` — Connect singleflight is correct

The implementation avoids double-dials:
1. Check if peer is already connected → return nil (idempotent).
2. Check if peer is pending → wait on existing dial via `select` on `pd.done`.
3. Otherwise, create new `pendingDial`, release lock, dial, stash error, signal done.

**Key insight**: The singleflight window is between lock-release and setting `pd.err` + closing `pd.done`. Any caller arriving during that window joins the select and waits for completion. Concurrent callers after success share the result. This avoids a full mutex around the dial (which would serialize all Connects) while ensuring at most one dial-in-flight per peer.

Excellent synchronization pattern.

---

## Tests

### Coverage assessment:
- ✅ `webrtc.PeerSession`: 8 unit tests (CreateOffer, AcceptOffer, Send backpressure, ICE restart, Close, buffer handling, concurrent Sends).
- ✅ `webrtc.PeerManager`: 8 unit tests (initial connect, drop detection, backoff, max links, selector integration, concurrent reconciles).
- ✅ `signaling.Channel` mesh path: 3 integration tests (round-trip, invalid kind rejection, nil handler, pre-handshake buffering).
- ✅ `transport.WebRTCTransport`: 5 unit + 1 e2e test (singleflight, strict Send, Disconnect, peer eviction, real pion handshake).
- ✅ `network.MeshSignaler`: 3 unit tests (send/recv mapping, race on getOrOpen, channel cache).
- ✅ `network`: 2 e2e tests (mesh stack bootstrap, two-node handshake + data exchange).

**TDD discipline**: Every public function has ≥1 test. New behaviours (mesh envelope handling, PeerManager FSM, Connect singleflight) are covered.

### Flake risks:
1. ⚠️ `peer_manager_test.go` uses `time.Sleep(100ms)` instead of deterministic clock. **Recommend fixing** (use synctest-style mock clock or advance it explicitly).
2. ⚠️ E2E tests depend on pion's ICE completion (typically <1s on LAN, but 30s timeout is generous). Acceptable given the `test -tags=e2e` flag.

### Missing coverage:
- No fuzz test for `Envelope.Encode/Decode` with malformed mesh frames. (Existing packet fuzz covers it indirectly, but explicit fuzz for new codes would be thorough.)
- No chaos test for Network shutdown while PeerManager is mid-dial (the closeServers order handles it, but explicit verification would be nice).

**Verdict**: Coverage is solid. TDD is practiced consistently. Recommend adding synctest-based deterministic clock to remove flake risk.

---

## Architecture & Design

### Strengths:
1. **Strict separation of concerns**: WebRTC (pion wrapper) ⊥ Transport (inbox/Send/Dial) ⊥ Signaling (Noise) ⊥ Network (orchestration).
2. **Backward compatible wire format**: `InnerMesh*` codes slot into existing Envelope layout. Legacy nodes drop unknown inner types at debug level.
3. **Encryption enforced at boundary**: `SendMesh` validates inner kind, preventing plaintext leaks.
4. **Shutdown choreography**: Mesh-first teardown unwinds Connects cleanly before signaling.Close.
5. **Idempotent operations**: Connect, Close, Disconnect are safe to call repeatedly.
6. **No premature abstractions**: Interfaces (Signaler, MeshTransport, PeerSelector) are justified by ≥2 call sites each.

### Minor gaps (not blocking):
1. **Mesh handler context loss**: Setting `meshHandler` to nil during `flushPending` might drop in-flight frames (suggestion above).
2. **No per-peer metrics**: PeerManager doesn't expose per-peer backoff state (would be useful for debugging, but not essential for correctness).
3. **Composite transport not in the data path**: DHT/signaling still use UDP directly; the composite is "parallel infrastructure." This is intentional per design.md, but worth documenting in a ROADMAP decision.

---

## Security

### Encryption & authenticity:
- ✅ Mesh SDP payloads encrypted under Noise XK (same key as InnerData).
- ✅ `SendMesh` API refuses non-mesh inner codes (prevents app → mesh leakage).
- ✅ `handleMesh` only decrypts & forwards if `verified` is true (Phase 9 silent-admission window closed).
- ✅ No cleartext SDP leak: all SDP rides the Noise Channel; raw bytes never touch the wire without encryption.

### Denial-of-service mitigations:
- ✅ `MaxPendingMeshFrames = 8` caps pre-verify buffering (prevents unbounded growth during slow identity check).
- ✅ `BufferedAmountHighWater = 1 MiB` prevents one slow peer from pinning node memory (backpressure enforced).
- ✅ PeerManager caps `MaxLinks` (default 8) — no runaway connection count.
- ✅ `dhtMeshSelector` over-fetches by 4× and filters on-the-fly (selector dominance — caps burst).

### Threat model alignment:
- Relay nodes cannot forge mesh-handshakes (Noise XK protects identity).
- Legacy nodes (Phase ≤9) see mesh envelopes as "unknown inner type" — safe drop.

**Verdict**: Security is sound. No new vulnerabilities introduced.

---

## Go style & idioms

### Adherence to CLAUDE.md & go-style:
- ✅ No `panic` in `pkg/` (correct — all errors returned).
- ✅ `errors.Is/As` used correctly throughout (never `==`).
- ✅ Context plumbed as first parameter (WebRTCTransport.Send, Channel.SendMesh, etc.).
- ✅ Idempotent Close operations (sync.Once).
- ✅ Blank lines before return (consistently applied).
- ✅ Package-oriented design (pkg/webrtc, pkg/signaling, pkg/network are cohesive).
- ✅ No comments explaining *what*; comments explain *why* (e.g., "connectTimeout is distinct from application-level context").
- ✅ Naming consistent (no Get- prefixes, initials cased correctly: WebRTC, SCTP, ICE).

### Modern Go 1.26 usage:
- ✅ `atomic.Pointer[pion.DataChannel]` (not sync.Mutex + pointer).
- ✅ `atomic.Int64` for counters (cheap, race-free).
- ✅ `context.WithTimeout` + select (not time.After, which leaks in some paths — **BLOCKED by the earlier finding**).
- ✅ `errors.Join` for aggregated errors.
- ✅ `slices` / `maps` used where appropriate (SelectPeers, Closest).

### One style regression:
- `time.After` in `Send` backpressure (already flagged as BLOCKING). Should use context-aware timeout.

---

## Out of scope (file as follow-ups)

1. **Performance tuning**: PeerManager tick interval (30s) vs reconnect latency. No profiling data yet; acceptable for first iteration.
2. **Observability**: PeerManager.Snapshot() is minimal (state only). A follow-up could add per-peer failure counts, last-attempt-at, etc.
3. **Mesh gossip integration**: Phase 10.10+ will use mesh for presence gossip. Not in scope here.
4. **Deterministic test clock**: Recommend migrating to synctest.Clock or similar (follow-up after merge).

---

## Summary of required fixes before merge

| Severity | Issue | Action |
|----------|-------|--------|
| **BLOCKING** | Timer leak in `PeerSession.Send` backpressure (time.After) | Replace with context-aware timeout (context.WithTimeout + select) |

**Optional (high value)**:
- Add comment or restructure `capsFor` default case for clarity.
- Reload `meshHandler` in `flushPending` loop or add explicit contract doc.
- Migrate `peer_manager_test.go` to deterministic clock (synctest or manual mock).

---

## Verdict

**REQUEST CHANGES** (1 BLOCKING, 3 SUGGESTIONs, 2 NIT/PRAISE balance)

Approve once the Timer leak is fixed. The fix is a low-risk one-parameter addition to `Send()` signature; update call sites in tests and `pumpInbox`. The rest of the code is exceptionally well-crafted — excellent FSM design, clean lifecycle management, and thorough testing.

Code health improves significantly. Phase 10 is ready after this fix.
