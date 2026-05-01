---
name: go-testing
description: Use when writing, designing, debugging, or reviewing Go tests — *_test.go files, table-driven tests, t.Run subtests, t.Parallel, fuzz, benchmarks, fixtures, helpers, assertions. Covers TDD red-green-refactor, testing/synctest for concurrency, testcontainers-go for integration, testify/suite for e2e. Targets Go 1.26.
when_to_use: Triggers — adding a TestX/BenchmarkX/FuzzX/ExampleX function; writing unit, integration, or e2e tests; configuring t.Cleanup / t.Context / t.Chdir / t.Setenv / t.TempDir; choosing parallel vs sequential; eliminating time.Sleep flakiness with synctest; picking testcontainers vs in-memory fake; running go test with build tags (integration, e2e); diagnosing flaky tests or -race failures; mocking dependencies via interfaces; deciding cmp.Diff vs errors.Is for assertions; setting up TestMain. Complements the go-style skill and CLAUDE.md (TDD policy).
---

# Go Testing — TDD, table-driven, parallel, synctest, testcontainers, suite

This skill governs how tests are written in this project. It complements:
- `CLAUDE.md` — TDD is mandatory; structure and toolchain.
- `go-style/SKILL.md` — general Go style.

This file deliberately repeats project-critical rules so it stays useful when invoked alone.

---

## 0. Test taxonomy — pick the right tool

| Type | Where | When | Tools |
|---|---|---|---|
| **Unit** | `pkg/` and `internal/` next to code | One package, no I/O, no real time | `testing`, table-driven, `cmp.Diff` |
| **Concurrency unit** | same as unit | Time, timers, goroutine coordination | `testing/synctest` (1.25 stable) |
| **Property / fuzz** | `pkg/<x>/fuzz_test.go` | Parsers, codecs, crypto, wire formats | `testing.F`, `f.Fuzz` |
| **Benchmark** | `*_test.go` next to code | Hot paths, before optimising | `testing.B` + `b.Loop()` (1.24+) |
| **Integration** | `internal/<x>/integration_test.go` build tag `integration` | Real DB, real net stack, multi-process | `testcontainers-go`, optionally `suite` |
| **E2E** | `test/e2e/...` build tag `e2e` | Full system from `cmd/*` boundary | `testify/suite` + `testcontainers-go` |

Default to unit tests. Reach for higher tiers only when the unit tier can't express the property under test.

---

## 1. TDD workflow (mandatory)

Per `CLAUDE.md`: every feature starts from a failing test.

1. **Red** — write the smallest test that names the desired behaviour. Run it, confirm it fails for the *right reason* (compile error, wrong return, missing function — not a typo in the test).
2. **Green** — write the minimum code to pass. Hard-coding return values is acceptable when only one case exists; the next failing test forces real logic.
3. **Refactor** — clean up while the bar stays green. No new behaviour during refactor.
4. Commit at green. Don't let a red test leak into version control.

**If code resists testing, the architecture is wrong, not the test.** Inject dependencies through interfaces, take `Clock`/`io.Reader` as parameters, return values instead of mutating globals.

---

## 2. File and function naming

- File: `xxx_test.go` next to the code under test.
- Same package (`package foo`) for white-box tests; `package foo_test` for black-box tests that prove the public API is sufficient.
- Function: `func TestThing_Condition_Outcome(t *testing.T)` or `func TestThing(t *testing.T)` for table-driven.
- Subtests get human-readable names: `t.Run("rejects empty input", ...)`, not `t.Run("case_1", ...)`.
- For fuzz: `func FuzzParse(f *testing.F)`. Benchmarks: `func BenchmarkX(b *testing.B)`. Examples: `func ExampleX()`.

---

## 3. Table-driven tests — the default form

