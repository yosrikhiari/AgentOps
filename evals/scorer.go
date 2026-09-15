package evals

import (
	"context"
	"strings"
	"unicode"
)

func SplitClaims(answer string) []string {
	var claims []string
	start := 0
	runes := []rune(answer)
	flush := func(end int) {
		c := strings.TrimSpace(string(runes[start:end]))
		if len([]rune(c)) >= 10 {
			claims = append(claims, c)
		}
	}
	for i, r := range runes {
		if r == '.' || r == '!' || r == '?' {
			next := i + 1
			if next >= len(runes) || unicode.IsSpace(runes[next]) {
				flush(i + 1)
				start = next
			}
		}
	}
	flush(len(runes))
	return claims
}

func PrecisionRecall(retrieved []string, relevant []string, k int) (precision, recall float64) {
	if k <= 0 || len(relevant) == 0 {
		return 0, 0
	}
	if len(retrieved) > k {
		retrieved = retrieved[:k]
	}
	rel := map[string]bool{}
	for _, d := range relevant {
		rel[d] = true
	}
	// precision: share of retrieved chunks that come from a relevant doc (two chunks of
	// the same doc both count — they are both useful context). recall: share of relevant
	// docs found at least once — a doc retrieved twice is still one doc, so recall never
	// exceeds 1.
	hits := 0
	found := map[string]bool{}
	for _, d := range retrieved {
		if rel[d] {
			hits++
			found[d] = true
		}
	}
	return float64(hits) / float64(len(retrieved)), float64(len(found)) / float64(len(relevant))
}

type ClaimResult struct {
	Claim         string `json:"claim"`
	Supported     bool   `json:"supported"`
	Justification string `json:"justification"`
}

type PairResult struct {
	Question     string        `json:"question"`
	Faithfulness float64       `json:"faithfulness"`
	Claims       []ClaimResult `json:"claims"`
	Precision    float64       `json:"precision_at_k"`
	Recall       float64       `json:"recall_at_k"`
	Retrieved    []string      `json:"retrieved"`
}

type Searcher func(ctx context.Context, query string, topK int) ([]Chunk, error)

func ScorePair(ctx context.Context, p Pair, search Searcher, judge Judge, topK int) (PairResult, error) {
	chunks, err := search(ctx, p.Question, topK)
	if err != nil {
		return PairResult{}, err
	}
	var ctxParts []string
	var retrieved []string
	for _, c := range chunks {
		ctxParts = append(ctxParts, c.Text)
		retrieved = append(retrieved, c.DocID)
	}
	contextText := strings.Join(ctxParts, "\n---\n")
	claims := SplitClaims(p.Answer)
	res := PairResult{Question: p.Question, Retrieved: retrieved}
	supported := 0
	for _, cl := range claims {
		ok, just, err := judge.JudgeClaim(ctx, cl, contextText)
		if err != nil {
			return res, err
		}
		res.Claims = append(res.Claims, ClaimResult{Claim: cl, Supported: ok, Justification: just})
		if ok {
			supported++
		}
	}
	if len(claims) > 0 {
		res.Faithfulness = float64(supported) / float64(len(claims))
	}
	res.Precision, res.Recall = PrecisionRecall(retrieved, p.DocIDs, topK)
	return res, nil
}
