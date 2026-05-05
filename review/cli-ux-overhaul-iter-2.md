# CLI UX overhaul — review iter 2

## Scope

All working-tree changes vs HEAD on branch `feat/webui-remote-auth`:

- `cmd/messenger/{main,cmd_run,cmd_public,cmd_status,profile,prompts,cert,acme,qr}.go` (+ `profile_test.go`)
- `internal/storage/{app_settings,auth}.go` and tests
- `internal/httpui/{server,security_middleware}.go` (added `Config.TLSConfig`)
- `go.mod` / `go.sum` (+`rsc.io/qr v0.2.0`)

Build green. `go vet ./...` clean. `go test -race ./cmd/messenger/... ./internal/storage/... ./internal/httpui/...` passes.

## Summary

20 findings — 4 BLOCKING, 8 SUGGESTION, 6 NIT, 2 PRAISE.

The biggest risks are concentrated in the `public-proxy` and `public-autocert` modes:
runtime validation forbids the very bind the CLI configures for `-via-proxy`, the
"reset" flow leaks live sessions, and the HSTS header silently disappears whenever
TLS is driven via `Config.TLSConfig` (i.e. autocert). The CLI overrides flag
detection in `cmd_run.go` is also broken. The lan-ip happy path and loopback
zero-config path both work and are well-structured.

## Verdict

REQUEST CHANGES — multiple BLOCKING items must be fixed before merge.

---

## BLOCKING

- **`cmd/messenger/cmd_public.go:165` — `-via-proxy` mode is broken on first run.**
  `enablePublic` writes `prof.BindHTTP = "127.0.0.1:" + port` for the public-proxy case. `udisend run` then loads that profile, sets `httpui.Config.Public = true` (because `prof.publicMode()` is true) and `Listen = "127.0.0.1:8443"`. Inside `internal/httpui/security_middleware.go:23`, `validatePublicModeConfig` rejects loopback bind in public mode unconditionally:
  ```
  if isLoopbackBind(cfg.Listen) {
      return errors.New("httpui: public mode requires a non-loopback listen address")
  }
  ```
  Result: every `udisend run` after `udisend public -enable -via-proxy <addr>` fails with `"httpui: public mode requires a non-loopback listen address"`. The roadmap acceptance smoke-test only exercises lan-ip and loopback, so this regressed silently. Fix direction: relax `validatePublicModeConfig` to allow loopback bind when `cfg.TrustProxy` is true (the proxy is the intended ingress and the bind is intentionally loopback to keep the upstream private), or have `enablePublic` bind to `0.0.0.0` and document that the proxy must restrict ingress. The first option matches the documented design ("proxy in front terminates TLS").

- **`internal/storage/auth.go:72` — `DeleteAuthCredentials` does not invalidate live sessions.**
  `resetEverything` advertises "wipe auth + profile" and the CLI prints `✓ everything cleared`. But `DeleteAuthCredentials` only touches `auth_credentials` and `auth_recovery_codes`. Rows in `auth_sessions` survive untouched. `auth.Sessions.Validate` (`internal/httpui/auth/session.go:88`) only checks that the session ID exists in `auth_sessions` — not that credentials still exist. Therefore: if a browser tab still holds a session cookie issued before reset, the operator runs `public -reset && public -enable …`, and that browser will continue to authenticate against the new credentials (it never had to type the new passphrase). The audit log row will say "session opened … pre-reset". Fix: add `DELETE FROM auth_sessions` to the same transaction (or call `DeleteAuthSessionsExcept(ctx, "")` from `resetEverything` after `DeleteAuthCredentials`). Either way, add a test to `auth_test.go` that creates a session, calls `DeleteAuthCredentials`, and asserts `GetAuthSession` returns nil.

