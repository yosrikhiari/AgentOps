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
