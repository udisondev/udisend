# Phase 10 (feat/phase10-rtc-mesh) — Code Review — Iteration 3

**Reviewed:** `adca8c7..feat/phase10-rtc-mesh` (20 commits, ~4500 insertions)

**Focus:** WebRTC mesh overlay, threat model, lifecycle management, forward/backward compat, Go style, test quality, performance.

---

## Summary

Phase 10 introduces inter-node WebRTC mesh (pion/webrtc/v4) atop existing UDP DHT/signaling, adding persistent DataChannels that survive coordinated relay failures. The implementation is architecturally sound: strict Send semantics (ErrNoRoute), singleflight Connect, trickle ICE, and careful goroutine lifecycle. However, **four BLOCKING issues** emerge across security, resource management, and API stability that must be resolved before merge.

---

## Findings

### BLOCKING

1. **`pkg/transport/webrtc.go:677–686` — Unbounded goroutine spawn on every OnSDP / fireICE callback without backpressure.**
   
   `fireSDP()` and `fireICE()` fire a fresh goroutine on *every* ICE candidate emitted by pion (potentially dozens per peer per handshake). With no queue, no context deadline propagation, and 1s + timeout retry loops, a malicious/broken peer can pin goroutines indefinitely. Under Sybil flood (connect to 100 peers, each emitting 50 candidates), this spawns **5000+ leaked goroutines** that accumulate until Close. The `t.wg.Wait()` in Close blocks shutdown. 
   
   **Fix direction:** (1) Replace goroutines with a bounded worker pool + buffered channel (e.g., `sync.Pool` of reusable sender contexts); (2) Attach a tight deadline (e.g., 5s, not `connectTimeout`); (3) Drop on overflow instead of queuing. Test with `TestWebRTCTransport_FireSDPBackpressure` using a stalled signaler.

2. **`pkg/network/network.go` — No eclipse-detection in mesh selector. DHT routing table is the sole source of truth; mesh peer-set is deterministic XOR distance + random subnet picks from DHT. If all K-bucket updates are DHT-reflected, a Sybil can eclipse the overlay the same way it eclipses DHT.**
   
   Design doc (§10) mentions "subnet-diversity — where exactly учитывается?" but the selector implementation in `network.go` (grep `SelectPeers` caller) does not validate that mesh links span independent subnets or ASN-groups. A malicious node in position to control many DHT k-bucket slots can clone itself into the mesh peer-set and intercept all mesh-layer messaging (not just DHT). 
   
   **Fix direction:** (1) If subnet-diversity is Phase 11, add a TODO + design-doc note pinning it explicitly; (2) If it's Phase 10 scope, implement `PeerSelector.SelectPeers` to filter on geolocation / reverse-DNS ASN (rough, but directional) and document the limitation; (3) Minimum: add a `MeshSelector` wrapper that logs when >4 candidates are from the same /24, so operators see the risk.

