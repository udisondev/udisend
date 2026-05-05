# CLI UX overhaul — review iter 1

Branch: `feat/webui-remote-auth`
Scope: `cmd/messenger/` rewrite, `internal/storage` Delete{Setting,AuthCredentials}, `internal/httpui` `Config.TLSConfig`.
Build: `go build ./...` clean. `go vet ./...` clean. `go test ./cmd/messenger/... ./internal/storage/... ./internal/httpui/...` green.

## Summary
20 findings — 3 BLOCKING, 6 SUGGESTION, 8 NIT, 3 PRAISE.

The change implements the planned CLI UX overhaul cleanly at the high level:
subcommand dispatch, profile persistence, embedded autocert. Three real defects
are buried in the details: HSTS is silently disabled in autocert mode (the very
mode that has a public TLS cert), `DeleteAuthCredentials` does not invalidate
existing sessions (so a stolen session survives a "reset" and then validates
against the next `public -enable`), and `enablePublic` produces malformed
URL/PublicHost strings for IPv6 LAN addresses. Plus: the new code is thinly
tested — only `profile.validate`/`isLoopbackBind`/`publicMode` get tests
(192 LOC of test for ~1500 LOC of new production code) — which violates the
project's TDD policy in `CLAUDE.md`.

## BLOCKING

- [BLOCKING] `internal/storage/auth.go:72-87` — `DeleteAuthCredentials` deletes
  `auth_credentials` and `auth_recovery_codes` but **does not delete from
  `auth_sessions`**. Flow: user runs `public -enable example.com` → logs in
  from a browser → browser cookie X is stored as a session row → attacker
  exfiltrates cookie X → user runs `public -reset` → user runs
  `public -enable example.com` again with a new passphrase. The new public
  server's `requireAuth → sessions.Validate(cookie X)` finds the still-live
  row in `auth_sessions` and admits the attacker without the new passphrase.
  Until `Idle` (default 24 h) elapses, reset gives the user **no protection**
  against a previously compromised session. Fix: add
  `DELETE FROM auth_sessions` to the same transaction. Also consider whether
  `auth_log` should be retained (probably yes, but document the choice).
  Add a test that asserts a pre-reset session no longer validates after reset.

- [BLOCKING] `internal/httpui/security_middleware.go:163` — HSTS is gated on
  `s.tlsCert != "" || X-Forwarded-Proto: https`. In `public-autocert` mode
  `cmd/messenger/cmd_run.go:187-188` explicitly sets `httpCfg.TLSCert = ""`
  and `TLSKey = ""` (the cert lives in `TLSConfig.GetCertificate`). Result:
  the only mode that actually serves a real Let's Encrypt cert ships responses
  **without** `Strict-Transport-Security`, defeating the main reason to use
  Let's Encrypt over self-signed. Either also key the HSTS check on
  `s.server.TLSConfig != nil` (or a stored `s.tlsActive bool`), or pass a
  separate flag in `Config` for "TLS is active in-process". Add a unit test
  in `internal/httpui` that hits a handler in autocert-style config and
  asserts the HSTS header is present.

- [BLOCKING] `cmd/messenger/cmd_public.go:152-184` — IPv6 LAN literals are
  malformed. `splitHostPort("[fe80::1]:8443", "8443")` returns
  `host="fe80::1"`, then on line 172 `prof.PublicHost = host + ":" + port` =
  `"fe80::1:8443"`, which is parsed by `net.ParseIP` as the IPv6
  `fe80:0:0:0:0:0:1:8443` (a *different* address). `printEnableSummary`
  emits `https://fe80::1:8443/login` which is an invalid URL — IPv6 in URLs
  needs `[fe80::1]:8443`. The TLS cert generated for "fe80::1:8443" lands
  in DNSNames (not IPAddresses) because `net.ParseIP` happily accepts the
  rewritten form. Fix: detect IPv6 host in `splitHostPort`/`enablePublic`
  and bracket it (`net.JoinHostPort(host, port)`). Mirror in
  `cert.go:53-62` so `IPAddresses` is set for the original IP.
  Add a test for `splitHostPort("[fe80::1]:8443", ...)` and an IPv6-host
  case in `TestProfileValidate`.

## SUGGESTION

