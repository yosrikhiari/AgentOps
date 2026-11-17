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
	// SwapsTotal is the prober's monotonic model-swap counter at sample time.
	// Stored so a later question ("did models fight over the card during this eval
	// run?") can be answered from history instead of from a process that has
	// already exited; omitted when zero so an old row reads naturally.
	SwapsTotal int64 `json:"swaps_total,omitempty"`
	// Throttled means the driver reported a thermal clock slowdown at this sample.
	// nil is a third fact, distinct from true and false: the driver does not know
	// clocks_event_reasons (older nvidia-smi) or answered something unparsable, so
	// nobody asked. Heat does not change what the model computes, but it changes how
	// long it takes, which is exactly what eval comparability and timeouts depend on.
	Throttled *bool `json:"throttled,omitempty"`
}

// Summary is what a time window did to the card: the aggregate the eval pages and
// `--score` need, without shipping thousands of samples to answer one question.
// Swaps is max-min of Sample.SwapsTotal inside the window, so a restart mid-window
// undercounts rather than inventing swaps.
type Summary struct {
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	Samples       int       `json:"samples"`
	UtilMean      float64   `json:"util_mean_pct"`
	UtilPeak      float64   `json:"util_peak_pct"`
	MemPeakBytes  int64     `json:"mem_peak_bytes"`
	MemTotalBytes int64     `json:"mem_total_bytes"`
	Swaps         int64     `json:"swaps"`
	// MaxTempC is the hottest reading in the window; ThrottledSamples counts samples
	// where the driver had cut clocks for heat. A hot run and a quiet run are not
	// comparable even when the answers are: the clock changed under them.
	MaxTempC         float64 `json:"max_temp_c,omitempty"`
	ThrottledSamples int     `json:"throttled_samples"`
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
	Name          string    `json:"name"`
	SizeVRAMBytes int64     `json:"size_vram_bytes"`
	ContextLength int       `json:"context_length"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// Snapshot is what the prober last saw. Available=false with a non-empty Error
// means the box has no usable nvidia-smi; the console must render that as an
// empty state, never as zero percent.
type Snapshot struct {
	At           time.Time  `json:"at"`
	Sample       Sample     `json:"sample"`
	Resident     []Resident `json:"resident"`
	Available    bool       `json:"available"`
	Error        string     `json:"error,omitempty"`
	SwapsLast10m int        `json:"swaps_last_10m"`
	// SwapsTotal is monotonic for the life of the process and is what gets
	// persisted; SwapsLast10m is the rolling window the page shows.
	SwapsTotal int64 `json:"swaps_total"`
}

// Reader is the one method the prober needs, so tests inject a fake and never
// shell out. Implementations must respect ctx: nvidia-smi
// (exec.CommandContext) and Ollama both get a hard deadline.
type Reader interface {
	Read(ctx context.Context) (Sample, error)
}
