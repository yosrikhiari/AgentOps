package router

// Contract tests from the 2026-11-18 audit: the OpenAI ingress must not accept
// parameters it cannot honour, and every error response must carry the id the
// API reference promises.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnsupportedOpenAIFieldsAreRefused(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("hi", 4))
	cases := map[string]string{
		"tools":        `{"prompt":"hi","tools":[{"type":"function","function":{"name":"f"}}]}`,
		"tool_choice":  `{"prompt":"hi","tool_choice":"auto"}`,
		"n":            `{"prompt":"hi","n":2}`,
		"logprobs":     `{"prompt":"hi","logprobs":true}`,
		"top_logprobs": `{"prompt":"hi","logprobs":true,"top_logprobs":2}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := post(srv, body, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status %d, want 400 (%s)", name, rec.Code, rec.Body.String())
			}
			got := rec.Body.String()
			if !strings.Contains(got, "unsupported_field") {
				t.Fatalf("%s: missing the machine code: %s", name, got)
			}
			if !strings.Contains(got, name) {
				t.Fatalf("%s: the message must name the field: %s", name, got)
			}
		})
	}
}

func TestNeutralValuesOfUnsupportedFieldsAreAccepted(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("hi", 4))
	for _, body := range []string{
		`{"prompt":"hi","n":1}`, // one choice is what we already return
		`{"prompt":"hi","logprobs":false}`,
	} {
		if rec := post(srv, body, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want 200 (%s)", body, rec.Code, rec.Body.String())
		}
	}
}

func TestUnknownRouteCarriesTheSameTraceIDInHeaderAndBody(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("hi", 4))
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/nope", http.StatusNotFound},
		{http.MethodPost, "/v1/models", http.StatusMethodNotAllowed},
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("%s %s: status %d, want %d", tc.method, tc.path, rec.Code, tc.status)
		}
		header := rec.Header().Get("X-Trace-ID")
		if header == "" {
			t.Fatalf("%s %s: X-Trace-ID is empty", tc.method, tc.path)
		}
		var body struct {
			Error struct {
				TraceID string `json:"trace_id"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.TraceID != header {
			t.Fatalf("%s %s: body trace_id %q != header %q", tc.method, tc.path, body.Error.TraceID, header)
		}
	}
}
