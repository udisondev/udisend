---
name: go-style
description: Use when writing, editing, refactoring, or reviewing Go code — .go files, package design, error handling, interfaces, generics, receivers, concurrency, performance, comments. Modernised for Go 1.21–1.26 (slices/maps/cmp/slog/synctest/PGO/green-tea-GC). Includes Package-Oriented Design (Kennedy) and Standard Package Layout (Johnson). Synthesises CockroachDB, Uber, Google, HashiCorp, Effective Go, Cheney, Gryski.
when_to_use: Triggers — implementing a Go function/struct/package; fixing a Go bug or perf regression; adding code under cmd/, pkg/, or internal/; reviewing a Go diff; choosing a Go idiom (slices vs loop, slog vs log, errors.Is vs ==, pointer vs value receiver); modernising pre-1.21 Go to 1.26; deciding naming, package boundaries, error semantics, or dependency direction; profiling or optimising hot paths (pprof, benchstat, escape analysis, sync.Pool, GC tuning, PGO); designing the package layer for a new feature. Supporting files alongside SKILL.md — modern-go.md (1.21–1.26 stdlib reference), performance.md (benchmarking, profiling, GC, PGO, allocations). Test-specific guidance lives in the go-testing skill.
---

# Go Style — synthesized from CockroachDB, Uber, Google, HashiCorp, Effective Go (modernised for Go 1.21–1.26)

This skill encodes the rules I follow when writing Go in this project. It complements `CLAUDE.md` (project structure and TDD policy) and the `go-testing` skill — apply together.

When the source style guides disagree, I pick the rule that fits this project (see §16). When they agree, the rule is non-negotiable.

This project targets **Go 1.26**. Modern idioms are the default. For the full version-by-version reference (what stdlib package replaces which hand-rolled pattern, what was retired), read **`modern-go.md`** in this skill directory — load it whenever you reach for a pre-1.21 pattern, or when picking between a stdlib helper and a manual loop.

---

## 0. Modern Go baseline (Go 1.21–1.26) — quick reference

Full reference: **`modern-go.md`** (load on demand). Always-applicable highlights:

- **Built-ins:** `min` / `max` / `clear`; `for i := range N`; `new(expr)` (1.26).
- **Stdlib over hand-rolled:** `slices`, `maps`, `cmp`, `log/slog`, `math/rand/v2`, `sync.OnceValue`, `sync.WaitGroup.Go` (1.25), `errors.AsType[T]` (1.26), `context.AfterFunc` / `WithoutCancel` / `WithTimeoutCause`, `runtime.AddCleanup` (1.24, replaces `SetFinalizer`).
- **Iterators (1.23):** return `iter.Seq[V]` / `iter.Seq2[K,V]` for streaming producers; consume with `for x := range seq`.
- **Loop scoping (1.22):** each iteration owns its variables — never write `i := i` shadow lines.
- **Crypto:** `crypto/ecdh` (not `crypto/elliptic`); `crypto/hkdf`, `crypto/pbkdf2`, `crypto/sha3`, `crypto/mlkem`, `crypto/hpke` from stdlib.
- **JSON:** `omitzero` for `time.Time` and pointer fields; `omitempty` only for slices/maps where empty matters.
- **Routing:** stdlib `net/http.ServeMux` with `"GET /items/{id}"` patterns — no third-party router needed.
- **Don't write:** `fmt.Sprintf("%s:%d", host, port)` (use `net.JoinHostPort`); `runtime.SetFinalizer` for new code; `rand.Seed(...)` (no-op since 1.24); `panic(nil)`.

If you're tempted by a pre-1.21 pattern, open `modern-go.md` and find the modern replacement.


## 1. Naming

### Packages
- Lowercase, single word, no underscores, no `mixedCaps`. `tabwriter`, not `tab_writer`.
- Short and concrete. **Never** `util`, `helpers`, `common`, `lib`, `misc`, `shared`. If you can't name the package precisely, the boundary is wrong.
- Name describes contents, not layer. `dht`, `transport`, `identity` — not `core`, `service`.
- Avoid stuttering with exported names. `bufio.Reader`, not `bufio.BufReader`. `dht.Table`, not `dht.DHTTable`.
- Package name in this repo must be unique across the whole module — this prevents `goimports` collisions (Cockroach rule, valuable). Disambiguate with parent: `kvserver`, `serverpb`.

