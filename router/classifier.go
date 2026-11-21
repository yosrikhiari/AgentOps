package router

import (
	"context"
	"fmt"
	"strings"
)

// OllamaClassifier returns a classifier function suitable for Server.Classifier.
// It asks a local Ollama model to label a prompt as "fast" or "quality" and
// parses the first word of the reply. On any error, timeout, or unparseable
// reply it returns an error so the router falls back to the heuristic.
func OllamaClassifier(client *OllamaClient, model string) func(ctx context.Context, prompt string) (string, string, error) {
	if client == nil || model == "" {
		return nil
	}
	return func(ctx context.Context, prompt string) (string, string, error) {
		msgs := []Message{
			{Role: "system", Content: "Classify the user's request as either \"fast\" (short, simple, factual lookup) or \"quality\" (reasoning, analysis, comparison, long or complex). Reply with exactly one word: fast or quality. Nothing else."},
			{Role: "user", Content: prompt},
		}
		text, _, err := client.Generate(ctx, model, msgs, 8)
		if err != nil {
			return "", "", err
		}
		word := strings.ToLower(strings.TrimSpace(text))
		// Take the first token in case the model added punctuation.
		if i := strings.IndexAny(word, " \t\n.,!?"); i >= 0 {
			word = word[:i]
		}
		switch word {
		case "fast":
			return "fast", "learned-classifier", nil
		case "quality":
			return "quality", "learned-classifier", nil
		default:
			return "", "", fmt.Errorf("unparseable classifier reply %q", text)
		}
	}
}
