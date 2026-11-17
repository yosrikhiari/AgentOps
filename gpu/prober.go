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
	// swapsTotal is monotonic for the life of this process. It is what makes
	// "swaps during that eval run" provable afterwards, so it is persisted with
	// every sample and exposed as the (correctly named) _total metric.
	swapsTotal int64
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
			p.swapsTotal++
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
	snap.SwapsTotal = p.swapsTotal
	snap.Sample.SwapsTotal = p.swapsTotal
	p.snap = snap
	p.mu.Unlock()

	// Persist after the accounting, so the stored sample carries the counter that
	// was true when it was taken. Best-effort: a dead database must not stop
	// local measurement, so the error is dropped on the floor.
	if snap.Available && p.store != nil {
		_ = p.store.InsertSample(ctx, snap.Sample)
	}
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

// Snapshot returns a copy, including a copy of the resident slice, so a caller
// cannot reach back into prober state through the struct it was handed.
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

// Expose renders the Prometheus exposition main.go appends to /metrics. When
// the driver could not be read it emits gpu_up 0 and stops: no utilisation,
// memory, power or temperature series at all, because a scraper must not read a
// fabricated zero for a fact nobody measured.
func (p *Prober) Expose() string {
	snap := p.Snapshot()
	var sb strings.Builder
	sb.WriteString("# HELP gpu_up 1 if the last nvidia-smi read succeeded.\n# TYPE gpu_up gauge\n")
	if snap.Available {
		sb.WriteString("gpu_up 1\n")
	} else {
		sb.WriteString("gpu_up 0\n")
	}
	sb.WriteString(fmt.Sprintf("# HELP gpu_swap_events_total Model swaps observed by this process (monotonic).\n# TYPE gpu_swap_events_total counter\ngpu_swap_events_total %d\n", snap.SwapsTotal))
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
	// A nil Throttled means the driver does not answer the question. Publishing 0
	// would be a fabricated "cool card", so the series simply does not exist - the
	// same way power and temperature vanish when the driver says [N/A].
	if snap.Sample.Throttled != nil {
		throttled := 0
		if *snap.Sample.Throttled {
			throttled = 1
		}
		sb.WriteString(fmt.Sprintf("# HELP gpu_thermal_throttled 1 when the driver has cut clocks for heat.\n# TYPE gpu_thermal_throttled gauge\ngpu_thermal_throttled %d\n", throttled))
	}
	return sb.String()
}