### Identifiers
- `MixedCaps` / `mixedCaps`. Never `snake_case`, never `SCREAMING_CASE` for constants.
- Initialisms keep uniform case: `URL`, `urlParser`, `XMLAPI` — never `Url`, `XmlApi`.
- Variable name length scales with scope. `i` in a 3-line loop is fine; a package-level variable needs a real name.
- No type info in name when context is clear: `users`, not `userSlice`; `count`, not `countInt`.
- Receiver names: 1–2 letters, consistent for the type across all methods. `func (t *Tray)`, not `(this *Tray)` or `(self *Tray)`.

### Getters / setters
- No `Get` prefix. `obj.Owner()`, not `obj.GetOwner()`. Setter is `SetOwner()`.
- Use `Get` only when the underlying concept genuinely uses the word (HTTP GET, etc.).

### Interfaces
- One-method interface: method name + `-er`. `Reader`, `Writer`, `Closer`, `Stringer`.
- Larger interfaces: pick a behavioural noun, not a `-er` mash-up.
- Interface defined by the **consumer**, not the producer. The package that implements behaviour exports concrete types; the package that needs to abstract over them defines the interface.

### Errors
- Exported sentinels: `ErrNotFound`, `ErrTimeout`. Unexported: `errClosed`.
- Exported error types: `XError` suffix (`ParseError`). Unexported: `xError`.

### Flags
- Flag names use underscores (`--max_packet_size`); the variable holding the value uses `mixedCaps` (`maxPacketSize`).

---

## 2. Package design — Package-Oriented Design

Synthesised from Bill Kennedy's *Package-Oriented Design* (Ardan Labs) and Ben Johnson's *Standard Package Layout*. These conventions govern package organisation and inter-package dependencies; they sit on top of, not inside, individual files.

### 2.1 Four design philosophies (Kennedy)

Each package is judged against these four questions. If any fails, redesign.

1. **Purpose** — what does this package *provide*? A package "provides", it doesn't "contain". `http`, `dht`, `transport` good; `util`, `helpers`, `common`, `lib`, `models`, `types` bad. If you can't name the package by the verb it lets callers express, the boundary is wrong.
2. **Usability** — can a caller figure out the API without reading the source? Small surface, intuitive names, zero-value-friendly types, godoc on every export.
3. **Portability** — could this package be lifted into another project unchanged? Stricter for `pkg/` (must be), looser for `internal/` (must work for *this* project; future is bonus). Implies: no project-specific logging, no project-specific config, no global state.
4. **Focus** — one cohesive idea per package. Two unrelated responsibilities → split. Tiny one-struct packages pretending to be modules → merge.

### 2.2 Package layers and dependency direction

This project uses three tiers (see `CLAUDE.md` for the exact directory layout):

| Tier | Where | Contents | Can import from |
|---|---|---|---|
| `cmd/<bin>` | per-binary | wiring only — flag parsing, logger setup, call into `internal/<app>.Run` | `internal/`, `pkg/`, stdlib, vendored |
| `internal/<x>` | project-private | application/domain logic specific to this project | `internal/` (carefully), `pkg/`, stdlib |
| `pkg/<x>` | reusable | foundational, project-agnostic, library-quality | `pkg/`, stdlib only |

**Dependency direction is one-way.** `pkg/` → never imports `internal/` or `cmd/`. `internal/` → never imports `cmd/`. Compiler enforces `internal/` against external modules; the rest is enforced by review and `go list`.

**Same-tier import discipline.** Two siblings in `internal/` should not freely import each other. If `internal/dht` and `internal/transport` both want each other, one of them owns the abstraction (define interface there, the other implements). If they're truly peers, look for a third package they both depend on — extract the shared types there.

