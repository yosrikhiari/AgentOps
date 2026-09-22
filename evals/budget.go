package evals

// Track R — context budget. A counted ledger for machine-assembled prompts
// (judge context, researcher context), never for user traffic: silently
// rewriting what the user asked would be a correctness violation worse than
// the unbounded growth this guards against.
//
// EstimateTokens is chars/4 rounded up: an ESTIMATE, good enough for a
// guardrail (not billing), optimistic on prose and loose on code/CJK. The
// default budget below carries headroom against that error; both numbers are
// documented in docs/API.md alongside the direction of the error.
// TrimToBudget drops from the tail: callers must pass relevance-ordered
// chunks (pgvector similarity order), so the head — the most relevant
// context — always survives.
const DefaultBudgetTokens = 3000

func EstimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// TrimToBudget keeps the relevance-ordered head that fits budget tokens and
// reports how many chunks were dropped. Under budget it returns the input
// untouched with zero trimmed.
func TrimToBudget(chunks []string, budget int) (kept []string, trimmed int) {
	used := 0
	for i, c := range chunks {
		if used+EstimateTokens(c) > budget && len(kept) > 0 {
			return chunks[:i], len(chunks) - i
		}
		used += EstimateTokens(c)
		kept = append(kept, c)
	}
	return kept, 0
}
