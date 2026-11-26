package router

import (
	"strings"
	"testing"
)

func FuzzAnalyse(f *testing.F) {
	f.Add("what is the router")
	f.Add("analyze this code")
	f.Add("")
	f.Add("```python\nprint('hi')\n```")
	f.Fuzz(func(t *testing.T, prompt string) {
		sig := Analyse(prompt)
		// Invariants: confidence in [0,1], difficulty in [0,1]
		if sig.Confidence < 0 || sig.Confidence > 1 {
			t.Fatalf("confidence out of range: %v", sig.Confidence)
		}
		if sig.Difficulty < 0 || sig.Difficulty > 1 {
			t.Fatalf("difficulty out of range: %v", sig.Difficulty)
		}
		// Intent must be one of the known intents
		valid := map[Intent]bool{IntentChat: true, IntentExtract: true, IntentSummarise: true,
			IntentClassify: true, IntentCode: true, IntentMath: true, IntentReason: true,
			IntentWrite: true}
		if !valid[sig.Intent] {
			t.Fatalf("unknown intent: %v", sig.Intent)
		}
		// No panic on any input (the fuzzer checks this)
		_ = strings.TrimSpace(prompt)
	})
}
