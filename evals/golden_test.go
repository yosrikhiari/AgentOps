package evals

import (
	"strings"
	"testing"
)

func TestValidateGolden(t *testing.T) {
	docs := map[string]bool{"01-router": true}
	ok := `{"question":"q1","answer":"The router exposes three metrics.","doc_ids":["01-router"]}
{"question":"q2","answer":"Metrics are served at GET /metrics.","doc_ids":["01-router"]}
`
	n, err := ValidateGolden([]byte(ok), docs)
	if err != nil || n != 2 {
		t.Fatalf("good file: n=%d err=%v", n, err)
	}
	cases := map[string]string{
		"unknown doc":  `{"question":"q","answer":"A full sentence here.","doc_ids":["nope"]}`,
		"duplicate q":  `{"question":"q","answer":"A full sentence here.","doc_ids":["01-router"]}` + "\n" + `{"question":"q","answer":"Another full sentence.","doc_ids":["01-router"]}`,
		"empty answer": `{"question":"q","answer":"  ","doc_ids":["01-router"]}`,
		"no claim":     `{"question":"q","answer":"Three","doc_ids":["01-router"]}`,
		"subordinate":  `{"question":"q","answer":"Because the scorer splits answers into claims.","doc_ids":["01-router"]}`,
		"malformed":    `{"question":`,
		"empty file":   "\n\n",
	}
	for name, raw := range cases {
		if _, err := ValidateGolden([]byte(raw), docs); err == nil {
			t.Fatalf("%s: expected error", name)
		} else if name == "no claim" && !strings.Contains(err.Error(), "no scorable claim") {
			t.Fatalf("no claim: wrong error %v", err)
		}
	}
}

// TestLoadGoldenIsTheScoringPath: the scored path must go through validation, or
// a duplicate question silently overwrites the earlier pair and an unknown doc
// makes recall meaningless (audit 2026-11-18 C3).
func TestLoadGoldenIsTheScoringPath(t *testing.T) {
	docs := map[string]bool{"01-router": true}
	ok := `{"question":"q1","answer":"The router exposes three metrics.","doc_ids":["01-router"]}
{"question":"q2","answer":"Metrics are served at GET /metrics.","doc_ids":["01-router"]}
`
	pairs, err := LoadGolden([]byte(ok), docs)
	if err != nil {
		t.Fatalf("valid golden rejected: %v", err)
	}
	if len(pairs) != 2 || pairs[0].Question != "q1" || len(pairs[0].DocIDs) != 1 {
		t.Fatalf("pairs = %+v", pairs)
	}

	for name, raw := range map[string]string{
		"duplicate":   `{"question":"q","answer":"A full sentence here.","doc_ids":["01-router"]}` + "\n" + `{"question":"q","answer":"Another full sentence.","doc_ids":["01-router"]}`,
		"unknown doc": `{"question":"q","answer":"A full sentence here.","doc_ids":["nope"]}`,
		"no claim":    `{"question":"q","answer":"Three","doc_ids":["01-router"]}`,
	} {
		if got, err := LoadGolden([]byte(raw), docs); err == nil {
			t.Fatalf("%s: LoadGolden accepted %d pair(s); the scored path must reject what --freeze-golden rejects", name, len(got))
		}
	}
}