- [SUGGESTION] `cmd/messenger/cmd_run.go:109` —
  `if fs.Lookup("trust-proxy") != nil && fs.Lookup("trust-proxy").Value.String() == "true"`
  is bizarre. The flag is always registered (Lookup always returns non-nil),
  and the string comparison is true only when the user passed `--trust-proxy`
  (or `--trust-proxy=true`). Explicit `--trust-proxy=false` is silently
  ignored — there is no way to override a persisted `TrustProxy=true` from
  the CLI. Use `fs.Visit` to detect "user actually set this flag":
  ```go
  trustProxySet := false
  fs.Visit(func(f *flag.Flag) { if f.Name == "trust-proxy" { trustProxySet = true } })
  if trustProxySet { prof.TrustProxy = *trustProxy }
  ```
  Same pattern would clean up the other `if *x != ""` overrides — empty
  string is a valid override for `public-host` (force loopback URL).

- [SUGGESTION] `cmd/messenger/cmd_public.go:101-146` — `resetEverything` is
  not transactional. Order: remove TLS files → `RemoveAll` autocert dir →
  `disablePublic` (deletes 7 settings) → `DeleteAuthCredentials`
  (transaction over creds + recovery codes) → remove recovery-codes.txt.
  If any step fails midway, you've half-reset state. Less critical than the
  session-survival BLOCKING above, but still surprising for a "reset"
  command. At minimum, run the two storage operations
  (`disablePublic` + `DeleteAuthCredentials`) inside a single
  `BeginTx` — or document the "best effort, idempotent" contract on
  `runPublic`'s `-reset` flag and add a regression test that re-running
  `-reset` after a partial failure converges.

- [SUGGESTION] `cmd/messenger/profile.go:192-203` vs.
  `internal/httpui/security_middleware.go:42-56` — two `isLoopbackBind`
  implementations exist and they disagree on `localhost`. The httpui one
  treats `localhost` as loopback; the cmd one does not. Today the CLI never
  generates `localhost`-bound profiles, so the divergence is invisible — but
  that's exactly when divergence becomes a footgun. Lift the function to a
  small `internal/netutil` (or `internal/httpui`) and import it from cmd.
  Pick one definition (the httpui one is more correct).

- [SUGGESTION] `cmd/messenger/cmd_run.go:266-277` `openInBrowser` — when the
  user is in loopback mode the URL contains the auth token, which `xdg-open`
  forwards to the browser. xdg-open passes argv to the browser as a
  command-line arg, so the token leaks into:
  - `ps`/`/proc/<pid>/cmdline` of the launched process (briefly),
  - the browser's session restore,
  - shell history if the user types the URL by hand later.
  Documented as best-effort by the comment, but worth a one-line note
  next to it ("don't use this when the URL contains a secret"). Or read
  the URL path-only (no query) and have the SPA cookie-bind the token on
  first request — out of scope here, FYI follow-up.

- [SUGGESTION] `cmd/messenger/acme.go:25-41` — the ACME challenge listener
  is hardcoded to HTTP-01 on `:80` via `m.HTTPHandler(nil)`. autocert also
  supports TLS-ALPN-01 over `:443` automatically when HTTPHandler is *not*
  called. For an operator on a non-root install (`:80` requires
  CAP_NET_BIND_SERVICE), TLS-ALPN-01 would be the obvious escape hatch.
  Either expose a flag (`--acme-challenge=http01|tls-alpn01`) or document
  that public-autocert mode demands `:80` capability and crashes
  otherwise. The current crash with "permission denied on :80" is correct
  but a footgun.

