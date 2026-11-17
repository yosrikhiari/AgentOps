# Local GPU Telemetry (Track U) Implementation Plan

> **For the implementing engineer:** this plan assumes you have never seen this repo and do not know Go well. Every task names the file, the test that must fail first, the command to run, and the code to write. Follow it in order. Do not skip the "run it and watch it fail" step — this repo's whole discipline is red-green.
>
> **Read first:** `AGENTS.md` (root), `docs/RULES.md`, `docs/tower-design-system.html`. The rules that will bite you are named inline below.

## Goal

Show how much of this machine's GPU is actually consumed when local models run: utilisation, VRAM used vs total, power, temperature, which models are resident and how much VRAM each holds, and how often Ollama is thrashing models in and out. Live in the Tower console, persisted for a week so you can answer "what happened during that eval run", exported to `/metrics`, and visible on a trace.

## Architecture

One new leaf package `gpu/` that knows how to ask two local daemons the truth and nothing else:

```
nvidia-smi ──┐
             ├──> gpu.Prober (goroutine, one poll per GPU_POLL_INTERVAL)
ollama /api/ps ┘        │
                        ├──> in-memory Snapshot (mutex, read by /v1/gpu and /metrics)
                        └──> gpu_samples rows in Postgres (one INSERT per tick)
                                     │
                      console.SQLStore ┘──> GET /v1/gpu ──> Tower #/gpu
```

`gpu` imports only the standard library. It declares the tiny `Queryer`/`Execer`/`Rows` interfaces this repo already uses (`tracker/tracker.go:59-72`, `evals/corpus.go:103-105,196-217`) so the package never imports `pgx` — `main.go` supplies a `gpuAdapter`, exactly like `trackerAdapter` (`main.go:88-93`) and `routerAdapter` (`main.go:75-86`).

The prober is a sibling of `router.HealthProber` (`router/backend.go:53-123`), not a replacement, and it is wired in the same place (`main.go:864-871`).

## Tech Stack

- Go 1.2x, stdlib only in `gpu/`. The single direct dependency stays `pgx` (`TestPolicySingleDirectDependency`).
- `os/exec` + `encoding/csv` for `nvidia-smi`; `net/http` + `encoding/json` for Ollama.
- Postgres via `migrations/0008_gpu.sql`.
- Hand-written SVG + `h()` in `console/static/app.js`. No framework, no chart library, no hex colours.

## Non-goals (say this out loud so nobody adds it later)

- **No spend, no pricing, no currency.** That is roadmap item #8 in `docs/MARKET.md` and needs sourced prices (`advisor.Rumor`: "a number without a source and a date is gossip"). This plan produces measured watts and nothing else.
- **No AMD/Intel/Apple GPU support.** `nvidia-smi` only. Linux/Windows/macOS-with-NVIDIA all work because we shell out; no driver bindings.
- **No per-request GPU cost attribution.** `nvidia-smi` sees one `ollama.exe` process, so a request cannot honestly be charged VRAM. We correlate by *nearest sample in time* and we label it that way in the UI.
- **No alerting, no paging, no SLO.**

## Settled: two boxes, one measurement

`docs/VRAM.md` describes the **demo box: RTX 4060, 8 GB**. The machine this was drafted on is the **dev box: GTX 1650, 4096 MiB**. Both are real, so:

- The design never hardcodes either number. VRAM total is read at runtime.
- The `advisor.BoxVRAMGB = 8` constant survives **only** as the fallback for `--advise` on a machine with no driver, and the report always prints which budget it used (`measured: NVIDIA GeForce GTX 1650` vs `docs/VRAM.md`).
- `docs/VRAM.md` keeps its 8 GB title and table — that is the demo contract — and gains a short section saying the numbers are a *budget* and `#/gpu` is the *reading*, plus the fact that on the 4 GB dev box the 7B-Q4 tier is correctly vetoed.
- **Acceptance test that proves the pairing:** `go run . --advise` on the dev box must print `4 GB` and `measured:`, and must veto `qwen2.5:7b-instruct-q4_K_M`. The same command on the 4060 must print `8 GB` and not veto it. Same code, two boxes, no flag.

---

## File map (what you will create and touch)

| Path | Change |
|---|---|
| `gpu/gpu.go` | new — `Sample`, `Resident`, `Snapshot`, `Reader` interface, `ReadOnce` |
| `gpu/nvidia.go` | new — `NvidiaSMI` reader + CSV parsing |
| `gpu/ollama.go` | new — `/api/ps` client |
| `gpu/prober.go` | new — polling goroutine, swap counter, `Expose()` |
| `gpu/store.go` | new — `SQLStore` over `gpu_samples`, local `Queryer`/`Execer`/`Rows` |
| `gpu/nvidia_test.go`, `gpu/prober_test.go`, `gpu/store_test.go` | new — offline tests, fake reader/rows |
| `migrations/0008_gpu.sql` | new — `gpu_samples` |
| `router/metrics.go` | edit — `Extra func() string` hook appended by `Expose()` |
| `console/store.go` | edit — `Store` gains `GPUSamples`, `GPUAt`; `SQLStore` implements them |
| `console/handler.go` | edit — `Deps.GPU func() gpu.Snapshot`, `GET /v1/gpu`, `Routes()` |
| `console/console_test.go` | edit — endpoint tests |
| `console/static/app.js` | edit — `pageGPU`, `gpuChart`, `pageTitles`, `pages`, trace chip |
| `console/static/index.html` | edit — nav button under **Operate** |
| `console/static/tower.css` | edit — `/* ===== GPU page ===== */` block |
| `docs/tower-design-system.html` | edit — new example section citing `pageGPU` |
| `advisor/advisor.go` | edit — `AdviseWithBudget`, `Advise` keeps the 8 GB default |
| `main.go` | edit — `gpuAdapter`, prober wiring, purge, advise budget, `/metrics` extra |
| `docs/API.md` | edit — `GPU_SMI`, `GPU_POLL_INTERVAL`, `GPU_HISTORY_KEEP`, `GET /v1/gpu` |
| `docs/VRAM.md` | edit — "measured, not assumed" paragraph (see Decision above) |
| `CHANGELOG.md` | edit — one `## Unreleased` line |

Full local gate, run before you say you are done:

```powershell
gofmt -l .
go vet ./...
go test -count=1 ./...
go build ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
go test -count=1 ./policy/
```

`-race` is **CI-only on this machine**: there is no C compiler (`where.exe gcc`,
`clang`, `zig` all fail and `CGO_ENABLED=0`), so `go test -race` cannot build
the detector here. CI runs it through `VERIFY_RACE=1 bash scripts/verify.sh`.
Locally, get the same signal from `go test -count=1 ./...` plus a read of every
new mutex-guarded field — if a test cannot exercise the concurrency, say so in
the commit body rather than implying `-race` passed.

---

## Task 1 — `gpu.Sample` and the `Reader` interface

**Files:** create `gpu/gpu.go`, `gpu/nvidia_test.go`

**Step 1 — write the failing test.**

```go
package gpu

import "testing"

// TestSampleHasOneUnit proves the whole package agrees that memory is bytes.
// nvidia-smi speaks MiB; the conversion happens once, at the reader boundary,
// so no caller can be off by 2^20.
func TestSampleHasOneUnit(t *testing.T) {
	s := Sample{MemUsedBytes: 1024 * 1024, MemTotalBytes: 4 * 1024 * 1024}
	if got := s.UsedPercent(); got != 25 {
		t.Fatalf("UsedPercent = %v, want 25", got)
	}
	if s.UsedGB() != 0.0009765625 {
		t.Fatalf("UsedGB = %v", s.UsedGB())
	}
}
```

**Step 2 — run it and watch it fail.** `go test ./gpu/` → `undefined: Sample`.

**Step 3 — implement the minimum.** `gpu/gpu.go`:

```go
// Package gpu measures what local model inference is doing to this machine's
// GPU. It shells out to nvidia-smi and asks Ollama which models are resident,
// because those are the only two honest sources: the driver knows
// utilisation/VRAM/power, Ollama knows which model holds it. Every poll is
// cheap (~100 ms) and never happens on a request path.
package gpu

import (
	"context"
	"time"
)

// Sample is one point in time. Memory is bytes everywhere in this package;
// nvidia-smi's MiB is converted once in nvidia.go.
type Sample struct {
	At            time.Time `json:"at"`
	Name          string    `json:"name"`
	UtilPct       float64   `json:"util_pct"`
	MemUsedBytes  int64     `json:"mem_used_bytes"`
	MemTotalBytes int64     `json:"mem_total_bytes"`
	PowerW        *float64  `json:"power_w,omitempty"` // nil when the driver says N/A
	TempC         *float64  `json:"temp_c,omitempty"`
}

func (s Sample) UsedPercent() float64 {
	if s.MemTotalBytes == 0 {
		return 0
	}
	return float64(s.MemUsedBytes) / float64(s.MemTotalBytes) * 100
}

func (s Sample) UsedGB() float64  { return float64(s.MemUsedBytes) / (1 << 30) }
func (s Sample) TotalGB() float64 { return float64(s.MemTotalBytes) / (1 << 30) }

// Resident is one model Ollama is holding in VRAM right now. SizeVRAMBytes is
// Ollama's own number and is the only per-model VRAM attribution that exists:
// nvidia-smi sees a single ollama process.
type Resident struct {
	Name           string    `json:"name"`
	SizeVRAMBytes  int64     `json:"size_vram_bytes"`
	ContextLength  int       `json:"context_length"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// Snapshot is what the prober last saw. Available=false with a non-empty Error
// means the box has no usable nvidia-smi; the console must render that as an
// empty state, never as zero percent.
type Snapshot struct {
	At         time.Time  `json:"at"`
	Sample     Sample     `json:"sample"`
	Resident   []Resident `json:"resident"`
	Available  bool       `json:"available"`
	Error      string     `json:"error,omitempty"`
	SwapsLast10m int      `json:"swaps_last_10m"`
}

// Reader is the one method the prober needs, so tests inject a fake and never
// shell out. Implementations must respect ctx: nvidia-smi
// (exec.CommandContext) and Ollama both get a hard deadline.
type Reader interface {
	Read(ctx context.Context) (Sample, error)
}
```

**Step 4 — run the test; it passes.** `go test ./gpu/ -run TestSampleHasOneUnit -v`

**Step 5 — commit.** `go build ./... && git add gpu && git commit -m "Add a gpu package with one unit for memory"`

---

## Task 2 — parse `nvidia-smi` without a driver on the test machine

**Files:** create `gpu/nvidia.go`, `gpu/nvidia_test.go`

CI has no GPU. Every test in this package parses a **literal CSV string** — no `exec`, ever, in a test.

**Step 1 — write the failing tests.** `gpu/nvidia_test.go`:

```go
package gpu

