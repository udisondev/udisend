# Performance — deep reference

Companion to the `go-style` skill. Read on demand for any of:
- benchmarking workflow,
- profiling (CPU / memory / block / mutex / trace / flight recorder),
- escape analysis,
- GC tuning (`GOGC`, `GOMEMLIMIT`, green tea GC),
- PGO collection,
- allocation-reduction tactics,
- struct layout / cache lines,
- `sync.Pool` and lock-free patterns.

`SKILL.md` §12 is the always-on summary. This file is the long-form.

Sources synthesised here: Dave Cheney's *High Performance Go Workshop*, Damian Gryski's *go-perfbook*, the official GC guide, the PGO docs, and Go 1.21–1.26 release notes.

---

## 1. Mental model — three levels of optimisation

Order matters. Each level dominates the level below by ~10×.

1. **Do less.** Algorithmic choice (O(n²) → O(n log n)), data-structure choice (linear scan → map / index), removing redundant work. Most wins live here.
2. **Do it less often.** Cache results, batch operations, deduplicate calls, hoist constant work out of loops, avoid repeated allocations of equivalent objects.
3. **Do it faster.** Constant-factor: reduce allocations, inline, devirtualise, choose better primitives. Wins here are 5–30%; algorithmic wins are 10×–1000×.

Most teams obsess over (3) while (1) is the only place that moves the needle. Always ask "is this work necessary?" before "can this work be faster?".

---

## 2. Benchmarking discipline

You cannot optimise what you cannot measure. Always.

### Writing a benchmark
Use Go 1.24+ `b.Loop()` form (see `go-testing/SKILL.md` §13). It auto-manages timer reset, prevents inlining-driven dead-code elimination, hoists setup out of the timed region.

```go
func BenchmarkEncode(b *testing.B) {
    p := newPacket(1024)
    b.ReportAllocs()
    for b.Loop() {
        _, _ = p.Encode()
    }
}
```

Anti-DCE for legacy `for i := 0; i < b.N` form: assign result to a package-level `var Sink T`.

### Running
```
go test -bench=BenchmarkEncode -count=10 -benchmem -benchtime=2s -cpu=1 ./pkg/...
```
- `-count=10` — 10 runs for statistical stability.
- `-benchmem` — allocations + bytes/op.
- `-benchtime=2s` — longer runs reduce noise.
- `-cpu=1` — single-core eliminates scheduler noise; rerun without to see scaling.

Save:
```
go test -bench=. -count=10 -benchmem ./pkg/x > old.txt
# … make change …
go test -bench=. -count=10 -benchmem ./pkg/x > new.txt
benchstat old.txt new.txt
```

### Reading benchstat
- Variance ≤2% — trustworthy.
- Variance 3–5% — acceptable, take with care.
- Variance >5% — your bench is unstable; fix the bench, not the code.
- p-value <0.05 — change is statistically significant. Above that, it's noise.

### Environment
- Idle, plugged-in machine. No browsers, builds, Slack.
- Disable CPU frequency scaling: `cpupower frequency-set -g performance` on Linux.
- VMs / cloud / shared CI runners are useless for absolute numbers — use them only for relative regression catches.

### Representative inputs
Production data distribution ≠ random. Skewed key distributions, hot/cold ratios, real packet sizes — measure with what you'll actually run. Microbenchmarks lie about whole-system behaviour.

---

## 3. Profiling — pick the right tool

Collect one profile type at a time; mixing distorts results.

| Tool | Reveals | Collect with |
|---|---|---|
| **CPU pprof** | Hot functions, time distribution | `-cpuprofile=cpu.out` or `/debug/pprof/profile` |
| **Heap pprof** | What allocates / what's retained | `-memprofile=mem.out` or `/debug/pprof/heap` |
| **Block pprof** | Goroutines blocked on sync primitives | `runtime.SetBlockProfileRate(1)` + `/debug/pprof/block` |
| **Mutex pprof** | Mutex contention | `runtime.SetMutexProfileFraction(1)` + `/debug/pprof/mutex` |
| **Goroutine pprof** | All live goroutines and their stacks | `/debug/pprof/goroutine` |
| **Execution trace** | Goroutine scheduling, GC pauses, blocking *over time* | `runtime/trace` or `/debug/pprof/trace?seconds=10` |
| **Flight recorder** (1.25+) | Last N seconds of trace, written on event | `runtime/trace.NewFlightRecorder()` |
| **Goroutine leak profile** (1.26 EXP) | Goroutines blocked on unreachable primitives | `GOEXPERIMENT=goroutineleakprofile` + `/debug/pprof/goroutineleak` |

