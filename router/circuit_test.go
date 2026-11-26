package router

import (
	"context"
	"errors"
	"testing"
	"time"
)

// circuitFakeBackend for circuit breaker tests.
type circuitFakeBackend struct {
	name    string
	failN   int // fail first N calls
	calls   int
	healthy bool
}

func (f *circuitFakeBackend) Name() string { return f.name }
func (f *circuitFakeBackend) Generate(ctx context.Context, model string, msgs []Message, maxTokens int) (string, Usage, error) {
	f.calls++
	if f.calls <= f.failN {
		return "", Usage{}, errors.New("boom")
	}
	return "ok", Usage{}, nil
}
func (f *circuitFakeBackend) Stream(ctx context.Context, model string, msgs []Message, maxTokens int, emit func(string)) (Usage, error) {
	f.calls++
	if f.calls <= f.failN {
		return Usage{}, errors.New("boom")
	}
	return Usage{}, nil
}
func (f *circuitFakeBackend) Health(ctx context.Context) error {
	if !f.healthy {
		return errors.New("unhealthy")
	}
	return nil
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	fb := &circuitFakeBackend{name: "test", failN: 10}
	cb := NewCircuitBreaker(fb, 3, 100*time.Millisecond)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, _, err := cb.Generate(ctx, "m", nil, 0)
		if err == nil {
			t.Fatalf("call %d: expected error", i)
		}
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("expected open, got %s", cb.State())
	}
	// while open, calls are rejected without hitting backend
	before := fb.calls
	_, _, err := cb.Generate(ctx, "m", nil, 0)
	if err != ErrCircuitOpen {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if fb.calls != before {
		t.Fatalf("backend called while open")
	}
}

func TestCircuitBreaker_HalfOpenRecovers(t *testing.T) {
	fb := &circuitFakeBackend{name: "test", failN: 3}
	cb := NewCircuitBreaker(fb, 3, 50*time.Millisecond)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		cb.Generate(ctx, "m", nil, 0)
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("expected open")
	}
	time.Sleep(60 * time.Millisecond)
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("expected half-open, got %s", cb.State())
	}
	// next call (4th) succeeds -> closed
	_, _, err := cb.Generate(ctx, "m", nil, 0)
	if err != nil {
		t.Fatalf("expected success on recovery, got %v", err)
	}
	if cb.State() != CircuitClosed {
		t.Fatalf("expected closed after recovery, got %s", cb.State())
	}
}

func TestCircuitBreaker_NameAndHealth(t *testing.T) {
	fb := &circuitFakeBackend{name: "backend1", healthy: true}
	cb := NewCircuitBreaker(fb, 2, time.Second)
	if cb.Name() != "backend1" {
		t.Fatalf("name mismatch")
	}
	if err := cb.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
	fb.healthy = false
	if err := cb.Health(context.Background()); err == nil {
		t.Fatalf("expected health error")
	}
}
