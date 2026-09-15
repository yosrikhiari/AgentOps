package router

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

var latencyBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}

type modelStats struct {
	requests uint64
	errors   uint64
	tokens   uint64
	latSum   float64
	latCount uint64
	buckets  map[float64]uint64
}

type Metrics struct {
	mu               sync.Mutex
	models           map[string]*modelStats
	backendUp        map[string]bool
	keyRequests      map[string]uint64
	keyRejects       map[string]uint64
	evalFaithfulness float64
	evalSet          bool
}

func NewMetrics() *Metrics {
	return &Metrics{models: map[string]*modelStats{}, backendUp: map[string]bool{}, keyRequests: map[string]uint64{}, keyRejects: map[string]uint64{}}
}

// SetBackendUp publishes the last health probe result for a backend.
func (m *Metrics) SetBackendUp(backend string, up bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backendUp[backend] = up
}

// ObserveKey counts a request attributed to an API key; rejected = 401/403/429 outcomes.
func (m *Metrics) ObserveKey(name string, rejected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rejected {
		m.keyRejects[name]++
	} else {
		m.keyRequests[name]++
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (m *Metrics) Observe(model string, latencySeconds float64, tokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.models[model]
	if !ok {
		st = &modelStats{buckets: map[float64]uint64{}}
		m.models[model] = st
	}
	st.requests++
	if tokens > 0 {
		st.tokens += uint64(tokens)
	}
	st.latSum += latencySeconds
	st.latCount++
	for _, b := range latencyBuckets {
		if latencySeconds <= b {
			st.buckets[b]++
		}
	}
}

// ObserveError counts a chat that failed before a model answered (Ollama down, bad
// upstream status). Latency is not recorded so the histogram stays "time to answer".
func (m *Metrics) ObserveError(model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.models[model]
	if !ok {
		st = &modelStats{buckets: map[float64]uint64{}}
		m.models[model] = st
	}
	st.errors++
}

type ModelSnapshot struct {
	Requests uint64
	Errors   uint64
	Tokens   uint64
}

func (m *Metrics) Snapshot() map[string]ModelSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]ModelSnapshot, len(m.models))
	for name, st := range m.models {
		out[name] = ModelSnapshot{Requests: st.requests, Errors: st.errors, Tokens: st.tokens}
	}
	return out
}

func (m *Metrics) SetEvalFaithfulness(v float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evalFaithfulness = v
	m.evalSet = true
}

func (m *Metrics) Expose() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sb strings.Builder
	models := make([]string, 0, len(m.models))
	for name := range m.models {
		models = append(models, name)
	}
	sort.Strings(models)
	sb.WriteString("# HELP router_requests_total Chat requests handled per model.\n")
	sb.WriteString("# TYPE router_requests_total counter\n")
	for _, name := range models {
		fmt.Fprintf(&sb, "router_requests_total{model=%q} %d\n", name, m.models[name].requests)
	}
	sb.WriteString("# HELP router_errors_total Chat requests that failed before a model answered, per model.\n")
	sb.WriteString("# TYPE router_errors_total counter\n")
	for _, name := range models {
		fmt.Fprintf(&sb, "router_errors_total{model=%q} %d\n", name, m.models[name].errors)
	}
	sb.WriteString("# HELP router_tokens_total Tokens processed per model.\n")
	sb.WriteString("# TYPE router_tokens_total counter\n")
	for _, name := range models {
		fmt.Fprintf(&sb, "router_tokens_total{model=%q} %d\n", name, m.models[name].tokens)
	}
	sb.WriteString("# HELP router_latency_seconds Request latency including model call.\n")
	sb.WriteString("# TYPE router_latency_seconds histogram\n")
	for _, name := range models {
		st := m.models[name]
		// Observe already counts a request in every bucket >= its latency, so each
		// bucket value is cumulative by construction — do not sum them again here.
		for _, b := range latencyBuckets {
			fmt.Fprintf(&sb, "router_latency_seconds_bucket{model=%q,le=\"%g\"} %d\n", name, b, st.buckets[b])
		}
		fmt.Fprintf(&sb, "router_latency_seconds_bucket{model=%q,le=\"+Inf\"} %d\n", name, st.latCount)
		fmt.Fprintf(&sb, "router_latency_seconds_sum{model=%q} %f\n", name, st.latSum)
		fmt.Fprintf(&sb, "router_latency_seconds_count{model=%q} %d\n", name, st.latCount)
	}
	if len(m.backendUp) > 0 {
		sb.WriteString("# HELP router_backend_up 1 if the last health probe of the backend succeeded.\n")
		sb.WriteString("# TYPE router_backend_up gauge\n")
		for _, n := range sortedKeys(m.backendUp) {
			v := 0
			if m.backendUp[n] {
				v = 1
			}
			fmt.Fprintf(&sb, "router_backend_up{backend=%q} %d\n", n, v)
		}
	}
	if len(m.keyRequests)+len(m.keyRejects) > 0 {
		set := map[string]bool{}
		for n := range m.keyRequests {
			set[n] = true
		}
		for n := range m.keyRejects {
			set[n] = true
		}
		names := sortedKeys(set)
		sb.WriteString("# HELP router_key_requests_total Requests accepted per API key.\n")
		sb.WriteString("# TYPE router_key_requests_total counter\n")
		for _, n := range names {
			fmt.Fprintf(&sb, "router_key_requests_total{key=%q} %d\n", n, m.keyRequests[n])
		}
		sb.WriteString("# HELP router_key_rejected_total Requests rejected per API key (401/403/429).\n")
		sb.WriteString("# TYPE router_key_rejected_total counter\n")
		for _, n := range names {
			fmt.Fprintf(&sb, "router_key_rejected_total{key=%q} %d\n", n, m.keyRejects[n])
		}
	}
	if m.evalSet {
		sb.WriteString("# HELP eval_faithfulness Last golden faithfulness score.\n")
		sb.WriteString("# TYPE eval_faithfulness gauge\n")
		fmt.Fprintf(&sb, "eval_faithfulness %f\n", m.evalFaithfulness)
	}
	return sb.String()
}