```go
func TestParse(t *testing.T) {
    t.Parallel()
    tests := []struct {
        name    string
        in      string
        want    Packet
        wantErr error
    }{
        {name: "empty input", in: "", wantErr: ErrShort},
        {name: "valid header", in: "...", want: Packet{Version: 1}},
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

Rules:
- **Always use `t.Run` with named cases.** Lets `-run TestParse/rejects_empty` work and surfaces failing case names in CI.
- **Don't write `tt := tt`** — Go ≥1.22 fixes loop-var scoping. Shadowing is noise.
- Case order shouldn't matter. Use a `map[string]struct{...}` instead of a slice when you want randomised iteration to enforce independence — but pay the cost of lost ordering for debugging.
- **Got first, then want** in failure messages: `got = %v, want %v`.
- Include the **input** in every failure message. Debugging without it is misery.
- One assertion per subtest is the ideal; multiple `t.Errorf`s are fine if they're independent.
- Use `cmp.Diff(want, got)` for non-trivial types — never hand-rolled field-by-field comparison.

When struct fields are unexported, pass `cmp.AllowUnexported(T{})` or implement `Equal` on the type.

---

## 4. Subtests with `t.Run`

- Subtests give you isolation, hierarchical naming, and selective execution.
- Failures in one subtest don't skip siblings.
- Parent setup (before the loop) runs once; teardown (after the loop) runs once even if subtests fail.
- Run a single case: `go test -run TestParse/rejects_empty`. Slashes split levels.
- Names get sanitised — spaces become underscores. Pick names that survive: `"empty input"` becomes `empty_input`, fine. Avoid `/` in names.

---

## 5. Parallel tests — rules

- Top-level `t.Parallel()` makes the test eligible to run alongside other parallel tests.
- For table-driven: call `t.Parallel()` **at both levels** — the outer test and each subtest.
- Parent test blocks until all parallel subtests complete; teardown after the loop runs after them.
- **Cannot use `t.Setenv`, `t.Chdir`** in a parallel test or any of its subtests — they mutate process-global state. Both will fail-fast at runtime.
- **Cannot share mutable state** across parallel subtests without synchronisation.
- `synctest.Test` cannot use `T.Parallel` inside the bubble (see §9).
- Run with `-parallel N` to cap concurrency (default = `GOMAXPROCS`).
- **`-race` is non-negotiable** for any package using goroutines, channels, or `sync.*`. CI must run `go test -race ./...`.

---

## 6. Helpers, cleanup, fixtures

### `t.Helper()`
Call **at the top of every assertion/setup helper.** Failures then point at the caller, not inside the helper.
```go
func mustOpen(t *testing.T, path string) *os.File {
    t.Helper()
    f, err := os.Open(path)
    if err != nil {
        t.Fatalf("open %s: %v", path, err)
    }
    return f
}
```

### `t.Cleanup(fn)` — preferred over `defer`
- LIFO order, runs even if the test panics.
- Survives subtests: cleanup registered in parent runs after all subtests.
- Compose helpers cleanly: a fixture-creating helper can register its own cleanup.
```go
func newServer(t *testing.T) *Server {
    t.Helper()
    s := startServer()
    t.Cleanup(s.Close)
    return s
}
```

### Built-in fixtures (Go 1.24+)
- `t.TempDir()` — fresh directory per test, removed on cleanup.
- `t.Setenv(key, val)` — auto-restored. Marks the test non-parallel-able.
- `t.Chdir(dir)` — auto-restored. Same parallel restriction.
- `t.Context()` — context cancelled at test end. Use everywhere a context is needed instead of `context.Background()`.

### `TestMain` — only when needed
For one-time package-level setup (start a shared resource, parse extra flags). Avoid if `t.Cleanup` per test will do.
```go
func TestMain(m *testing.M) {
    // setup
    code := m.Run()
    // teardown
    os.Exit(code)
}
```
Don't put assertions in `TestMain` — it has no `*testing.T`.

---

## 7. Assertions and failure messages

This project uses **plain `testing` + `cmp.Diff`** for unit tests. No `testify/assert`, no `gomega`.

Reasons (all five style guides agree on the spirit):
- Assertion libraries hide where the failure originated.
- They tend to short-circuit, hiding subsequent failures in the same test.
- Standard `t.Error/t.Fatal` plus `cmp.Diff` produces clearer output and fewer dependencies.

**Exception:** `testify/suite` is permitted for e2e and integration tests (see §11), and `suite.Equal` etc. are part of that package. Don't import `testify/assert` standalone in unit tests.

### When to use `t.Error` vs `t.Fatal`
- `t.Error` (or `Errorf`) — failure, but keep going. Use when subsequent checks are still meaningful (multiple independent fields).
- `t.Fatal` (or `Fatalf`) — failure, stop. Use when continuing would cascade (nil pointer, missing setup).
- **Never call `t.Fatal` from a goroutine other than the test's main goroutine.** It calls `runtime.Goexit` only on the calling goroutine; the test will hang or report misleadingly. Use `t.Error` and `return`, or send the failure over a channel.

### Format
```
Func(input) = got, want want
```
Always: function name + input → got, want. Print **got first, then want** — matches IDE diff conventions.

---

## 8. `errors.Is` / `errors.As` in tests

```go
if !errors.Is(err, ErrNotFound) {
    t.Fatalf("err = %v, want ErrNotFound", err)
}