- **`internal/httpui/security_middleware.go:163` — HSTS is dropped in autocert mode.**
  The HSTS guard is `s.publicMode && (s.tlsCert != "" || strings.HasPrefix(r.Header.Get("X-Forwarded-Proto"), "https"))`. In `public-autocert` mode `cmd_run.go:187` explicitly clears `httpCfg.TLSCert = ""` and supplies `Config.TLSConfig` instead; the `Server` struct has no `tlsConfig` field tracking that path, so `s.tlsCert` is empty. There is no upstream proxy adding `X-Forwarded-Proto`. Result: real-world deployments of `public-autocert` over HTTPS get the security headers minus HSTS. Browser users have a degraded baseline (downgrade attacks via http://, no inclusion in HSTS preload). Fix: add an `s.tls bool` set when either `tlsCert != ""` *or* a `TLSConfig` was provided, and use it in the HSTS check (and update the comment in `secureHeaders` accordingly). Add a test for both inline-TLS and TLSConfig paths.

- **`cmd/messenger/cmd_run.go:109` — "trust-proxy" override never overrides.**
  ```go
  if fs.Lookup("trust-proxy") != nil && fs.Lookup("trust-proxy").Value.String() == "true" {
      prof.TrustProxy = *trustProxy
  }
  ```
  `fs.Lookup("trust-proxy")` is always non-nil (the flag is registered above). The string check evaluates to whatever `*trustProxy` is, so the whole conditional reduces to `if *trustProxy { prof.TrustProxy = *trustProxy }` — i.e. only sets to `true`, and only when the user passed `-trust-proxy`. Concrete consequences: (1) `udisend run -trust-proxy=false` against a public-proxy profile cannot turn off the persisted `TrustProxy=true` (the override is skipped). (2) The intent (per the comment in cmd_run.go: "Empty string means 'keep persisted'; this lets us flip individual fields without rewriting the profile") is to treat *unset* as keep-persisted, not *false-by-default*. This is the standard `flag.Visit` use case. Fix:
  ```go
  visited := map[string]bool{}
  fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
  if visited["trust-proxy"] { prof.TrustProxy = *trustProxy }
  ```
  Apply the same to every other override block in `cmd_run.go:94-110` — currently the boolean rule is wrong, and the string rule cannot distinguish "user passed empty string" from "user did not pass" (rare in practice but principled).

---

## SUGGESTION

- **`cmd/messenger/cmd_public.go:172` — IPv6 address rendering is broken.**
  `prof.PublicHost = host + ":" + port` produces `2001:db8::1:8443` for an IPv6 input — invalid as a URL authority and ambiguous to anyone reading it. The output of `printEnableSummary` then says `URL: https://2001:db8::1:8443/login`, which a browser will reject. Use `net.JoinHostPort(host, port)` everywhere a host:port pair is constructed (also `splitHostPort` itself trims whitespace but does not bracket-strip — minor). The CLI nominally only documents IPv4 + DNS, so this isn't a hot path, but it's a gratis fix.

- **`cmd/messenger/cmd_public.go:152-184` — re-running `-enable` does not surface that TOTP is already enrolled.**
  If an operator runs `public -enable` a second time with an existing TOTP secret and answers "no" to the `Enable TOTP second factor?` prompt, the *old* secret stays in place silently. The operator is reasonably likely to have intended "TOTP off" — they're just re-running the wizard. Either explicitly warn ("TOTP is currently enrolled; declining will keep it") or call `auth.ResetTOTP` when the answer is no.

- **`internal/httpui/server.go:252-257` — TLSConfig is silently overridden.**
  ```go
  if cfg.TLSCert != "" && cfg.TLSKey != "" {
      s.server.TLSConfig = hardenedTLSConfig()
  }
  if cfg.TLSConfig != nil {
      s.server.TLSConfig = cfg.TLSConfig
  }
  ```
  `validatePublicModeConfig` already rejects setting both, so the second `if` overriding the first is unreachable unless `cfg.Public == false`. In loopback mode neither path is exercised, so the dead branch doesn't matter — but the structure is brittle. Make the relationship explicit: an `else if cfg.TLSConfig != nil` (or a single `switch`) is one line and removes the "second wins" coincidence.

- **`cmd/messenger/cmd_public.go:152` — `enablePublic` is monolithic and untested.**
  The function does address parsing, profile construction, passphrase prompting, TOTP enrollment, cert generation, validation, and persistence — six concerns in 80 lines. None of these branches has a unit test (no `cmd_public_test.go`). The interactive bits are admittedly hard, but `splitHostPort`, the address-classification switch, and `printEnableSummary` are pure functions and trivial to cover. Refactor + tests would catch the IPv6 bug above (and would have caught the BLOCKING `via-proxy` bind defect).

- **`cmd/messenger/cmd_run.go:122-133` — log-level lookup swallows storage errors.**
  ```go
  if v, ok, _ := store.GetSetting(ctx, "log.level"); ok { … }
  ```
  Discarding the SQLite error means a corrupt DB silently falls back to whatever `-v` chose, with no signal in the logs. At minimum log it (`logger.Warn("read log.level", "err", err)`); the logger is already constructed by this point.

- **`cmd/messenger/profile.go:192` vs `internal/httpui/security_middleware.go:42` — two different `isLoopbackBind` implementations.**
  The httpui one treats `localhost` as loopback; the profile one does not. So `udisend run -bind-http localhost:8443` on a loopback profile passes the httpui validator but is rejected by `profile.validate` ("loopback mode requires loopback bind_http"). Pick one definition (the httpui version is more correct: `localhost` is a loopback name) and share it via a small helper. Drift will only get worse.

- **`internal/httpui/server.go:109-119` — no test exercises `Config.TLSConfig`.**
  The new field has no positive test (does ServeTLS actually pick up GetCertificate?), no exclusion test (are `TLSCert+TLSKey` and `TLSConfig` actually rejected together?), and no test for the HSTS interaction discussed in the BLOCKING above. Adding the validator-level test is one cheap table case in `security_middleware_test.go`. Per CLAUDE.md ("каждая фича пишется начиная с теста"), this is the gap.

- **`cmd/messenger/cmd_public.go:336` — recovery-codes file `os.OpenFile(..., O_TRUNC, 0o600)` does not enforce 0o600 on existing files.**
  `OpenFile` only applies the mode bits on file creation. If a previous run wrote the file as 0o600 it's fine; if anything (operator chmod, previous bug, OS umask peculiarity) widened it, the new write inherits the old perms. Cheap belt-and-suspenders: `os.Chmod(path, 0o600)` after Close, or do a `Remove + OpenFile(O_CREATE|O_EXCL)` dance. Combined with the fact that the file is silently truncated on re-enroll without any warning, the operator who used `tee log.txt` to capture output can't tell the file was rewritten.

---

## NIT

- **`cmd/messenger/qr.go:26` — `(size+2*pad)*1` has a no-op multiplier.** Refactor leftover. Drop `*1`.

- **`cmd/messenger/qr.go:36` — only top quiet zone is rendered, not bottom.**
  `for range pad / 2` writes one blank top line; the bottom relies on the loop adding `pad` rows of "not-black" cells (which are the colored-space cells), which scanners may or may not accept as a quiet zone. Symmetric padding would be more robust and is one extra `for` after the main loop.

- **`cmd/messenger/qr.go:46-53` — every cell carries an SGR escape pair.**
  A 33×33 QR generates ~33×33×2 = ~2 KiB of escape codes per render. A terminal that doesn't honor SGR (CI logs, file redirection) shows raw escape bytes. Set the SGR once before the QR and reset once after.

- **`cmd/messenger/prompts.go:65` — `readChoice` and `readDefault` are dead code.** Neither is referenced anywhere outside `prompts.go` itself (`readDefault` is called only by `readChoice`). Remove or wire them in.

- **`cmd/messenger/cmd_public.go:182` — silent override of user-provided port in autocert mode.**
  `udisend public -enable udisend.example.com:8443` parses host+port, then ignores port and binds 443. The comment acknowledges this but the user gets no warning. A printed `note: autocert mode forces :443 (ACME HTTP-01 needs :80) — your :8443 was ignored` line would defuse the surprise.

- **`cmd/messenger/cmd_public.go:369` — `openStore` returns `context.Background()`.**
  Long-lived `udisend public -enable` flow (Argon2id hashing alone is ~100 ms; TOTP prompt is human-typing) runs under a context that never cancels on Ctrl-C. SQLite ops will continue past Ctrl-C until the process is killed. Cheap upgrade: `signal.NotifyContext` like `cmd_run.go` does.

---

## PRAISE

- **`cmd/messenger/profile.go:130-179` — `validate` is excellent.**
  Mode-by-mode required/forbidden field shape is explicit, error messages name the offending field, and the table-driven test in `profile_test.go` covers all the modes plus the negative cases. This is the kind of cross-field validation that's usually tangled in callers; centralising it pays for itself.

- **`internal/storage/auth.go:72` — `DeleteAuthCredentials` is properly transactional.**
  Wrapping the credentials + recovery-codes wipe in a single transaction is the right call — a partial wipe (passphrase gone, codes still good) would leave a re-enrollable but un-resettable account. The accompanying test in `auth_test.go:49` covers idempotency-before-set, full-wipe, and the post-condition that `SetTOTPSecret` then surfaces `ErrCredentialsNotSet`. Note it's incomplete vs. sessions (BLOCKING above), but the in-scope behaviour is exactly right.

---

## Tests

- New tests cover `DeleteSetting` (idempotent + standard), `DeleteAuthCredentials` (idempotent + post-condition + error mirroring), and `profile.validate` (13 cases) + `isLoopbackBind` + `publicMode`. All table-driven, all `t.Parallel()`, no `time.Sleep`. Good.
- Gaps the BLOCKING/SUGGESTION items already enumerate: TLSConfig in `httpui.Config` (validation + ServeTLS path + HSTS), session invalidation on credential delete, IPv6 host rendering, public-proxy bind reachability, trust-proxy override semantics. Each is a 5–15-line table case.
- No new fuzz / property tests; this slice doesn't really demand them — wire formats and crypto live elsewhere.

## Out of scope (file as follow-ups)

- Audit-log entries for CLI auth state changes (passphrase set, TOTP enroll, reset, profile change). Currently every audit row originates from HTTP handlers; the CLI is invisible to anyone reviewing `auth_log` later.
- `loadProfile` does seven sequential `GetSetting` calls; a single `SELECT key, value FROM app_settings WHERE key IN (…)` is cheaper and atomic, but only matters if concurrency becomes real.
- ROADMAP.md says `udisend public -reset` "стирает auth (`status` показывает passphrase=not set)"; once the BLOCKING session-wipe lands, update the line to mention sessions explicitly.
