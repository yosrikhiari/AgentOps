package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// eventHub broadcasts span records to live viewers (Tower #/live). It is
// in-memory, bounded and lossy by design: every asynchronous edge here must be
// bounded, non-blocking and safe to lose (RULES §5). A slow browser drops spans
// and counts them; the request path and the database never wait for it.
type eventHub struct {
	mu      sync.Mutex
	subs    map[chan spanRecord]struct{}
	dropped atomic.Uint64
}

const eventHubBuffer = 64

func newEventHub() *eventHub {
	return &eventHub{subs: map[chan spanRecord]struct{}{}}
}

func (h *eventHub) Subscribe() chan spanRecord {
	ch := make(chan spanRecord, eventHubBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *eventHub) Unsubscribe(ch chan spanRecord) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Publish fans one span out to every subscriber without ever blocking.
// A full subscriber buffer drops the span and counts it.
func (h *eventHub) Publish(rec spanRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- rec:
		default:
			h.dropped.Add(1)
		}
	}
}

func (h *eventHub) Dropped() uint64 { return h.dropped.Load() }

// ServeHTTP streams spans as server-sent events until the client goes away.
// Payloads are the already-redacted span attributes — nothing here carries
// prompt text (see TestPolicyPromptsNeverReachSpans).
func (h *eventHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "events_unavailable", "message": "streaming not supported"}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := h.Subscribe()
	defer h.Unsubscribe(ch)
	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			fl.Flush()
		case rec := <-ch:
			raw, _ := json.Marshal(map[string]any{
				"trace_id": rec.traceID, "span_id": rec.spanID, "parent_id": rec.parentID,
				"name": rec.name, "attrs": json.RawMessage([]byte(rec.attrs)),
			})
			_, _ = w.Write(append(append([]byte("data: "), raw...), '\n', '\n'))
			fl.Flush()
		}
	}
}
