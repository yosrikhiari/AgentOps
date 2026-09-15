package evals

import (
	"errors"
	"strings"
	"testing"
)

type fakeGenerator struct {
	out string
	err error
}

func (f *fakeGenerator) Complete(prompt string) (string, error) {
	return f.out, f.err
}

func TestDraftPairsParsesJSON(t *testing.T) {
	gen := &fakeGenerator{out: `Here are pairs:
[{"question": "What port?", "answer": "11434"}, {"question": "Bad", "answer": ""}]`}
	pairs, err := DraftPairs(gen, []Chunk{{DocID: "03-ollama", Text: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].Question != "What port?" {
		t.Fatalf("got %+v", pairs)
	}
	if len(pairs[0].DocIDs) != 1 || pairs[0].DocIDs[0] != "03-ollama" {
		t.Fatalf("bad doc ids %+v", pairs[0])
	}
}

func TestDraftPairsSkipsGarbage(t *testing.T) {
	gen := &fakeGenerator{out: "no json here"}
	pairs, err := DraftPairs(gen, []Chunk{{DocID: "d", Text: "x"}})
	if err != nil || len(pairs) != 0 {
		t.Fatalf("got %+v %v", pairs, err)
	}
}

func TestDraftPairsPropagatesError(t *testing.T) {
	gen := &fakeGenerator{err: errors.New("down")}
	_, err := DraftPairs(gen, []Chunk{{DocID: "d", Text: "x"}})
	if err == nil || !strings.Contains(err.Error(), "down") {
		t.Fatalf("want wrapped error, got %v", err)
	}
}
