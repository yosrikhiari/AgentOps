package evals

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSplitClaims(t *testing.T) {
	got := SplitClaims("Ollama runs on port 11434. It serves local models! Is that right? Yes.")
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if len(SplitClaims("hi")) != 0 {
		t.Fatal("short text should yield no claims")
	}
}

func TestPrecisionRecall(t *testing.T) {
	p, r := PrecisionRecall([]string{"a", "b", "c"}, []string{"b", "c", "d"}, 3)
	if p != 2.0/3 || r != 2.0/3 {
		t.Fatalf("got %f %f", p, r)
	}
	p, r = PrecisionRecall([]string{"a"}, []string{"b"}, 5)
	if p != 0 || r != 0 {
		t.Fatalf("got %f %f", p, r)
	}
	if p, r := PrecisionRecall(nil, nil, 3); p != 0 || r != 0 {
		t.Fatalf("empty should be zero: %f %f", p, r)
	}
}

type fakeJudge struct {
	supported bool
	calls     int
}

func (f *fakeJudge) JudgeClaim(ctx context.Context, claim, contextText string) (bool, string, error) {
	f.calls++
	return f.supported, "test justification", nil
}

func TestScorePair(t *testing.T) {
	search := func(ctx context.Context, q string, k int) ([]Chunk, error) {
		return []Chunk{{DocID: "01-router", Text: "ctx"}}, nil
	}
	p := Pair{Question: "q?", Answer: "Ollama runs on port 11434. It serves local models.", DocIDs: []string{"01-router"}}
	res, err := ScorePair(context.Background(), p, search, &fakeJudge{supported: true}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Faithfulness != 1 || res.Precision != 1 || res.Recall != 1 {
		t.Fatalf("got %+v", res)
	}
	if len(res.Claims) != 2 {
		t.Fatalf("want 2 claims, got %+v", res.Claims)
	}
}

func TestScoreGeneratedPair(t *testing.T) {
	search := func(ctx context.Context, q string, k int) ([]Chunk, error) {
		return []Chunk{{DocID: "01-router", Text: "ctx"}}, nil
	}
	p := Pair{Question: "q?", Answer: "ignored golden answer here", DocIDs: []string{"01-router"}}
	answer := func(ctx context.Context, question, contextText string) (string, int, error) {
		if question != "q?" || contextText != "ctx" {
			t.Fatalf("answerer got %q %q", question, contextText)
		}
		return "Ollama runs on port 11434. It serves local models.", 12, nil
	}
	res, err := ScoreGeneratedPair(context.Background(), p, search, &fakeJudge{supported: true}, answer, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Faithfulness != 1 || res.Tokens != 12 {
		t.Fatalf("got %+v", res)
	}
	if res.LatencyS < 0 {
		t.Fatalf("negative latency: %+v", res)
	}
}

func TestScoreGeneratedPairErrorFailsLoud(t *testing.T) {
	search := func(ctx context.Context, q string, k int) ([]Chunk, error) {
		return []Chunk{{DocID: "01-router", Text: "ctx"}}, nil
	}
	p := Pair{Question: "q?", Answer: "Ollama runs on port 11434.", DocIDs: []string{"01-router"}}
	answer := func(ctx context.Context, question, contextText string) (string, int, error) {
		return "", 0, errors.New("model down")
	}
	if _, err := ScoreGeneratedPair(context.Background(), p, search, &fakeJudge{supported: true}, answer, 5); err == nil {
		t.Fatal("generation failure must fail the pair, not score zero")
	}
}

func TestWithRetrySucceeds(t *testing.T) {
	n := 0
	err := WithRetry(context.Background(), 3, time.Millisecond, func() error {
		n++
		if n < 3 {
			return errors.New("flaky")
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("err=%v n=%d", err, n)
	}
}

func TestWithRetryGivesUp(t *testing.T) {
	err := WithRetry(context.Background(), 2, time.Millisecond, func() error {
		return errors.New("always")
	})
	if err == nil || !strings.Contains(err.Error(), "always") {
		t.Fatalf("got %v", err)
	}
}

func TestWithRetryNeverRetries4xx(t *testing.T) {
	n := 0
	err := WithRetry(context.Background(), 4, time.Millisecond, func() error {
		n++
		return &StatusError{Backend: "groq", Code: 401}
	})
	if err == nil || n != 1 {
		t.Fatalf("4xx must fail fast: err=%v n=%d", err, n)
	}
	n = 0
	_ = WithRetry(context.Background(), 3, time.Millisecond, func() error {
		n++
		return &StatusError{Backend: "groq", Code: 503}
	})
	if n != 3 {
		t.Fatalf("5xx must retry: n=%d", n)
	}
	n = 0
	_ = WithRetry(context.Background(), 2, time.Millisecond, func() error {
		n++
		return &RateLimitError{}
	})
	if n != 2 {
		t.Fatalf("429 must retry: n=%d", n)
	}
}

func TestParseVerdict(t *testing.T) {
	ok, _ := parseVerdict("Evidence here.\nVERDICT: SUPPORTED")
	if !ok {
		t.Fatal("want supported")
	}
	ok, _ = parseVerdict("Nope.\nverdict: refuted")
	if ok {
		t.Fatal("want refuted")
	}
	ok, _ = parseVerdict("no verdict line at all")
	if ok {
		t.Fatal("missing verdict must default refuted")
	}
	ok, _ = parseVerdict("Context says 3B.\nVERDICT: NOT SUPPORTED")
	if ok {
		t.Fatal("NOT SUPPORTED must be refuted")
	}
	ok, _ = parseVerdict("VERDICT: UNSUPPORTED")
	if ok {
		t.Fatal("UNSUPPORTED must be refuted")
	}
}

// A retrieval miss (recall 0) must never be scored as unfaithful: the judge
// would see the wrong context, so ScorePair skips it entirely with zero calls.
func TestRetrievalMissNotScoredAsUnfaithful(t *testing.T) {
	search := func(ctx context.Context, q string, k int) ([]Chunk, error) {
		return []Chunk{{DocID: "other-doc", Text: "wrong context"}}, nil
	}
	p := Pair{Question: "q?", Answer: "Ollama runs on port 11434. It serves local models.", DocIDs: []string{"01-router"}}
	j := &fakeJudge{supported: true}
	res, err := ScorePair(context.Background(), p, search, j, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !res.RetrievalMiss {
		t.Fatalf("recall 0 must be a retrieval miss, got %+v", res)
	}
	if j.calls != 0 {
		t.Fatalf("judge must not be called on a miss, calls=%d", j.calls)
	}
	if res.Recall != 0 {
		t.Fatalf("want recall 0, got %+v", res)
	}
	if len(res.Claims) != 0 {
		t.Fatalf("miss must carry no judged claims, got %+v", res.Claims)
	}
}

// A doc that split into two chunks can be retrieved twice; recall must still be capped at
// the number of distinct relevant docs found (live run printed recall=2.0 for one pair).
func TestRecallCountsUniqueDocs(t *testing.T) {
	p, r := PrecisionRecall([]string{"08-judges", "08-judges", "01-router", "02-models", "03-ollama"}, []string{"08-judges"}, 5)
	if p != 0.4 {
		t.Fatalf("precision %v, want 0.4 (2 of 5 chunks from the relevant doc)", p)
	}
	if r != 1.0 {
		t.Fatalf("recall %v, want 1.0 (one relevant doc, found)", r)
	}
}