import "testing"

func TestParseNvidiaSMI(t *testing.T) {
	line := "NVIDIA GeForce GTX 1650, 39, 77, 4096, 3.01, 64"
	s, err := parseSMI(line)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "NVIDIA GeForce GTX 1650" || s.UtilPct != 39 {
		t.Fatalf("got %+v", s)
	}
	if s.MemUsedBytes != 77*(1<<20) || s.MemTotalBytes != 4096*(1<<20) {
		t.Fatalf("memory not converted from MiB: %+v", s)
	}
	if s.PowerW == nil || *s.PowerW != 3.01 || s.TempC == nil || *s.TempC != 64 {
		t.Fatalf("power/temp: %+v", s)
	}
}

// TestParseSMIHandlesNotAvailable is the reason this is not a one-liner:
// laptop and datacenter drivers both report [N/A] for power, and older ones
// for temperature. A sentinel value would be a lie; nil is a missing fact.
func TestParseSMIHandlesNotAvailable(t *testing.T) {
	s, err := parseSMI("Tesla T4, 0, 0, 15360, [N/A], [N/A]")
	if err != nil {
		t.Fatal(err)
	}
	if s.PowerW != nil || s.TempC != nil {
		t.Fatalf("expected nil power/temp, got %+v", s)
	}
}

func TestParseSMIRejectsGarbage(t *testing.T) {
	if _, err := parseSMI("no commas here"); err == nil {
		t.Fatal("want error")
	}
	if _, err := parseSMI("Tesla T4, 0, 0, 15360, 10, 20, 30, 40"); err == nil {
		t.Fatal("want error on wrong field count")
	}
}
```

**Step 2 — run, watch fail.** `go test ./gpu/ -run TestParse -v`

**Step 3 — implement.** `gpu/nvidia.go`:

```go
package gpu

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const smiQuery = "name,utilization.gpu,memory.used,memory.total,power.draw,temperature.gpu"

// NvidiaSMI reads the first GPU via the driver's own CLI.
type NvidiaSMI struct {
	Path    string        // "" means "nvidia-smi" from PATH
	Timeout time.Duration // default 3s
}

// NewNvidiaSMI returns a reader pointed at path (or PATH), with a 3s cap.
// The cap matters: a wedged driver must not wedge the prober goroutine.
func NewNvidiaSMI(path string) *NvidiaSMI {
	if path == "" {
		path = "nvidia-smi"
	}
	return &NvidiaSMI{Path: path, Timeout: 3 * time.Second}
}

