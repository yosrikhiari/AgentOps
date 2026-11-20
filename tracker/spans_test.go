package tracker

import (
	"context"
	"strings"
	"testing"
	"time"
)

// started_at is the time the single writer thread persisted a span, and the OS clock is
// coarse enough that two spans emitted back-to-back share it to the microsecond. The old
// ORDER BY started_at, span_id therefore ordered a router's answer by a random id; the
// tree is the fact, the clock is not.
func TestListSpansOrdersParentsBeforeChildren(t *testing.T) {
	at := time.Date(2026, 11, 19, 13, 19, 43, 454163000, time.UTC)
	got := orderSpans([]Span{
		{TraceID: "t", SpanID: "c", ParentID: "b", Name: "router.respond", StartedAt: at},
		{TraceID: "t", SpanID: "b", ParentID: "a", Name: "router.answer", StartedAt: at},
		{TraceID: "t", SpanID: "a", ParentID: "", Name: "route.decide", StartedAt: at},
	})
	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Name)
	}
	if want := "route.decide,router.answer,router.respond"; strings.Join(names, ",") != want {
		t.Fatalf("order = %v, want %s", names, want)
	}
}

func TestListSpansRedacted(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	var n int
	mk := func(out string) StepFunc {
		return func(ctx context.Context, in string) (string, error) {
			n++
			return out, nil
		}
	}
	if _, err := RunToy(ctx, store, "wf-trace", "q?", mk("r"), mk("d"), mk("v")); err != nil {
		t.Fatal(err)
	}
	spans, err := store.ListSpans(ctx, "wf-trace")
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 3 {
		t.Fatalf("want 3 spans, got %d", len(spans))
	}
	for _, sp := range spans {
		if sp.TraceID == "" || sp.SpanID == "" || sp.Name == "" {
			t.Fatalf("span missing OTel ids: %+v", sp)
		}
		if sp.TraceID != "wf-trace" {
			t.Fatalf("bad trace %q", sp.TraceID)
		}
		if strings.Contains(sp.Attrs, `"prompt"`) || strings.Contains(sp.Attrs, `"input"`) {
			t.Fatalf("attrs not redacted: %s", sp.Attrs)
		}
	}
	empty, err := store.ListSpans(ctx, "nope")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("want 0 spans for unknown trace, got %d", len(empty))
	}
}
