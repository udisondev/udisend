# Modern Go baseline (1.21–1.26) — what to use, what to retire

Reference for the `go-style` skill. This file is normative for any Go code in this project. Read on demand — `SKILL.md` references it for modern-idiom decisions.

This project targets **Go 1.26**. Older patterns from pre-1.21 guides are explicitly superseded; if a third-party guide tells you to write a hand-rolled loop where `slices.X` exists, ignore it.

---

## Built-ins and language

- **`min` / `max` / `clear`** (1.21) — use them. Don't write a 4-line `if a < b` ternary or a manual `for` loop to zero a slice/map.
  ```go
  cap := min(want, MaxBufSize)
  clear(buf)         // zero a slice
  clear(seen)        // empty a map (do not reassign nil)
  ```
- **`new(expr)`** (1.26) — `new` accepts an initial value, returning a pointer to it.
  ```go
  age := new(42)             // *int -> 42
  cfg := new(Config{Port:8080})
  ```
  Use when the only thing you need is `&value` but `value` isn't addressable inline (literal, expression result).
- **`for i := range N`** (1.22) — iterate N times directly.
  ```go
  for i := range 10 { ... }                  // 0..9
  for range retries { try() }                // ignore index
  ```
  Don't write `for i := 0; i < 10; i++` for a fixed count.
- **For-loop variable scoping** (1.22) — each iteration has its own `i`/`v`. Closures capturing the loop variable now do the obvious thing. **Don't** keep writing `i := i` shadow lines; they're noise on Go ≥1.22.
- **`range`-over-func iterators** (1.23) — first-class iterators with `iter.Seq[V]` / `iter.Seq2[K,V]`.
  ```go
  func Lines(r io.Reader) iter.Seq[string] { ... }
  for line := range Lines(r) { ... }
  ```
  Prefer iterators when the producer can stream lazily. Don't return `[]T` if the caller commonly bails early.
- **Self-referential generic constraints** (1.26) — `type Adder[A Adder[A]] interface { Add(A) A }` works. Use for fluent generic APIs (CRDTs, monoids, builder-style combinators).
- **Generic type aliases** (1.24, default-on) — `type Map[K comparable, V any] = map[K]V` is allowed. Use sparingly, only to shorten genuinely awkward repeated parameterisations.

## Standard-library packages — preferred over hand-rolled code

| Use this | Instead of |
|---|---|
| `slices` (1.21) + iterator helpers (1.23) | hand-written sort/search/contains/reverse/index loops |
| `maps` (1.21) + iterator helpers (1.23) | hand-written `for k,v := range m` to copy/clone/keys/values |
| `cmp.Compare` / `cmp.Or` (1.21/1.22) | manual three-way compare, fallback chains |
| `log/slog` (1.21) | stdlib `log`, third-party loggers (zap/zerolog/logrus) |
| `math/rand/v2` (1.22) | `math/rand` (frozen, not deprecated but prefer v2) |
| `crypto/rand.Reader` | always for any cryptographic use, never `math/rand*` |
| `sync.OnceFunc` / `OnceValue` / `OnceValues` (1.21) | hand-rolled `sync.Once` + closure |
| `sync.WaitGroup.Go(fn)` (1.25) | `wg.Add(1); go func(){ defer wg.Done(); ... }()` |
| `context.WithoutCancel` (1.21) | "detach" hacks via `context.Background()` for fire-and-forget cleanup |
| `context.AfterFunc` (1.21) | spawning a goroutine that selects on `ctx.Done()` just to run a callback |
| `context.WithTimeoutCause` / `WithDeadlineCause` (1.21) | bare timeouts when you need to surface the reason |
| `errors.AsType[T]` (1.26) | `var e *MyErr; errors.As(err, &e)` boilerplate |
| `unique.Make` (1.23) | hand-written interning maps |
| `weak.Pointer[T]` (1.24) | hand-rolled weak caches via `runtime.SetFinalizer` tricks |
| `runtime.AddCleanup` (1.24) | `runtime.SetFinalizer` (deprecated in spirit; cleanup is faster, supports multiples, no cycles) |
| `os.Root` (1.24) | `filepath.Clean` + manual prefix checks for sandboxed FS access |
| `crypto/hkdf`, `crypto/pbkdf2`, `crypto/sha3`, `crypto/mlkem`, `crypto/hpke` (1.24/1.26) | `golang.org/x/crypto/...` equivalents — stdlib versions are now canonical |
| `crypto/ecdh` | `crypto/elliptic` (deprecated since 1.21) |
| `unsafe.Slice` / `SliceData` / `String` / `StringData` | `reflect.SliceHeader` / `StringHeader` (deprecated since 1.21) |
| `strings.Lines` / `SplitSeq` / `FieldsSeq` (1.24) | `strings.Split` + range when you don't need the materialised slice |
| `bytes.Lines` / `SplitSeq` / `FieldsSeq` (1.24) | same, for `[]byte` |
| `slog.NewMultiHandler` (1.26) | hand-rolled fan-out handler |
| `net/netip` | `net.IP` for new code (since 1.18; reinforced by 1.26 `Prefix.Compare`) |
| `net.JoinHostPort(host, port)` | `fmt.Sprintf("%s:%d", host, port)` — vet `hostport` analyzer (1.25) flags this; breaks IPv6 |

## slices / maps idioms (1.21–1.23)

