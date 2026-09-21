package console

// Track L — benchmark comparison builder (TDD RED: BuildComparison and the
// BenchmarkData store method do not exist yet).

import (
	"strings"
	"testing"
)

func tiedPairs(n int, faith float64) []ScoredPair {
	long := strings.Repeat("x", 201)
	out := make([]ScoredPair, 0, n)
	for i := 0; i < n; i++ {
		q := "is the router fast?"
		if i%2 == 1 {
			q = long // second scenario: long-or-complex-prompt
		}
		out = append(out, ScoredPair{Question: q, Faithfulness: faith, LatencyS: 0.5, Tokens: 40})
	}
	return out
}

func TestBuildComparisonTiedBelowSignificance(t *testing.T) {
	in := BenchmarkInput{Golden: "v3", Judge: "qwen3:8b", Runs: []ModelRunData{
		{Model: "fast-m", Pairs: tiedPairs(72, 1.0)},
		{Model: "quality-m", Pairs: tiedPairs(72, 1.0)},
	}}
	c := BuildComparison(in)
	if !c.Comparable {
		t.Fatalf("two same-judge runs must compare: %+v", c)
	}
	if c.Verdict.Winner != "" {
		t.Fatalf("72 pairs can never reach n>=100: winner=%q", c.Verdict.Winner)
	}
	if !strings.Contains(c.Verdict.Reason, "tied") {
		t.Fatalf("reason=%q", c.Verdict.Reason)
	}
	if len(c.Scenarios) < 2 {
		t.Fatalf("want >=2 scenarios, got %+v", c.Scenarios)
	}
}

func TestBuildComparisonNeedsTwoRuns(t *testing.T) {
	in := BenchmarkInput{Golden: "v3", Judge: "qwen3:8b", Runs: []ModelRunData{
		{Model: "fast-m", Pairs: tiedPairs(72, 1.0)},
	}}
	c := BuildComparison(in)
	if c.Comparable || c.Verdict.Winner != "" {
		t.Fatalf("one run must not compare: %+v", c)
	}
}

func TestBuildComparisonJudgeChangedInvalidates(t *testing.T) {
	in := BenchmarkInput{Golden: "v3", Runs: []ModelRunData{
		{Model: "fast-m", Judge: "judge-a", Pairs: tiedPairs(72, 1.0)},
		{Model: "quality-m", Judge: "judge-b", Pairs: tiedPairs(72, 1.0)},
	}}
	c := BuildComparison(in)
	if c.Comparable {
		t.Fatalf("different judges must invalidate: %+v", c)
	}
}

func TestBuildComparisonWinnerAtSignificance(t *testing.T) {
	fast := make([]ScoredPair, 0, 120)
	quality := make([]ScoredPair, 0, 120)
	for i := 0; i < 120; i++ {
		fast = append(fast, ScoredPair{Question: "is the router fast?", Faithfulness: 0, LatencyS: 0.5, Tokens: 40})
		quality = append(quality, ScoredPair{Question: "is the router fast?", Faithfulness: 1, LatencyS: 0.9, Tokens: 60})
	}
	in := BenchmarkInput{Golden: "v3", Judge: "qwen3:8b", Runs: []ModelRunData{
		{Model: "fast-m", Pairs: fast},
		{Model: "quality-m", Pairs: quality},
	}}
	c := BuildComparison(in)
	if c.Verdict.Winner != "quality-m" {
		t.Fatalf("120-vs-0 separation at n=120 must promote: %+v", c.Verdict)
	}
}
