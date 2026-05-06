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

### DHT routing-table poisoning via unsolicited requests

- **Status:** ✅ mitigated (2026-05-05)
- **Mitigation:** `pkg/dht.Node.handlePacket` follows the canonical
  Kademlia rule: routing-table inserts happen ONLY on response paths
  (PongMsg / NodesMsg / StoreOKMsg / ValueMsg). On request paths the
  sender's chosen `SrcID` is NOT trusted directly — instead
  `maybeProbe` schedules an asynchronous PING to the observed source
  address. The peer is admitted only if the PONG comes back, i.e. the
  liveness proof was elicited by us, not bytes the peer chose to send.
  An in-flight probe set (`probesInFlight`) suppresses probe storms
  from a single requester.
- **Limitation:** `maybeProbe` proves liveness at the source address,
  not identity-binding. A live attacker at IP X who chose `SrcID = Y`
  can still respond to PING with the same `Y`. Closes the *passive*
  attack (replay of captured packets, request-spoofing without UDP
  return-path) and forces *active* attackers to maintain bidirectional
  UDP plus the per-/24 bucket cap. Tighter identity-binding (PoW on
  nodeID) is opt-in via `pkg/identity.GenerateWithPoW`; combine for
  stronger Sybil resistance.
- **Test:** `pkg/dht/node_test.go::TestRouting_DoesNotAdmitSilentRequester`.

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
- **Implemented (2026-05-03):** disjoint-paths lookup
  (`pkg/dht.iterativeFind` with `Config.Disjoint=3` default; shared
  visited-set guarantees no peer is queried by two paths) and
  sibling-list replication (`RoutingTable.Siblings(s)` exposed,
  `PutValue` replicates onto K closest ∪ own siblings).
  Tests: `pkg/dht/skademlia_test.go::TestPutValue_ReplicatesToSiblings`,
  `pkg/dht/skademlia_internal_test.go::TestClaimBatch_*`.

### MITM on signaling

- **Status:** ✅ mitigated
- **Mitigation:** `pkg/webrtc.SignedSDP.Sign/Verify` (Ed25519 over a
  domain-separated tuple of
  `version‖kind‖recipient‖session_id‖issued_at‖sdp`) executed in Go
  before SDP reaches the browser. Signaling channel itself is Noise XK
  end-to-end (`pkg/noise`, `pkg/signaling`), so any tampering forces
  decrypt failure or signature-verify failure. Browser then uses the
  (verified) `a=fingerprint:` to authenticate the DTLS handshake.
- **Channel binding (2026-05-05):** the signed payload now includes the
  intended recipient hash, the signaling SessionID and an IssuedAt
  timestamp. `Verify` rejects any envelope whose recipient or session
  does not match the verifier's expectation, or whose timestamp falls
  outside `MaxClockSkew=5min`. Closes cross-recipient and cross-session
  replay paths flagged in the 2026-05-05 audit.
- **Test:**
  - `pkg/webrtc/signed_sdp_test.go` — sign/verify happy + tamper +
    cross-recipient replay + cross-session replay + stale-timestamp
    replay + within-skew accept.
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
- **Cardinality caps (2026-05-05):** every per-IP bookkeeping map now
  has a hard size cap so an attacker rotating source IPs cannot grow
  the limiter's own state without bound:
  - `pkg/ratelimit.Limiter.MaxKeys` — default 100K, LRU evict on
    insert past the cap.
  - `pkg/dht.MemoryStore.MaxEntries` — default 16K, evict
    nearest-expiry on insert past the cap.
  - `pkg/presence.RateLimitedStore.MaxTrackedIPs` — default 100K,
    fresh IPs past the cap pass through without quota slot.
  - `pkg/signaling.MaxHalfOpenPerIP` — default 8, refuses HELLO_INIT
    over the per-IP responder budget so an INIT-flood cannot pin a
    Noise responder allocation per spoofed SessionID.
  - `pkg/stun` — per-IP token bucket on binding-success responses
    (rate=30, burst=60) closes the 4× reflective-amplification class
    flagged in NETSCOUT 2024.
