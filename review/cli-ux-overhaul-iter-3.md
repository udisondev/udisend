# CLI UX overhaul — review iter 3

## Summary
Branch `feat/webui-remote-auth` introduces three CLI subcommands (`run`, `public`, `status`), an embedded autocert flow for public-DNS deployments, profile persistence in `app_settings`, two new storage helpers (`DeleteSetting`, `DeleteAuthCredentials`), a `tls.Config` plumb-through in `httpui`, self-signed cert generation, and a unified prompter. Build clean, tests pass, `go vet` quiet. Overall the design is solid — orthogonal subcommand surface, idempotent teardown, mode-driven validate. Most findings concentrate on three areas: (1) `public -reset` does not wipe `auth_sessions` rows, leaving live cookies pointing at deleted credentials; (2) `cmd_run.go` ignores Ctrl-C during long IO and leaks the browser-open child; (3) the `--trust-proxy=false` override is unreachable because of how flag-presence is detected.

10 findings — 3 BLOCKING, 4 SUGGESTION, 2 NIT, 1 PRAISE.

## BLOCKING

- **`internal/storage/auth.go:72-87`** — `DeleteAuthCredentials` deletes `auth_credentials` and `auth_recovery_codes` but NOT `auth_sessions`. After `udisend public -reset` any browser tab that was already authenticated retains a valid session cookie, and `requireAuth` (server.go:401) will keep serving snapshots, history, contact lists, even let the user change settings — the row in `auth_sessions` is still there, so `Validate()` returns a non-nil session. This is the worst kind of state divergence: the operator believes they have wiped credentials, but anyone with a stolen cookie keeps API access until the cookie expires (idle TTL). Add `DELETE FROM auth_sessions` to the same transaction. Tests must cover: set passphrase → CreateAuthSession → DeleteAuthCredentials → GetAuthSession returns nil. The auth_log table arguably wants to retain the audit trail, so leaving it alone is defensible — but sessions must go.

- **`cmd/messenger/cmd_public.go:369` + `cmd/messenger/cmd_status.go:25`** — both subcommands build `context.Background()` and never wire `signal.NotifyContext`. `enablePublic` does interactive prompts AND a synchronous `auth.SetPassphrase` (Argon2id, 64 MiB / 3 iter — ~1s on a slow box) AND opens an SQLite transaction. If the user hits Ctrl-C during the passphrase prompt or the Argon2 hash, the prompt is interrupted but the SQLite Argon2 + `SetPassphrase` call cannot be cancelled (no ctx propagation actually exists in the flow because the ctx is `Background()`). This is the same shape of bug `cmd_run.go` correctly avoids with `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`. Use a signal-aware ctx in `openStore` and pass it through; without it, an operator who panics mid-enable can corrupt the db's WAL and is forced to `kill -9`.

- **`cmd/messenger/cmd_run.go:109-111`** — `--trust-proxy=false` cannot override a persisted `trust_proxy=true`. The "was the flag set?" detection is `fs.Lookup("trust-proxy").Value.String() == "true"` which is *the value*, not whether the user passed the flag. So passing `-trust-proxy=false` reads as "not set" and the persisted true wins. The roadmap explicitly lists this as an override flag for ops; setting it false on the CLI is a real path (e.g. operator wants to test with TLSCert/TLSKey while a previous profile said proxy). The idiom for "was this flag passed?" is `fs.Visit` populating a set, e.g. `seen := map[string]bool{}; fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })`, then `if seen["trust-proxy"] { prof.TrustProxy = *trustProxy }`. The same shape applies if anyone later wants `--open=false` to override a hypothetical persisted preference.

## SUGGESTION

- **`cmd/messenger/cmd_run.go:266-277`** — `openInBrowser` returns immediately after `cmd.Start()` and never `Wait`s or `Release()`s. On Linux the child's exit becomes a zombie until messenger exits (or SIGCHLD reaping by Go runtime — non-deterministic). `xdg-open` typically forks-and-exits quickly but with `darwin`/`open -a` and Windows `rundll32` the parent process can linger longer. Either `go cmd.Wait()` in a fire-and-forget goroutine, or `cmd.Process.Release()` after Start. The leaked child is bounded but it's a clean idiom miss in code that already shows discipline elsewhere (errgroup, signal-aware ctx).

- **`cmd/messenger/cmd_public.go:188-220`** — order of state changes in `enablePublic` saves the passphrase + (optional) TOTP + (optional) self-signed cert BEFORE saving the deployment profile. If `generateSelfSignedCert` fails after `SetPassphrase` succeeded, the operator now has new auth credentials but no profile, so `udisend run` falls back to loopback — and the credentials sit unused but persisted. Idempotent re-run recovers, but the "atomicity" promise the comment at line 152 makes is not delivered. Either save the profile last AND have the validate run before any side effect, or wrap the whole enable in a sqlite tx alongside cert-write. Acceptable to defer with a note that the failure mode is rare and recovery is just running `-enable` again.

- **`cmd/messenger/cmd_public.go:140-141`** — `os.Remove(filepath.Join(storageDir, "recovery-codes.txt"))` is best-effort but a left-over recovery file after `-reset` is a security wart (operator's recovery codes for a now-deleted account sit on disk). Worth checking the error and at least logging on anything other than `os.IsNotExist`. Same for the other `_ = os.Remove`/`os.RemoveAll` calls in this function — silent failure on cert/key cleanup means status will misreport.

- **`cmd/messenger/cmd_public.go:80-95`** — `disablePublic` does seven separate `DeleteSetting` round-trips. Each is idempotent and cheap individually, but if the first three succeed and the fourth errors (db locked, transient), the on-disk profile is a torn half-state: mode key gone (so `loadProfile` sees "no profile" → loopback), but `bind_http`/`bind_p2p`/`public_host` persist as orphans. Wrap in a single tx, or at minimum delete `deploy.mode` LAST so a partial failure leaves the mode-and-fields intact. Today the worst case is harmless (validate would handle nil profile), but the same pattern in any future schema is a bug waiting.

## NIT

- **`cmd/messenger/prompts.go:65-83`** — `readChoice` is dead code; never invoked anywhere in `cmd/messenger/*.go`. Drop or use it. Per CLAUDE.md "no abstractions without a real consumer."

- **`cmd/messenger/profile.go:94`** — `trustProxy, _ := strconv.ParseBool(trustProxyStr)` silently maps any unparseable persisted value to false. Cheap to log a warning when the stored value is non-empty and non-parseable; today a database hand-edit typo is silently ignored.

## PRAISE

- **`cmd/messenger/profile.go:130-182`** — `validate` is exemplary: every mode's required-vs-forbidden field shape is enforced with a clear error message that names the offending key, and the test in `profile_test.go` covers all four modes through both happy and at least one rejection path each (13 cases). This is exactly the level of cross-field invariant coverage that blocks classes of mis-config bugs. The `hasCreds bool` parameter wiring keeps the storage dependency at the call site, where it belongs, instead of the validator reaching into the store.

## Out-of-scope follow-ups

- Add a CLI audit-log entry to `auth_log` on `public -enable`/`-disable`/`-reset` (event="cli_enable" etc., remote_ip="local"). Today these state changes are invisible to the in-UI security panel.
- Consider validating that `public -enable <DNS>` does not silently override a user-supplied `-port` value — current behaviour forces 443/80 with no warning printed.
