package router

import (
	"strings"
)

const (
	FastReason    = "short-simple-prompt"
	QualityReason = "long-or-complex-prompt"
)

var complexKeywords = []string{
	"analyze", "compare", "prove", "contract", "summarize",
	"analyse", "comparer", "résume", "contrat", "facture",
}

func Classify(prompt string) (tier string, reason string) {
	p := strings.TrimSpace(prompt)
	lower := strings.ToLower(p)
	if len(p) > 200 {
		return "quality", QualityReason
	}
	for _, kw := range complexKeywords {
		if strings.Contains(lower, kw) {
			return "quality", QualityReason
		}
	}
	return "fast", FastReason
}