- [SUGGESTION] `cmd/messenger/*.go` — TDD adherence (`CLAUDE.md` →
  "TDD — обязательно" and "Каждая публичная функция в `pkg/` имеет
  тесты"). New code lands without unit tests for: `splitHostPort`,
  `loadProfile`/`saveProfile` round-trip, `formatSecret`,
  `writeRecoveryCodes` (especially the file-mode-0600 invariant —
  worth a regression test), `generateSelfSignedCert` (parses back as a
  valid cert with the expected SAN), `readCertExpiry`, `renderQR` (round-
  trip via `qr.Decode`), `prompter.readChoice`/`readYesNo` parsing, and
  the `runPublic` orchestration paths (table-test against a temp storage).
  Roadmap §391-410 lists this CL as Phase-8 work, which per `CLAUDE.md`
  triggers the post-phase 3-iteration review — but the **TDD** rule
  applies to each task as it lands, not as a post-hoc cleanup. Most of the
  helpers are pure and trivially testable.

## NIT

- [NIT] `cmd/messenger/qr.go:26` — `(size+2*pad)*1` has a stray `*1`.

- [NIT] `cmd/messenger/qr.go:36` — `for range pad / 2` adds 1 extra blank
  top line on top of the implicit padding rows the main loop already
  emits (rows where `y < pad` produce all-false `black()`). Visually fine
  but doubles the top margin.

- [NIT] `cmd/messenger/qr.go:46-53` — ANSI color escapes are emitted per
  cell. One escape at the start of each line (and one reset at the end)
  would shrink output by ~10× and reduce terminal-side rendering cost.

- [NIT] `cmd/messenger/cmd_status.go:101` — hardcoded `%d/10` for
  recovery codes. `internal/httpui/auth.RecoveryCodeCount` is the source
  of truth; use it.

- [NIT] `cmd/messenger/cmd_run.go:42` — `--bind-http` flag default of
  `""` is documented in the doc comment line 35-37 as
  "empty string means keep persisted" — but `flag.String` default is
  rendered as `""` in `--help`, which reads as "default is empty",
  not "default keeps persisted value". Consider either documenting in
  the flag's help text or using `*string` semantics via a custom
  `flag.Value` so the help is unambiguous.

- [NIT] `cmd/messenger/cmd_public.go:191` —
  `Set a passphrase (≥%d chars, will not echo)\n` is good UX, but the
  Cyrillic ✓ printed elsewhere (`✓ passphrase saved`) won't render on
  legacy Windows consoles. Fine for the project's scope; flag in case
  Windows support matters. Also: mixed Russian/English in user-facing
  strings inside this branch (`✓ public mode disabled` vs. CLAUDE.md's
  "сообщения коммитов на русском или английском по контексту" — fine
  for code comments but pick one for user-facing strings).

- [NIT] `cmd/messenger/cert.go:34` — RSA-2048 is fine but ed25519 produces
  smaller certs and is supported by all modern browsers (and stdlib
  `x509.CreateCertificate` works with ed25519 keys directly). Self-signed
  certs are a niche path; not worth blocking on.

- [NIT] `cmd/messenger/cmd_public.go:312` —
  `_, _ = pr.readLine("Press Enter when you have stored the recovery codes... ")`
  silently swallows errors from a closed stdin (operator hit Ctrl-D).
  Print a hint, or return the error.

## PRAISE

- [PRAISE] `cmd/messenger/prompts.go:19-27` — moving to a single shared
  `bufio.Scanner` over stdin is the right fix for the bug noted in the
  comment (`bufio.NewReader` per prompt drops piped input). The TTY
  fallback path in `readPassphrase` is exactly the right shape: real
  TTY → `term.ReadPassword`, otherwise reuse the scanner.

- [PRAISE] `internal/httpui/security_middleware.go:19-36` — the
  `TLSCert+TLSKey` ⊕ `TLSConfig` mutually-exclusive validation closes
  the obvious foot-gun where both are set and one silently wins. The
  error message names what to do.

- [PRAISE] `internal/storage/auth_test.go:49-85` — the `Delete` test
  asserts both idempotency before any set AND that
  `SetTOTPSecret` post-delete surfaces `ErrCredentialsNotSet`. That's
  the correct invariant to lock in (delete really removes the row,
  not just clears fields).

## Tests

The storage-layer changes (`DeleteSetting`, `DeleteAuthCredentials`) are
properly TDD'd with new failing-test-first cases that verify
idempotency, TOTP-after-delete behavior, and the recovery-codes
sweep. `httpui.Config.TLSConfig` has a validate test path
(implicitly, via `validatePublicModeConfig` — but no positive test
that proves an autocert-style `TLSConfig` actually serves TLS and
emits HSTS).

`cmd/messenger/` is the gap: only `profile.validate`,
`isLoopbackBind`, and `publicMode` are tested. `splitHostPort`,
`loadProfile`/`saveProfile`, `generateSelfSignedCert`,
`readCertExpiry`, `formatSecret`, `writeRecoveryCodes`, the prompt
helpers, and the entire `runRun`/`runPublic`/`runStatus` orchestration
have zero direct coverage. Per CLAUDE.md TDD rule this is non-trivial
debt — most of these are pure functions trivially tested.

No flake risks in the new tests (no `time.Sleep`, no goroutines).

## Out of scope (file as follow-ups)

- Switch ACME challenge type to TLS-ALPN-01 by default; it's the simpler
  path for users who don't have `:80` capability.
- Replace `xdg-open <url-with-token>` with a minimal launcher that puts
  the token in stdin or a temp file, so it doesn't hit `argv`.
- Document Windows/Linux/macOS `defaultStorageDir` paths in `--help` so
  users know where their config lives without reading the source.
- IPv6-aware `lan-ip` mode (bind to `[::]` when host is IPv6).
