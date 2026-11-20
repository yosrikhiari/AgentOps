package router

// Perimeter tests, batch 2 (audit 2026-11-18, findings S3 and C-quality).
// A remote client picks these values, and they land on the machine that hosts the
// model: num_ctx sizes the KV cache, num_gpu places layers on the card, keep_alive
// pins VRAM. None of them may be unbounded, and an oversized body must be refused
// before it is materialised.

import (
	"net/http"
	"strings"
	"testing"
)

func TestParamsAcceptSaneValues(t *testing.T) {
	p := GenParams{
		Options:   map[string]any{"num_ctx": float64(8192), "num_gpu": float64(20), "temperature": 0.2},
		KeepAlive: "30m",
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("sane parameters rejected: %v", err)
	}
}

func TestParamsRejectResourceOptionsOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		p    GenParams
	}{
		{"num_ctx too large", GenParams{Options: map[string]any{"num_ctx": float64(2000000)}}},
		{"num_ctx negative", GenParams{Options: map[string]any{"num_ctx": float64(-1)}}},
		{"num_gpu too large", GenParams{Options: map[string]any{"num_gpu": float64(1000)}}},
		{"num_predict too large", GenParams{Options: map[string]any{"num_predict": float64(1000000)}}},
		{"num_thread too large", GenParams{Options: map[string]any{"num_thread": float64(4096)}}},
		{"repeat_last_n too large", GenParams{Options: map[string]any{"repeat_last_n": float64(999999)}}},
		{"num_ctx not a number", GenParams{Options: map[string]any{"num_ctx": "huge"}}},
		{"keep_alive forever", GenParams{KeepAlive: "-1"}},
		{"keep_alive too long", GenParams{KeepAlive: "999h"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.p.Validate(); err == nil {
				t.Fatalf("%s: Validate accepted an unbounded value", c.name)
			}
		})
	}
}

// TestChatRejectsOversizedBody: the JSON is fully materialised before any backend
// call, so the cap has to be on the reader; a 413 with the one error shape is the
// only honest answer.
func TestChatRejectsOversizedBody(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("hello", 4))
	body := `{"prompt":"` + strings.Repeat("x", maxRequestBody+64) + `"}`
	rec := post(srv, body, nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "request_too_large") {
		t.Fatalf("413 must carry the machine code: %s", rec.Body.String())
	}
}

// TestChatRejectsUnboundedOption: a client asking for a two-million-token context
// must be told no, in the same error shape as every other client mistake.
func TestChatRejectsUnboundedOption(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("hello", 4))
	rec := post(srv, `{"prompt":"hi","options":{"num_ctx":2000000}}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unbounded option = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "option_out_of_range") {
		t.Fatalf("400 must name the machine code: %s", rec.Body.String())
	}
}