### Workflow
```
go test -cpuprofile=cpu.out -bench=BenchmarkX ./pkg/x
go tool pprof -http=:8080 cpu.out
```
Profiles since Go 1.9 are self-contained — no binary needed.

### Heap profile views
- `-inuse_space` / `-inuse_objects` — currently live (find leaks, retained memory).
- `-alloc_space` / `-alloc_objects` — total allocated since start (find churn, GC pressure).
- For GC-pressure work, look at **alloc_objects** — many small allocations dominate GC cost regardless of total bytes.

### Production profiling
```go
import _ "net/http/pprof"
func init() {
    go http.ListenAndServe("localhost:6060", nil)  // localhost only — never expose
}
```
Sampling: `curl 'http://prod:6060/debug/pprof/profile?seconds=30' > prod.pprof`.

### Execution trace — use when CPU profile says "everything's idle"
CPU profile shows where execution happened. Trace shows where execution *didn't* happen — goroutines blocked on channels, GC stop-the-world, scheduler latency. Overhead since Go 1.21 is 1–2% (down from 10–20%), so traces are now production-safe.

```go
import "runtime/trace"
trace.Start(f); defer trace.Stop()
// or annotate:
ctx, task := trace.NewTask(ctx, "handle-request")
defer task.End()
trace.WithRegion(ctx, "decode", func() { decode(...) })
```
View: `go tool trace trace.out` or [gotraceui](https://gotraceui.dev/).

### Flight recorder (1.25+) — continuous tracing
Keep recent trace data in a ring buffer; flush only when a slow request / error happens.
```go
fr := trace.NewFlightRecorder()
fr.Start()
// on interesting event:
var b bytes.Buffer
fr.WriteTo(&b)
os.WriteFile("trace.out", b.Bytes(), 0o644)
```
Cheap enough to leave on in production.

---

## 4. Escape analysis

Heap allocations cost: a malloc, GC mark-scan time on every cycle, cache pollution. Stack allocations cost: a stack-pointer adjustment. Roughly 100× difference for small objects.

### Inspecting
```
go build -gcflags=-m ./pkg/x          # decisions
go build -gcflags='-m=2' ./pkg/x      # verbose reasoning
go build -gcflags='-m=3' ./pkg/x      # exhaustive
```
Look for `escapes to heap`. Common reasons:
- Returned a pointer to a local.
- Stored value in an `interface{}` (boxing).
- Passed a value through a `func(...interface{})` variadic (e.g., `fmt.Println(x)` — `x` escapes).
- Closure captures a local by reference.
- Slice/map element grew large and forced heap allocation.
- Stored in a slice/map whose lifetime exceeds the function.

### Tactics to keep values on the stack
- Don't return pointers to locals if the value type is small and movable — return value, let caller decide.
- Don't pass `*string`, `*int`, `*bool`, `*interface{}` — fixed-size, no reason to indirect.
- Replace interface parameters with concrete types or generics in hot paths.
- Avoid `fmt.*` in hot paths — they take `...any`, every argument escapes. Use `strconv.*`.
- Pre-size local slices with constant `make([]T, N)` — provably bounded, stays on stack.

### Bounds-check elimination
Sequential, in-order indexing lets the compiler skip per-access bounds checks:
```go
// good — BCE on v[1..7]
_ = v[7]
for i := 0; i < 8; i++ { use(v[i]) }
```
Random-order indexing can't be eliminated. For arrays (not slices), checks are compile-time when indices are constants.

---

## 5. Allocation-reduction tactics

### Pre-size
```go
buf := make([]byte, 0, knownCap)
m   := make(map[K]V, knownN)
```
`append` reallocates by doubling; pre-sizing avoids the chain of intermediate allocations.

### Reset and reuse
```go
buf = buf[:0]                // keeps backing array, len = 0
m = make(map[K]V, len(m))    // until 1.21; since 1.21 use clear(m)
clear(m)                     // 1.21+: empties without reallocating buckets
```

### Buffer-passing API
Hot-path APIs accept a destination buffer the caller owns:
```go
func (p *Packet) AppendTo(dst []byte) []byte { ... }   // not Encode() []byte
```
Caller can recycle `dst`; library doesn't allocate on every call. Stdlib idiom (`time.Time.AppendFormat`, `strconv.AppendInt`).

### `strings.Builder` for concatenation
```go
var b strings.Builder
b.Grow(estimate)
for _, s := range parts { b.WriteString(s) }
result := b.String()
```
`+=` in a loop is O(n²) allocations.

### `strconv` over `fmt`
- `strconv.Itoa(n)` is ~2× faster than `fmt.Sprint(n)` and zero-alloc.
- `strconv.AppendInt(buf, n, 10)` for hot paths — appends to a buffer, no allocation.
- Reserve `fmt.*` for human-formatted output (errors, logs at human level).

### Compile regex once
```go
var packetRE = regexp.MustCompile(`^...$`)   // package-level
```
Never `regexp.MustCompile` in a hot path.

### Pre-create errors
```go
var ErrShort = errors.New("packet too short")     // package-level
return ErrShort                                    // free
return fmt.Errorf("packet too short: %d", n)      // allocates every call
```
`errors.New` returns a pointer to a struct, but reused across all returns — free.

### Avoid `defer` in tight loops
Each `defer` allocates a defer record (cheaper since 1.14, still nonzero). Loops that iterate millions of times should use explicit cleanup. Outside hot loops, defer is fine.

### Reflection is slow
`encoding/json`, `binary.Read/Write`, `text/template` use reflection. For hot wire formats, hand-write encoders. For 80% paths, reflection is fine — measure first.

### `[]byte` ↔ `string` conversions
Each conversion copies. In hot paths:
- Compare `[]byte` to `string` directly: `bytes.Equal(b, []byte(s))` allocates; instead `string(b) == s` may be optimised by the compiler when result doesn't escape.
- Map lookup with `[]byte` key: `m[string(b)]` is special-cased — no allocation.
- Last resort: `unsafe.String(unsafe.SliceData(b), len(b))` for zero-copy. **Read-only**; mutating `b` afterwards is undefined. Document the constraint.

### Pointer-free containers reduce GC scan time
`[]int` scans nothing during GC. `[]*Item` scans every element. For large containers and tight scan budgets, prefer indices into a value slice over pointer slices.

---

## 6. Struct layout and cache

### Field ordering
Modern CPUs fetch 64 bytes per cache line. For frequently-accessed structs:
- Put hot fields first — they share a line with the struct header.
- Group fields accessed together.
- Pad lock-protected fields when each field has its own writer:
  ```go
  type stripe struct {
      mu     sync.Mutex
      counts map[K]int
      _      [40]byte  // pad to 64-byte cache line; prevents false sharing
  }
  ```
- Use `unsafe.Sizeof` and `unsafe.Offsetof` in tests to lock layout.

### False sharing
Two goroutines writing to fields on the same cache line force cache-line bouncing between cores. Symptoms: "atomic incr is slow when sharded across goroutines." Fix: pad each shard.

### Tools
- `dominikh/go-tools/structlayout` — visualises layout and padding.
- `structlayout-optimize` — suggests reorder for minimum padding.

### GC scan cost and pointer placement
GC scans struct fields up to the **last pointer** in the struct. Group all pointer fields at the start:
```go
type Item struct {
    next *Item    // pointers first
    name string   // string contains pointer — also "pointer field"
    id   int64    // pointer-free fields after
    size int64
}
```
Lets GC stop scanning early. For pointer-free large arrays at the end, this is a real win.

---

## 7. Concurrency performance

### Goroutine cost
- ~2 KB starting stack (grows as needed).
- Spawning a goroutine: ~few hundred ns.
- Don't spawn a goroutine for trivially-sequential work — the spawn itself + context switch dominates.

### Channel vs mutex
- Mutex is faster for protecting shared state; channels are clearer for ownership transfer.
- Buffered channels of size 1 are a way to pass mutable ownership without locks; size > 1 is back-pressure.
- Unbuffered channels synchronise — sender blocks until receiver ready.

### Atomics over mutex for simple cases
- Counters: `atomic.AddInt64`, `atomic.LoadInt64` — ~10× faster than `mu.Lock; n++; mu.Unlock`.
- Flags: `atomic.CompareAndSwap`.
- Bitwise: `atomic.OrUint32`, `atomic.AndUint32` (1.23+).
- `atomic.Pointer[T]` (typed) — replaces `unsafe.Pointer` patterns; single-writer, multi-reader pattern is straightforward.

### `sync.RWMutex`
Heavier per-operation than `sync.Mutex`. Only wins when reads vastly dominate (10:1+) and read sections are non-trivial. For short critical sections, plain `Mutex` is faster.

### `sync.Map`
Optimised for two patterns only:
1. Write-once-then-mostly-read.
2. Disjoint key sets per goroutine.
For everything else, `map[K]V + sync.RWMutex` is faster and simpler.

### Lock sharding
High-contention map: shard into N independent `map+mutex` pairs by `hash(key) % N`. Cuts contention proportionally. Common N=16 or 32.

### Don't spin
Failed CAS in a tight loop burns a core. Either back off (exponential), use a real mutex, or rethink the design.

### Goroutine pool — usually unnecessary
Go's scheduler is cheap. Pools add complexity, lifetime management, and can hide leaks. Use only when goroutine startup itself dominates (rare) or you need to bound concurrency.

---

## 8. `sync.Pool` — when, when not

### When it helps
- High-frequency allocate-and-discard of *equivalent* objects.
- The allocation cost (or GC scan cost) is measurably high.
- Object size is non-trivial (small int slices: don't bother).

### Pattern
```go
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func handle() {
    buf := bufPool.Get().(*bytes.Buffer)
    defer func() { buf.Reset(); bufPool.Put(buf) }()
    // ...
}
```

### Caveats
- Pool is cleared on every GC cycle (well, it pre-1.13 was; 1.13+ uses victim cache so survives one cycle). Treat as a *cache hint*, not storage.
- Reset state before `Put`. Forgetting causes data leaks across requests.
- Pooling small objects often costs more than it saves (synchronisation overhead).
- Profile before introducing; verify a real reduction in `alloc_objects`.

### Object pools that aren't `sync.Pool`
- A bounded channel of preallocated objects works for fixed-cardinality pools.
- For large frequently-resized buffers, `bytes.Buffer` + `sync.Pool` is the standard.

---

## 9. GC tuning

### Mental model
- GC cost / cycle ≈ fixed overhead + (scanning cost × live heap).
- GC frequency = allocation rate / heap-growth budget.
- Tuning is trading **memory for CPU** — there is no free lunch.

### `GOGC` (default 100)
Heap can grow to `(live + 100%) × live` before next GC.
- Raise to 200/300 — fewer GCs, more memory, more CPU per cycle.
- Lower to 50 — more GCs, less memory, more CPU overall.
- `off` — only for benchmarks measuring allocator behaviour without GC interference.

### `GOMEMLIMIT` (1.19+)
Soft cap on total runtime memory. Use in containers to avoid OOM kills.
```
GOMEMLIMIT=512MiB ./app
```
- Soft: GC respects 50% CPU ceiling — won't thrash itself to death.
- Leave 5–10% headroom for stacks, goroutines, off-heap allocations.
- Combined with high `GOGC`: program runs with low GC overhead until pressure rises, then GC kicks in.

### Container-aware `GOMAXPROCS` (1.25+)
Default since 1.25. Reads cgroup CPU limits; scales `GOMAXPROCS` accordingly. Auto-updates on limit changes. Disable with `GODEBUG=containermaxprocs=0` only with reason.

### Green Tea GC (1.26 default)
10–40% reduction in GC overhead on small-object-heavy workloads. Default in 1.26; opt-out only for debugging via `GOEXPERIMENT=nogreenteagc`. Removed in 1.27 — assume it's permanent.

### Inspecting GC behaviour
```
GODEBUG=gctrace=1 ./app           # one line per GC cycle
GODEBUG=gcpacertrace=1 ./app      # detailed pacer
```
Look for:
- `runtime.gcAssistAlloc` >5% of CPU profile → application is helping GC because allocation outpaces it. Reduce allocations or raise `GOGC`.
- `runtime.gcBgMarkWorker` time → background GC; if dominant, you have too much live heap or too many pointers.
- `runtime.scanobject` → pointer-density problem; consider value-type containers.

### Linux transparent huge pages
For heaps ≥1 GiB, enabling THP (`defer` mode) is ~10% throughput win:
```
echo defer > /sys/kernel/mm/transparent_hugepage/defrag
echo 0    > /sys/kernel/mm/transparent_hugepage/khugepaged/max_ptes_none
```

---

## 10. PGO (Profile-Guided Optimisation)

Stable since 1.21; default-on if `default.pgo` exists in the main package. Typical gains 2–14%.

### Workflow
1. Add the pprof handler:
   ```go
   import _ "net/http/pprof"
   ```
2. Run a representative workload in production (or a real-world rep benchmark).
3. Capture 30 seconds of CPU profile:
   ```
   curl 'http://prod:6060/debug/pprof/profile?seconds=30' > default.pgo
   ```
4. Place `default.pgo` next to `cmd/<binary>/main.go`. Commit it. `go build` picks it up.
5. Refresh quarterly or after major refactors.

### Multi-binary projects
Each binary needs its own profile. Don't share a single `default.pgo` across `cmd/*` — only the matching profile drives optimisation.

### Merging profiles
```
go tool pprof -proto p1.pprof p2.pprof p3.pprof > merged.pprof
mv merged.pprof default.pgo
```
Make sure all source profiles have the same wall duration so longer ones don't dominate.

### When PGO helps most
- Programs with long-tail CPU: lots of medium-hot functions where the compiler's static heuristics get inlining wrong.
- Type-assertion / interface-heavy code: PGO devirtualises hot calls.

### When PGO doesn't help
- CPU-light, IO-bound services — wall time dominated by syscalls.
- Tiny programs where everything's already inlined.
- Microbenchmarks — they don't represent real call distributions.

### Caveats
- An unrepresentative profile won't make code slower — worst case is no gain.
- Profile drifts as code changes. Stale profile = optimisation applied to wrong functions.
- Build time increases on first build (all packages reconsidered). Incremental builds are cached.

---

## 11. Devirtualisation and inlining

### Inlining
- Compiler inlines small leaf functions (~40 SSA-node budget).
- Mid-stack inlining (1.12+) inlines fast paths through deeper call stacks.
- `go build -gcflags='-m'` shows inlining decisions.
- `//go:noinline` to force-out — for benchmarking individual functions.
- Indirect calls (interface method, function value) generally don't inline.

### Devirtualisation
- Static call (concrete type) → may inline.
- Interface call → indirect, doesn't inline.
- Compiler can devirtualise when type is known statically; PGO extends this to hot paths.
- In hot paths, prefer concrete types or generics — both let the compiler inline.

### Generics performance
- Compiled via gcshape — one instantiation per "shape" (rough type-class).
- For pointer-shaped types, usually one shared instantiation.
- For value-shaped basic types, monomorphised per type — fastest, occasionally slower-than-ideal due to boxing in containers.
- In hot paths, generics are usually competitive with hand-rolled concrete code; rarely faster than the most aggressive concrete code.

---

## 12. Anti-patterns to grep for

| Pattern | Why slow | Fix |
|---|---|---|
| `fmt.Sprintf("%d", n)` in hot path | 2 allocations + reflection | `strconv.Itoa(n)` or `strconv.AppendInt` |
| `for ... { defer ... }` | Defer record per iteration | Hoist defer or call directly |
| `regexp.MustCompile` inside function | Recompiles on every call | Package-level `var` |
| `errors.New(...)` returned per call | Allocation | Package-level `var ErrX` |
| `[]byte(s)` and `string(b)` in hot loop | Copies | `unsafe.String` / `unsafe.Slice` once at boundary |
| `interface{}` parameter in hot path | Boxing → escape | Concrete type or generic |
| `*int`, `*string`, `*bool` arguments | Indirect + escape | Pass by value |
| `make([]T, 0); for { append }` | Repeated reallocation | `make([]T, 0, n)` |
| `m := map[K]V{}; for { m[k]=v }` | Bucket reallocation | `make(map[K]V, n)` |
| `strings.Replace(s, "x", "y", -1)` per call | Allocates | Build a `strings.Replacer` once |
| `runtime.SetFinalizer` for new code | Slow, error-prone | `runtime.AddCleanup` (1.24+) |
| `time.Sleep(d); poll(); ...` | Burns CPU, flaky | Synchronise on a channel / `synctest` in tests |
| Goroutine per item for trivial work | Spawn cost > work | Batch or sequential |
| `sync.RWMutex` for short critical sections | Heavier than `Mutex` | Plain `Mutex` |
| `json.Marshal` in hot path | Reflection | Hand-write encoder or use `encoding/json/v2` (1.25 EXP) |

---

## 13. Order of operations when something is slow

1. **Reproduce** with a stable benchmark or production workload.
2. **CPU profile** — find the hot function. Often surprises you.
3. If CPU is idle but throughput is bad — **execution trace** for blocking, GC pauses, scheduler latency.
4. If allocations dominate the profile — **heap profile** with `alloc_objects`. Find the churn site.
5. Apply targeted change. **Re-benchmark** with `benchstat`. Discard the change if p-value isn't <0.05.
6. Only after the local change is verified, ask: is there an algorithmic win? A batching opportunity? An entire layer of work that's unnecessary?
7. Update the benchmark suite so the win is regression-tested.

If you skipped any step, you're guessing.
