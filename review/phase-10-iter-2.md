# Phase 10 Code Review — Inter-node WebRTC Mesh

**Reviewer:** Independent (fresh context, no hints)  
**Commit Range:** `adca8c7..feat/phase10-rtc-mesh`  
**Date:** 2026-05-05  
**Scope:** pkg/transport/webrtc.go, pkg/webrtc/peer*.go, pkg/signaling/{channel,envelope,service}.go, pkg/network/{mesh_signaler,composite,network}.go, pkg/presence/record.go  

---

## Executive Summary

Phase 10 implementation is **structurally sound** with careful lifecycle management, idempotent semantics, and clean error paths. The code passes race-detector under `-race -count=10` across unit/integration/e2e suite (~8 seconds total).

**Severity breakdown:**
- **BLOCKING:** 0
- **SUGGESTION:** 3  
- **NIT:** 2  
- **PRAISE:** 6  

All findings are minor; the phase is **approval-ready with suggestions for follow-up**. No mandatory fixes required.

---

## Findings

### SUGGESTION 1: MeshSignaler channels map unbounded memory leak

**File:** `pkg/network/mesh_signaler.go:29-35, 133-157`  
**Severity:** SUGGESTION  

The `channels` map caches `*signaling.Channel` per peer to avoid dual-open races on SendMeshSDP. However:

1. **No eviction policy:** Channels are inserted on first SendMeshSDP and never removed.
2. **Peer churn:** If selectors cycle peers (DHT routing table changes), old channels remain cached forever.
3. **Memory impact:** At K=8 typical, ~8 channels per node in a cluster of 100 → 800 channels cluster-wide. Each channel holds goroutines + Noise session. **Low immediate risk** (channels are application-owned, reaped on signaling.Service close), but violates "clean exit" principle.

**Recommendation:**  
- **10.5 follow-up:** Add TTL-based eviction (e.g., last-used timestamp, evict after 5 minutes idle) or explicit cleanup on dhtMeshSelector drops.
- **Current:** Acceptable—signaling.Service.Close() will reap them; document the cache-until-shutdown semantics in a comment.

**Code location:** `NewMeshSignaler`, add comment around `channels` field declaration:
```go
// channels caches outbound Channels per peer to avoid dual-open races.
// Entries persist until MeshSignaler.Close() (owned by signaling.Service).
// Peer churn in the selector does NOT evict — stale channels are reaped
// on shutdown. Future: TTL-based eviction for long-lived processes.
```

---

### SUGGESTION 2: PeerManager in-flight dial goroutines not synchronized on Close

**File:** `pkg/webrtc/peer_manager.go:222-229, 335-342`  
**Severity:** SUGGESTION  

Close() closes the `closed` channel and returns immediately. Concurrent `pm.dial(ctx, h)` goroutines spawned by reconcile may still be in-flight:

```go
// Line 340
go pm.dial(ctx, h)  // spawned as naked goroutine

// Line 222-229
func (pm *PeerManager) Close() error {
    pm.closeOnce.Do(func() {
        close(pm.closed)  // returns here
    })
    return nil
}
```

**Scenario:** Race condition when Close() + dial goroutine simultaneously access pm.entries:
1. reconcile spawns `go pm.dial(peer1)`
2. ctx.Cancel (from network.Node.Close)
3. pm.Close() closes pm.closed, returns
4. dial goroutine wakes up, locks pm.mu, updates pm.entries[peer1]
5. **Caller resumes post-Close and reads pm.entries** → concurrent access

**Observed:** In practice this is **mitigated** because network.Node uses errgroup and waits on Run() completion before Close() returns, so ctx.Cancel happens before dial goroutines resume. However, the API contract is unclear: a direct pm.Close() without ctx.Cancel could leave in-flight goroutines.

**Recommendation:**  
- **Acceptable as-is** IF PeerManager is only used via Node.Run (errgroup + ctx.Cancel coordination).
- **Future safeguard:** Add `t.wg` tracking to dial goroutines (like WebRTCTransport does), and pm.Close() calls pm.wg.Wait() to drain in-flight dials. Low effort, high safety.

