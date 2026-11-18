package evals

import (
	"math"
	"testing"
)

// TestRouteCeiling: beta counts questions every rung fails; fast-optimal counts questions
// where the cheap rung is at least as good. A question one rung did not answer is excluded,
// because comparing models on different question sets is how routing gains get invented.
func TestRouteCeiling(t *testing.T) {
	out := []RungOutcome{
		{Question: "q1", Model: "fast", Faithfulness: 1.0},
		{Question: "q1", Model: "slow", Faithfulness: 0.2},
		{Question: "q2", Model: "fast", Faithfulness: 0.2},
		{Question: "q2", Model: "slow", Faithfulness: 0.9},
		{Question: "q3", Model: "fast", Faithfulness: 0.1},
		{Question: "q3", Model: "slow", Faithfulness: 0.0},
		{Question: "q4", Model: "fast", Miss: true},
		{Question: "q4", Model: "slow", Faithfulness: 0.8},
		{Question: "q5", Model: "fast", Faithfulness: 0.9}, // slow did not answer
	}
	c := RouteCeiling(out, "fast")
	if c.Questions != 4 {
		t.Fatalf("questions = %d, want 4 (q5 excluded)", c.Questions)
	}
	if len(c.Rungs) != 2 {
		t.Fatalf("rungs = %v", c.Rungs)
	}
	if math.Abs(c.Beta-0.25) > 1e-9 {
		t.Fatalf("beta = %v, want 0.25", c.Beta)
	}
	if math.Abs(c.FastOptimal-0.5) > 1e-9 {
		t.Fatalf("fast-optimal = %v, want 0.5 (q1, q3)", c.FastOptimal)
	}
	if math.Abs(c.MeanFaith["fast"]-(1.0+0.2+0.1)/3) > 1e-9 {
		t.Fatalf("fast mean = %v", c.MeanFaith["fast"])
	}
	if math.Abs(c.MeanFaith["slow"]-(0.2+0.9+0.0+0.8)/4) > 1e-9 {
		t.Fatalf("slow mean = %v", c.MeanFaith["slow"])
	}
}

// TestRouteCeilingNeedsTwoRungs: one rung cannot support a co-failure rate, and the zero
// value must not be mistaken for a measurement (beta 0 would read as "no failures").
func TestRouteCeilingNeedsTwoRungs(t *testing.T) {
	c := RouteCeiling([]RungOutcome{{Question: "q", Model: "only", Faithfulness: 1}}, "only")
	if c.Questions != 0 || c.Beta != 0 || c.FastOptimal != 0 {
		t.Fatalf("one rung produced numbers: %+v", c)
	}
	if len(c.Rungs) != 1 || c.Rungs[0] != "only" {
		t.Fatalf("rungs = %v", c.Rungs)
	}
}

// TestRouteCeilingTieCountsForFast: a tie goes to the cheaper model, or the fast-optimal
// share would understate how often routing is unnecessary.
func TestRouteCeilingTieCountsForFast(t *testing.T) {
	c := RouteCeiling([]RungOutcome{
		{Question: "q", Model: "fast", Faithfulness: 0.8},
		{Question: "q", Model: "slow", Faithfulness: 0.8},
	}, "fast")
	if c.FastOptimal != 1 || c.Beta != 0 {
		t.Fatalf("tie = %+v", c)
	}
}
