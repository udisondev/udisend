# Threat-model audit — udisend

> Companion to `p2p-messenger-design.md` §8. Each attack from the design
> doc gets a row with: **status** (✅ mitigated / 🟡 partial / 🔴 accepted
> risk / 🌙 deferred), the **mitigation** (code path or protocol property),
> and a **test/reference** so future reviewers can replay the check.
>
> Update this file in the same commit that introduces or removes any
> mitigation.

---

## Network layer

### Eclipse attack on DHT

- **Status:** 🟡 partial
- **Mitigation:** `internal/storage.SeenPeersDiverse` reseeds bootstrap
  preferring distinct /24 (IPv4) / /64 (IPv6) prefixes when CLI bootstrap
  is empty (`internal/messenger.Open`). Hardcoded curated bootstrap list
  and DNS-seeds remain pre-release work — without them an attacker who
  controls the *first* seed (e.g. demo defaults) can still eclipse.
- **Test:** `internal/storage/diverse_peers_test.go`.
- **Follow-up:** ship a community-curated bootstrap list across diverse
  ASNs/operators before public release.

### Sybil on presence

- **Status:** ✅ mitigated (multi-layer)
- **Mitigation:**
  1. Every record signed Ed25519; signature checked on read
     (`pkg/presence.Record.Verify`).
  2. `pkg/presence.RateLimitedStore` caps records per source IP
     (default 16) — wraps the network node's DHT store.
  3. `pkg/identity.GenerateWithPoW` raises the cost of identity-mass-
     production via S/Kademlia-style leading-zero bits on the
     destination hash. Opt-in; deployers choose difficulty.
- **Test:**
  - `pkg/presence/rate_limited_store_test.go::TestRateLimitedStore_RejectsAfterLimit`
  - `pkg/identity/pow_test.go::TestGenerateWithPoW_FindsSeedForLowDifficulty`
- **Deferred:** disjoint-paths lookup, sibling-list replication
  (require DHT algorithm changes; tracked under "S/Kademlia hardening").

### MITM on signaling

- **Status:** ✅ mitigated
- **Mitigation:** `pkg/webrtc.SignedSDP.Sign/Verify` (Ed25519 over
  `kind‖sdp`) executed in Go before SDP reaches the browser. Signaling
  channel itself is Noise XK end-to-end (`pkg/noise`, `pkg/signaling`),
  so any tampering forces decrypt failure or signature-verify failure.
  Browser then uses the (verified) `a=fingerprint:` to authenticate the
  DTLS handshake.
- **Test:**
  - `pkg/webrtc/signed_sdp_test.go` — sign/verify happy + tamper.
  - `pkg/signaling/signaling_test.go::TestService_ConnectExchange` —
    end-to-end Noise XK exchange.

### MITM on first contact (TOFU)

- **Status:** ✅ mitigated (with user discipline)
- **Mitigation:** `internal/storage.UpsertContact` returns
  `ErrFingerprintChanged` on key change. Browser modal shows both
  safety numbers side by side and asks the user to compare out-of-band
  before marking verified (`internal/httpui/assets/app.js` →
  `showVerifyModal`). QR-code scan is post-MVP (typed safety number is
  the only OOB channel today).
- **Test:** `internal/storage/store_test.go::TestUpsertContact_FingerprintChange`.
- **Follow-up:** QR-code + auto-verify if camera scan matches.

### DoS on a node

- **Status:** ✅ mitigated
- **Mitigation:** `pkg/dht.Node` runs every inbound packet through a
  per-source-IP token bucket (`pkg/ratelimit`, default 100 tok/s, burst
  200). Signaling envelopes share the socket so they inherit the cap.
  `pkg/turn` enforces a separate per-IP limiter on AUTH attempts (5
  tok/s, burst 10).
- **Test:**
  - `pkg/ratelimit/ratelimit_test.go` — TokenBucket primitive.
  - `pkg/turn/turn_test.go::TestServer_RejectsExpiredCredential` covers
    the auth path; rate-limit interaction is unit-tested in ratelimit.

### Spoofed presence record

- **Status:** ✅ mitigated
- **Mitigation:** `Record.Sign/Verify` (Ed25519, signing-bytes cover
  Public, Address, Capabilities, IssuedAt, UptimeHint, MaxRelaySlots).
  Receivers reject any record whose signature does not match the
  embedded public identity.
- **Test:** `pkg/presence/record_test.go`,
  `pkg/presence/record_v2_test.go::TestRecord_V2RoundtripUptimeAndSlots`.

### Traffic analysis on signaling relays

- **Status:** 🌙 deferred (post-MVP)
- **Mitigation:** none in MVP. Relay can see `(sender_hash → recipient_hash,
  envelope size, timing)` but not content.
- **Plan:** Onion routing for signaling — listed in design.md §13
  open questions.

### TURN relay intercepting media

- **Status:** ✅ mitigated by protocol
- **Mitigation:** WebRTC media is SRTP/DTLS end-to-end between peers;
  TURN relay only sees encrypted bytes, has no decryption material.
- **Reference:** RFC 8825 §6 — TURN does not see plaintext when DTLS is
  used.

### Compromised volunteer node

- **Status:** ✅ mitigated
- **Mitigation:** Signaling envelopes are end-to-end Noise XK between A
  and B; intermediate relays cannot decrypt. Worst a malicious volunteer
  can do is refuse to relay (denial-of-service for that path) — peers
  pick another. With per-IP rate-limiting and presence Sybil cap, a
  single volunteer cannot starve the network.
- **Test:** `pkg/signaling/relay_test.go::TestRelay_ForwardsToNextHop`
  + Noise XK guarantees from `pkg/noise/noise_test.go`.

### Replay attack (signaling + chat)

- **Status:** ✅ mitigated
- **Mitigation:** Noise XK AEAD nonce counter rejects repeated
  ciphertext; signaling.Service drops duplicate `HELLO_INIT` for a known
  session.
- **Test:** `pkg/signaling/replay_test.go::TestReplay_DataFrameRejected`,
  `TestReplay_DuplicateHelloInitDropped`.

---

## WebRTC layer

### Forward secrecy

- **Status:** ✅ mitigated
- **Mitigation:** DTLS 1.3 ephemeral keys per session — native browser
  property. No long-term key derives session keys.

### Identity binding

- **Status:** ✅ mitigated (see "MITM on signaling" above).

### Replay (WebRTC handshake)

- **Status:** ✅ mitigated
- **Mitigation:** DTLS handshake includes nonce + Random per side (RFC
  6347). SignedSDP is single-use per offer/answer.

### DataChannel / MediaTrack confidentiality

- **Status:** ✅ mitigated
- **Mitigation:** SRTP/DCEP encrypted by DTLS-derived keys. Browser
  enforces.

---

## Production / release-time gaps

The following items are NOT mitigations of specific attacks but are
expected to land before any public deploy:

- **Reproducible builds + signed releases** — see Phase 8 task list.
- **Threat-model regression CI** — every release should re-run the
  cross-references in this file (a `task threat-model-check` target is
  TODO).
- **Real bootstrap/DNS-seed addresses** — currently the demo expects
  `--bootstrap`; without a curated list a fresh client cannot reach the
  network.
- **Onion routing for signaling** — design.md §13 open question.

---

## How this file is used

1. Before merging any change that touches a security-relevant path,
   verify the relevant rows above still hold.
2. When adding a new attack class to design.md §8, add a row here in
   the same commit. Empty rows are easier to spot than missing rows.
3. Update `review/PENDING.md` when an item moves between 🌙/🟡/✅.
