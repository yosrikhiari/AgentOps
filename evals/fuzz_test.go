package evals

import "testing"

func FuzzParseVerdict(f *testing.F) {
	f.Add("VERDICT: ENTAILED because x")
	f.Add("no verdict here")
	f.Add("VERDICT: BASELESS - the context neither supports nor contradicts it")
	f.Add("VERDICT: NOT SUPPORTED\nstuff")
	f.Fuzz(func(t *testing.T, raw string) {
		// Must never panic; any input is admissible.
		label, just := parseVerdict(raw)
		_ = label
		_ = just
	})
}