var pathErr *fs.PathError
if !errors.As(err, &pathErr) {
    t.Fatalf("err = %v, want *fs.PathError", err)
}
```

Or, on Go 1.26:
```go
pathErr, ok := errors.AsType[*fs.PathError](err)
if !ok { t.Fatalf(...) }
```

Never compare error messages with `err.Error() == "..."`.

---

## 9. `testing/synctest` — concurrency without flakiness

Stable since Go 1.25. **Use this for any test involving timers, sleeps, deadlines, retries, or goroutine coordination.** Eliminates `time.Sleep(50*time.Millisecond)` flakiness.

### Core API
- `synctest.Test(t, func(t *testing.T) { ... })` — runs the body in a "bubble" with virtualised time, starting at midnight UTC 2000-01-01.
- `synctest.Wait(t)` — blocks until every other goroutine in the bubble is **durably blocked** (only unblockable by another bubble goroutine).
- Time advances only when *all* bubble goroutines are durably blocked — jumps to the next event.

### Restrictions inside a bubble
- No `t.Run`, no `t.Parallel`, no `t.Deadline`.
- Bubble channels/timers/`sync.WaitGroup` cannot be touched from outside the bubble.
- `sync.Mutex.Lock`, network I/O, syscalls — **not** durably blocking. Use `net.Pipe()` for in-process fake networking.
- Package-level `var wg sync.WaitGroup` is risky; prefer `var wg = new(sync.WaitGroup)`.

### Pattern: timeout test
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

### Pattern: retry with deadline
```go
func TestRetryUntilDeadline(t *testing.T) {
    synctest.Test(t, func(t *testing.T) {
        ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
        defer cancel()
        var attempts int
        err := retry(ctx, func() error {
            attempts++
            time.Sleep(2 * time.Second)
            return ErrTransient
        })
        if !errors.Is(err, context.DeadlineExceeded) {
            t.Fatalf("err = %v, want DeadlineExceeded", err)
        }
        if attempts < 5 { t.Errorf("attempts = %d, want >= 5", attempts) }
    })
}
```

### Pattern: goroutine ordering
```go
func TestNotifierFires(t *testing.T) {
    synctest.Test(t, func(t *testing.T) {
        n := NewNotifier()
        var got string
        go func() { got = <-n.Ch() }()
        synctest.Wait(t)        // goroutine now durably blocked on receive
        n.Notify("hello")
        synctest.Wait(t)        // goroutine has now received and exited
        if got != "hello" { t.Errorf("got %q, want hello", got) }
    })
}
```

---

## 10. `testcontainers-go` — integration with real services

Use when a real dependency (Postgres, Redis, full DHT peer) is required and an in-memory fake would lie. Avoid in unit tests.

### Minimal setup
```go
//go:build integration