func (n *NvidiaSMI) Read(ctx context.Context) (Sample, error) {
	if n.Timeout == 0 {
		n.Timeout = 3 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, n.Path,
		"--query-gpu="+smiQuery, "--format=csv,noheader,nounits").Output()
	if err != nil {
		return Sample{}, fmt.Errorf("nvidia-smi: %w", err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	s, err := parseSMI(line)
	if err != nil {
		return Sample{}, err
	}
	s.At = time.Now().UTC()
	return s, nil
}

func parseSMI(line string) (Sample, error) {
	f := strings.Split(line, ",")
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	if len(f) != 6 {
		return Sample{}, fmt.Errorf("nvidia-smi: want 6 fields, got %d in %q", len(f), line)
	}
	util, err := strconv.ParseFloat(f[1], 64)
	if err != nil {
		return Sample{}, fmt.Errorf("nvidia-smi utilisation %q: %w", f[1], err)
	}
	usedMiB, err := strconv.ParseFloat(f[2], 64)
	if err != nil {
		return Sample{}, fmt.Errorf("nvidia-smi memory.used %q: %w", f[2], err)
	}
	totalMiB, err := strconv.ParseFloat(f[3], 64)
	if err != nil {
		return Sample{}, fmt.Errorf("nvidia-smi memory.total %q: %w", f[3], err)
	}
	return Sample{
		Name:          f[0],
		UtilPct:       util,
		MemUsedBytes:  int64(usedMiB * (1 << 20)),
		MemTotalBytes: int64(totalMiB * (1 << 20)),
		PowerW:        optionalFloat(f[4]),
		TempC:         optionalFloat(f[5]),
	}, nil
}

// optionalFloat returns nil for the driver's "[N/A]" and "[Not Supported]".
func optionalFloat(s string) *float64 {
	s = strings.Trim(s, "[] ")
	if s == "" || strings.EqualFold(s, "N/A") || strings.EqualFold(s, "Not Supported") {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}
```

**Step 4 — tests pass.** `go test ./gpu/ -v`

**Step 5 — prove the parser against the real driver, by eye, once.** There is no test for this on purpose (CI has no GPU). Run the exact command the reader runs:

```powershell
nvidia-smi --query-gpu=name,utilization.gpu,memory.used,memory.total,power.draw,temperature.gpu --format=csv,noheader,nounits
```

Expect **one line, six comma-separated fields** — `NVIDIA GeForce GTX 1650, 39, 77, 4096, 3.01, 64` on the dev box — which is exactly the literal in `TestParseNvidiaSMI`. If this box prints fewer fields or a different order, the test fixture is wrong and the test must change to match reality, not the other way round. On a box with no NVIDIA driver expect a non-zero exit; that becomes the prober's empty state (Task 4) and nothing else.

Real end-to-end proof of `Read` happens in Task 6 (a row in `gpu_samples` every ten seconds) and Task 7 (`gpu_*` in `/metrics`) — this step only confirms the fixture is not fiction.

**Step 6 — commit.** `git commit -am "Parse nvidia-smi, including the N/A cases"`

---

## Task 3 — Ollama `/api/ps` is the only per-model VRAM source

**Files:** create `gpu/ollama.go`, append to `gpu/nvidia_test.go` (or a new `gpu/ollama_test.go` — prefer the new file)

**Step 1 — failing test.** `gpu/ollama_test.go`:

```go
package gpu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaPSDecodesResident(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[
			{"name":"qwen2.5:3b-instruct","size":2500000000,"size_vram":2100000000,
			 "context_length":4096,"expires_at":"2026-11-16T12:00:00Z"}]}`))
	}))
	defer srv.Close()

	got, err := OllamaPS{URL: srv.URL}.Resident(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "qwen2.5:3b-instruct" {
		t.Fatalf("got %+v", got)
	}
	if got[0].SizeVRAMBytes != 2100000000 || got[0].ContextLength != 4096 {
		t.Fatalf("got %+v", got[0])
	}
}

// TestOllamaPSEmptyIsNotAnError: no resident model is a normal state
// (idle box), and the console shows "nothing resident", not an error.
func TestOllamaPSEmptyIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()
	got, err := OllamaPS{URL: srv.URL}.Resident(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}
```

**Step 2 — watch fail.** `go test ./gpu/ -run OllamaPS -v`

**Step 3 — implement.** `gpu/ollama.go`:

```go
package gpu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// OllamaPS lists the models Ollama is holding in VRAM. This is the only
// per-model VRAM attribution that exists on this box: nvidia-smi reports one
// ollama process, so splitting its memory across models would be invention.
type OllamaPS struct {
	URL     string // e.g. http://localhost:11434 — reuse OLLAMA_URL
	Timeout time.Duration
}

func (o OllamaPS) Resident(ctx context.Context) ([]Resident, error) {
	if o.Timeout == 0 {
		o.Timeout = 3 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, o.URL+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama /api/ps: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama /api/ps: status %d", resp.StatusCode)
	}
	var body struct {
		Models []struct {
			Name          string    `json:"name"`
			SizeVRAM      int64     `json:"size_vram"`
			ContextLength int       `json:"context_length"`
			ExpiresAt     time.Time `json:"expires_at"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("ollama /api/ps: %w", err)
	}
	out := make([]Resident, 0, len(body.Models))
	for _, m := range body.Models {
		out = append(out, Resident{Name: m.Name, SizeVRAMBytes: m.SizeVRAM,
			ContextLength: m.ContextLength, ExpiresAt: m.ExpiresAt})
	}
	return out, nil
}
```

**Step 4 — pass.** `go test ./gpu/ -run OllamaPS -v`

**Step 5 — commit.** `git commit -am "Read Ollama's resident models as the per-model VRAM source"`

---

## Task 4 — the prober: snapshot, swap counting, and a metrics exposition

**Files:** create `gpu/prober.go`, `gpu/prober_test.go`

Mirror `router.HealthProber` (`router/backend.go:53-123`): a struct with a mutex, `ProbeOnce(ctx)` for tests and boot, `Run(ctx, every)` for production.

**Step 1 — failing tests.** `gpu/prober_test.go`:

```go
package gpu

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeReader struct {
	samples []Sample
	err     error
	i       int
}

func (f *fakeReader) Read(ctx context.Context) (Sample, error) {
	if f.err != nil {
		return Sample{}, f.err
	}
	s := f.samples[f.i%len(f.samples)]
	f.i++
	return s, nil
}

type fakeModels struct{ sets [][]Resident }

func (f *fakeModels) Resident(ctx context.Context) ([]Resident, error) {
	if len(f.sets) == 0 {
		return nil, nil
	}
	set := f.sets[0]
	if len(f.sets) > 1 {
		f.sets = f.sets[1:]
	}
	return set, nil
}

func TestProberSnapshotAfterOneProbe(t *testing.T) {
	p := NewProber(&fakeReader{samples: []Sample{{UtilPct: 42, MemUsedBytes: 1 << 30, MemTotalBytes: 4 << 30}}}, nil, nil)
	p.ProbeOnce(context.Background())
	snap := p.Snapshot()
	if !snap.Available || snap.Sample.UtilPct != 42 {
		t.Fatalf("got %+v", snap)
	}
}

// TestProberUnavailableIsNotZeroPercent is the honesty rule: no driver means
// "no data", and the console must say that rather than draw a flat 0% line.
func TestProberUnavailableIsNotZeroPercent(t *testing.T) {
	p := NewProber(&fakeReader{err: errors.New("nvidia-smi: not found")}, nil, nil)
	p.ProbeOnce(context.Background())
	snap := p.Snapshot()
	if snap.Available {
		t.Fatal("available should be false")
	}
	if !strings.Contains(snap.Error, "nvidia-smi") {
		t.Fatalf("error not surfaced: %q", snap.Error)
	}
}

// TestProberCountsModelSwaps: every change in the resident set is a swap —
// the thing that turns a 48-pair eval into a 90-minute one (docs/VRAM.md).
func TestProberCountsModelSwaps(t *testing.T) {
	models := &fakeModels{sets: [][]Resident{
		{{Name: "qwen2.5:3b-instruct"}},
		{{Name: "qwen3:8b"}},
		{{Name: "qwen3:8b"}},
		{{Name: "qwen2.5:3b-instruct"}},
	}}
	p := NewProber(&fakeReader{samples: []Sample{{UtilPct: 1}}}, models, nil)
	for i := 0; i < 4; i++ {
		p.ProbeOnce(context.Background())
	}
	if got := p.Snapshot().SwapsLast10m; got != 2 {
		t.Fatalf("swaps = %d, want 2", got)
	}
}

func TestProberExpose(t *testing.T) {
	p := NewProber(&fakeReader{samples: []Sample{{Name: "X", UtilPct: 50,
		MemUsedBytes: 2 << 30, MemTotalBytes: 4 << 30}}}, nil, nil)
	p.ProbeOnce(context.Background())
	out := p.Expose()
	for _, want := range []string{
		"gpu_utilization_percent 50",
		"gpu_memory_used_bytes 2147483648",
		"gpu_memory_total_bytes 4294967296",
		"gpu_swap_events_total 0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// TestProberExposeUnavailable: a Prometheus scrape must not read 0 when the
// truth is "no GPU". Emit a labelled 0 for availability instead.
func TestProberExposeUnavailable(t *testing.T) {
	p := NewProber(&fakeReader{err: errors.New("nope")}, nil, nil)
	p.ProbeOnce(context.Background())
	out := p.Expose()
	if !strings.Contains(out, "gpu_up 0") {
		t.Fatalf("want gpu_up 0, got:\n%s", out)
	}
	if strings.Contains(out, "gpu_utilization_percent") {
		t.Fatalf("must not emit utilisation without a sample:\n%s", out)
	}
}
```

**Step 2 — watch fail.** `go test ./gpu/ -run Prober -v`

**Step 3 — implement.** `gpu/prober.go`:

```go
package gpu

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Prober polls the driver and Ollama on an interval and keeps the last answer
// in memory. It never runs on a request path: nvidia-smi costs ~100 ms.
type Prober struct {
	reader Reader
	models modelLister
	store  SampleStore // nil in tests and when Postgres is disabled
	now    func() time.Time

	mu      sync.RWMutex
	snap    Snapshot
	swaps   []time.Time // timestamps of model-set changes, last 10 min kept
	lastSet string      // sorted resident names, for change detection
	seeded  bool        // the first good read is the baseline, not a swap
}

// modelLister is OllamaPS; an interface so tests inject a fake.
type modelLister interface {
	Resident(ctx context.Context) ([]Resident, error)
}

// SampleStore persists one sample per tick. main.go supplies SQLStore.
type SampleStore interface {
	InsertSample(ctx context.Context, s Sample) error
}

func NewProber(r Reader, m modelLister, store SampleStore) *Prober {
	return &Prober{reader: r, models: m, store: store, now: func() time.Time { return time.Now().UTC() }}
}

// ProbeOnce reads both sources now. Errors are recorded in the snapshot, never
// returned: a missing driver is a state the console renders, not a crash.
func (p *Prober) ProbeOnce(ctx context.Context) {
	snap := Snapshot{At: p.now()}

	s, err := p.reader.Read(ctx)
	if err != nil {
		snap.Available = false
		snap.Error = err.Error()
	} else {
		snap.Available = true
		snap.Sample = s
		if p.store != nil {
			// best-effort: a dead database must not stop local measurement
			_ = p.store.InsertSample(ctx, s)
		}
	}

	modelsOK := false
	if p.models != nil {
		if res, err := p.models.Resident(ctx); err == nil {
			snap.Resident = res
			modelsOK = true
		} else if snap.Error == "" {
			snap.Error = err.Error()
		}
	}

	p.mu.Lock()
	// Swap counting is set-based, not per-probe. The first good read only
	// establishes the baseline; after that a change in the sorted set of
	// resident names is one swap and a repeat of the same set is none. A failed
	// Ollama read leaves the previous set alone: "we could not ask" is not the
	// same fact as "the models were unloaded".
	if modelsOK {
		key := residentKey(snap.Resident)
		if !p.seeded {
			p.seeded, p.lastSet = true, key
		} else if key != p.lastSet {
			p.swaps = append(p.swaps, snap.At)
			p.lastSet = key
		}
	}
	// Prune so SwapsLast10m cannot grow forever.
	cutoff := snap.At.Add(-10 * time.Minute)
	kept := p.swaps[:0]
	for _, ts := range p.swaps {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	p.swaps = kept
	snap.SwapsLast10m = len(kept)
	p.snap = snap
	p.mu.Unlock()
}

// Run probes until ctx is cancelled, immediately then every `every`.
func (p *Prober) Run(ctx context.Context, every time.Duration) {
	p.ProbeOnce(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.ProbeOnce(ctx)
		}
	}
}

func (p *Prober) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := p.snap
	out.Resident = append([]Resident(nil), p.snap.Resident...)
	return out
}

func residentKey(rs []Resident) string {
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// Expose renders the Prometheus exposition main.go appends to /metrics.
func (p *Prober) Expose() string {
	snap := p.Snapshot()
	var sb strings.Builder
	sb.WriteString("# HELP gpu_up 1 if the last nvidia-smi read succeeded.\n# TYPE gpu_up gauge\n")
	if snap.Available {
		sb.WriteString("gpu_up 1\n")
	} else {
		sb.WriteString("gpu_up 0\n")
	}
	sb.WriteString(fmt.Sprintf("# HELP gpu_swap_events_total Ollama model swaps in the last 10 minutes.\n# TYPE gpu_swap_events_total gauge\ngpu_swap_events_total %d\n", snap.SwapsLast10m))
	if !snap.Available {
		return sb.String()
	}
	sb.WriteString("# HELP gpu_utilization_percent GPU utilisation.\n# TYPE gpu_utilization_percent gauge\n")
	sb.WriteString(fmt.Sprintf("gpu_utilization_percent %g\n", snap.Sample.UtilPct))
	sb.WriteString("# HELP gpu_memory_used_bytes VRAM in use.\n# TYPE gpu_memory_used_bytes gauge\n")
	sb.WriteString(fmt.Sprintf("gpu_memory_used_bytes %d\n", snap.Sample.MemUsedBytes))
	sb.WriteString("# HELP gpu_memory_total_bytes VRAM installed.\n# TYPE gpu_memory_total_bytes gauge\n")
	sb.WriteString(fmt.Sprintf("gpu_memory_total_bytes %d\n", snap.Sample.MemTotalBytes))
	if snap.Sample.PowerW != nil {
		sb.WriteString(fmt.Sprintf("# HELP gpu_power_watts Board power draw.\n# TYPE gpu_power_watts gauge\ngpu_power_watts %g\n", *snap.Sample.PowerW))
	}
	if snap.Sample.TempC != nil {
		sb.WriteString(fmt.Sprintf("# HELP gpu_temperature_celsius GPU temperature.\n# TYPE gpu_temperature_celsius gauge\ngpu_temperature_celsius %g\n", *snap.Sample.TempC))
	}
	return sb.String()
}
```

**Step 4 — pass.** `go test ./gpu/ -v`

**Step 5 — commit.** `git commit -am "Poll the GPU, count model swaps, expose gpu_* gauges"`

---

## Task 5 — persist a week of samples

**Files:** create `migrations/0008_gpu.sql`, `gpu/store.go`, `gpu/store_test.go`

Numbering must be contiguous (`TestPolicyMigrationsContiguous`): after `0007_audit_log.sql` the next is **0008** and nothing else.

**Step 1 — write the migration.**

```sql
-- Track U: local GPU telemetry. One row per poll (~10 s by default), pruned by
-- GPU_HISTORY_KEEP. Memory is bytes; the reader converted nvidia-smi's MiB
-- once so every reader of this table agrees.
CREATE TABLE IF NOT EXISTS gpu_samples (
    id           BIGSERIAL PRIMARY KEY,
    ts           TIMESTAMPTZ NOT NULL,
    gpu_name     TEXT NOT NULL,
    util_pct     DOUBLE PRECISION NOT NULL,
    mem_used     BIGINT NOT NULL,
    mem_total    BIGINT NOT NULL,
    power_w      DOUBLE PRECISION,
    temp_c       DOUBLE PRECISION
);

CREATE INDEX IF NOT EXISTS gpu_samples_ts_idx ON gpu_samples (ts DESC);
```

**Step 2 — failing tests.** `gpu/store_test.go`:

```go
package gpu

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeExec struct {
	sql  string
	args []any
}

func (f *fakeExec) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	f.sql, f.args = sql, args
	return 1, nil
}

type fakeQueryer struct{ rows Rows }

func (f *fakeQueryer) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return f.rows, nil
}

type oneRow struct {
	scan func(dest ...any) error
	done bool
}

func (o *oneRow) Next() bool { if o.done { return false }; o.done = true; return true }
func (o *oneRow) Scan(dest ...any) error { return o.scan(dest...) }
func (o *oneRow) Err() error { return nil }
func (o *oneRow) Close() {}

func TestInsertSample(t *testing.T) {
	ex := &fakeExec{}
	s := SQLStore{Exec: ex}
	if err := s.InsertSample(context.Background(), Sample{
		At: time.Unix(0, 0).UTC(), Name: "GTX 1650", UtilPct: 39,
		MemUsedBytes: 77 << 20, MemTotalBytes: 4096 << 20,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ex.sql, "INSERT INTO gpu_samples") {
		t.Fatalf("sql = %s", ex.sql)
	}
	if len(ex.args) != 8 {
		t.Fatalf("args = %d, want 8", len(ex.args))
	}
}

// TestSeriesMapsRows is the regression test for the bug Track S shipped: a
// query naming a column the migration did not create 502'd in production.
func TestSeriesMapsRows(t *testing.T) {
	ts := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	rows := &oneRow{scan: func(dest ...any) error {
		*dest[0].(*time.Time) = ts
		*dest[1].(*string) = "GTX 1650"
		*dest[2].(*float64) = 39
		*dest[3].(*int64) = 77 << 20
		*dest[4].(*int64) = 4096 << 20
		*dest[5].(**float64) = nil
		*dest[6].(**float64) = nil
		return nil
	}}
	got, err := SQLStore{Query: &fakeQueryer{rows: rows}}.Series(context.Background(), ts, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UtilPct != 39 || got[0].At != ts {
		t.Fatalf("got %+v", got)
	}
}
```

**Step 3 — watch fail.** `go test ./gpu/ -run 'Insert|Series' -v`

**Step 4 — implement.** `gpu/store.go` — the same tiny-interface trick as `tracker/tracker.go:59-72`:

```go
package gpu

import (
	"context"
	"time"
)

// The three interfaces below are the repo's established shape (tracker doubles
// them). main.go's gpuAdapter satisfies them; this package never imports pgx.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
}

type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

type SQLStore struct {
	Exec  Execer
	Query Queryer
}

func (s SQLStore) InsertSample(ctx context.Context, x Sample) error {
	_, err := s.Exec.Exec(ctx,
		`INSERT INTO gpu_samples (ts, gpu_name, util_pct, mem_used, mem_total, power_w, temp_c)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		x.At, x.Name, x.UtilPct, x.MemUsedBytes, x.MemTotalBytes, x.PowerW, x.TempC)
	return err
}

// Series returns samples newest-first. since bounds the window; limit bounds
// the rows so a week-long window cannot return five figures to a browser.
func (s SQLStore) Series(ctx context.Context, since time.Time, limit int) ([]Sample, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT ts, gpu_name, util_pct, mem_used, mem_total, power_w, temp_c
		 FROM gpu_samples WHERE ts >= $1 ORDER BY ts DESC LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var x Sample
		if err := rows.Scan(&x.At, &x.Name, &x.UtilPct, &x.MemUsedBytes, &x.MemTotalBytes, &x.PowerW, &x.TempC); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Nearest returns the sample closest in time to at, within tolerance — the
// honest way to say "the GPU during this request" without claiming a
// per-request measurement that does not exist.
func (s SQLStore) Nearest(ctx context.Context, at time.Time, tolerance time.Duration) (Sample, bool, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT ts, gpu_name, util_pct, mem_used, mem_total, power_w, temp_c
		 FROM gpu_samples WHERE ts BETWEEN $1 AND $2
		 ORDER BY abs(extract(epoch FROM ts - $3)) LIMIT 1`,
		at.Add(-tolerance), at.Add(tolerance), at)
	if err != nil {
		return Sample{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Sample{}, false, rows.Err()
	}
	var x Sample
	if err := rows.Scan(&x.At, &x.Name, &x.UtilPct, &x.MemUsedBytes, &x.MemTotalBytes, &x.PowerW, &x.TempC); err != nil {
		return Sample{}, false, err
	}
	return x, true, nil
}

// Purge drops samples older than keep. Called at boot and hourly.
func (s SQLStore) Purge(ctx context.Context, keep time.Duration) (int64, error) {
	return s.Exec.Exec(ctx,
		`DELETE FROM gpu_samples WHERE ts < now() - make_interval(secs => $1)`, keep.Seconds())
}
```

> `make_interval(secs => $1)` and not `($1 || ' days')::interval` — the latter is the exact bug `TestPurgeConversationsBuildsTypedInterval` guards. Use the typed constructor.

**Step 5 — pass.** `go test ./gpu/ -v && go test -count=1 ./policy/ -run TestPolicyMigrationsContiguous`

**Step 6 — commit.** `git commit -am "Persist GPU samples in a prepared migration"`

---

## Task 6 — wire the prober into the binary

> **Execution order:** this task needs `router.Metrics.Extra`, which Task 7 adds. Do Task 7 first (it is independent and small), then come back here. Otherwise `main.go` will not compile.

**Files:** `main.go` (adapter near `main.go:88-93`, wiring near `main.go:858-871`, console deps near `main.go:931`)

**Step 1 — add the adapter**, exactly like `trackerAdapter`:

```go
// gpuAdapter exists only because gpu.Rows is a distinct named type.
type gpuAdapter struct {
	pgAdapter
}

func (g gpuAdapter) Query(ctx context.Context, sql string, args ...any) (gpu.Rows, error) {
	rows, err := g.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
```

**Step 2 — construct and run it** right after the health prober starts (`main.go:864-871`):

```go
gpuStore := gpu.SQLStore{Exec: pgAdapter{pool}, Query: gpuAdapter{pgAdapter{pool}}}
gpuProber := gpu.NewProber(
	gpu.NewNvidiaSMI(envOr("GPU_SMI", "nvidia-smi")),
	gpu.OllamaPS{URL: cfg.OllamaURL},
	gpuStore,
)
go gpuProber.Run(probeCtx, gpuPollInterval())
srv.Metrics.Extra = gpuProber.Expose   // see Task 7
gpuKeep := gpuHistoryKeep()
if n, err := gpuStore.Purge(context.Background(), gpuKeep); err == nil && n > 0 {
	log.Printf("gpu: dropped %d samples older than %s", n, gpuKeep)
}
```

**Step 3 — config helpers** (in `config.go`, beside `envOr`):

```go
// gpuPollInterval is how often nvidia-smi is asked. 10s is one order of
// magnitude cheaper than the 15s health probe's blast radius and still dense
// enough to see a model swap.
func gpuPollInterval() time.Duration {
	d, err := time.ParseDuration(envOr("GPU_POLL_INTERVAL", "10s"))
	if err != nil || d < time.Second {
		return 10 * time.Second
	}
	return d
}

func gpuHistoryKeep() time.Duration {
	d, err := time.ParseDuration(envOr("GPU_HISTORY_KEEP", "168h"))
	if err != nil || d < time.Hour {
		return 168 * time.Hour
	}
	return d
}
```

**Step 4 — build and boot.** `go build ./...` then run the binary with Postgres up; expect the migration `0008` to apply and, if this box has a driver, one `gpu_samples` row every 10 s:

```powershell
docker compose up -d postgres
go run .
# in another shell
docker compose exec postgres psql -U agentops -d agentops -c "select count(*) from gpu_samples"
```

**Step 5 — commit.** `git commit -am "Sample the GPU every ten seconds in the server"`

---

## Task 7 — export to `/metrics` without teaching `router` about GPUs

**Files:** `router/metrics.go`, `router/router_test.go` (or a new `router/metrics_test.go`)

**Step 1 — failing tests.** Create `router/metrics_test.go` with all three. The third is not optional: it is the test that forces the callback out of the lock.

```go
func TestMetricsExtraIsAppended(t *testing.T) {
	m := NewMetrics()
	m.Extra = func() string { return "gpu_up 1\n" }
	if !strings.Contains(m.Expose(), "gpu_up 1") {
		t.Fatalf("extra not appended:\n%s", m.Expose())
	}
}

func TestMetricsExtraNilIsSafe(t *testing.T) {
	if !strings.Contains(NewMetrics().Expose(), "router_requests_total") {
		t.Fatal("base exposition missing")
	}
}

// TestMetricsExtraRunsOutsideTheLock pins the contract that Expose never calls a
// foreign callback while holding m.mu: Extra here takes m.mu itself, so an
// implementation that appends inside the lock deadlocks and trips the timeout.
func TestMetricsExtraRunsOutsideTheLock(t *testing.T) {
	m := NewMetrics()
	m.Extra = func() string {
		m.SetBackendUp("probe", true)
		return "gpu_up 1\n"
	}
	done := make(chan string, 1)
	go func() { done <- m.Expose() }()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	select {
	case got := <-done:
		if !strings.Contains(got, "gpu_up 1") {
			t.Fatalf("extra not appended:\n%s", got)
		}
	case <-timeout.C:
		t.Fatal("Expose called Extra while holding the lock: deadlock")
	}
}
```

**Step 2 — watch fail.** `go test ./router/ -run TestMetricsExtra -v`

**Step 3 — implement.** Add the field to `Metrics`:

```go
// Extra, when set, appends another package's exposition to /metrics. main.go
// points it at the GPU prober, so router stays ignorant of GPUs and gpu stays
// ignorant of router.
Extra func() string
```

Then split the existing method. Rename today's `Expose` to `exposeLocked` and **delete its `m.mu.Lock()` / `defer m.mu.Unlock()`** — the new wrapper holds the lock, and Go mutexes are not reentrant, so leaving the lock inside `exposeLocked` self-deadlocks on every call:

```go
// exposeLocked renders the router-owned series. The caller must hold m.mu.
func (m *Metrics) exposeLocked() string {
	var sb strings.Builder
	// ...today's body, unchanged, minus the two lock lines...
	return sb.String()
}

// Expose renders the Prometheus exposition. Extra runs after the lock is
// released: it belongs to another package and may take locks of its own.
func (m *Metrics) Expose() string {
	m.mu.Lock()
	s := m.exposeLocked()
	extra := m.Extra
	m.mu.Unlock()
	if extra != nil {
		s += extra()
	}
	return s
}
```

Note what this forbids: `sb.WriteString(m.Extra())` inside the locked body. That reads as the obvious one-line change and is exactly the bug — the callback would run under router's mutex, and a prober that touches its own state from a metrics scrape can then deadlock against the scrape goroutine.

**Step 4 — pass.** `go test ./router/ -v`

**Step 5 — verify by hand.** `curl -s localhost:8080/metrics | Select-String -Pattern '^gpu_'`

**Step 6 — commit.** `git commit -am "Append GPU gauges to the metrics endpoint"`

---

## Task 8 — `GET /v1/gpu`

**Files:** `console/store.go`, `console/handler.go`, `console/console_test.go`, `main.go` (wire it, Step 5)

**Step 1 — failing tests.** There is no `newTestHandler` in this package; every test builds `New(Deps{...})` and drives it with `httptest`. Five tests: four on the handler through the `memStore` fake, and one on `SQLStore.GPUSamples` through a small `gpu.Queryer` fake (the row-mapping a fake store cannot catch).

```go
// TestGPUEndpointLive: live snapshot from the prober, history from the store.
func TestGPUEndpointLive(t *testing.T) {
	now := time.Now().UTC()
	h := New(Deps{
		Store: &memStore{gpuHist: []gpu.Sample{{At: now, Name: "GTX 1650", UtilPct: 12}}},
		GPU: func() gpu.Snapshot {
			return gpu.Snapshot{At: now, Available: true, SwapsLast10m: 2,
				Sample:   gpu.Sample{Name: "GTX 1650", UtilPct: 39, MemUsedBytes: 77 << 20, MemTotalBytes: 4096 << 20},
				Resident: []gpu.Resident{{Name: "qwen2.5:3b-instruct", SizeVRAMBytes: 2 << 30}}}
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/gpu", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Now struct {
			Available bool `json:"available"`
			Sample    struct {
				UtilPct float64 `json:"util_pct"`
			} `json:"sample"`
			Resident []struct {
				Name string `json:"name"`
			} `json:"resident"`
			Swaps10m int `json:"swaps_last_10m"`
		} `json:"now"`
		History []struct {
			UtilPct float64 `json:"util_pct"`
		} `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Now.Available || got.Now.Sample.UtilPct != 39 || got.Now.Swaps10m != 2 || len(got.Now.Resident) != 1 {
		t.Fatalf("now = %+v", got.Now)
	}
	if len(got.History) != 1 || got.History[0].UtilPct != 12 {
		t.Fatalf("history = %+v", got.History)
	}
}

// TestGPUEndpointNoProber: no GPU is not an error and not a 500. The page must
// be able to tell "no driver" from "idle GPU", so available=false carries an
// explanation.
func TestGPUEndpointNoProber(t *testing.T) {
	h := New(Deps{Store: &memStore{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/gpu", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("body = %s", rec.Body)
	}
}

// TestGPUEndpointEmptyHistoryIsAList: gpu.Series returns a nil slice for no
// rows, which marshals as null. The page does history.map(...) — null throws.
func TestGPUEndpointEmptyHistoryIsAList(t *testing.T) {
	h := New(Deps{Store: &memStore{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/gpu", nil))
	if !strings.Contains(rec.Body.String(), `"history":[]`) {
		t.Fatalf("history is not an empty array: %s", rec.Body)
	}
}

// TestGPUEndpointKeepsNowWhenHistoryFails: the live reading comes from the
// prober, not the database. A dead Postgres must not hide the card it is meant
// to describe, so the read stays 200 with "now" and an explained empty chart -
// and history_error is what stops "no samples yet" looking like "store down".
func TestGPUEndpointKeepsNowWhenHistoryFails(t *testing.T) {
	h := New(Deps{
		Store: &memStore{err: errors.New("no database")},
		GPU: func() gpu.Snapshot {
			return gpu.Snapshot{At: time.Now().UTC(), Available: true,
				Sample: gpu.Sample{Name: "GTX 1650", UtilPct: 39}}
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/gpu", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Now struct {
			Available bool `json:"available"`
			Sample    struct {
				UtilPct float64 `json:"util_pct"`
			} `json:"sample"`
		} `json:"now"`
		History      []gpu.Sample `json:"history"`
		HistoryError string       `json:"history_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Now.Available || got.Now.Sample.UtilPct != 39 {
		t.Fatalf("now = %+v", got.Now)
	}
	if got.HistoryError == "" {
		t.Fatalf("store failure is not explained: %s", rec.Body)
	}
	if got.History == nil {
		t.Fatalf("history is null, not []: %s", rec.Body)
	}
}

// TestSQLStoreGPUSamplesMapsEveryColumn: a fake store cannot catch a Scan that
// silently leaves mem_used or power_w at zero.
func TestSQLStoreGPUSamplesMapsEveryColumn(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	q := fakeGPUQueryer{rows: [][]any{{
		now, "GTX 1650", 39.0, int64(77 << 20), int64(4096 << 20), 12.5, 66.0,
	}}}
	got, err := SQLStore{GPU: q}.GPUSamples(context.Background(), now.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d", len(got))
	}
	s := got[0]
	if !s.At.Equal(now) || s.Name != "GTX 1650" || s.UtilPct != 39 ||
		s.MemUsedBytes != 77<<20 || s.MemTotalBytes != 4096<<20 {
		t.Fatalf("sample = %+v", s)
	}
	if s.PowerW == nil || *s.PowerW != 12.5 || s.TempC == nil || *s.TempC != 66 {
		t.Fatalf("power/temp = %v/%v", s.PowerW, s.TempC)
	}
}
```

Three things in that last test are deliberate and will fail until you fix them:

- **`fakeQueryer.Query` returns `evals.Rows`, so it cannot satisfy `gpu.Queryer`** (same method name, different return type). Add a one-line wrapper — `fakeRows` already implements `gpu.Rows`, because the two row interfaces are identical:

  ```go
  type fakeGPUQueryer struct{ rows [][]any }

  func (f fakeGPUQueryer) Query(ctx context.Context, sql string, args ...any) (gpu.Rows, error) {
  	return &fakeRows{rows: f.rows}, nil
  }
  ```

- **`fakeRows.Scan` has no `*int64` case** (`console/console_test.go:380-395`) — only `*string`, `*time.Time`, `*int`, `*float64`. `MemUsedBytes`/`MemTotalBytes` are `int64`, so it would silently leave them at 0 and the assertion above would fail with `mem_used=0`. Add `case *int64: *p = row[i].(int64)`.
- **It has no `**float64` case either** — `PowerW`/`TempC` are `*float64`, scanned as `**float64`. Add `case **float64:` that assigns nil when `row[i] == nil` and a fresh pointer otherwise.

`fakeQueryer` returns the same rows for every query, which is all this test needs; do not add SQL matching.

`memStore` also needs one field (`gpuHist []gpu.Sample`) and the method. Use the existing `m.err` for the failure path — every other `memStore` method already returns it, and a second error field would let a test fail one method and pass another:

```go
func (m *memStore) GPUSamples(ctx context.Context, since time.Time, limit int) ([]gpu.Sample, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.gpuHist, nil
}
```

**Step 2 — watch fail.** `go test ./console/ -run 'GPUEndpoint|GPUSamples' -v`. Expect `h.GPUSamples undefined` / `SQLStore{…}.GPUSamples undefined` — the handler is not registered, so the first test also 404s.

**Step 3 — add to the `Store` interface** (`console/store.go:75-95`), and give `SQLStore` the queryer it needs:

```go
	// GPUSamples is Track U's history: local GPU utilisation and VRAM,
	// newest-first. A nil slice (no rows) is not an error.
	GPUSamples(ctx context.Context, since time.Time, limit int) ([]gpu.Sample, error)
```

`SQLStore` gains one field. Do **not** reuse the `evals.Queryer` field: it returns `evals.Rows`, and `gpu` declares its own `Rows`, so the two are not assignable (`TestPolicySingleDirectDependency` is also why `console` may import `gpu` but the reverse must never happen).

```go
type SQLStore struct {
	Query evals.Queryer
	Exec  evals.Execer
	Row   evals.Querier
	// GPU is Track U's sample reader. gpu.Rows is a distinct named type from
	// evals.Rows, so it needs its own queryer; main.go supplies gpuAdapter.
	GPU gpu.Queryer
}
```

```go
// GPUSamples delegates to gpu.SQLStore so the SQL for gpu_samples lives in one
// package. GPU is nil in tests that only exercise spans, so guard it: a nil
// queryer is "no history", never a panic on a page load.
func (s SQLStore) GPUSamples(ctx context.Context, since time.Time, limit int) ([]gpu.Sample, error) {
	if s.GPU == nil {
		return nil, nil
	}
	return gpu.SQLStore{Query: s.GPU}.Series(ctx, since, limit)
}
```

`GPUAt` is **not** part of this task — Task 10 adds it together with the trace chip that consumes it. An interface method with no caller is dead weight in the diff.

**Step 4 — handler.** `Deps` gains:

```go
	// GPU is Track U's live snapshot; nil when the prober is off (tests,
	// non-GPU builds) and the page renders the empty state.
	GPU func() gpu.Snapshot
```

Route in `New` (`console/handler.go:65-79`) and in `Routes()`:

```go
	h.mux.HandleFunc("GET /v1/gpu", h.gpu)
```

```go
// gpu serves what the prober last measured plus the stored history. It is a
// read of in-memory state plus one indexed query: it never calls nvidia-smi, so
// a scrape cannot cost 100 ms.
func (h *Handler) gpu(w http.ResponseWriter, r *http.Request) {
	now := gpu.Snapshot{At: time.Now().UTC(), Error: "GPU prober not running"}
	if h.deps.GPU != nil {
		now = h.deps.GPU()
	}
	since := time.Now().Add(-6 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Minute && d <= 168*time.Hour {
			since = time.Now().Add(-d)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	hist, err := h.deps.Store.GPUSamples(ctx, since, 720)
	if hist == nil {
		hist = []gpu.Sample{} // gpu.Series returns nil for no rows; null breaks the page
	}
	out := map[string]any{"now": now, "history": hist}
	if err != nil {
		// The live reading is measured locally, not read from Postgres, so a
		// dead database must not take the GPU page down with it: that is
		// exactly when an operator wants to see the card. Keep the 200, hand
		// back "now", and say why the chart has no history instead of making
		// "no samples yet" and "store unreachable" look the same.
		out["history_error"] = "gpu store unavailable"
	}
	writeJSON(w, http.StatusOK, out)
}
```

Four details that are requirements, not style:

- `writeErr` takes **four** arguments — `(w, status, code, message)`. `writeErr(w, http.StatusBadGateway, err.Error())` will not compile, and a new error shape fails `TestPolicyOneErrorShape`. Use `store_unavailable`, like every other handler. `gpu` itself does not call it any more: see the next bullet.
- **The store being down is not this endpoint's 502.** Every other handler reads Postgres and therefore fails when it does; `gpu` reads the live half from the prober in memory. 502 here would hide a pegged card behind a database the card has nothing to do with — and on a box where Postgres is down precisely because something else is wrong, that is the reading you need most. So the read stays `200`, `history` is `[]`, and `history_error` is set, which is what lets the page say "history unavailable" instead of "no samples yet". `TestGPUEndpointKeepsNowWhenHistoryFails` pins it.
- `hist == nil` → `[]gpu.Sample{}`. `gpu.Series` declares `var out []Sample`, so no rows is a nil slice, which marshals as `null`; the page does `history.map(...)`.
- The 10 s `context.WithTimeout` matches every other store-backed handler in the file; `r.Context()` alone would let a stuck query hold the browser's fetch open.

**Step 5 — wire it in `main.go`.** The endpoint is dead code until the console gets a prober and a queryer, and forgetting this is invisible to every test above (they inject `Deps` directly). At `main.go:969-970`:

```go
	ui := console.New(console.Deps{
		Store:         console.SQLStore{Query: pgAdapter{pool}, Exec: pgAdapter{pool}, Row: pgAdapter{pool}, GPU: gpuAdapter{pgAdapter{pool}}},
		Router:        srv,
		GPU:           gpuProber.Snapshot,
		GoldenVersion: *driftGolden,
```

`gpuProber` is the one Task 6 constructed at ~line 866, so it is in scope here; `gpuAdapter` is the Task 6 type. Confirm both exist before you wire — if Task 6 changed the variable name, use the real one.

**Step 6 — pass.** `go test ./console/ -v`

**Step 7 — live proof.** Boot, then `curl.exe -s localhost:8080/v1/gpu`. Expect `"available":true` with a real `sample` on this box. On history, be precise about what you can prove:

- **With Postgres up:** wait one poll interval, then the endpoint returns a non-empty `history`. This is the only way to prove the insert→select round trip live.
- **With Postgres down** (this machine's Docker daemon was off while the plan was written): `pgxpool.New` does not connect eagerly, so the server still boots and `/v1/gpu` still answers `200` — with `"history":[]`, a live `"now"`, and `"history_error":"gpu store unavailable"`. That is the whole reason the handler does not 502 like its siblings (Step 4). Say that plainly; do not claim a row-count proof you did not run. The insert path is already covered by Task 5's offline tests.

The `"history":[]` half of that is the useful one either way: `null` there is the bug Task 8 exists to prevent, and `/v1/gpu` is where you see it.

**Step 8 — commit.** `git commit -am "Serve live and historical GPU telemetry at /v1/gpu"`

---

## Task 9 — the Tower `#/gpu` page

**Files:** `console/static/app.js`, `console/static/index.html`, `console/static/tower.css`, `docs/tower-design-system.html`

**Read `docs/tower-design-system.html` before writing a line.** Every class you use must already exist in `tower.css` (`TestPolicyConsoleClassesExist`) and every colour must be a `--tower-*` token (`TestPolicyConsoleColoursAreTokens`). You will add exactly one CSS block and one design-system example.

**Step 1 — nav.** In `index.html` under `Operate`, after `cockpit`. The nav items use inline SVGs, not icon-font spans — `tower-nav-ico` does not exist, and referencing it would fail `TestPolicyConsoleClassesExist`:

```html
    <button class="tower-nav-btn" data-page="gpu">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 10h12M6 14h8"/></svg>
      <span>GPU</span>
    </button>
```

**Step 2 — register the page.** `app.js:102` and `app.js:842`:

```js
  const pageTitles = { overview: 'Overview', traces: 'Traces', evals: 'Evals & drift', benchmarks: 'Benchmarks', workflows: 'Workflows', cockpit: 'Cockpit', gpu: 'GPU' };
```

```js
  const pages = { overview: pageOverview, traces: pageTraces, evals: pageEvals, benchmarks: pageBenchmarks, workflows: pageWorkflows, cockpit: pageCockpit, gpu: pageGPU };
```

**Step 3 — the page.** Follow `pageBenchmarks`'s four-state shape (`app.js:473-518`): `content.replaceChildren(skeleton(6))`, `await api('/v1/gpu', null, signal)`, and on failure `content.replaceChildren(errorBox('Could not load GPU: ' + e.message, () => route()))`.

The primitives that exist, and must be used as they are: `h`, `api`, `fmt` (`ms`/`score`/`time`/`date`/`short`), `pill`, `chip`, `errorBox`, `skeleton`, `metric`, `panel`, plus the `tower-empty` / `tower-empty-tall` / `tower-empty-title` / `tower-empty-actions` cluster (`app.js:384-386`). There is **no** `chartPanel`, `skeletonPanel`, `emptyTall`, `fmtGB`, `fmtPct`, `residentPanel` or `tower-stack` — the earlier draft of this plan invented all six. Charts go inside `panel(title, sub, svg)`; the byte and percent formatters are two local functions in the GPU block; pages hand their nodes straight to `content.replaceChildren(...)`, which is already a column.

```js
  // ---------- GPU (Track U) ----------
  // Two charts, one table, one honesty rule: if the prober has no driver, the
  // page says so and draws nothing — a flat 0% line would be a lie.
  function gpuPct(v) { return v == null ? '–' : Number(v).toFixed(0) + '%'; }
  function gpuGB(b) { return b == null ? '–' : (Number(b) / (1 << 30)).toFixed(2) + ' GB'; }

  // gpuChart is deliberately the same hand-rolled-SVG idea as trafficChart
  // (app.js:105): no library, one path, tokens only.
  function gpuChart(points, key, max) {
    const w = 900, hgt = 140, pad = 8;
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 ' + w + ' ' + hgt);
    svg.setAttribute('class', 'tower-gpu-chart');
    if (!points.length || !max) return svg;
    const x = i => pad + i * (w - pad * 2) / Math.max(1, points.length - 1);
    const y = v => hgt - pad - Math.min(1, v / max) * (hgt - pad * 2);
    const d = points.map((p, i) => (i ? 'L' : 'M') + x(i).toFixed(1) + ' ' + y(p[key] || 0).toFixed(1)).join(' ');
    const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    path.setAttribute('d', d);
    path.setAttribute('class', 'tower-gpu-line');
    svg.append(path);
    return svg;
  }

  // residentTable takes the snapshot's error as well as its list: with the
  // driver up and Ollama unreachable the list is empty and err says why, so
  // saying "nothing resident" would claim an idle GPU we could not observe.
  function residentTable(list, err) {
    if (!list.length) {
      return h('div', { class: 'tower-empty' }, err
        ? 'Resident models unknown — ' + err
        : 'Nothing resident. Ollama unloads a model once its keep-alive expires.');
    }
    return h('table', { class: 'tower-table' },
      h('thead', null, h('tr', null, ...['model', 'VRAM', 'context', 'expires'].map(t => h('th', null, t)))),
      h('tbody', null, list.map(m => h('tr', null,
        h('td', null, chip(m.name)),
        h('td', { class: 'tower-mono' }, gpuGB(m.size_vram_bytes)),
        h('td', { class: 'tower-mono' }, m.context_length || '–'),
        h('td', { class: 'tower-mono' }, fmt.date(m.expires_at))))));
  }

  async function pageGPU(signal, _sub, polling) {
    if (!polling) content.replaceChildren(skeleton(6));
    let data;
    try { data = await api('/v1/gpu', null, signal); }
    catch (e) { if (e.name === 'AbortError') return; content.replaceChildren(errorBox('Could not load GPU: ' + e.message, () => route())); return; }
    const now = data.now || {};
    if (!now.available) {
      // No driver: one tall empty state, no charts. A zeroed chart is a lie.
      content.replaceChildren(panel('GPU', 'What local inference does to this machine',
        h('div', { class: 'tower-empty tower-empty-tall' },
          h('div', { class: 'tower-empty-title' }, 'No GPU telemetry'),
          now.error || 'the prober is not running',
          h('div', { class: 'tower-empty-actions' },
            h('span', { class: 'tower-panel-sub' }, 'Start the server on a machine with an NVIDIA driver, or set GPU_SMI to the nvidia-smi path.')))));
    } else {
      const s = now.sample || {};
      const hist = (data.history || []).slice().reverse(); // Series() is newest-first
      // history_error is set when the live read worked but the store did not.
      // "No history yet" would then be a guess; say what actually happened.
      const histErr = data.history_error || '';
      const histEmpty = msg => h('div', { class: 'tower-empty' },
        histErr ? 'History unavailable — ' + histErr : msg);
      const metrics = h('div', { class: 'tower-metric-row' },
        metric('Utilisation', gpuPct(s.util_pct), s.name || ''),
        metric('VRAM', gpuGB(s.mem_used_bytes) + ' / ' + gpuGB(s.mem_total_bytes),
          gpuPct(s.mem_total_bytes ? s.mem_used_bytes * 100 / s.mem_total_bytes : null)),
        metric('Power', s.power_w == null ? '–' : Number(s.power_w).toFixed(0) + ' W', 'driver reading'),
        metric('Temperature', s.temp_c == null ? '–' : Number(s.temp_c).toFixed(0) + ' °C', fmt.time(now.at)));
      const swaps = now.swaps_last_10m || 0;
      const nodes = [metrics,
        panel('Utilisation', 'Last 6 hours, one point per poll',
          hist.length ? gpuChart(hist, 'util_pct', 100) : histEmpty('No history yet — the first sample lands within one poll interval')),
        panel('VRAM used', 'In use against the ' + gpuGB(s.mem_total_bytes) + ' this card has installed',
          hist.length ? gpuChart(hist, 'mem_used_bytes', s.mem_total_bytes) : histEmpty('No history yet')),
        panel('Resident models', swaps + ' model swap' + (swaps === 1 ? '' : 's') + ' in the last 10 minutes',
          residentTable(now.resident || [], now.error))];
      if (swaps > 2) {
        nodes.push(h('div', { class: 'tower-note' },
          'Ollama swapped models ' + swaps + ' times in the last 10 minutes. Every swap unloads one model to load another, so a run that straddles one is not a fair speed comparison — see docs/VRAM.md.'));
      }
      content.replaceChildren(...nodes);
    }
    // Page-local refresh, mirroring pageOverview's poll: skip the skeleton so
    // the charts do not flash, and stop as soon as the route changes.
    pollTimer = setTimeout(() => { if (location.hash.startsWith('#/gpu')) pageGPU(signal, '', true); }, 5000);
  }
```

**The partial-degradation rule — do not skip this.** `available:true` does not mean everything worked. `Prober` reads two independent sources, so the snapshot can be:

| `available` | `error` | `resident` | What happened | What the page must show |
|---|---|---|---|---|
| `true` | `""` | any | both sources answered | the metric row, both charts, the resident table |
| `true` | `"…ollama: connection refused"` | `[]` | **driver fine, Ollama unreachable** | the metric row and charts (real GPU data), and below them a `tower-note` / inline empty saying Ollama could not be asked — **not** an empty "no models resident" table |
| `false` | `"…nvidia-smi: not found"` | any | no driver | the tall empty state; no charts at all |

`residentTable(list, err)` therefore takes the error: when `list.length === 0 && err` it renders the note ("resident models unknown — Ollama unreachable") rather than the table's empty row; when both are empty it renders the table's honest "nothing resident" row. Rendering an empty table for an unreachable Ollama is the bug this row exists to prevent — it tells the operator the GPU is idle when what actually happened is that we could not ask.

The second axis is history. Task 8's handler returns `200` with `history: []` and a `history_error` string when the store is unreachable, so `hist.length === 0` has two meanings and the page must not collapse them: an empty chart that says "the first sample lands within one poll interval" while Postgres is down is a promise the server cannot keep. `histEmpty(msg)` above picks the wording — `history_error` wins over the default message. On this box, with the Docker daemon off, that is the state the page will spend its whole life in, which makes it the one you will actually see.

**Step 4 — the one CSS block.** Append to `tower.css` in a new `/* ===== GPU page ===== */` section, after the Traces block. Tokens only:

```css
/* ===== GPU page ===== */
.tower-gpu-chart { width: 100%; height: auto; display: block; }
.tower-gpu-line { fill: none; stroke: var(--tower-amber); stroke-width: 2; vector-effect: non-scaling-stroke; }
```

If you need a second colour (VRAM), add `--tower-*` to `:root` first with a comment saying why, or reuse an existing accent.

**Step 5 — design-system example.** `AGENTS.md` requires this in the same change, and the page is the contract: if the example is not there, the component is undocumented.

Two edits to `docs/tower-design-system.html`:

1. A nav entry after the Charts link (`#charts`, line 59), same inline-SVG shape as its neighbours:

   ```html
   <a class="tower-nav-btn" href="#gpu"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 10h12M6 14h8"/></svg><span>GPU page</span></a>
   ```

2. A `<section class="ds-section" id="gpu">` between `#charts` and `#traces`, containing **static** markup (this page has no fetch; do not call `pageGPU` from it):

   - `ds-lead`: what the page answers — utilisation, VRAM, power and temperature for the card this box actually has, plus which models are resident and how often Ollama swapped them.
   - a `.tower-metric-row` with the four metrics (`Utilisation 39%`, `VRAM 2.10 GB / 4.00 GB`, `Power 61 W`, `Temperature 74 °C`), copied as HTML;
   - one `.tower-panel` holding an inline `<svg class="tower-gpu-chart" viewBox="0 0 900 140">` with a `<path class="tower-gpu-line" d="…">` — a literal path that rises and falls, so the two new classes are visible on the page that documents them;
   - one `.tower-note` with the swap-thrash sentence;
   - a `.ds-spec` table whose rows cite the real functions: `pageGPU()` (four states, `history_error` handling), `gpuChart(points, key, max)` (one path, y clamped to `max`, empty when there are no points), `residentTable(list, err)` (says "unknown" when Ollama could not be asked), plus the two honesty rules as rows: `available:false` ⇒ tall empty state and no chart; `history_error` ⇒ "History unavailable", never "No history yet".

   Say plainly in the section: the VRAM total is read from the card, never hardcoded — the same page renders a 4 GB 1650 and an 8 GB 4060.

**Step 6 — check the policies.** `go test -count=1 ./policy/ -run 'Console' -v`

**Step 7 — look at it.** `go run .` then open `http://localhost:8080/#/gpu`. You must see all four states:

1. **live** data on the driver box — metric row, both charts once two polls have landed, resident table;
2. **empty** (`GPU_SMI=/nonexistent`) — the tall "No GPU telemetry" panel and no charts at all, never a flat 0% line;
3. **skeleton** for the ~200 ms before the first fetch resolves — photograph it with the `SLOW_MS` harness rather than trusting a fast machine to show you;
4. **error** with Retry when the request itself fails (server down, or the endpoint 502s). Note what is *not* an error any more: with Postgres stopped, `/v1/gpu` still answers 200, so the page shows live metrics and charts whose empty slot reads "History unavailable — gpu store unavailable". That is the partial-degradation path, and it is the state this box will be in for the whole verification.

Check the browser console while you do it: a `null` history, a `.map` of `undefined`, or a missing class shows up there before it shows up in the page.

**Step 8 — commit.** `git commit -am "Add the GPU page to the Tower console"`

---

## Task 10 — put the GPU on a trace, and tell the advisor the measured budget

**Files:** `console/static/app.js`, `advisor/advisor.go`, `main.go`, `main_test.go`

### 10a — trace chip

**Where the trace response actually lives.** Not in `console/`. `GET /v1/traces/{id}` is a closure in `main.go` (around line 1024) that calls `queryTrace(db, id)`, which uses `tracker.SQLStore.ListSpans` and returns `{"trace_id":…, "spans":[…]}`. There is no per-trace store method in `console.Store`, and adding `Store.GPUAt` there would give it no caller — exactly the dead weight Task 8's note argues against. So this task does **not** touch `console/store.go` or `console/handler.go`.

**Step 1 — failing test** in `main_test.go` (package `main`, already has `TestExtraModelsSkipsPaths`), offline and DB-free: split the JSON assembly into a pure function so it can be tested without a `pgDB`.

```go
// TestTraceJSONIncludesNearestGPUSample: a span set plus a nearest-sample
// reader becomes one body. gpu_ok is the flag the page keys on: a zero sample
// must never be distinguishable from a real 0% reading by omission alone.
func TestTraceJSONIncludesNearestGPUSample(t *testing.T) {
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	spans := []tracker.Span{
		{TraceID: "t1", SpanID: "s1", Name: "chat", StartedAt: start},
		{TraceID: "t1", SpanID: "s2", Name: "model.generate", StartedAt: start.Add(time.Second)},
	}
	out := traceJSON(context.Background(), "t1", spans, func(_ context.Context, at time.Time) (gpu.Sample, bool, error) {
		if !at.Equal(start) {
			t.Fatalf("correlated on %v, want the first span's start %v", at, start)
		}
		return gpu.Sample{At: start, Name: "GTX 1650", UtilPct: 39, MemUsedBytes: 77 << 20}, true, nil
	})
	if out["gpu_ok"] != true {
		t.Fatalf("gpu_ok = %v", out["gpu_ok"])
	}
	g, ok := out["gpu"].(gpu.Sample)
	if !ok || g.UtilPct != 39 {
		t.Fatalf("gpu = %#v", out["gpu"])
	}
}

// TestTraceJSONOmitsGPUWithoutASample: no sample in the window is not "0%".
// The keys must be absent so the page renders no chip at all.
func TestTraceJSONOmitsGPUWithoutASample(t *testing.T) {
	out := traceJSON(context.Background(), "t1", []tracker.Span{{TraceID: "t1", StartedAt: time.Now()}},
		func(context.Context, time.Time) (gpu.Sample, bool, error) {
			return gpu.Sample{}, false, nil
		})
	if _, ok := out["gpu"]; ok {
		t.Fatal("gpu present without a sample")
	}
	if _, ok := out["gpu_ok"]; ok {
		t.Fatal("gpu_ok present without a sample")
	}
}
```

**Step 2** watch fail (`undefined: traceJSON`).

**Step 3** — in `main.go`, extract the body and keep `queryTrace` as the I/O wrapper:

```go
// traceJSON is the /v1/traces/{id} body. The GPU sample is the one nearest the
// first span's start — correlation, not attribution: nvidia-smi sees one ollama
// process, so nothing here claims a request owned the card. When no sample is
// in the window the keys are absent, because "unmeasured" and "0%" are different
// facts and only one of them is a reading. ctx is the caller's 10 s bound, so a
// wedged sample query cannot outlive the request.
func traceJSON(ctx context.Context, traceID string, spans []tracker.Span, near func(context.Context, time.Time) (gpu.Sample, bool, error)) map[string]any {
	out := map[string]any{"trace_id": traceID, "spans": spans}
	if len(spans) == 0 || near == nil {
		return out
	}
	if s, ok, err := near(ctx, spans[0].StartedAt); err == nil && ok {
		out["gpu"] = s
		out["gpu_ok"] = true
	}
	return out
}
```

`queryTrace` then returns `traceJSON(ctx, id, spans, func(ctx context.Context, at time.Time) (gpu.Sample, bool, error) { return gpu.SQLStore{Query: gpuAdapter{pgAdapter{db}}}.Nearest(ctx, at, 15*time.Second) })`, where `ctx` is the context `queryTrace` already opened with its 10 s timeout. The `err == nil &&` guard is deliberate: a dead `gpu_samples` table must not cost you the trace you came to read. `ListSpans` orders by `started_at`, so `spans[0]` is the trace's start.

**Step 4** — pass, `go test ./ -run TraceJSON -v`.

**Step 5** — `app.js`'s `traceView(data)` (line 278) adds one chip to the header row, next to `chip(kind)`, reusing the `gpuGB` helper Task 9 adds rather than a second formatter:

```js
        data.gpu_ok ? chip('GPU ' + Math.round(data.gpu.util_pct) + '% · ' + gpuGB(data.gpu.mem_used_bytes)) : null,
```

Give it a `title`-free chip; the honesty label belongs in the design-system example, not the chip.

**Step 6** — commit: `git commit -am "Show the nearest GPU sample on a trace"`.

### 10b — measured budget for `--advise`

**Step 1 — failing test** in `advisor/advisor_test.go`:

```go
func TestAdviseUsesMeasuredBudget(t *testing.T) {
	r := AdviseWithBudget([]string{"qwen2.5:7b-instruct-q4_K_M"}, nil, 4, "measured: GTX 1650")
	if !strings.Contains(r.String(), "4 GB") || !strings.Contains(r.String(), "measured") {
		t.Fatalf("budget not reported:\n%s", r.String())
	}
	for _, v := range r.Models {
		if v.Name == "qwen2.5:7b-instruct-q4_K_M" && !v.Veto {
			t.Fatal("7B must be vetoed on a 4 GB box")
		}
	}
}
```

**Step 2** watch fail. **Step 3** refactor `advisor` so `Advise(names, pulled)` is a one-line wrapper:

```go
// Advise is the docs/VRAM.md budget (Track K's default).
func Advise(names []string, pulled map[string]bool) Report {
	return AdviseWithBudget(names, pulled, BoxVRAMGB, "docs/VRAM.md")
}

// AdviseWithBudget answers the same question against a measured budget. Every
// verdict prints the source, so a 4 GB 1650 and an 8 GB 4060 never get the
// same sentence by accident.
func AdviseWithBudget(names []string, pulled map[string]bool, budgetGB float64, source string) Report { ... }
```

Thread `budgetGB`/`source` through the code. The four `BoxVRAMGB` sites are `advisor/advisor.go:111` (the veto comparison), `:118` (the VETO sentence), `:120` (the fits-alone sentence) and `:163` (`Report.String`'s header, which currently prints the literal `"(docs/VRAM.md)"`). Two of those are in `String()`, which has no arguments — so `Report` needs to carry what it printed: add `BudgetGB float64` and `BudgetSource string` fields (unexported extra state on an existing public struct is fine; `Report` is only built in this package) and set them in `AdviseWithBudget`. `String()` then prints `"advisor: %g GB VRAM budget (%s)"`. Do not thread them as `String(budget, source)` arguments — the console calls `String()` with none, and a second signature would ripple.

`Advise` keeps `BoxVRAMGB` and `"docs/VRAM.md"`, so every existing advisor test and every caller that does not have a driver still gets exactly today's text.

**Step 4** pass existing advisor tests. **Step 5** in `main.go`'s `runAdvise` (`main.go:325-338`), read the driver once and pass the measured total. The existing last line is `fmt.Print(advisor.Advise(names, pulled).String())` at `main.go:337`:

```go
	budget, source := float64(advisor.BoxVRAMGB), "docs/VRAM.md"
	if s, err := gpu.NewNvidiaSMI(envOr("GPU_SMI", "nvidia-smi")).Read(ctx); err == nil && s.MemTotalBytes > 0 {
		budget, source = s.TotalGB(), "measured: "+s.Name
	}
	fmt.Print(advisor.AdviseWithBudget(names, pulled, budget, source).String())
```

`ctx` is already the 10 s context `runAdvise` opened for `PulledModels`, so the driver read is bounded by it too; `gpu.NewNvidiaSMI` takes a path and `Read` respects the context. The fallback is not a guess to hide: when there is no driver the report prints `docs/VRAM.md`, which is the truth about where the number came from.

**Step 6 — live proof.** `go run . --advise` on this box must print `4 GB` in the header with `measured: NVIDIA GeForce GTX 1650`, and veto the 7B. With `GPU_SMI=/nonexistent` it must print `8 GB` and `docs/VRAM.md` instead — run both, because the fallback is the half that would otherwise only exist in a comment.

**Step 7 — commit.** `git commit -am "Judge model fit against the VRAM the box actually has"`

---

## Task 11 — documentation, changelog, and the full gate

**Step 0 — make one comment true.** `gpu/store.go:82` says `Purge` is "Called at boot and hourly", but `main.go` only calls it at boot (Task 6, in a bounded 10 s context). Retention is therefore applied on restart, not continuously — which is acceptable at 168h, but the comment must not claim a ticker that does not exist. Either fix the comment (preferred: it is the honest small change) or add the ticker and update `docs/API.md` to say "purged at boot and hourly". Do not leave the sentence as it is.

**Step 1 — `docs/API.md`.** The three `GPU_*` environment rows are **already there** — Task 6 had to add them or `TestPolicyEnvVarsDocumented` failed the build. Do not re-add them; verify them and fix only what is wrong. Likewise the `GET /v1/gpu` endpoint row landed with Task 8, including the `history_error` exception; read it and make sure it still describes what the handler does, rather than adding a second row.

What `docs/API.md` should say, for comparison (it already does):

| Endpoint | Returns |
|---|---|
| `GET /v1/gpu` | the prober's live snapshot plus stored history (`?since=` accepts a duration, 1m–168h, default 6h). `available:false` means the driver could not be read; it is not a zero reading. Degrades instead of 502ing: a dead store costs it `history` (plus `history_error`), not `now` |

The rows as they currently stand, for reference (fix the retention wording to match the code — the purge runs at boot, not hourly, unless you also add the ticker):

| Variable | Default | Meaning |
|---|---|---|
| `GPU_SMI` | `nvidia-smi` | Path to the driver CLI. Point it anywhere or leave it missing; the console renders the empty state. |
| `GPU_POLL_INTERVAL` | `10s` | How often the driver is asked. Clamped to ≥1s. `nvidia-smi` costs ~100 ms per call, so do not go below 5s on a busy box. |
| `GPU_HISTORY_KEEP` | `168h` | Retention for `gpu_samples`, pruned at boot. At 10s that is ~60k rows. |

**Step 2 — `docs/VRAM.md`.** The file's title and table are the *8 GB demo box* budget and stay as they are; what it is missing is the distinction between that budget and a reading. Append a section, and keep it ASCII-only (hyphens, straight quotes): the file already contains non-UTF-8 bytes (`�?"`), so writing typographic dashes or curly quotes into it will mix encodings and show up as mojibake in the diff.

```markdown
## Measured, not assumed

The numbers above are a budget for the 8 GB demo box, not a reading. `#/gpu` in
the console (and `GET /v1/gpu`) report what the driver and Ollama say right now:
utilisation, VRAM used vs installed, watts, temperature, which models are
resident and how much VRAM each holds, and how many model swaps happened in the
last ten minutes. `go run . --advise` reads the total VRAM from the card when a
driver is present and falls back to the 8 GB budget when it is not - the report
always prints which one it used (`measured: NVIDIA GeForce GTX 1650` vs
`docs/VRAM.md`). On the 4 GB dev box that means the 7B is now vetoed there,
instead of being told it "fits alone".
```

That last sentence is the reason this section exists: the same question has two different correct answers on the two boxes, and a single hard-coded number cannot give both.

**Step 3 — `CHANGELOG.md`.** One line at the top of `## Unreleased`:

```markdown
- **Track U — local GPU telemetry:** a `gpu` package polls `nvidia-smi` and Ollama's `/api/ps` every `GPU_POLL_INTERVAL` (default 10s) and keeps the last answer in memory; `GET /v1/gpu` and Tower `#/gpu` show utilisation, VRAM used vs installed, watts, temperature, resident models with per-model VRAM, and model swaps in the last ten minutes; `gpu_samples` (`0008_gpu.sql`) keeps `GPU_HISTORY_KEEP` (default 168h) of history so an eval run can be explained after the fact; `/metrics` gains `gpu_up`, `gpu_utilization_percent`, `gpu_memory_used_bytes`, `gpu_memory_total_bytes`, `gpu_power_watts`, `gpu_temperature_celsius`, `gpu_swap_events_total`; `--advise` now judges fit against the VRAM the box actually has, printing the source. Measured on a GTX 1650 4 GB: the 7B tier is now correctly vetoed instead of "fits alone". Tests are offline (fake reader, literal CSV) — CI has no GPU and no Ollama.
```

**Step 4 — run the full gate.**

```powershell
gofmt -l .
go vet ./...
go test -count=1 ./...
go build ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
go test -count=1 ./policy/
```

`gofmt -l .` must print nothing. Every policy test must be green. `-race` cannot
run on this machine (no C compiler, `CGO_ENABLED=0`); CI covers it, record that
in the commit body instead of claiming a local `-race` run.

**Step 5 — final commit.** `git commit -am "Document GPU telemetry and record it in the changelog"`

---

## Validation checklist (the definition of done)

- [ ] `go test -count=1 ./gpu/ ./console/ ./router/ ./advisor/` green **without** Postgres, Ollama, or a GPU (local). CI adds `-race`; this machine has no C compiler, so `-race` is CI evidence only.
- [ ] `go test -count=1 ./policy/` green, especially `MigrationsContiguous`, `EnvVarsDocumented`, `ConsoleColoursAreTokens`, `ConsoleClassesExist`, `SingleDirectDependency`.
- [ ] `nvidia-smi` absent → `/v1/gpu` returns `available:false` with the error text, page shows the empty state, `/metrics` says `gpu_up 0` and emits **no** utilisation line.
- [ ] `nvidia-smi` present → one `gpu_samples` row per interval. **Only provable with Postgres up:** `docker compose exec postgres psql -U agentops -d agentops -c "select count(*) from gpu_samples"` grows. On this box the daemon is off; the insert→select path is covered by Task 5's offline tests and must be reported as such, not claimed.
- [ ] `/metrics` contains the seven `gpu_*` series.
- [ ] `#/gpu` renders skeleton → live → empty → error with Retry (force each one). The error state is a failing request (server down, endpoint 502); a dead store is now the *live + "History unavailable"* state, and both are worth a screenshot.
- [ ] `go run . --advise` prints `measured: <card>` on this box and `docs/VRAM.md` on a box without a driver.
- [ ] `docs/tower-design-system.html` has the new example, citing `pageGPU`.
- [ ] `CHANGELOG.md` `## Unreleased` has the Track U line.

## Risks and how to know you hit them

| Risk | Symptom | Fix |
|---|---|---|
| Polling too fast | `nvidia-smi` processes pile up; utilisation reads 100% from our own probe | Keep ≥5s; `exec.CommandContext` with a 3s cap is already in place |
| `nvidia-smi` wedges | Prober goroutine never returns; `/v1/gpu` goes stale | The 3s context kills the child; `Snapshot().At` shows the staleness and the UI must show it |
| Ollama identity mismatch | `/api/ps` names include `:latest` while routing uses a bare name | Same normalisation as `PulledModels` — reuse it, don't re-implement |
| DB down | Sampling silently stops persisting | Insert is best-effort by design; the page still works from the in-memory snapshot and says history is empty |
| 60k rows in a week | Slow `Series` | `gpu_samples_ts_idx` + `limit 720` + a 6h default window; raise only via `?since=` |
| Someone adds a price per kWh | The dashboard stops being a measurement | Non-goal, stated above; a currency column would need `advisor.Rumor` provenance (source + date) |

## Commit sequence (one logical change each, imperative, <72 chars)

1. `Add a gpu package with one unit for memory`
2. `Parse nvidia-smi, including the N/A cases`
3. `Read Ollama's resident models as the per-model VRAM source`
4. `Poll the GPU, count model swaps, expose gpu_* gauges`
5. `Persist GPU samples in a prepared migration`
6. `Append GPU gauges to the metrics endpoint`  *(done before 7 — Task 6 needs the hook)*
7. `Sample the GPU every ten seconds in the server`
8. `Serve live and historical GPU telemetry at /v1/gpu`
9. `Add the GPU page to the Tower console`
10. `Show the nearest GPU sample on a trace`
11. `Judge model fit against the VRAM the box actually has`
12. `Document GPU telemetry and record it in the changelog`