- **Test:**
  - `pkg/ratelimit/ratelimit_test.go::TestLimiter_BoundedKeyCardinality_EvictsLRU`
  - `pkg/dht/store_test.go::TestMemoryStore_BoundedSize_EvictsNearestExpiry`
  - `pkg/presence/rate_limited_store_test.go::TestRateLimitedStore_TrackedIPsBounded`
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

### Capability spoofing (claim CanSTUN/CanTURN, point at victim's IP)

- **Status:** ✅ mitigated (2026-05-05)
- **Mitigation:** `pkg/network.ICEServers` now sends a STUN binding
  request to every advertised STUN responder before returning it as
  an ICE candidate (`probeSTUNReachable`, default 500 ms per probe,
  parallel across candidates). A peer that signs a record with
  `CapCanSTUN` and points at a victim's IP fails the probe — the
  victim doesn't run STUN — and the candidate is dropped before any
  RTCPeerConnection probes the victim. The signature on the record
  proves *who* claimed the bit, not that the address is reachable;
  the runtime probe binds the claim to actual network behaviour.
- **Test:**
  - `pkg/network/ice_probe_test.go::TestProbeSTUNReachable_AcceptsRealResponder`
  - `TestProbeSTUNReachable_RejectsSilentTarget`
  - `TestProbeSTUNReachable_RejectsClosedPort`

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
  session. Per-IP half-open responder cap (`MaxHalfOpenPerIP=8`) limits
  the cost of forcing fresh-SessionID HELLO_INIT replays. SignedSDP
  channel-binding (`Recipient‖SessionID‖IssuedAt`) prevents cross-session
  and cross-recipient replay of captured offers/answers.
- **Test:** `pkg/signaling/replay_test.go::TestReplay_DataFrameRejected`,
  `TestReplay_DuplicateHelloInitDropped`,
  `pkg/webrtc/signed_sdp_test.go::TestSignedSDP_RejectsCrossSessionReplay`,
  `TestSignedSDP_RejectsCrossRecipientReplay`,
  `TestSignedSDP_RejectsStaleReplay`.

---

## Web UI (HTTP + auth) layer

### DNS-rebinding to loopback API

- **Status:** ✅ mitigated (2026-05-05)
- **Mitigation:** `internal/httpui.Server.checkHost` rejects every
  request whose `Host` header is not in the allowlist
  (`127.0.0.1`, `localhost`, `::1`, configured public host). Closes
  the CVE-2024-28224 (Ollama) / CVE-2025-66414 (MCP TS SDK) class:
  attacker-controlled DNS pointing `evil.attacker.com → 127.0.0.1`
  no longer reaches the loopback API even with a valid token, because
  the Host header carries the attacker's name. Returns 421 Misdirected
  Request per RFC 9110 § 15.5.20.
- **Test:** `internal/httpui/server_test.go::TestHostHeader_RejectsForeignHost`.
- **Companion mitigations already in place:** `Origin` cross-origin
  check on `/login` POST, `SameSite=Strict` on session cookie,
  `X-Requested-With: udisend` custom-header CSRF gate on every
  state-changing endpoint, no CORS configured.

### Login brute-force via tracking-map saturation

- **Status:** ✅ mitigated (2026-05-05)
- **Mitigation:** `internal/httpui/auth.RateLimiter` now fail-closes
  when `MaxTrackedIPs` is hit instead of returning a transient
  `ipState{}` whose mutations were silently discarded. The previous
  behaviour let an attacker fill the tracking map (XFF spoof, IPv6 /64
  churn) and then hammer `/login` from any other fresh IP without
  ever incrementing failure counters. Refusal is self-healing: the
  hourly Cleanup drains stale rows.
- **Test:** `internal/httpui/auth/ratelimit_test.go::TestRateLimiter_FailClosedWhenSaturated`.

### Login TOTP code replay within step

- **Status:** ✅ mitigated (2026-05-05)
- **Mitigation:** `/login` now uses `auth.VerifyTOTPStep` and persists
  the consumed step under `auth.last_totp_step` — same discipline as
  `requireSecondFactor`. A captured TOTP code can no longer be replayed
  on a fresh session within its 30-second window.
- **Test:** `internal/httpui/auth_handlers_test.go::TestLoginPOST_TOTPCodeNotReusableSameStep`.

