package router

import (
	"context"
	"sync"
	"time"
)

// CircuitState is the state of a circuit breaker.
type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	}
	return "unknown"
}

// CircuitBreaker wraps a Backend and trips after consecutive failures.
type CircuitBreaker struct {
	mu           sync.Mutex
	backend      Backend
	failures     int
	threshold    int
	openUntil    time.Time
	openDuration time.Duration
	state        CircuitState
}

// NewCircuitBreaker creates a circuit breaker with the given failure threshold
// and open duration.
func NewCircuitBreaker(b Backend, threshold int, openDuration time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 5
	}
	if openDuration <= 0 {
		openDuration = 30 * time.Second
	}
	return &CircuitBreaker{
		backend:      b,
		threshold:    threshold,
		openDuration: openDuration,
		state:        CircuitClosed,
	}
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == CircuitOpen && time.Now().After(cb.openUntil) {
		cb.state = CircuitHalfOpen
	}
	return cb.state
}

// Name delegates to the wrapped backend.
func (cb *CircuitBreaker) Name() string { return cb.backend.Name() }

// Health checks the backend health, respecting circuit state.
func (cb *CircuitBreaker) Health(ctx context.Context) error {
	if cb.State() == CircuitOpen {
		return ErrCircuitOpen
	}
	err := cb.backend.Health(ctx)
	cb.record(err == nil)
	return err
}

// Generate wraps the backend's Generate with circuit logic.
func (cb *CircuitBreaker) Generate(ctx context.Context, model string, msgs []Message, maxTokens int) (string, Usage, error) {
	if cb.State() == CircuitOpen {
		return "", Usage{}, ErrCircuitOpen
	}
	text, usage, err := cb.backend.Generate(ctx, model, msgs, maxTokens)
	cb.record(err == nil)
	return text, usage, err
}

// Stream wraps the backend's Stream with circuit logic.
func (cb *CircuitBreaker) Stream(ctx context.Context, model string, msgs []Message, maxTokens int, emit func(delta string)) (Usage, error) {
	if cb.State() == CircuitOpen {
		return Usage{}, ErrCircuitOpen
	}
	usage, err := cb.backend.Stream(ctx, model, msgs, maxTokens, emit)
	cb.record(err == nil)
	return usage, err
}

func (cb *CircuitBreaker) record(success bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if success {
		cb.failures = 0
		cb.state = CircuitClosed
		return
	}
	cb.failures++
	if cb.failures >= cb.threshold {
		cb.state = CircuitOpen
		cb.openUntil = time.Now().Add(cb.openDuration)
	}
}

// ErrCircuitOpen is returned when the circuit is open.
var ErrCircuitOpen = &circuitOpenError{}

type circuitOpenError struct{}

func (e *circuitOpenError) Error() string { return "circuit breaker open" }
