package router

import "strings"

// Intent is the task type of a request. It is a closed taxonomy of labels - never the
// prompt - so it can ride on spans and in the decision table under the same rule as every
// other attribute: prompts are not persisted.
//
// Phase 0 of docs/RESEARCH-intent-routing.md: the signal is recorded, not acted on. The
// 2026 audit of commercial routers found that intent/domain matching alone loses to
// random-at-matched-cost, so intent is one feature of the policy that lands in Phase 1,
// never the whole decision.
type Intent string

const (
	IntentChat      Intent = "chat"
	IntentExtract   Intent = "extract"
	IntentSummarise Intent = "summarise"
	IntentClassify  Intent = "classify"
	IntentCode      Intent = "code"
	IntentMath      Intent = "math"
	IntentReason    Intent = "reason"
	IntentWrite     Intent = "write"
)

// IntentMethodHeuristic names the classifier that produced the signal. Later rungs
// (embedding kNN, a small local model, a learned scorer) report their own method so a
// replay can compare them without guessing.
const IntentMethodHeuristic = "heuristic-v0"

// Signal is what the gateway learned about a prompt before choosing a model. Confidence is
// deliberately capped: a keyword rule that claims 0.99 would be lying, and the policy
// shrinks weak signals toward the roster average.
type Signal struct {
	Intent     Intent  `json:"intent"`
	Confidence float64 `json:"confidence"`
	Difficulty float64 `json:"difficulty"`
	Method     string  `json:"method"`
}

// intentRule is one ordered heuristic. First match wins, so more specific intents (code,
// math) sit before broader ones (reason, chat).
type intentRule struct {
	intent Intent
	words  []string
}

// Rules are bilingual because the router already carries French keywords and this repo's
// operator-facing traffic is mixed. Keep them lowercase; matching lowercases the prompt.
var intentRules = []intentRule{
	{IntentCode, []string{"```", "func ", "function(", "stack trace", "compile", "sql query",
		"panic", "segmentation fault", "regex", "unit test", "typescript", "python", "golang"}},
	{IntentMath, []string{"calculate", "compute the", "solve for", "equation", "integral",
		"derivative", "probability", "pourcentage", "calculer", "prove that"}},
	{IntentSummarise, []string{"summarise", "summarize", "tl;dr", "summary of", "résume",
		"resume ce", "in three bullets", "key points"}},
	{IntentClassify, []string{"classify", "which category", "is this a", "label the",
		"positive or negative", "sentiment", "classer"}},
	{IntentExtract, []string{"extract", "pull out", "list the", "return json", "as json",
		"fields", "extrais", "identify the"}},
	{IntentWrite, []string{"draft", "rewrite", "email", "cover letter", "translate",
		"rédige", "reformulate", "make this sound"}},
	{IntentReason, []string{"why", "compare", "analyse", "analyze", "trade-off", "tradeoff",
		"step by step", "explain", "pourquoi", "comparer", "walk me through"}},
}

// structural words that raise difficulty without deciding the intent.
var structureWords = []string{"json", "numbered list", "bullet", "table", "schema",
	"step by step", "cite", "quote", "sources", "constraints"}

// Analyse labels one prompt. It reads only the last user turn (the router's own input
// convention) and returns a signal whose fields are all bounded.
func Analyse(prompt string) Signal {
	p := strings.TrimSpace(prompt)
	lower := strings.ToLower(p)

	sig := Signal{Intent: IntentChat, Confidence: 0.5, Method: IntentMethodHeuristic}
	for _, rule := range intentRules {
		hits := 0
		for _, w := range rule.words {
			if strings.Contains(lower, w) {
				hits++
			}
		}
		if hits == 0 {
			continue
		}
		sig.Intent = rule.intent
		// One keyword is a hint; several agreeing keywords are a stronger hint. The cap is
		// intentional: a heuristic has no business being certain.
		sig.Confidence = 0.6
		if hits >= 2 {
			sig.Confidence = 0.75
		}
		if rule.intent == IntentCode && strings.Contains(lower, "```") {
			sig.Confidence = 0.9
		}
		break
	}
	sig.Difficulty = difficultyOf(p, lower)
	return sig
}

// difficultyOf is 0-1 and monotonic in the signals a local router can see: length,
// complexity keywords, required output structure, and how many questions are bundled.
// It is separate from intent because difficulty - not topic - is what predicts a
// strong-model win (docs/RESEARCH-intent-routing.md §2.1).
func difficultyOf(prompt, lower string) float64 {
	d := float64(len([]rune(prompt))) / 800.0
	for _, kw := range complexKeywords {
		if strings.Contains(lower, kw) {
			d += 0.3
			break
		}
	}
	for _, w := range structureWords {
		if strings.Contains(lower, w) {
			d += 0.2
			break
		}
	}
	if strings.Count(prompt, "?") > 1 {
		d += 0.1
	}
	if d > 1 {
		d = 1
	}
	if d < 0 {
		d = 0
	}
	return d
}