### Argon2id parameter poisoning (DB-level)

- **Status:** ✅ mitigated (2026-05-05)
- **Mitigation:** `internal/httpui/auth.decodePHC` clamps the parsed
  `m`/`t`/`p` parameters to safe bounds before handing them to
  `argon2.IDKey`. Previously a corrupted (or attacker-poisoned) PHC
  row with `m=4294967295` would crash the next login attempt with
  OOM. Defaults remain `m=64MiB, t=3, p=4` (OWASP 2025 recommendation).
- **Test:** `internal/httpui/auth/password_test.go::TestVerifyPassphrase_RejectsAbsurdParams`.

### Auth token leak via process logs

- **Status:** ✅ mitigated (2026-05-05)
- **Mitigation:** `cmd/messenger/main.go` no longer logs the URL
  containing the bearer token; it logs the bound address and public
  flag instead. The full URL still goes to stdout for the operator's
  eyes (the box-banner) — the attack surface that closed is log
  shippers / systemd journal silently capturing the token.

### TURN shared secret on CLI

- **Status:** ✅ mitigated (2026-05-05)
- **Mitigation:** `cmd/network` reads the TURN shared secret from a
  file (`-turn-secret-file`) or `UDISEND_TURN_SECRET` env var. The
  former CLI flag (`-turn-secret`) is removed: passing a secret on
  the command line exposes it via `ps -ef` and `/proc/<pid>/cmdline`
  to every local user on the host.

### SQLite at-rest exposure

- **Status:** 🟡 partial (2026-05-05)
- **Mitigation:** `internal/storage.Open` chmods the DB file to 0600
  after creation; `cmd/messenger` chmods the storage directory to
  0700. This blocks the casual local-user-snooping path on a
  multi-tenant box. Identity export is already AEAD-protected on a
  per-blob basis (passphrase-derived key, AAD = header‖params‖salt).
- **Limitation:** the on-disk SQLite, the `identity.key` seed, and
  chat-history `body` blobs are stored plaintext within the user's
  home directory. An attacker with full file-access (lost laptop,
  unencrypted backup, root on shared box) reads everything. The
  passphrase Argon2 hash is *not* a meaningful additional barrier
  here — once the disk is fully readable, an offline attacker
  re-derives credentials at leisure.
- **Operator control:** full-disk encryption (LUKS / FileVault /
  BitLocker) is the canonical mitigation for the disk-compromise
  threat. Application-level AEAD over `totp_secret`/`body` would
  only obfuscate without solving the underlying problem (the
  decryption key would still need to live somewhere on the same
  disk or be supplied by the user at every startup).
- **Test:** `internal/storage/store_test.go::TestOpen_ChmodsDBFile_OwnerOnly`.
- **Follow-up:** integrate OS-level secret store (libsecret / KWallet
  / macOS Keychain) for the application master key, OR require a
  decryption passphrase at every messenger startup. Both are
  larger-than-MVP scope; tracked as out-of-MVP. SQLCipher is *not*
  on the path because the project is committed to a CGO-free
  toolchain (modernc.org/sqlite).

---

## WebRTC layer

### Forward secrecy

- **Status:** 🟡 partial
- **Mitigation:** DTLS 1.3 ephemeral keys per session — native browser
  property. No long-term key derives session keys.
- **Limitation (2026-05-05 audit):** the Noise XK signaling session
  has no rekey API. flynn/noise's CipherState exposes a 64-bit nonce
  counter that is not rotated; a session sending past `2^64-1`
  messages would wrap. As a defence-in-depth measure
  `pkg/noise.MaxMessagesPerKey=2^32` triggers `ErrSessionExhausted`
  far below the danger boundary so the operator tears the session
  down and a fresh handshake re-derives keys. Real signaling traffic
  never reaches this — the bound is a hard backstop.
- **Test:** `pkg/noise/noise_test.go::TestEncrypt_RefusesPastMessageBudget`.
- **Follow-up:** implement Noise-spec rekey (derive new chain key,
  swap CipherStates) for genuinely long-lived sessions; not blocking
  for MVP given the 2^32 budget headroom.

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
