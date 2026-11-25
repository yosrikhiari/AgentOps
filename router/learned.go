package router

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
)

// learnedWeights is the on-disk format produced by ml/train_router.py. It is a
// small logistic regression: features are extracted from the prompt, scaled by
// weights, summed with bias, passed through a sigmoid. prob >= threshold means
// "quality", otherwise "fast". Keeping the model this small (a handful of
// floats) is what makes scoring sub-millisecond and the binary self-contained.
type learnedWeights struct {
	Version   string    `json:"version"`
	Features  []string  `json:"features"`
	Mean      []float64 `json:"mean"`
	Scale     []float64 `json:"scale"`
	Weights   []float64 `json:"weights"`
	Bias      float64   `json:"bias"`
	Threshold float64   `json:"threshold"`
}

// LearnedClassifier returns a classifier function that scores a prompt with a
// pre-trained logistic regression loaded from path. Returns nil if path is empty
// or the file cannot be read/parsed, so the caller falls back to the heuristic.
// The classifier is cheap: feature extraction + a dot product, no I/O on the
// hot path after the first load (weights are cached).
func LearnedClassifier(path string) func(ctx context.Context, prompt string) (string, string, error) {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	w, err := loadLearnedWeights(path)
	if err != nil {
		return nil
	}
	return func(ctx context.Context, prompt string) (string, string, error) {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		default:
		}
		prob, err := w.score(prompt)
		if err != nil {
			return "", "", err
		}
		if prob >= w.Threshold {
			return "quality", "learned-classifier", nil
		}
		return "fast", "learned-classifier", nil
	}
}

func loadLearnedWeights(path string) (*learnedWeights, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w learnedWeights
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	if len(w.Features) == 0 || len(w.Features) != len(w.Weights) ||
		len(w.Features) != len(w.Mean) || len(w.Features) != len(w.Scale) {
		return nil, fmt.Errorf("learned weights: feature/weight/mean/scale length mismatch")
	}
	if w.Threshold <= 0 || w.Threshold >= 1 {
		w.Threshold = 0.5
	}
	return &w, nil
}

// score computes the probability that the prompt is "quality".
func (w *learnedWeights) score(prompt string) (float64, error) {
	feats := extractFeatures(prompt)
	if len(feats) != len(w.Features) {
		return 0, fmt.Errorf("feature count %d != weights %d", len(feats), len(w.Features))
	}
	z := w.Bias
	for i, f := range feats {
		scaled := f
		if w.Scale[i] != 0 {
			scaled = (f - w.Mean[i]) / w.Scale[i]
		}
		z += scaled * w.Weights[i]
	}
	return 1.0 / (1.0 + math.Exp(-z)), nil
}

// extractFeatures turns a prompt into the numeric vector the model was trained on.
// Order must match ml/train_router.py's FEATURE_ORDER.
func extractFeatures(prompt string) []float64 {
	lower := strings.ToLower(prompt)
	words := strings.Fields(lower)
	reasoning := 0
	for _, rw := range []string{"why", "explain", "compare", "reason", "analyze", "describe"} {
		if strings.Contains(lower, rw) {
			reasoning++
		}
	}
	qmarks := strings.Count(prompt, "?")
	return []float64{
		float64(len(prompt)),
		float64(len(words)),
		float64(reasoning),
		float64(qmarks),
	}
}
