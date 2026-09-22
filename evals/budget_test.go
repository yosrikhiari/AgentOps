package evals

// Track R — context budget (TDD RED: budget.go does not exist yet).

import (
	"context"
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(strings.Repeat("a", 400)); got != 100 {
		t.Fatalf("400 chars -> %d tokens, want 100", got)
	}
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("empty -> %d, want 0", got)
	}
}

func TestTrimToBudgetDropsTail(t *testing.T) {
	// Relevance-ordered input: head kept, tail dropped, count reported.
	in := []string{strings.Repeat("a", 4000), strings.Repeat("b", 4000), strings.Repeat("c", 4000)}
	kept, trimmed := TrimToBudget(in, 2500)
	if len(kept) != 2 || trimmed != 1 {
		t.Fatalf("kept=%d trimmed=%d, want 2/1", len(kept), trimmed)
	}
	if !strings.HasPrefix(kept[0], "a") || !strings.HasPrefix(kept[1], "b") {
		t.Fatal("trim must drop from the tail (lowest relevance) only")
	}
}

func TestTrimToBudgetNoOpUnderBudget(t *testing.T) {
	in := []string{"a", "b"}
	kept, trimmed := TrimToBudget(in, 2500)
	if len(kept) != 2 || trimmed != 0 {
		t.Fatalf("kept=%d trimmed=%d, want 2/0", len(kept), trimmed)
	}
}

func TestScorePairReportsTrimmed(t *testing.T) {
	big := strings.Repeat("x", 5000)
	search := func(ctx context.Context, q string, k int) ([]Chunk, error) {
		return []Chunk{
			{DocID: "d", Text: big}, {DocID: "d", Text: big}, {DocID: "d", Text: big},
			{DocID: "d", Text: big}, {DocID: "d", Text: big},
		}, nil
	}
	p := Pair{Question: "q?", Answer: "Ollama runs on port 11434. It serves local models.", DocIDs: []string{"d"}}
	res, err := ScorePair(context.Background(), p, search, &fakeJudge{supported: true}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Trimmed == 0 {
		t.Fatal("25K chars against a 3000-token budget must trim")
	}
	if res.Faithfulness != 1 {
		t.Fatalf("trimmed context still judges: %+v", res)
	}
}
