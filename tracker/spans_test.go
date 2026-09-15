package tracker

import (
	"context"
	"strings"
	"testing"
)

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
