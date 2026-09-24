package main

// Event-hub tests: the live view subscribes here. The hub is in-memory,
// bounded and lossy by design (RULES §5: a full buffer drops and counts,
// it never blocks the request path).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedRecorder lets the test read the body while the handler is still
// streaming into it; a bare httptest.ResponseRecorder is not safe for that.
type lockedRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func (l *lockedRecorder) Header() http.Header { return l.rec.Header() }

func (l *lockedRecorder) WriteHeader(code int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rec.WriteHeader(code)
}

func (l *lockedRecorder) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Write(b)
}

func (l *lockedRecorder) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rec.Flush()
}

func (l *lockedRecorder) body() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Body.String()
}

func TestEventHubBroadcastsSpan(t *testing.T) {
	hub := newEventHub()
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)
	hub.Publish(spanRecord{traceID: "abc", spanID: "s1", parentID: "", name: "mcp.tool", attrs: `{"tool":"list_models","ok":true}`})
	select {
	case rec := <-ch:
		if rec.name != "mcp.tool" || rec.traceID != "abc" {
			t.Fatalf("wrong record: %+v", rec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber got nothing")
	}
}

func TestEventHubSlowSubscriberNeverBlocks(t *testing.T) {
	hub := newEventHub()
	_ = hub.Subscribe() // never drained: buffers must fill, then drop
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			hub.Publish(spanRecord{traceID: "t", spanID: "s", name: "mcp.tool", attrs: `{}`})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	if hub.Dropped() == 0 {
		t.Fatal("expected drops to be counted")
	}
}

func TestEventsEndpointStreamsSpans(t *testing.T) {
	hub := newEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil).WithContext(ctx)
	rec := &lockedRecorder{rec: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() {
		hub.ServeHTTP(rec, req)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // let the handler subscribe
	hub.Publish(spanRecord{traceID: "t1", spanID: "s1", parentID: "", name: "mcp.tool", attrs: `{"tool":"get_stats","ok":true}`})
	deadline := time.After(2 * time.Second)
	for {
		body := rec.body()
		if strings.Contains(body, "mcp.tool") && strings.HasPrefix(body, "data: ") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no SSE data flushed, got %q", body)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not exit on client close")
	}
}