func TestRedisClient(t *testing.T) {
    ctx := t.Context()
    c, err := tcredis.Run(ctx, "redis:7-alpine",
        testcontainers.WithWaitStrategy(wait.ForListeningPort("6379/tcp")),
    )
    testcontainers.CleanupContainer(t, c)
    if err != nil { t.Fatalf("start redis: %v", err) }

    endpoint, err := c.Endpoint(ctx, "")
    if err != nil { t.Fatalf("endpoint: %v", err) }
    // ... use endpoint
}
```

### Rules
- **Always pair `Run` with `testcontainers.CleanupContainer(t, c)`** — handles nil safely, runs on test end.
- **Always set a wait strategy.** `wait.ForListeningPort`, `wait.ForLog`, `wait.ForHTTP`, `wait.ForSQL`. Skipping it = race condition.
- **Each test gets its own container.** Random host ports mean parallel tests don't collide.
- Use prebuilt modules (`tcpostgres`, `tcredis`, …) over `GenericContainer` when one exists.
- Build tag: `//go:build integration` — keep out of default `go test ./...`.
- For multi-container topologies use `network.New(...)` + `WithNetwork`.
- Containers are slow (~1–10s startup). Group related assertions in one test where possible.
- CI must have Docker available — document it.

### Common functional options
`WithExposedPorts`, `WithEnv`, `WithFiles`, `WithCmd`, `WithEntrypoint`, `WithWaitStrategy`, `WithNetwork`, `WithName`, `WithLabels`, `WithLogger`.

---

## 11. `testify/suite` — for e2e and integration

Permitted for e2e/integration only. Forbidden in pure unit tests (use plain `testing`).

### When suites help
- Multiple tests share expensive setup (a started container, a populated DB).
- You want lifecycle hooks (`SetupSuite`, `TearDownSuite`, `SetupTest`, `TearDownTest`).
- You want stats / `BeforeTest` / `AfterTest` cross-cutting hooks.