3. **`pkg/webrtc/peer.go:405–411` — inbox channel is never explicitly closed; data race on Close() → GC reclaim assumption.**
   
   Comment on line 370 admits: "inbox is fed by OnMessage which holds an internal pion lock; we cannot safely close it without coordinating, so let GC reclaim." This is unsound. If a reader does `for msg := range p.inbox`, after Close() the reader goroutine hangs forever waiting for close signal on the channel. The assumption that GC will eventually reclaim the reader violates the contract (a reader on `p.inbox <- x` does not panic on Close, it's an implicit goroutine leak). 
   
   **Fix direction:** Use a separate sentinel channel (`p.inboxClosed`) that readers multiplex on (`select case msg := <-p.inbox; case <-p.inboxClosed`). Or document explicitly that callers MUST NOT `range` over `Recv()` — only use it in a select with a Close-guarded context.

4. **`pkg/presence/record.go` & capability negotiation — CapCanWebRTCMesh bit is published but not validated at handshake.**
   
   A Phase 9 node that does NOT set `CapCanWebRTCMesh` can still receive a mesh offer (InnerMeshOffer arrives as an unknown inner type and is dropped at debug-log, no escalation). But the inverse: a Phase 10 node attempting to mesh with a Phase 9 node will wait on `<-sess.Opened()` indefinitely if the Phase 9 node silently drops the offer. The signaling path should validate that the peer advertises the mesh capability before firing CreateOffer. 
   
   **Fix direction:** In `WebRTCTransport.Connect()` or `dial()`, resolve the peer's presence record and check `cap.Has(presence.CapCanWebRTCMesh)` before starting the handshake. Return a new error (`ErrPeerDoesNotSupportMesh`) so callers know to skip or fall back. Test with Phase 9 peer presence in a fixture.

---

### SUGGESTION

5. **`pkg/transport/webrtc.go:321–356` — Send() acquires mu twice (lookup + error mapping) with no batch-read optimization.**
   
   `Send` locks, reads `t.peers[peer]`, unlocks, then calls `entry.sess.Send(payload)`. If `Send` returns a mapped error, there's a race: by the time we return ErrNoRoute, the session might have been closed by another goroutine. This is benign (the caller treats ErrNoRoute as "try UDP next"), but the double-lock pattern is inefficient under high throughput. Consider: lock once, snapshot `entry`, unlock, then `Send`. Document the semantics clearly in the godoc.

6. **`pkg/network/network.go` — Mesh stack wiring has no explicit dependency ordering in Run().**
   
   `Open()` creates `mesh`, `rtcTransport`, `peerManager` but `Run()` does not specify or enforce an order: `rtcTransport.Run()` must start before `peerManager.Run()` (the manager calls `transport.Connect`, which will hang if `Run` is not pumping inbound). There's no explicit dependency or documentation. If a caller calls `n.Run(ctx)` but it only spins up 2 of 3 components, PeerManager's Connect calls deadlock. 
   
   **Fix direction:** Add explicit Run phase ordering in `Node.Run()` with a comment, or bundle mesh startup into a separate func (e.g., `n.startMesh(ctx)` called before the main loop). Document that MeshEnabled setup must complete before income pump starts.

7. **`pkg/webrtc/peer_manager.go:reconcile()` — Backoff FSM does not distinguish transient (ICE timeout) from permanent (peer no longer in DHT).**
   
   The state machine is binary: `stateDisconnected` → `stateConnecting` → `stateConnected`. When a Connect fails, it increments `failures`, schedules the next attempt, but does not ask "why did it fail?" A peer that left the DHT should be evicted immediately, not retried exponentially. The selector will eventually drop it from the desired set, but until then, we burn backoff delays on a dead peer. 
   
   **Fix direction:** Plumb error codes from `transport.Connect()` (e.g., `ErrContextDeadline` vs `ErrPeerNotFound` vs signaling-side errors). Immediate backoff reset for context-driven timeouts; skip-retry for permanent errors. Alternatively, document the current backoff window (default 64s) is acceptable and close as-is — this is a SUGGESTION, not a blocker.

8. **`pkg/signaling/envelope.go:154–155` — v1 envelope rejection is commented but untested.**
   
   Phase 9 audit rejected v1 envelopes outright (envelopeVersion check), but there's no test case verifying that a v1-format packet triggers ErrEnvelope and doesn't cause a panic. Add a `TestDecode_V1Rejected` with a hand-crafted v1 envelope (no Hops field) to ensure the rejection is explicit.

9. **`pkg/webrtc/peer.go:327–328` — Single buffered-amount-low notification; concurrent Send() can thrash between blocked and free.**
   
   `bufferLow` is a chan struct{} with buffer=1. If two Send() calls race on the same peer and both hit the high-water mark, the first receives the signal, the second misses it and waits for a timeout. This is handled (line 328: timeout returns ErrBufferFull), but it's a rough-edged behavior. Consider documenting that users should not fire parallel Sends to the same peer, or use a semaphore instead of a 1-cap channel.

---

### NIT

10. **`pkg/transport/webrtc.go:328` — Type assertion error message does not show the actual type.**
    
    ```go
    return fmt.Errorf("transport: rtc got %T, want WebRTCAddr", to)
    ```
    
    Would read better as: `"transport: Send called with %T, expected WebRTCAddr"`. Tiny readability win.

11. **`pkg/webrtc/peer_manager.go:109` — `Now` field is called "Now" but is a func; would be clearer as `Clock` or `NowFunc`.**
    
    Following go-style conventions (see CLAUDE.md), time sources should be explicitly named as interfaces. A bare `Now func() time.Time` in a struct is easy to miss. Consider `Clock interface { Now() time.Time }` (borrowed from clockwork) to make the dependency explicit.

12. **`pkg/network/mesh_signaler.go:95–101` — handle() does not validate that the Channel belongs to the expected peer.**
    
    The handler assumes `ch` is the open Channel from `peer`. But if the Service routes an envelope with a spoofed `Sender` hash, the handler's peer-key lookup succeeds anyway. This is mitigated by Noise XK (the peer must know the shared key), but document it in the godoc to clarify the trust boundary.

---

### QUESTION

13. **`pkg/transport/webrtc.go:675–686` — Why not use a buffered channel + worker loop for SDP/ICE dispatch instead of unbounded goroutines?**
    
    The fire-and-forget pattern works, but under Sybil it's a DOS vector. Did you consider a dispatching channel + `sync.Pool` early and reject it for complexity? This would be useful context for future review.

---

### PRAISE

14. **Strict Send semantics (ErrNoRoute).** Refusing to dial on-demand in the hot path is exactly right — keeps the DHT and signaling layers clean. Separating explicit `Connect` (with backoff FSM) from `Send` is a clean API boundary.

15. **Singleflight on Connect.** The `pendingDial` machinery elegantly deduplicates concurrent dials to the same peer without races. Well-designed for the Sybil-flood scenario.

16. **Trickle ICE + fire-and-forget Send.** Using pion's OnICECandidate with trickle instead of waiting for gathering-complete keeps latency low. Good intuition.

17. **Inbox fan-in pattern in compositeTransport.** The dual-transport façade is clean. Dispatch by address type is explicit and testable.

18. **Capability bit forwarding.** The Phase 9 ↔ Phase 10 coexistence via CapCanWebRTCMesh is well-structured. Unknown inner types are logged at debug, not fatal.

---

## Tests

**Strengths:**
- Table-driven unit tests on WebRTCAddr roundtrip, envelope decode/encode.
- fakeMeshTransport lets peer_manager_test run without pion.
- `TestNode_MeshEnabled_StartsAndStops` smoke-tests the wiring.

**Gaps:**
- No test for goroutine leak under fireICE/fireSDP backpressure (blocks BLOCKING #1 fix).
- No test for v1 envelope rejection (SUGGESTION #8).
- No test for CapCanWebRTCMesh validation (blocks BLOCKING #4 fix).
- No test for inbox-channel close semantics (blocks BLOCKING #3 fix).
- PeerManager reconcile FSM missing edge cases: permanent peer removal (SUGGESTION #7), conflicting selector/transport state.
- E2E test (`mesh_e2e_test.go`) is smoke-level; would benefit from heartbeat / drop detection / ICE-restart scenarios.

**-race coverage:** The code uses atomic.Pointer, sync.Mutex, and proper channel discipline. No obvious races detected by manual review, but the goroutine leak (BLOCKING #1) will flood race detector under load.

---

## Out of scope (file as follow-ups)

- Peer reputation / reputation-weighted selection (Phase 11).
- Subnet-diversity enforcement (eclipse hardening); Phase 11 decision expected.
- Mesh-specific observability (latency histograms, drop rates per peer).
- DTLS certificate pinning audit (assumes pion handles it; defer to Phase 11 if needed).
- Connection establishment latency tuning (currently generic 30s timeout).

---

## Verdict: REQUEST CHANGES

**4 BLOCKING issues** prevent merge:
1. Unbounded goroutine spawn on SDP/ICE (resource leak under Sybil).
2. No eclipse detection in mesh selector (trust model gap).
3. inbox channel not closed safely (goroutine leak on Close).
4. CapCanWebRTCMesh not validated at handshake (Phase 9 incompatibility).

**2 critical SUGGESTION items** that should be addressed before Phase 11:
5. Send() double-lock (minor efficiency issue).
6. Mesh run-order dependency not explicit (latent deadlock risk).

Resolve all 4 BLOCKING + the 2 SuGGESTION items with tests, then re-request review on changed code.

