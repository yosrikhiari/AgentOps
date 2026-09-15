package router

import (
	"strings"
	"testing"
)

type spanCall struct {
	traceID  string
	spanID   string
	parentID string
	name     string
}

func TestChatEmitsThreeSpans(t *testing.T) {
	gen := &fakeGen{text: "hello", tokens: 5}
	srv := NewServer("fast-m", "quality-m", gen)
	var calls []spanCall
	srv.SpanSink = func(traceID, spanID, parentID, name, attrs string) {
		calls = append(calls, spanCall{traceID, spanID, parentID, name})
	}
	out, err := srv.Chat("hi", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("want 3 spans, got %d", len(calls))
	}
	for _, c := range calls {
		if c.traceID == "" || c.spanID == "" || c.name == "" {
			t.Fatalf("span missing OTel ids: %+v", c)
		}
		if c.traceID != out.TraceID {
			t.Fatalf("span trace %q != response trace %q", c.traceID, out.TraceID)
		}
	}
	if calls[0].parentID != "" {
		t.Fatalf("root span should have empty parent, got %q", calls[0].parentID)
	}
	if calls[1].parentID != calls[0].spanID {
		t.Fatalf("gen span parent %q != decide span %q", calls[1].parentID, calls[0].spanID)
	}
	if calls[2].parentID != calls[1].spanID {
		t.Fatalf("respond span parent %q != gen span %q", calls[2].parentID, calls[1].spanID)
	}
}

func TestEvalFaithfulnessGauge(t *testing.T) {
	m := NewMetrics()
	if strings.Contains(m.Expose(), "eval_faithfulness ") {
		t.Fatal("gauge should be absent before first eval")
	}
	m.SetEvalFaithfulness(0.82)
	out := m.Expose()
	if !strings.Contains(out, "eval_faithfulness 0.820000") {
		t.Fatalf("missing gauge:\n%s", out)
	}
}
