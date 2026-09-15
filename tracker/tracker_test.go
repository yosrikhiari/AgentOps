package tracker

import (
	"context"
	"errors"
	"testing"
)

func okFunc(count *int, out string) StepFunc {
	return func(ctx context.Context, in string) (string, error) {
		*count++
		return out, nil
	}
}

func TestHappyPath(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	var nR, nD, nV int
	final, err := RunToy(ctx, store, "wf-happy", "what is agentops?",
		okFunc(&nR, "ctx: agentops routes requests"),
		okFunc(&nD, "draft: agentops routes requests fast"),
		okFunc(&nV, "approved: draft looks good"))
	if err != nil {
		t.Fatal(err)
	}
	if final != "approved: draft looks good" {
		t.Fatalf("bad final %q", final)
	}
	steps, err := store.ListSteps(ctx, "wf-happy")
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range steps {
		if st.Status != StatusDone {
			t.Fatalf("step %d status %q", st.Seq, st.Status)
		}
		if st.Attempts != 1 {
			t.Fatalf("step %d attempts %d", st.Seq, st.Attempts)
		}
	}
	if nR != 1 || nD != 1 || nV != 1 {
		t.Fatalf("counts %d %d %d", nR, nD, nV)
	}
	if store.SpanCount() != 3 {
		t.Fatalf("spans %d", store.SpanCount())
	}
	if got := store.WorkflowStatus("wf-happy"); got != StatusDone {
		t.Fatalf("workflow status %q, want done", got)
	}
}

func TestKillResume(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	var nR, nD, nV int
	researcher := okFunc(&nR, "ctx: durable workflows")
	drafterFail := func(ctx context.Context, in string) (string, error) {
		nD++
		return "", errors.New("killed mid-drafter")
	}
	reviewer := okFunc(&nV, "approved")
	_, err := RunToy(ctx, store, "wf-kill", " durable?", researcher, drafterFail, reviewer)
	if err == nil {
		t.Fatal("expected kill error")
	}
	steps, _ := store.ListSteps(ctx, "wf-kill")
	if steps[0].Status != StatusDone {
		t.Fatalf("researcher should stay done, got %q", steps[0].Status)
	}
	if steps[1].Status == StatusDone {
		t.Fatal("drafter should not be done after kill")
	}
	drafterOK := okFunc(&nD, "draft: durable workflows resume")
	final, err := RunToy(ctx, store, "wf-kill", " durable?", researcher, drafterOK, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if final != "approved" {
		t.Fatalf("bad final %q", final)
	}
	if nR != 1 {
		t.Fatalf("researcher re-ran %d times, want 1 (idempotent resume)", nR)
	}
	if nD != 2 {
		t.Fatalf("drafter attempts %d, want 2 (1 kill + 1 resume)", nD)
	}
	if nV != 1 {
		t.Fatalf("reviewer count %d", nV)
	}
	steps, _ = store.ListSteps(ctx, "wf-kill")
	for _, st := range steps {
		if st.Status != StatusDone {
			t.Fatalf("step %d not done after resume", st.Seq)
		}
	}
	if got := store.WorkflowStatus("wf-kill"); got != StatusDone {
		t.Fatalf("workflow status after resume %q, want done", got)
	}
}

// A resume must answer the question the workflow was started with, even if the
// resuming process was launched with a different (or default) input.
func TestResumeUsesStoredInput(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	var seen []string
	researcher := func(ctx context.Context, in string) (string, error) {
		seen = append(seen, in)
		if len(seen) == 1 {
			return "", errors.New("killed mid-researcher")
		}
		return "ctx for " + in, nil
	}
	var nD, nV int
	drafter := okFunc(&nD, "draft")
	reviewer := okFunc(&nV, "approved")
	if _, err := RunToy(ctx, store, "wf-input", "original question", researcher, drafter, reviewer); err == nil {
		t.Fatal("first run should fail")
	}
	out, err := RunToy(ctx, store, "wf-input", "DIFFERENT default input", researcher, drafter, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[1] != "original question" {
		t.Fatalf("resume must reuse stored input, researcher saw %q", seen)
	}
	if out != "approved" {
		t.Fatalf("final %q", out)
	}
}
