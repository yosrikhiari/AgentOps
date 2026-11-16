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
