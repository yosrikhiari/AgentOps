package evals

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateGolden checks a golden .jsonl before it is frozen. It returns the number of
// pairs and the first problem found. Every failure here would otherwise surface later as a
// silent wrong score: an unknown doc id makes recall meaningless, a duplicate question
// collides on the eval_pair_scores primary key, and an answer with no scorable claim
// (SplitClaims drops fragments under 10 runes) always scores faithfulness 0.
func ValidateGolden(raw []byte, knownDocs map[string]bool) (int, error) {
	n := 0
	seen := map[string]int{}
	for i, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var p Pair
		if err := json.Unmarshal(line, &p); err != nil {
			return n, fmt.Errorf("line %d: %w", i+1, err)
		}
		if prev, dup := seen[p.Question]; dup {
			return n, fmt.Errorf("line %d: duplicate question of line %d (eval_pair_scores keys on question)", i+1, prev)
		}
		seen[p.Question] = i + 1
		if strings.TrimSpace(p.Question) == "" || strings.TrimSpace(p.Answer) == "" || len(p.DocIDs) == 0 {
			return n, fmt.Errorf("line %d: empty field", i+1)
		}
		for _, d := range p.DocIDs {
			if !knownDocs[d] {
				return n, fmt.Errorf("line %d: unknown doc %q", i+1, d)
			}
		}
		if len(SplitClaims(p.Answer)) == 0 {
			return n, fmt.Errorf("line %d: answer %q yields no scorable claim — write it as a full sentence", i+1, p.Answer)
		}
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("no pairs")
	}
	return n, nil
}
