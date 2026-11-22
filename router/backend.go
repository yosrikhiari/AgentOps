package router

import (
	"context"
	"log"
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

// PresenceReporter is implemented by backends that can list the models on
// disk. The prober refreshes the set every tick; the server filters unpulled
// refs out of routing and marks them in /v1/models. Backends without it
// (cloud APIs) report unknown presence, which never filters.
type PresenceReporter interface {
	PulledModels(ctx context.Context) (map[string]bool, error)
}

// HealthProber pings every backend on an interval and publishes router_backend_up.
type HealthProber struct {
	mu       sync.RWMutex
	backends []Backend
	up       map[string]bool
	// probed records backends that have answered at least once, so the first probe
	// reports the state instead of staying silent about a backend that was down from
	// boot - the case that matters most, because every request silently fails over.
	probed  map[string]bool
	lastErr map[string]string
	pulled  map[string]map[string]bool
	metrics *Metrics
	// AfterProbe, when set, runs at the end of every ProbeOnce (boot included).
	// main.go uses it to log served-but-missing models.
	AfterProbe func(context.Context)
}

func NewHealthProber(m *Metrics, backends ...Backend) *HealthProber {
	p := &HealthProber{backends: backends, up: map[string]bool{}, probed: map[string]bool{}, lastErr: map[string]string{}, metrics: m}
	for _, b := range backends {
		p.up[b.Name()] = false
	}
	return p
}

// ProbeOnce checks every backend now (used at startup and by tests).
func (p *HealthProber) ProbeOnce(ctx context.Context) {
	var wg sync.WaitGroup
	for _, b := range p.backends {
		b := b
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := b.Health(cctx)
			cancel()

			p.mu.Lock()
			up, wasProbed := err == nil, p.probed[b.Name()]
			changed := !wasProbed || p.up[b.Name()] != up
			p.up[b.Name()] = up
			p.probed[b.Name()] = true
			if err != nil {
				p.lastErr[b.Name()] = err.Error()
			} else {
				delete(p.lastErr, b.Name())
			}
			detail := p.lastErr[b.Name()]
			p.mu.Unlock()

			// Say it once: a backend that is down from boot is the case that hides a
			// silent failover, and a transition is when an operator can still act.
			if changed {
				if up {
					log.Printf("backend %s is reachable", b.Name())
				} else if isLocal(b) {
					log.Printf("backend %s is unreachable (%s) - local requests will fail over to a cloud backend until it returns", b.Name(), detail)
				} else {
					log.Printf("backend %s is unreachable (%s)", b.Name(), detail)
				}
			}
			if p.metrics != nil {
				p.metrics.SetBackendUp(b.Name(), err == nil)
			}
			if pr, ok := b.(PresenceReporter); ok {
				pctx, pcancel := context.WithTimeout(ctx, 5*time.Second)
				set, perr := pr.PulledModels(pctx)
				pcancel()
				if perr == nil {
					p.mu.Lock()
					if p.pulled == nil {
						p.pulled = map[string]map[string]bool{}
					}
					p.pulled[b.Name()] = set
					p.mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if p.AfterProbe != nil {
		p.AfterProbe(ctx)
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

// Pulled reports whether model is on disk at backend. known=false means no
// presence information (no probe yet, backend down, backend cannot report) —
// callers must treat unknown as present, never as missing.
func (p *HealthProber) Pulled(backend, model string) (pulled, known bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	set, ok := p.pulled[backend]
	if !ok {
		return false, false
	}
	return set[model], true
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