If imagining the import graph is hard, draw it. Cycles are forbidden by the language; near-cycles (A → B, B → C, C → A') are forbidden by good taste.

### 2.3 Naming — provides, not contains

- Lowercase, single word. `tabwriter`, not `tab_writer` or `tabWriter`.
- Name the *behaviour the caller invokes*, not the data inside. `dht`, not `dhtmodels`. `transport`, not `transportstuff`.
- Banned: `util`, `helpers`, `common`, `lib`, `misc`, `shared`, `types`, `models`, `core`. These are dumping grounds; they couple the project to a single point of dependency that nothing can be modularised away from.
- No stuttering with the type name: `dht.Table`, not `dht.DHTTable`. `transport.Conn`, not `transport.TransportConn`.
- For `pkg/` packages, the name must be unique across the module so `goimports` doesn't randomly rename. Disambiguate with parents: `serverpb`, `kvserver`.

### 2.4 Domain types — where do they live?

Ben Johnson's rule, adapted: **domain types and the interfaces over them live in the package that names the domain**, not in a generic `types` package.

For this project: a `Packet` lives in `pkg/packet`, not `pkg/types`. The `Transport` interface lives in `pkg/transport`, alongside its UDP implementation, not in some abstract `pkg/api`. The interface is co-located with the canonical implementation; alternative implementations import the interface package.

If two unrelated packages need a common type and neither owns it, that's a sign the type belongs in a *third* package that names the abstraction. Don't shove it into a parent or a `common`.

### 2.5 Policy boundaries — what each tier may do

This is the rule that keeps `pkg/` truly reusable:

| Concern | `pkg/` | `internal/` | `cmd/` |
|---|---|---|---|
| Set application policy (config defaults, retry counts, timeouts) | **No** | Yes | Yes |
| Read environment variables / config files | **No** | Sparingly (config layer) | Yes (parse + inject) |
| Log directly | **No** — accept a `*slog.Logger` parameter or expose hooks | Yes | Yes |
| Initialise metrics / telemetry exporters | **No** | Sometimes | Yes |
| Open files / sockets at package init | **No** | **No** | **No** (do it in `Run`, not `init`) |
| Wrap errors with project-specific context | **No** — return root-cause errors | Yes | Yes |
| `panic` across public boundary | **Never** | Never | Allowed at startup-only invariant violations |
| `os.Exit` / `log.Fatal` | **Never** | Never | Yes (only here) |

Reasoning: `pkg/` packages are reusable only if they don't impose project-specific opinions. The moment a `pkg/` package logs to a specific destination, exits the process, or reads `MY_PROJECT_*` env vars, it stops being a library and becomes part of the application.

### 2.6 Interfaces — consumer-defined, late-extracted

- **The consumer defines the interface, not the producer** (Go idiom; reinforced by Kennedy). The package that needs the abstraction declares the interface; the package that implements concrete types exports the structs and constructors.
- Don't pre-create interfaces. Add them when a second implementation or a real test fake demands it. Premature interfaces are dead weight.
- **Functions take interfaces, return concrete types.** Caller gets full method set; abstraction is at the seam, not the leaf.
- Keep interfaces small (1–3 methods) — composition is cheap, fat interfaces are not.
- Interface check: `var _ Transport = (*UDPTransport)(nil)` — fails the build if the implementation drifts.

### 2.7 When to split, when to merge

**Split when:**
- Naming the package becomes hard ("it's the package with both X and Y").
- Two responsibilities have different change rates / lifetimes.
- Two responsibilities have different test fixtures or test environments.
- A subset of consumers wants only one half of the API.

**Merge when:**
- One package always imports another and exposes its types directly.
- The "smaller" package has only a single struct used by exactly one consumer.
- Splitting was speculative — the second consumer never appeared.

### 2.8 Anti-patterns

- **Single point of dependency.** A `types`/`models`/`common` package that *everything* imports. Couples the project; nothing can change without touching everything. Fix: domain types live with their behaviour.
- **Layer-by-package** (`controllers/`, `services/`, `repositories/`). This forces siblings to import each other and turns the project into a Java app. Use feature/domain packages instead.
- **`init()` doing real work.** Opening connections, starting goroutines, reading config. Use explicit constructors / `Run` methods. Acceptable `init`s: registering with a language-mandated global (`database/sql`), `flag.StringVar` setup. That's it.
- **Mutable globals.** Inject dependencies through constructors. The only acceptable globals are immutable sentinels: `var ErrX = errors.New(...)`, `var defaultLogger = slog.Default()`.
- **Re-exporting from a parent package** to "simplify imports". This grows API surface and hides the real dependency.
- **Cyclic temptation.** A and B want to import each other. Solution: extract the shared interface to a third package, or move one inside the other's tree to expose the relationship.

### 2.9 Validating a package — checklist

Before merging a new or changed package:

- [ ] Name describes what it provides. Not `util`/`common`/`models`.
- [ ] Single, expressible purpose. Can describe in one sentence without "and".
- [ ] Imports respect tier: `pkg/` only imports `pkg/`+stdlib; `internal/` doesn't import `cmd/`.
- [ ] No same-tier import that should be a third-package extraction.
- [ ] Public API has godoc on every exported identifier.
- [ ] No `init()` doing I/O or starting goroutines.
- [ ] No mutable global state.
- [ ] If in `pkg/`: no logging direct, no env var reads, no `os.Exit`, no error wrapping with project terms.
- [ ] Interface (if any) is consumer-defined, in the consumer package — not pre-emptively in this one.

---

## 3. Error handling

The single most consequential area. All five guides agree on the core; they diverge on tooling.

### Returning errors
- Return `error` (the interface), not a concrete type. Returning `*MyError` triggers the famous nil-interface bug when callers compare against `nil`.
- Error is the **last** return value: `func F() (T, error)`.
- Never return sentinel "values that mean error" (`-1`, `""`, `nil`). Always a separate `error` (or `(value, ok)` for lookup-style APIs).
- Never silently discard with `_`. Either handle, return, or — only at process boundary — log + decide.

### Constructing errors
| Need | Tool |
|------|------|
| Static message, no programmatic match | `errors.New("msg")` |
| Dynamic message, no match | `fmt.Errorf("msg %s", x)` |
| Sentinel (callers do `errors.Is`) | `var ErrX = errors.New(...)` at package level |
| Typed error (callers do `errors.As`) | Custom struct with `Error() string` method |

### Wrapping
- Add context with `fmt.Errorf("doing X: %w", err)`. The `%w` preserves the chain for `errors.Is`/`errors.As`.
- `%w` goes at the **end** of the format string — reads newest-to-oldest on print.
- Wrap only when adding real information. Don't wrap with "failed to" — `err` already says it failed. Add the *what*: file path, key, operation.
- Don't double-wrap: if you'll return the error, don't also log it. **Handle errors once.**

### Error message text
- Lowercase start, no trailing period, no newlines: `"connect: connection refused"`.
- Exception: when the message starts with an exported identifier (`SomePackage.Foo failed`), keep its case.

### Inspecting errors
- `errors.Is(err, ErrX)` for sentinels.
- `errors.As(err, &target)` for typed errors.
- Never `if err.Error() == "..."` — string-matching couples you to message text.

### Panic / Fatal
- `panic` is for unrecoverable programmer errors (impossible state, broken invariants). Not for "the network call failed."
- Library code (`pkg/`) **never** panics across its public boundary. If you panic internally, recover before returning.
- `log.Fatal` only in `main` or in `init` for unrecoverable startup failures. Returning from `main` is preferred over `os.Exit`.
- `MustX` naming: helpers that panic on failure. Acceptable for package-level initialisation (`regexp.MustCompile`), tests, and one-off setup. Never on user input.

### Error flow shape
- Happy path stays at column 0. Handle errors and return; don't nest the success branch in `else`.
```go
// Yes
v, err := f()
if err != nil {
    return fmt.Errorf("f: %w", err)
}
use(v)

// No
v, err := f()
if err == nil {
    use(v)
} else {
    return err
}
```

---

## 4. Receivers, values, pointers

- **Pick one style per type.** All methods on `T` should be either value receivers or pointer receivers. Don't mix.
- Use **pointer receivers** when:
  - The method mutates the receiver.
  - The struct contains a `sync.Mutex` (or any sync primitive — they must not be copied).
  - The struct is large.
  - The struct contains pointers/maps/slices whose mutation should be visible.
- Use **value receivers** when:
  - The type is a small POD.
  - The type is a `map`, `func`, `chan`, or a slice that won't be reslized.
  - You want each call to operate on a logical copy (functional style).
- Don't pass pointers just to "save bytes." Compiler/runtime is smarter than you. Pass by pointer for *semantics*, not micro-optimisation.
- Never pass `*string`, `*int`, `*bool`, `*interface{}` — fixed-size values, no reason to indirect.
- `sync.Mutex` zero value is ready: `var mu sync.Mutex`, never `mu := new(sync.Mutex)`.
- Don't embed `sync.Mutex` in a public struct (it leaks `Lock`/`Unlock` to the API). Use a named field `mu sync.Mutex` and document what it guards.

---

## 5. Concurrency

- **Make every goroutine's exit obvious.** Either it's bounded (does work then returns), or it watches `ctx.Done()` / a stop channel. Fire-and-forget is a bug.
- Wait for goroutines you start. Use `wg.Go(fn)` (1.25) for fan-out, `errgroup` if any can fail, channel-as-signal for one-off. Don't write `wg.Add(1); go func(){ defer wg.Done(); ... }()` — the new method is the canonical form.
- Never start goroutines from `init()` or from constructors. Spawn them in an explicit `Run`/`Start` method that returns when work completes or context is cancelled.
- **Prefer synchronous functions.** A caller can always wrap a sync function in `go`. The reverse — taking concurrency out — is much harder.
- Channels: unbuffered or size 1 by default. Other sizes need a written justification (back-pressure, batching).
- Always declare channel direction in function signatures: `func consume(ch <-chan T)`.
- "Share by communicating, don't communicate by sharing." But: `sync.Mutex` is fine for hot paths and reference counts. Use channels for ownership transfer; mutexes for protecting state.
- `select` with `default:` is the non-blocking idiom. Use deliberately, not as a panic button.

---

## 6. Context

- `context.Context` is **always the first parameter**, named `ctx`.
- Never store `Context` in a struct field. Plumb it explicitly.
- Don't create custom context types. Don't use a `Context`-shaped interface other than `context.Context`. (Google rule, no exceptions.)
- The function that receives the context decides whether to honour deadlines and cancellation — document if behaviour is non-standard.
- `context.Background()` only at process roots (`main`, top of an RPC handler). `context.TODO()` while migrating; never in finished code.

---

## 7. Function & API design

- Keep signatures on one line. If a signature wraps, the function takes too many arguments.
- More than ~4 parameters: take an option struct or `Options` pattern. For optional knobs that callers usually omit: variadic functional options.
- Functional options: parameters must accept arguments (`WithTimeout(d)`); never use option *presence* as a flag (`WithRetries()` with no arg). Document precedence — last-wins is the convention.
- Don't name return values for documentation only. Name them when:
  - Multiple values of the same type need disambiguating.
  - You use naked `return` in a small function.
  - Documenting the contract via signature genuinely helps.
- Avoid naked returns in functions longer than ~10 lines.
- Boolean parameters at call sites are unreadable. Either:
  - Add an inline comment: `f(true /* verbose */, false /* dry-run */)`.
  - Use a named type or option struct.

---

## 8. Data: literals, slices, maps

### Struct literals
- **Always use field names** in struct literals across package boundaries. Within a package, field names optional for ≤3 fields.
- `&T{}` is preferred over `new(T)` when you also need to set fields.
- Plain `new(T)` is fine for "give me a pointer to a zero `T`."
- Omit zero-value fields unless their absence would be confusing.

### Slices
- `nil` slice is valid: it has `len == 0`, you can `range` it, you can `append` to it. **Prefer `var s []T`** to `s := []T{}` for an empty slice.
- API contract: don't differentiate `nil` from empty. Use `len(s) == 0`, never `s == nil`, when checking for emptiness.
- Pre-allocate with `make([]T, 0, n)` when `n` is known. Don't pre-allocate "just in case."
- Be explicit about ownership: when you store or return a slice, document whether you keep a reference or copy. Copy at trust boundaries.

### Maps
- Maps must be initialised before write. Reading a `nil` map returns the zero value (no panic).
- Use `m, ok := mp[k]` to distinguish "missing" from "zero-valued." Use `_, ok` for set-membership.
- `map[K]struct{}` is the idiomatic set type — empty struct uses zero memory.
- Same boundary-copy rule as slices.

### Type assertions
- Always use the comma-ok form: `v, ok := x.(T)`. Bare `v := x.(T)` panics on mismatch — only acceptable when you've just type-switched and know the type.

### `iota`
- For enums: start at `iota + 1` so the zero value means "unspecified" and accidentally-zero-valued enums fail loudly. (Cockroach rule, broadly useful.)
- Always pair the enum type with a `String()` method (use `stringer`).

---

## 9. Comments & docs

- Every exported identifier has a doc comment. Unexported types/functions with non-obvious behaviour also.
- Doc comment starts with the identifier: `// Reader reads bytes from a stream.` (Optional article: `// A Reader reads bytes...`)
- Package comment immediately precedes the `package` clause, no blank line.
- Comment full sentences. Capital first letter, period at end. (Inline trailing comments may be fragments.)
- **Don't narrate code.** `// increment counter` above `c++` is noise. Comments explain *why*, not *what*.
- Mark naked literal arguments with inline comments at call sites: `f(true /* verbose */)`. Use the parameter's actual name verbatim.
- For our project: prefer no comment over a comment that just restates the code (per `CLAUDE.md`).

---

## 10. Imports

- Group: stdlib, then third-party, then this module. `goimports` does this automatically.
- Don't rename imports unless required (collision, generated proto packages, package name disagrees with path).
- **Never use `import .`** It hides where symbols come from.
- Blank imports (`import _ "..."`) only in `main` packages or test files.
- For generated protobuf packages, suffix the import name with `pb`: `foopb "x/y/foo_go_proto"`.

---

## 11. Testing

(See `CLAUDE.md` for TDD policy. Style rules below.)

### Table-driven, parallel
```go
func TestParse(t *testing.T) {
    t.Parallel()
    tests := []struct {
        name    string
        in      string
        want    Packet
        wantErr error
    }{
        {name: "empty", in: "", wantErr: ErrShort},
        {name: "valid", in: "...", want: Packet{...}},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            got, err := Parse(tt.in)
            if !errors.Is(err, tt.wantErr) {
                t.Fatalf("Parse(%q) err = %v, want %v", tt.in, err, tt.wantErr)
            }
            if diff := cmp.Diff(tt.want, got); diff != "" {
                t.Errorf("Parse(%q) mismatch (-want +got):\n%s", tt.in, diff)
            }
        })
    }
}
```

Note: with Go ≥1.22 each iteration has its own `tt`; **don't** write `tt := tt` shadowing.

### Time, concurrency, deadlines — `testing/synctest`
For any test that involves timers, sleeps, or coordinated goroutines, wrap in `synctest.Test`. Real time is virtualised inside the bubble; `synctest.Wait` blocks until all goroutines in the bubble are durably blocked — eliminating polling and arbitrary `time.Sleep`s in tests.

```go
func TestSessionExpires(t *testing.T) {
    synctest.Test(t, func(t *testing.T) {
        s := NewSession(5 * time.Second)
        time.Sleep(6 * time.Second)   // virtual — instantaneous
        synctest.Wait(t)
        if s.Active() { t.Error("expected expired") }
    })
}
```

### Test-scoped context, working dir, artifacts
- `t.Context()` (1.24) — context cancelled at test end. Use it everywhere a test launches goroutines or hits a context-aware API.
- `t.Chdir(dir)` (1.24) — auto-restored on cleanup. Replaces manual `os.Chdir` + `t.Cleanup`.
- `t.Attr(key, val)` (1.25) — structured metadata, surfaced via `-json`. Use for environment, build, version info.
- `t.ArtifactDir()` (1.26) — write per-test files (golden mismatches, captured traces, pcap dumps). Combine with `-artifacts` flag.

### Assertions and failure messages
- Format: `Func(input) = got, want want`. Print **got first, then want.**
- Include the input in the error message — debugging without it is misery.
- Use `t.Error` to keep going, `t.Fatal` only when continuing is meaningless.
- `t.Helper()` in helpers so failure points at the caller.
- Never call `t.Fatal` from a goroutine other than the one running the test — use `t.Error` and return.
- Use `cmp.Diff` for structural comparison; avoid hand-rolled field-by-field assertions.
- Avoid assertion libraries that hide failure context (no `assert.Equal(...)` style here).

### Test fakes & doubles
- Name fakes by behaviour: `AlwaysDeclines`, `StubCharges`, not `MockProcessor`.
- Prefer real implementations over mocks when feasible — e.g. `httptest.Server` over a hand-mocked HTTP client.
- For network code, in-memory `Transport` implementation is the default fake (per project architecture).

### Compile-time interface checks
- Assert at the package level when a type is meant to satisfy an interface:
```go
var _ Transport = (*UDPTransport)(nil)
```
This catches breakage at build time, not test time.

### Fuzz
- Wire-format parsers, crypto code, and decoders get `FuzzX` in addition to unit tests.
- E2E tests behind `//go:build e2e` — kept out of the default `go test ./...`.

---

## 12. Performance

Full reference (benchmarking workflow, profiling, escape analysis, GC tuning, PGO, sync.Pool, cache lines, anti-patterns table): **`performance.md`** in this skill directory. Load it whenever the task involves measuring, optimising, or tuning Go code.

### Mental model
1. **Do less.** Algorithm + data-structure choice. 10×–1000× wins.
2. **Do it less often.** Cache, batch, hoist constant work. 2×–10× wins.
3. **Do it faster.** Allocations, inlining, primitives. 5–30% wins.

Most teams obsess over (3) while (1) is the only place that moves the needle. Always ask "is this work necessary?" before "can it be faster?".

### Measure first, always
- No optimisation without a benchmark + `benchstat` showing p<0.05.
- Use `b.Loop()` (1.24+), `-count=10 -benchmem -benchtime=2s`, on an idle machine.
- Pick the right profile: CPU (where time goes), heap `alloc_objects` (allocation churn / GC pressure), execution trace / flight recorder (where time *doesn't* go — blocked goroutines, GC pauses, scheduler latency).
- If CPU profile is "idle" and throughput is bad → trace, not pprof.

### Always-cheap, always-do-right (no profile needed)
- Pre-size `make([]T, 0, n)` and `make(map[K]V, n)` when `n` is known.
- `strconv.Itoa` / `strconv.AppendInt` over `fmt.Sprint*` for primitives.
- `strings.Builder` (with `Grow`) for loops that build strings.
- Package-level `var ErrX = errors.New(...)` and `var re = regexp.MustCompile(...)`.
- Reset-and-reuse: `buf = buf[:0]`, `clear(m)` (1.21+).
- Buffer-passing APIs: `AppendTo(dst []byte) []byte` over `Encode() []byte` for hot wire-format paths.
- Group pointer fields at the start of structs — GC stops scanning at the last pointer.
- `atomic.Int64`/`atomic.Pointer[T]` for simple counters/flags instead of `Mutex` + scalar.

### Need-a-profile-before-using
- `sync.Pool` — easy to misuse (stale state, cleared on GC). Verify a real `alloc_objects` reduction before introducing.
- `unsafe.String` / `unsafe.Slice` for zero-copy `[]byte`↔`string`. Read-only; document the constraint.
- Cache-line padding for sharded counters (false sharing). Verify with `dominikh/structlayout`.
- Hand-written encoders replacing `encoding/json` reflection. Benchmark first.
- Lock sharding — only if `mutex` profile shows real contention.

### Modern Go performance baseline (1.21–1.26)
- **PGO** (1.21 stable, default-on): drop `default.pgo` next to `cmd/<bin>/main.go`. 2–14% gain on real workloads. Refresh quarterly.
- **Green Tea GC** (1.26 default): 10–40% reduction in GC overhead on small-object-heavy workloads. Permanent — opt-out only for debugging.
- **Container-aware `GOMAXPROCS`** (1.25 default): respects cgroup CPU limits; auto-updates on changes. Don't hardcode `runtime.GOMAXPROCS` in containers.
- **`GOMEMLIMIT`** (1.19+): soft memory limit. In containers, set to ~90% of allocated memory + headroom; combine with `GOGC=200` for low-overhead-until-pressure behaviour.
- **`b.Loop()`** (1.24+): canonical benchmark form — manages timer reset, prevents inlining-driven DCE.
- **Flight recorder** (1.25+): cheap continuous tracing. Enable in production; flush only on slow-request events.
- **Goroutine leak profile** (1.26 EXPERIMENTAL): finds goroutines blocked on unreachable primitives. `GOEXPERIMENT=goroutineleakprofile`.

### Anti-patterns (grep for these)
`fmt.Sprintf` in hot loops · `defer` in tight loops · `regexp.MustCompile` inside functions · `*int`/`*string`/`*bool` parameters · `interface{}` in hot paths · `[]byte(s)`/`string(b)` round-trips per iteration · `make([]T, 0)` then thousands of `append`s · goroutine-per-trivial-item · `sync.RWMutex` for short critical sections · `runtime.SetFinalizer` (use `runtime.AddCleanup`, 1.24+) · `time.Sleep`-based polling.

### When you genuinely need to optimise
1. Reproduce with a stable benchmark.
2. CPU profile → find the hot function.
3. If CPU is idle → execution trace.
4. If allocations dominate → heap `alloc_objects`.
5. Apply targeted change. `benchstat` it. Discard if p ≥ 0.05.
6. Only then ask: algorithmic win? batching? layer of work that's unnecessary?
7. Lock the win in with a regression benchmark.

If you skipped a step, you're guessing.

---

## 13. The blank identifier — legitimate uses

- Discard unwanted return: `_, err := f()`.
- Side-effect import: `import _ "net/http/pprof"`.
- Compile-time interface check: `var _ Iface = (*T)(nil)`.
- Suppress "imported and not used" only in throwaway debugging code; never commit.

**Not** acceptable: `_ = err` to silence error checking.

---

## 14. Generics — when, not whether

- Use generics when removing them would force `interface{}` + type assertions across multiple call sites.
- Don't use generics to build DSLs, error frameworks, or assertion libraries.
- A generic container with one concrete instantiation is over-engineered. Wait for the second.
- Constraint sets stay in the package that defines the generic type; don't export `Ordered`-style constraints unless they're the public API.

---

## 15. Linters & tools

- `gofmt` / `goimports` — non-negotiable.
- `go vet` — must pass before commit.
- `staticcheck` — recommended.
- `golangci-lint` aggregator if the team standardises on it. Don't hand-roll lint configurations.
- `-race` in CI for any package with concurrency.

---

## 16. Cross-source disagreements & this project's call

| Topic | Cockroach | Uber / Google / Effective Go | This project |
|---|---|---|---|
| Formatter | `crlfmt -tab 2` (100 cols code, 80 cols comments) | `gofmt` defaults | **`gofmt`** — stay with the standard |
| Embedding `sync.Mutex` | OK in private types | Discouraged universally | **Don't embed.** Always named `mu` field |
| Error library | `github.com/cockroachdb/errors` (redactability) | stdlib `errors` + `fmt.Errorf("%w")` | **stdlib** — no PII redaction concern at this scale |
| Naked returns | Discouraged | Allowed in small functions | **Allowed in ≤10-line functions** |
| `cmp.Diff` for everything | Yes | Yes | **Yes** — including pure-value comparisons when types are non-trivial |

---

## Quick checklist before sending code

- [ ] All exported identifiers have doc comments starting with the name.
- [ ] Errors wrapped with `%w` and contextual info, lowercase, no trailing period.
- [ ] No `init()` doing real work, no goroutines from constructors.
- [ ] Every goroutine's exit path is obvious.
- [ ] `ctx context.Context` is the first parameter where applicable.
- [ ] Receivers consistent (all value or all pointer for a type).
- [ ] No mutable globals; dependencies injected.
- [ ] Tests are table-driven and parallel; failure messages include input and `got, want`.
- [ ] `go vet` and `-race` pass.
- [ ] No `interface{}`/`any` at API boundaries unless genuinely heterogeneous.
- [ ] No `panic` across package boundary.