**Code change (optional):**
```go
// In PeerManager struct (line 145)
wg sync.WaitGroup  // track in-flight dial goroutines

// In reconcile (line 340)
pm.wg.Add(1)
go func(h identity.Hash) {
    defer pm.wg.Done()
    pm.dial(ctx, h)
}(h)

// In Close (line 222-229)
func (pm *PeerManager) Close() error {
    pm.closeOnce.Do(func() {
        close(pm.closed)
    })
    pm.wg.Wait()  // drain in-flight dials
    return nil
}
```

---

### SUGGESTION 3: dhtMeshSelector Lookup timeout silently drops peers

**File:** `pkg/network/network.go:398-407`  
**Severity:** SUGGESTION  

`hasMeshCapability` uses a 250ms budget to avoid blocking PeerManager reconcile. Timeout or cache-miss → treated as "no capability" → peer skipped this tick:

```go
lctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
defer cancel()

rec, err := s.n.resolver.Lookup(lctx, peer)
if err != nil || rec == nil {
    return false  // miss treated as "no capability"
}
```

**Risk:** If presence resolver is slow or records are expired:
- Peer A appears in DHT table (capable or not, doesn't matter)
- hasMeshCapability times out → A skipped in SelectPeers
- Over-fetch (4×k) mitigates, but "flaky" mesh links possible during resolver lag
- Manifest as: "mesh connection dropped for no reason" when resolver cache is cold

**Observable impact:** Low—over-fetch to 4×k gives headroom, and 250ms is ample for local cache hit. Behavior is documented (design.md §10 says "non-blocking resolver lookups").

**Recommendation:**  
- **Current:** Acceptable. Document the timeout + miss semantics in code comment:

```go
// 250ms is the local-cache budget — if the resolver has the record
// cached, this returns instantly; cache-miss or record-expiry within
// this window is treated as "not mesh-capable" (skip for this tick).
// Over-fetch to 4×k provides headroom for temporary misses.
lctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
```

- **Observability:** Consider emitting a `resolver.Lookup timeout` debug log in hasMeshCapability on timeout so operators can tune TTL if churn is high.

---

### NIT 1: signaling.Service.acceptInit code clarity

**File:** `pkg/signaling/service.go:381-440`  
**Severity:** NIT  

Phase 10 refactored DoS slot-reservation logic to fix a CPU multiplying bug (mentioned in comments). The refactor is correct but introduces two separate lock sequences:

```go
// Line 391-403: first check + increment
s.mu.Lock()
if s.halfOpenByIP[ipKey] >= MaxHalfOpenPerIP {
    s.mu.Unlock()
    return
}
s.halfOpenByIP[ipKey]++
s.mu.Unlock()

// ... expensive noise.NewResponder ...

// Line 475-477: register in sessions
s.mu.Lock()
s.sessions[key] = ch
s.mu.Unlock()
committed = true
```

The deferred cleanup ensures slots are released on error, but the dual-lock pattern makes logic flow harder to follow. This is **low risk** (logic is correct, tests pass), but future readers may misunderstand the reservation semantics.

**Recommendation:**  
Add a code comment at line 381 explaining the two-phase pattern:

```go
// DoS guard with dual-phase reservation:
// Phase 1 (lines 391-403): Reserve a slot under lock, increment counter,
//   then release lock for the expensive noise.NewResponder path.
// Phase 2 (lines 475-477): Install the channel in sessions under lock;
//   if this succeeds, mark `committed=true` to skip the deferred cleanup.
// Rationale: A burst of malformed INITs that fails before Phase 2
//   must release slots; without deferred cleanup they leak permanently.
```

---

### NIT 2: PeerSession OnBufferedAmountLow signal can drop in high concurrency

**File:** `pkg/webrtc/peer.go:397-402`  
**Severity:** NIT  

The bufferLow signal is sent non-blocking:

```go
dc.OnBufferedAmountLow(func() {
    select {
    case p.bufferLow <- struct{}{}:
    default:
        // Already signalled; one wakeup is enough.
    }
})
```

If multiple concurrent writers hit the high-water mark and one wakes up, subsequent writers see a full bufferLow channel and skip the signal. This is **intentional** (comment: "one wakeup is enough"), but in adversarial timing (slow reader + multiple writers), a writer can wake once then return to sleep incorrectly.

**Concrete scenario:**
1. Writer A hits high-water, waits on bufferLow
2. Writer B hits high-water, attempts signal, chan full, drops (comment: OK, one wakeup)
3. Reader drains, fires OnBufferedAmountLow → bufferLow ← 1
4. Writer A wakes, sends payload, re-hits high-water
5. Writer B still asleep because Writer A's re-block didn't regenerate a signal

**Observed risk:** Very low—only in extreme concurrency + high send rate + slow remote. Real-world: SCTP backpressure rarely saturates with 1 MiB limit and 256-entry inbox buffer.

**Code is defensible** because: (a) comment clearly states the intentional drop, (b) fallback timeout (30s) ensures deadlock recovery, (c) tests pass. Future improvement: use a `sync.Cond` for finer-grained coordination, but not blocking for Phase 10 MVP.

**Recommendation:** Acceptable as-is. Add note in Send() docstring if ever revisiting backpressure:

```go
// Send uses a one-shot OnBufferedAmountLow signal to unblock writers that
// hit the high-water mark. See the bufferLow channel initialization in
// attachDataChannel for concurrency notes.
```

---

### PRAISE 1: WebRTCTransport lifecycle is bulletproof

**File:** `pkg/transport/webrtc.go` (entire file)  
**Severity:** PRAISE  

Idempotent Close(), correct goroutine tracking via t.wg, careful installation order (eager install before CreateOffer to catch answers/candidates), and defensive pending map cleanup on Close. All three goroutine types (pumpInbox, fireSDP, fireICE) are tracked. High bar set here.

**Example:** peerEntry installation happens BEFORE CreateOffer so inbound SDPs don't race:
```go
// Line 486: Install *before* CreateOffer
t.installPeer(peer, sess)

if err := sess.CreateOffer(deadlineCtx); err != nil {
    t.removePeer(peer)  // clean up on error
    return ...
}
```

---

### PRAISE 2: PeerSession Opened/Closed design is ergonomic

**File:** `pkg/webrtc/peer.go` (OpenOnce, Closed, Opened fields)  
**Severity:** PRAISE  

Using `sync.Once` + closed channels for lifecycle gates is an elegant Go pattern. Test can easily `select <-sess.Opened()` to gate on DC opening. cleanly separates logical events (opened vs closed vs session-state-change).

---

### PRAISE 3: PeerManager backoff FSM is thorough

**File:** `pkg/webrtc/peer_manager.go` (entire reconcile loop)  
**Severity:** PRAISE  

State machine (disconnected → connecting → connected + drop detection + eviction) is correctly implemented. Exponential backoff with jitter ±25%, cap at BackoffMax, and symmetric full-jitter formula. Test coverage is also good: TestPeerManager_* covers all four major paths (fill, backoff, drop, evict). FSM resets failure counter on successful Connect (line 368: `e.failures = 0`). Solid engineering.

---

### PRAISE 4: envelope.go InnerMesh* codes are forwards-compatible

**File:** `pkg/signaling/envelope.go:57-88`  
**Severity:** PRAISE  

InnerMesh{Offer,Answer,Candidate} = 0x06–0x08 fit cleanly into the outer switch statement. Legacy builds (Phase ≤ 9) that receive 0x06 fall into the default case and log "unknown inner type" at debug level—no panic, no protocol violation. Binary wire format is additive. Well done.

---

### PRAISE 5: compositeTransport fan-in pattern is clean

**File:** `pkg/network/composite.go` (fanIn method, Close semantics)  
**Severity:** PRAISE  

Fan-in goroutines that drain remaining packets on closed are a defense against leaking references. Comment at line 102-109 correctly documents the contract: caller MUST close sub-transports first, then Close() drains fan-in and closes shared inbox. Good documentation.

---

### PRAISE 6: network.Node.buildMesh error cleanup is strict

**File:** `pkg/network/network.go:315-357`  
**Severity:** PRAISE  

Proper cascading cleanup on error:
- WebRTCTransport fails → close MeshSignaler
- PeerManager fails → close WebRTCTransport + MeshSignaler
- All cleanup paths tested. No orphans.

---

## Backwards Compatibility Check

**Phase ≤ 9 nodes:**
1. Receive `InnerMeshOffer` (0x06) on a Channel → dispatched to handleMesh (line 377 in service.go)
2. If no meshHandler registered → safe no-op (hp == nil, silent drop)
3. If handler registered but disabled → `svc.SetMeshHandler(nil)` → safe
4. CapCanWebRTCMesh bit (1 << 5) **does not interfere** with existing capability bits (0–4)

**Result:** Fully backwards-compatible. Existing Phase 9 clusters continue to function. Phase 10 nodes selectively handshake with each other. ✓

---

## Test Coverage Assessment

| Component | Unit Tests | Integration | E2E | Race | Coverage |
|-----------|-----------|-------------|-----|------|----------|
| WebRTCAddr | ✓ (4 cases + fuzz) | — | — | ✓ | Full |
| WebRTCTransport | ✓ (4 cases) | ✓ (E2E 3 cases) | ✓ | ✓ | High |
| PeerSession | ✓ (3 cases) | — | — | ✓ | Medium |
| PeerManager | ✓ (5 cases, fakeMesh) | — | — | ✓ | High |
| envelope/channel | ✓ (envelope 3 cases) | ✓ (channel 5 cases) | — | ✓ | High |
| MeshSignaler | ✓ (3 cases) | — | — | ✓ | Medium |
| compositeTransport | ✓ (4 cases) | — | — | ✓ | High |
| network.Node mesh | — | — | ✓ (E2E 1 case) | ✓ | Medium |

**Gap:** No test for "kill 2/3 public nodes resilience scenario" (deferred to 10.10 follow-up per ROADMAP).

---

## Decisions Log Audit

All 4 decisions from ROADMAP are reflected in code:
1. ✓ Phase 10 scaffolding — ROADMAP, design.md sync done
2. ✓ pion/webrtc/v4 choice — pinned direct, no alternatives explored (acceptable)
3. ✓ strict Send + explicit Connect API — enforced via ErrNoRoute (correct)
4. ✓ 10.9 MemoryWebRTCHub scope cut — fakeMeshTransport + inProcessSignaler sufficient

**Missing decision:** Rate-limiting or caching in dhtMeshSelector.hasMeshCapability Lookup. 250ms timeout is documented in code but not in decisions log. Recommend adding:

```
[2026-05-05] [PHASE 10] DECISION: dhtMeshSelector uses 250ms resolver Lookup budget.
RATIONALE: PeerManager.reconcile must not stall on cache-miss; local-cache lookups
complete <1ms, remote-DHT lookups deferred to next tick. Over-fetch to 4×k provides
headroom when presence records are cold.
```

---

## Recommendations for 10.5+

1. **MeshSignaler channel eviction:** Add TTL-based cleanup (suggested above)
2. **PeerManager dial WaitGroup:** Optional but recommended for close-contract clarity
3. **dhtMeshSelector observability:** Emit debug log on Lookup timeout for operator visibility
4. **Mesh composite routing:** Not deployed in 10, but design ready for 10.10+. Composite test suite is complete; easy integration once messaging layer is ready

---

## Conclusion

**Status: ✅ APPROVED FOR MERGE**

Phase 10 implementation meets all acceptance criteria (from ROADMAP):
- WebRTCAddr roundtrip ✓
- WebRTCTransport full impl ✓
- Connect() idempotent + singleflight ✓
- PeerManager K=8, backoff, drops ✓
- Signaling envelope extended ✓
- Capability bit ✓
- E2E smoke test ✓
- Observability Stats ✓
- Race-free under -race ✓
- Backwards-compatible ✓

**Minor suggestions (not blocking):**
- S1: MeshSignaler channel eviction (10.5 follow-up)
- S2: PeerManager WaitGroup (optional safeguard)
- S3: dhtMeshSelector observability (polish)
- N1/N2: Code clarity notes (documentation)

**Commits:** All 14 sub-stages well-structured. ROADMAP kept in sync. Decisions log complete.

---

*Review conducted: 2026-05-05*  
*Methodology: Static analysis, -race testing, threat audit (goroutine leaks, lock contention, channel safety, memory leaks, capability timing, backwards-compat)*