### When **not** to use
- Single test or a handful of independent ones — plain `testing` is shorter.
- Tests must run in parallel — **`testify/suite` does not support parallel tests** (issue #934).

### Pattern
```go
//go:build e2e

type DHTSuite struct {
    suite.Suite
    nodes []*Node
}

func TestDHTSuite(t *testing.T) { suite.Run(t, new(DHTSuite)) }

func (s *DHTSuite) SetupSuite() {
    s.nodes = startCluster(s.T(), 5)
}

func (s *DHTSuite) TearDownSuite() {
    for _, n := range s.nodes { n.Close() }
}

func (s *DHTSuite) TestPeerLookup() {
    ctx := s.T().Context()
    addr, err := s.nodes[0].Find(ctx, s.nodes[3].PubKey())
    s.Require().NoError(err)
    s.Equal(s.nodes[3].Addr(), addr)
}
```

### Hooks (run order)
`SetupSuite` → for each test: `SetupTest` → `BeforeTest` → `TestX` (with optional `s.Run` subtests wrapping `SetupSubTest`/`TearDownSubTest`) → `AfterTest` → `TearDownTest` → `TearDownSuite`.

### Assertions
- `s.Require().X()` — fatal (`t.FailNow`). Use for preconditions.
- `s.Equal/NoError/...` (via embedded assertions) — non-fatal.
- For deep comparison still prefer `cmp.Diff` over `s.Equal` on complex structs — better diffs.

---

## 12. Fuzz testing

Required for: wire-format parsers, decoders, crypto primitives, anything reachable from untrusted input.

```go
func FuzzParsePacket(f *testing.F) {
    seeds := [][]byte{
        {},
        {0x01, 0x02, 0x03},
        validPacket,
    }
    for _, s := range seeds { f.Add(s) }

    f.Fuzz(func(t *testing.T, in []byte) {
        p, err := Parse(in)
        if err != nil { return }
        out, err := p.Encode()
        if err != nil { t.Fatalf("encode: %v", err) }
        if !bytes.Equal(in[:len(out)], out) {
            t.Fatalf("round-trip mismatch")
        }
    })
}
```

Run continuously with `go test -fuzz=FuzzParsePacket -fuzztime=30s`. Commit corpus from `testdata/fuzz/...` so regressions stay covered.

Allowed input types: `[]byte`, `string`, numeric types, `bool`, `byte`, `rune`. Compose multiple parameters as needed.

---

## 13. Benchmarks — Go 1.24+ `b.Loop()` form

```go
func BenchmarkEncode(b *testing.B) {
    p := newPacket()
    b.ReportAllocs()
    for b.Loop() {
        _, _ = p.Encode()
    }
}
```

- Use `b.Loop()` — manages timers, prevents inlining-driven dead-code elimination, hoists setup out of the timed region automatically.
- Don't use the legacy `for i := 0; i < b.N; i++` form unless backporting.
- `b.ReportAllocs()` for any allocation-sensitive code.
- `b.RunParallel` for measuring contention.
- `b.SetBytes(n)` when meaningful — gives MB/s in output.
- Run with `-benchmem -count=10` and feed to `benchstat` for honest comparisons.

---

## 14. Examples — runnable docs

```go
func ExampleHash() {
    h := Hash([]byte("hi"))
    fmt.Printf("%x", h[:4])
    // Output: 8f434346
}
```

- Verified by `go test`. The `// Output:` line must match exactly (`// Unordered output:` for sets).
- Use for non-trivial public APIs in `pkg/`. They surface in pkg.go.dev docs.
- Names: `ExampleType`, `ExampleType_Method`, `Example_suffix` for variants.

---

## 15. Build tags and test categories

- `//go:build integration` — needs Docker / external deps.
- `//go:build e2e` — full system tests.
- Default `go test ./...` runs only unit tests + fast integration if explicitly tagged.

```go
//go:build integration

package foo_test
```

CI matrix:
```
go test -race ./...                           # unit
go test -race -tags=integration ./...         # integration
go test -race -tags=e2e ./test/e2e/...        # e2e
go test -fuzz=. -fuzztime=2m ./pkg/packet     # nightly fuzz
```

---

## 16. Common pitfalls

- **Calling `t.Fatal` from a goroutine** other than the test main. Use `t.Error` + return + channel-send the failure.
- **Forgetting `t.Helper()`** in assertion helpers. Failure lines point at the helper, not the test.
- **`t.Parallel()` plus `t.Setenv`/`t.Chdir`.** Runtime panic; fix by removing one.
- **Race: shared maps in parallel subtests.** Each parallel subtest must own its data.
- **`time.Sleep` in tests.** Use `synctest` or expose a `Clock` interface and inject a fake.
- **Missing `t.Cleanup`** for resources opened by helpers — leaks across tests.
- **Asserting on log output** instead of behaviour. Make the contract observable some other way.
- **Goroutine leaks in tests.** A test that passes but leaves goroutines running poisons later tests in the same `go test` run. Use `synctest.Wait` or a leak detector.
- **Fuzz target without short-circuit.** Forgetting to `return` on expected error makes the fuzzer flag valid inputs.
- **Container without wait strategy.** Test sometimes connects before the service is up. Always set one.
- **Suite + parallel.** `testify/suite` doesn't support it; don't try.

---

## 17. Pre-commit checklist

- [ ] Test was written first (red), then code (green), then refactored.
- [ ] Table-driven where multiple cases share shape.
- [ ] `t.Parallel()` at both outer and inner levels for parallel-safe tests.
- [ ] `cmp.Diff` (or `errors.Is`/`AsType`) for assertions; failure messages start with function call + input.
- [ ] Helpers call `t.Helper()`.
- [ ] Resources cleaned via `t.Cleanup`, not `defer`.
- [ ] No `time.Sleep` — used `synctest` or injected `Clock`.
- [ ] `t.Context()` used instead of `context.Background()`.
- [ ] `go test -race ./...` passes.
- [ ] No `testify/assert` in unit tests.
- [ ] Build tags set on integration / e2e tests.
- [ ] Wait strategy set on every testcontainer.
- [ ] No `t.Fatal` from non-main goroutines.
