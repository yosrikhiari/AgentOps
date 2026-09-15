package router

import (
	"context"
	"sync"
	"time"
)

// Message is one chat turn in the OpenAI shape.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage mirrors OpenAI's usage block.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Backend is one place a model can run: local Ollama, an OpenAI-compatible cloud API, a
// fake in tests. Generate returns the whole answer; Stream hands deltas to emit as they
// arrive and returns the usage once the stream ends. maxTokens <= 0 means model default.
type Backend interface {
	Name() string
	Generate(ctx context.Context, model string, msgs []Message, maxTokens int) (string, Usage, error)
	Stream(ctx context.Context, model string, msgs []Message, maxTokens int, emit func(delta string)) (Usage, error)
	Health(ctx context.Context) error
}

// Local reports whether a backend keeps data on this machine. Sensitive requests may only
// use local backends (the fail-closed rule).
type Local interface {
	Local() bool
}

func isLocal(b Backend) bool {
	if l, ok := b.(Local); ok {
		return l.Local()
	}
	return false
}

// HealthProber pings every backend on an interval and publishes router_backend_up.
type HealthProber struct {
	mu       sync.RWMutex
	backends []Backend
	up       map[string]bool
	lastErr  map[string]string
	metrics  *Metrics
}

func NewHealthProber(m *Metrics, backends ...Backend) *HealthProber {
	p := &HealthProber{backends: backends, up: map[string]bool{}, lastErr: map[string]string{}, metrics: m}
	for _, b := range backends {
		p.up[b.Name()] = false
	}
	return p
}

// ProbeOnce checks every backend now (used at startup and by tests).
func (p *HealthProber) ProbeOnce(ctx context.Context) {
	for _, b := range p.backends {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := b.Health(cctx)
		cancel()
		p.mu.Lock()
		p.up[b.Name()] = err == nil
		if err != nil {
			p.lastErr[b.Name()] = err.Error()
		} else {
			delete(p.lastErr, b.Name())
		}
		p.mu.Unlock()
		if p.metrics != nil {
			p.metrics.SetBackendUp(b.Name(), err == nil)
		}
	}
}

// Run probes until ctx is cancelled.
func (p *HealthProber) Run(ctx context.Context, every time.Duration) {
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

// Up reports the last probe result; unknown backends are reported down.
func (p *HealthProber) Up(name string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.up[name]
}

type BackendStatus struct {
	Name  string `json:"name"`
	Up    bool   `json:"up"`
	Local bool   `json:"local"`
	Error string `json:"error,omitempty"`
}

func (p *HealthProber) Statuses() []BackendStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]BackendStatus, 0, len(p.backends))
	for _, b := range p.backends {
		out = append(out, BackendStatus{Name: b.Name(), Up: p.up[b.Name()], Local: isLocal(b), Error: p.lastErr[b.Name()]})
	}
	return out
}