```go
// Sort and search
slices.Sort(s)
slices.SortFunc(s, func(a, b T) int { return cmp.Compare(a.K, b.K) })
i, ok := slices.BinarySearch(s, target)

// Membership / index
if slices.Contains(s, x) { ... }
i := slices.IndexFunc(s, pred)

// Mutation
s = slices.Delete(s, i, j)        // zeros removed elements (1.22+)
s = slices.Insert(s, i, vals...)
s = slices.Concat(a, b, c)        // 1.22

// Iterators (1.23)
for i, v := range slices.All(s)        { ... }
for v := range slices.Values(s)        { ... }
for v := range slices.Backward(s)      { ... }
for chunk := range slices.Chunk(s, 64) { ... }
collected := slices.Collect(seq)
sorted    := slices.Sorted(seq)

// Maps
m2 := maps.Clone(m1)
maps.Copy(dst, src)
for k := range maps.Keys(m)   { ... }
for v := range maps.Values(m) { ... }
m  := maps.Collect(seq2)
```

Don't write `func contains[T comparable](s []T, x T) bool { ... }` — it's `slices.Contains`.

## Logging — `log/slog` only

- One logger type for the whole project: `*slog.Logger`. Inject it; don't use the package-level default outside `main`.
- Use `slog.JSONHandler` in production, `slog.TextHandler` in dev.
- Structured key-value calls: `logger.Info("connected", "peer", id, "rtt_ms", rtt.Milliseconds())`.
- Levels: `Debug`, `Info`, `Warn`, `Error`. Don't invent custom levels.
- For request-scoped fields, attach with `logger.With("req_id", id)` and pass the derived logger down.
- Never log secrets or PII.

## Concurrency primitives modernised

- Spawn-and-track: `var wg sync.WaitGroup; wg.Go(work); wg.Wait()` (1.25). Cleaner than the `Add`/`go`/`Done` triplet.
- Single-init values: `cfg := sync.OnceValue(loadConfig)` (1.21). Replaces the `var (once sync.Once; cfg Config); once.Do(...)` boilerplate.
- Atomic bitwise: `atomic.OrUint32`, `atomic.AndUint32` (1.23) — use for lock-free flag manipulation.

## Testing modernised

See the `go-testing` skill for full guidance. Highlights:
- **`testing/synctest`** (1.25, stable) — virtualised time for concurrency tests; replaces flaky `time.Sleep` patterns.
- **`t.Context()`** (1.24) — pre-cancelled at test end. Use instead of `context.Background()`.
- **`t.Chdir(dir)`** (1.24) — auto-restored on cleanup.
- **`t.Attr(key, val)`** (1.25) — structured metadata, surfaced via `-json`.
- **`t.ArtifactDir()`** (1.26) — write per-test artifacts.
- **`testing/cryptotest.SetGlobalRandom`** (1.26) — deterministic crypto RNG for tests.

## HTTP — modernised routing

- Use the enhanced `net/http.ServeMux` patterns (1.22). No third-party router needed for typical services.
  ```go
  mux := http.NewServeMux()
  mux.HandleFunc("GET /items/{id}", show)
  mux.HandleFunc("POST /items", create)
  mux.HandleFunc("GET /files/{path...}", serve)
  // r.PathValue("id"), r.PathValue("path")
  ```
- CSRF: wrap with `http.CrossOriginProtection()` middleware (1.25) — modern, token-free.

## JSON

- **`omitzero`** (1.24) — use for fields whose zero value should be omitted, especially `time.Time` (where `omitempty` is broken). `omitempty` remains for slices/maps where "empty but present" matters.
  ```go
  type T struct {
      When time.Time `json:"when,omitzero"`
      Tags []string  `json:"tags,omitempty"`
  }
  ```
- `encoding/json/v2` (1.25, GOEXPERIMENT) — track but **don't** turn on yet; wait for stabilisation.

## Tooling baseline

- `go.mod` `tool` directive (1.24) — pin lint/codegen tools in `go.mod` instead of a `tools.go` hack.
  ```
  tool github.com/golangci/golangci-lint/cmd/golangci-lint
  ```
  Run via `go tool golangci-lint run`.
- `go.mod` `ignore` directive (1.25) — exclude scratch directories from `./...` patterns cleanly.
- `go fix` (1.26 revamp) — runs the modernizer suite (`//go:fix inline`, etc.). Run periodically; it'll convert pre-1.21 idioms to current ones.
- PGO: drop `default.pgo` next to `main.go` to enable; ~2–14% improvement on hot paths.

## Retired / superseded patterns

| Don't write | Why |
|---|---|
| `i := i` inside a `for` loop closure | 1.22 fixed scoping; line is now noise |
| `rand.Seed(time.Now().UnixNano())` | `math/rand.Seed` is a no-op since 1.24; `math/rand/v2` self-seeds |
| `for i := 0; i < n; i++ {}` for plain counting | use `for i := range n` (1.22) |
| Hand-rolled `Contains`, `Index`, `Reverse`, `Map[K]V` clone, etc. | `slices`/`maps` packages |
| `runtime.SetFinalizer` for new code | `runtime.AddCleanup` (1.24) is the replacement |
| `crypto/elliptic` for ECDH | `crypto/ecdh` |
| `reflect.SliceHeader`/`StringHeader` | `unsafe.Slice`/`SliceData`/`String`/`StringData` |
| `fmt.Sprintf("%s:%d", host, port)` | `net.JoinHostPort` — vet flags this in 1.25 |
| `time.NewTimer(...).Stop(); <-t.C` drain pattern | 1.23+ timers don't deliver stale values; the drain is wrong now |
| `panic(nil)` to signal | panics with `*runtime.PanicNilError` since 1.21 — just don't |
