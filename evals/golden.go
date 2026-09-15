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
// leadingSubordinator returns the opening word when the answer begins with a clause that
// only makes sense next to the question ("Because the scorer…"). "When X, Y" keeps its
// main clause and is fine; "Because X." has none.
// The judge never sees the question, and such answers scored 0 in v1/v2 runs.
func leadingSubordinator(answer string) string {
	first := strings.ToLower(strings.TrimSpace(answer))
	for _, w := range []string{"because ", "since ", "so that ", "to ", "in order to "} {
		if strings.HasPrefix(first, w) {
			return strings.TrimSpace(w)
		}
	}
	return ""
}

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
		if w := leadingSubordinator(p.Answer); w != "" {
			return n, fmt.Errorf("line %d: answer starts with %q — a subordinate clause is not a checkable claim on its own; start with the main clause", i+1, w)
		}
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("no pairs")
	}
	return n, nil
}
