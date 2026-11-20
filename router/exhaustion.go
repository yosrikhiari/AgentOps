package router

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ExhaustionError is returned by a backend when it cannot serve the request
// because its quota/rate limit is exhausted (or, for local backends, when it
// reports a 429-equivalent). The router benches the backend for Until and
// falls through to the next candidate. Status and Body are preserved so the
// error message still contains what callers and tests assert on.
type ExhaustionError struct {
	Backend    string
	Status     int
	Body       string
	RetryAfter time.Duration // provider-supplied reset window; 0 = compute from Status/Body
}

func (e *ExhaustionError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("%s status %d: %s", e.Backend, e.Status, e.Body)
	}
	return fmt.Sprintf("%s status %d", e.Backend, e.Status)
}

// HTTPStatusError is the typed error returned by backends on any non-2xx
// response. The router classifies it into "exhaustion" (429/402/403) vs
// "transient" (5xx/other) without string-parsing.
type HTTPStatusError struct {
	Backend    string
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *HTTPStatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("%s status %d: %s", e.Backend, e.Status, e.Body)
	}
	return fmt.Sprintf("%s status %d", e.Backend, e.Status)
}

// isExhaustion reports whether err is a quota/rate-limit exhaustion that
// should bench the backend for a long cooldown rather than a quick retry.
func isExhaustion(err error) bool {
	switch e := err.(type) {
	case *ExhaustionError:
		return true
	case *HTTPStatusError:
		return e.Status == http.StatusTooManyRequests ||
			e.Status == http.StatusPaymentRequired ||
			e.Status == http.StatusForbidden
	}
	return false
}

// retryAfter extracts a provider-supplied reset window from err, if any.
func retryAfter(err error) time.Duration {
	switch e := err.(type) {
	case *ExhaustionError:
		return e.RetryAfter
	case *HTTPStatusError:
		return e.RetryAfter
	}
	return 0
}

// ExhaustionTracker holds per-backend bench state. Safe for concurrent use.
// State is in-memory and resets on process restart (by design).
type ExhaustionTracker struct {
	mu      sync.RWMutex
	benched map[string]time.Time // backend name -> benched until
	reason  map[string]string    // backend name -> "quota" | "transient"
	metrics *Metrics
}

func NewExhaustionTracker(m *Metrics) *ExhaustionTracker {
	return &ExhaustionTracker{benched: map[string]time.Time{}, reason: map[string]string{}, metrics: m}
}

// Bench marks a backend as unavailable until the given time.
func (t *ExhaustionTracker) Bench(name string, until time.Time, kind string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.benched[name] = until
	t.reason[name] = kind
	if t.metrics != nil {
		t.metrics.SetBackendBenched(name, true)
		t.metrics.ObserveExhaustion(name, kind)
	}
}

// Benched reports whether a backend is currently benched and, if so, until when.
func (t *ExhaustionTracker) Benched(name string) (bool, time.Time) {
	if t == nil {
		return false, time.Time{}
	}
	t.mu.RLock()
	until, ok := t.benched[name]
	t.mu.RUnlock()
	if !ok {
		return false, time.Time{}
	}
	if time.Now().After(until) {
		// Expired: drop the entry so the rung is retried.
		t.mu.Lock()
		delete(t.benched, name)
		delete(t.reason, name)
		if t.metrics != nil {
			t.metrics.SetBackendBenched(name, false)
		}
		t.mu.Unlock()
		return false, time.Time{}
	}
	return true, until
}

// Snapshot returns the current bench state for all backends (for /metrics, console).
func (t *ExhaustionTracker) Snapshot() []BackendExhaustionState {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := time.Now()
	out := make([]BackendExhaustionState, 0, len(t.benched))
	for name, until := range t.benched {
		if now.After(until) {
			continue
		}
		out = append(out, BackendExhaustionState{Name: name, BenchedUntil: until, Kind: t.reason[name]})
	}
	return out
}

// BackendExhaustionState is the serialisable view of a benched backend.
type BackendExhaustionState struct {
	Name         string    `json:"name"`
	BenchedUntil time.Time `json:"benched_until"`
	Kind         string    `json:"kind"` // "quota" | "transient"
}

// CooldownFor computes the bench duration for an exhaustion error.
// Quota/daily: until next midnight UTC (capped 24h, min 5m).
// Per-minute: RetryAfter (capped 5m, min 30s).
func CooldownFor(err error) (time.Duration, string) {
	ra := retryAfter(err)
	if ra > 5*time.Minute {
		// Treat large retry-after as a daily/quota window.
		return capDuration(ra, 24*time.Hour, 5*time.Minute), "quota"
	}
	if ra > 0 {
		min := 30 * time.Second
		if v := os.Getenv("EXHAUST_COOLDOWN_PERMINUTE"); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d >= time.Second {
				min = d
			}
		}
		return capDuration(ra, 5*time.Minute, min), "quota"
	}
	// No provider hint: classify by status/message.
	msg := ""
	if e, ok := err.(*HTTPStatusError); ok {
		msg = e.Body
	} else if e, ok := err.(*ExhaustionError); ok {
		msg = e.Body
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "per day") || strings.Contains(lower, "daily") ||
		strings.Contains(lower, "quota") || strings.Contains(lower, "insufficient") {
		// Bench until next midnight UTC.
		now := time.Now().UTC()
		midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		d := time.Until(midnight)
		if d < 5*time.Minute {
			d = 5 * time.Minute
		}
		if d > 24*time.Hour {
			d = 24 * time.Hour
		}
		return d, "quota"
	}
	return 60 * time.Second, "quota"
}

func capDuration(d, max, min time.Duration) time.Duration {
	if d > max {
		d = max
	}
	if d < min {
		d = min
	}
	return d
}

// isTransient reports whether err is a transient failure (5xx, timeout, connection)
// eligible for one same-rung retry.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*HTTPStatusError); ok {
		return e.Status >= 500 && e.Status < 600
	}
	// Network/timeout errors are transient.
	return true
}
