---
name: go-review
description: Use when reviewing Go code — pull requests, branch diffs, individual files, or proposed implementations. Produces a structured review with severity-tagged findings (BLOCKING / SUGGESTION / NIT / PRAISE) and file:line references. Reviews against go-style, go-testing, and CLAUDE.md as the single source of truth — this skill is the "how", those are the "what".
when_to_use: Triggers — "review this PR / diff / file / branch / code"; "audit this implementation"; "look at this Go code"; "feedback on my changes"; "any issues with this?"; "what do you think of this code?"; reading output of git diff, gh pr view, or a *.diff/*.patch file; assessing a proposed function, struct, or package before implementation; pre-merge sanity check; preparing PR comments. Delegates rule lookups to go-style (style/idioms) and go-testing (test quality); use those skills for "what is the rule"; use this skill for "what should I flag and how". Pairs with /ultrareview for multi-agent cloud reviews.
---

# Go code review — process, severity, output format

This skill governs how Go code is reviewed in this project. It deliberately does **not** restate style rules or test rules — those live in:

- **`go-style/SKILL.md`** + `go-style/modern-go.md` — naming, package design, error handling, receivers, concurrency, comments, modern stdlib (Go 1.21–1.26).
- **`go-testing/SKILL.md`** — TDD, table-driven, parallel rules, synctest, testcontainers, suite, fuzz, benchmarks.
- **`CLAUDE.md`** — project structure (`cmd/`/`pkg/`/`internal/`), TDD policy, go.mod baseline.

When this skill says "violates §3 of go-style", read that section. **Don't duplicate rules here** — drift between skills is worse than a one-line cross-reference.

---

## 0. Mission

Per Google's standard: **"Reviewers should favor approving a CL once it is in a state where it definitely improves the overall code health of the system, even if the CL isn't perfect."**

Translation:
- Goal is *code health over time*, not perfect code.
- Block merges only on real defects (correctness, security, irreversible API mistakes, missing tests for new logic, regressions).
- Style/idiom misses are SUGGESTION/NIT — author can ignore NIT freely.
- A review that lists 40 nits and no blockers is a bad review. Find the *important* things first.

---

## 1. Before reading any code — orient

1. **Read the description.** What is the change trying to do? What problem does it solve? If unclear, that's your first finding.
2. **Look at the diff size.** > ~400 lines of non-mechanical change → consider asking the author to split. Large CLs hide bugs.
3. **Identify the scope.** Which packages? `pkg/` (public, stricter) vs `internal/` vs `cmd/` (must stay minimal per `CLAUDE.md`)?
4. **Check the commits.** One logical change per commit? Or a kitchen-sink merge?
5. **Run the diff through your head as a user of the API.** Does the new shape make sense from the call site?

Only after this orientation do you read the implementation.

---

## 2. Review pass order — design first, style last

Google's order, applied here. Don't invert it: it's pointless to nit-pick variable names in code that's architecturally wrong.

### Pass 1 — Design (most important)
- Does the change belong in this codebase? In *this* package?
- Does it respect the `cmd/` → `internal/` → `pkg/` rule (per `CLAUDE.md`)? `pkg/` must not import `internal/`.
- Are new abstractions justified by ≥2 real call sites? Or is this premature interface design?
- Does it match adjacent code's patterns, or does it invent a parallel one for no reason?
- For `pkg/` additions: is the API surface minimal? Could it be a function instead of a type?

### Pass 2 — Correctness & functionality
- Does it actually do what the description says?
- Edge cases: empty input, nil pointers, zero values, max values, concurrent access, cancelled context, partial failure, repeated calls?
- Concurrency: every goroutine has a defined exit path? `ctx` plumbed and honoured? Channels closed by the right side? Mutexes guard the documented fields?
- Error handling: errors wrapped with `%w` and context (per go-style §3)? No silent `_ = err`? No double-log? Are error sentinels and types right (`errors.Is` vs `errors.As`)?
- Resource lifecycle: things opened are closed; goroutines started have visible Stop/Wait; no leaked timers (Go 1.23+ timers don't need draining — per go-style §0).

### Pass 3 — Security & robustness
- Untrusted input: validated at boundary? Length-bounded? Decoded without panic on malformed bytes?
- Crypto: `crypto/rand` (never `math/rand*`)? Constant-time comparison for secrets? No homemade primitives?
- Filesystem: path traversal handled? `os.Root` (1.24) used when sandboxing?
- Logs: no PII, secrets, full tokens, or unbounded user input?
- Time: `time.Now()` injected as `Clock` interface (so tests are deterministic)?
- Panics: do not escape `pkg/` boundary (per go-style §3)?

### Pass 4 — Tests
Apply `go-testing` rules. Specifically:
- New behaviour ↔ new test (TDD). No new public function without test in same CL.
- Table-driven where multiple cases share shape; `t.Parallel()` at both levels; named cases.
- Concurrency tested with `synctest` — flag any `time.Sleep` in tests as a likely flake source.
- Integration tests gated by `//go:build integration`; e2e by `//go:build e2e`.
- testcontainers calls have `WaitStrategy` and `CleanupContainer`.
- Failure messages format: `Func(input) = got, want want` (got first).
- No `t.Fatal` from non-test goroutines.
- Tests don't sleep, don't depend on wall-clock time, don't share mutable state across parallel subtests.

### Pass 5 — Style, idioms, modern Go
Apply `go-style` rules.
- Modernity: `slices`/`maps`/`cmp`/`slog` instead of hand-rolled equivalents (cross-check `modern-go.md`).
- Receivers consistent (all value or all pointer for a type).
- `context.Context` is first parameter, never in struct.
- No `init()` doing real work, no goroutines from constructors.
- Naming: no `util`/`helpers`/`common` packages; no `Get` prefixes; initialism casing uniform.
- Errors: lowercase, no trailing period, wrapped with context.
- Doc comments on every exported identifier, starting with the name.

### Pass 6 — Performance (only when relevant)
- Hot path? Otherwise skip.
- Allocations: pre-sized `make` when `n` is known; `strings.Builder` in loops; `strconv` over `fmt.Sprintf` for primitives.
- `sync.Pool` flagged as suspicious — only after profiling justification.
- Premature `goroutine`/`channel` for trivially-sequential work — push back.

---

## 3. Severity levels — every comment carries one

Use these prefixes consistently. They tell the author what action is required.

| Prefix | Meaning | Author's expected action |
|---|---|---|
| **BLOCKING:** | Bug, security flaw, broken test, API mistake that's costly to reverse later, missing test for new logic. | Must fix before merge. |
| **SUGGESTION:** | Real improvement (clarity, idiom, a real-world risk that's not yet a bug). | Should address; can be deferred to follow-up with explicit acknowledgment. |
| **NIT:** | Style preference, micro-optimisation, alternative phrasing. | Free to ignore. Don't accumulate these — they're noise above ~5 per CL. |
| **QUESTION:** | I don't understand X — explain or fix the code so it explains itself. | Reply, or rewrite the code to be self-explanatory. |
| **PRAISE:** | Good design choice, clever simplification, well-placed test. | None — but include these. They calibrate severity of the rest. |
| **FYI:** | Future consideration not actionable in this CL. | None. |

Rules:
- One severity per comment. Don't write "BLOCKING/SUGGESTION".
- If three or more NITs would land on the same hunk, condense to one SUGGESTION about the pattern.
- A review with zero PRAISE on a non-trivial CL reads as adversarial. Find at least one honest positive.

---

## 4. How to phrase feedback

Per Google's review comments guide:

- **Critique the code, not the author.** "This adds complexity without measurable benefit" — not "Why did you use threads here?".
- **Explain why, not just what.** "Use `errors.Is(err, ErrX)` here — `==` breaks once anyone wraps the error" — not "use errors.Is".
- **Guide, don't solve.** Point out the problem and the principle. Spell out a fix only when the principle is genuinely hard to apply, or when a one-line patch is shorter than the explanation.
- **Prefer concrete file:line references.** `internal/dht/table.go:142 — BLOCKING: …` so the author can jump there.
- **Make the code self-explanatory, not the review.** If you needed a paragraph to understand why this works, the *code* needs the comment, not the PR.

Anti-patterns:
- "Looks good." with nothing behind it. Either you actually reviewed (then say what you checked) or you didn't (then don't approve).
- 30 NITs on naming — bikeshedding. Pick the worst three.
- Asking the author to do a refactor unrelated to the change. File a follow-up issue instead.

---

## 5. Output format — when producing a review

When asked to "review this PR / file / diff", produce a structured report:

```
## Summary
<2-3 sentences: what the change does, overall verdict, biggest risk if any>

## Verdict
APPROVE / APPROVE WITH NITS / REQUEST CHANGES / NEEDS DISCUSSION

## Findings

### BLOCKING
- `internal/dht/table.go:142` — <issue>. <why it's a defect>. <suggested direction>.
- ...

### SUGGESTION
- `pkg/transport/udp.go:88` — ...

### NIT
- ...

### QUESTION
- ...

### PRAISE
- ...

## Tests
<one paragraph: are tests adequate? what's missing? any flake risks?>

## Out of scope (file as follow-ups)
- <things worth doing but not in this CL>
```

Verdict criteria:
- **APPROVE** — no BLOCKING, ≤2 SUGGESTIONs, code health clearly improves.
- **APPROVE WITH NITS** — same as APPROVE plus minor stylistic suggestions; author may merge after addressing or ignoring.
- **REQUEST CHANGES** — at least one BLOCKING, or several SUGGESTIONs that compound into a redesign.
- **NEEDS DISCUSSION** — design pass raised concerns; a short conversation is faster than written back-and-forth. Name what to discuss.

---

## 6. Approval discipline

When to approve:
- Every BLOCKING is resolved (or formally deferred with author's commitment).
- Tests cover new behaviour at the right level (unit by default; integration only when needed).
- The CL leaves the codebase healthier than it found it. Not perfect — *better*.

When **not** to approve:
- You haven't read every changed line.
- You don't understand a non-trivial section. (File it as a QUESTION.)
- A BLOCKING is unresolved.
- New public API in `pkg/` without godoc on every exported identifier.
- New goroutine without a defined exit path.
- Concurrency change without a `synctest` test (or an explanation why one isn't possible).

When to escalate:
- Author and you disagree on a design choice and neither budges after one round.
- The change touches an area with non-obvious historical context — ask someone who was there.
- Don't let a CL sit because of an unresolved disagreement. Schedule the conversation.

---

## 7. Common reviewer anti-patterns — avoid

- **Bikeshedding.** Long debate on names while ignoring a real concurrency bug two hunks down.
- **Scope creep.** Asking for refactors unrelated to the change. The fix: file a follow-up, don't grow this CL.
- **Style without "why".** "Don't use that pattern." Useless. Either link to the rule (go-style §X) or skip the comment.
- **Approving without reading.** Worse than no review.
- **Hostile tone.** "This is wrong" — replace with "This breaks when X. Suggested fix: Y."
- **Demanding perfection.** The standard is *better*, not *flawless*. If the CL improves the codebase, approve and file follow-ups.
- **Reviewing the author, not the code.** Never relevant. Never useful.

---

## 8. Special cases

### Tiny CLs (typo, one-line fix)
Skip Pass 1–3, glance at Pass 5. Approve. Don't perform full ceremony.

### Generated code
Don't review line-by-line. Verify the generator inputs and that the regeneration command is documented. Skip style nits — the generator owns them.

### Mechanical refactors (rename, move, format-only)
Verify the change is mechanical (no behavioural delta). Then approve. Tests should still pass identically.

### Brand-new package in `pkg/`
Higher bar:
- API surface explicitly justified (godoc explains the *why*).
- Tests at >80% coverage of public surface.
- No transitive dependency on `internal/`.
- Doc comment on the package itself (`// Package foo …`).

### Performance-claiming CL
Demand a benchmark in the CL, before/after numbers via `benchstat`. "I think this is faster" is not a measurement.

### Security-touching CL
Slow down. Do Pass 3 twice. Verify there's a test for the malicious input case, not just the happy path.

---

## 9. Quick reviewer checklist

Before clicking approve / writing the review:

- [ ] I read the description and understood the goal.
- [ ] I read every changed line, not just the diff summary.
- [ ] I identified the biggest risk — and either it's mitigated or it's a BLOCKING.
- [ ] Tests exist for new behaviour and target the right level.
- [ ] No `time.Sleep` in tests (use `synctest`).
- [ ] All goroutines started have visible exit paths.
- [ ] All errors wrapped with context (`%w` + info).
- [ ] No new public surface without godoc.
- [ ] Severity prefixes on every comment; PRAISE included where deserved.
- [ ] No drive-by refactor requests; out-of-scope items filed as follow-ups.
- [ ] Verdict matches the findings.
