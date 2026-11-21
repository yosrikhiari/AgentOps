package router

import (
	"context"
	"testing"
)

func TestPlanLearnedFallsBackToHeuristicOnError(t *testing.T) {
	local := &fakeBackend{name: "ollama", local: true}
	cloud := &fakeBackend{name: "groq"}
	refs := []ModelRef{
		{Tier: "fast", Model: "f", Backend: local},
		{Tier: "quality", Model: "q", Backend: local},
		{Tier: "cloud", Model: "c", Backend: cloud},
	}
	msgs := []Message{{Role: "user", Content: "hi"}}
	// classifier always errors
	bad := func(ctx context.Context, prompt string) (string, string, error) {
		return "", "", context.DeadlineExceeded
	}
	r, err := PlanWithPolicy("auto", msgs, false, refs, "learned", bad)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tier != "fast" {
		t.Fatalf("tier = %q, want fast (heuristic fallback)", r.Tier)
	}
}

func TestPlanLearnedUsesClassifierResult(t *testing.T) {
	local := &fakeBackend{name: "ollama", local: true}
	cloud := &fakeBackend{name: "groq"}
	refs := []ModelRef{
		{Tier: "fast", Model: "f", Backend: local},
		{Tier: "quality", Model: "q", Backend: local},
		{Tier: "cloud", Model: "c", Backend: cloud},
	}
	msgs := []Message{{Role: "user", Content: "hi"}}
	good := func(ctx context.Context, prompt string) (string, string, error) {
		return "quality", "learned-classifier", nil
	}
	r, err := PlanWithPolicy("auto", msgs, false, refs, "learned", good)
	if err != nil {
		t.Fatal(err)
	}
	if r.Tier != "quality" {
		t.Fatalf("tier = %q, want quality (from classifier)", r.Tier)
	}
	if r.Reason != "learned-classifier" {
		t.Fatalf("reason = %q", r.Reason)
	}
}
