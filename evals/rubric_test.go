package evals

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type scriptCompleter struct {
	reply string
	err   error
	calls int
}

func (s *scriptCompleter) Complete(string) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return s.reply, nil
}

// TestParseRubric: only a real 1-5 after SCORE: is a grade. A missing number is 0
// (unscored), never rounded down to a 1 that would read as "terrible".
func TestParseRubric(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"It answers directly.\nSCORE: 5", 5},
		{"Partly.\nSCORE: 3", 3},
		{"Off topic.\nscore: 1", 1},
		{"Two paragraphs.\nSCORE: 4 - good but vague", 4},
		{"No score line at all", 0},
		{"SCORE: 7", 0},
		{"SCORE: 0", 0},
		{"SCORE: five", 0},
	}
	for _, c := range cases {
		if got := parseRubric(c.raw); got.Value != c.want {
			t.Fatalf("%q: score = %d, want %d", c.raw, got.Value, c.want)
		}
	}
	if reason := parseRubric("Justification here.\nSCORE: 4").Reason; !strings.Contains(reason, "Justification here.") {
		t.Fatalf("reason not split off the score: %q", reason)
	}
}

func TestGradeAnswer(t *testing.T) {
	gen := &scriptCompleter{reply: "It answers.\nSCORE: 4"}
	got, err := GradeAnswer(context.Background(), gen, "what?", "an answer")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != 4 || gen.calls != 1 {
		t.Fatalf("grade = %+v calls=%d", got, gen.calls)
	}
	// An empty answer is not a 1: there is nothing to grade.
	if _, err := GradeAnswer(context.Background(), &scriptCompleter{}, "what?", "   "); err == nil {
		t.Fatal("empty answer must not be graded")
	}
	// A judge failure is an error, not a zero score. A cancelled context makes the retry
	// loop exit after the first attempt, so the test does not sleep through the backoff.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GradeAnswer(cctx, &scriptCompleter{err: errors.New("down")}, "q", "a"); err == nil {
		t.Fatal("judge failure must be an error")
	}
}

type gradeExec struct {
	sql  string
	args []any
}

func (g *gradeExec) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	g.sql, g.args = sql, args
	return 1, nil
}

func TestSaveWorkflowGrade(t *testing.T) {
	ex := &gradeExec{}
	err := SaveWorkflowGrade(context.Background(), ex, WorkflowGrade{
		WorkflowID: "wf1", Judge: "phi4-mini:3.8b", Prompt: RubricPromptVersion,
		Score: 4, Reason: "answers it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ex.sql, "INSERT INTO workflow_grades") || !strings.Contains(ex.sql, "ON CONFLICT") {
		t.Fatalf("sql = %s", ex.sql)
	}
	if len(ex.args) != 6 {
		t.Fatalf("args = %d", len(ex.args))
	}
	if ex.args[0] != "wf1" || ex.args[3] != 4 || ex.args[2] != RubricPromptVersion {
		t.Fatalf("args = %+v", ex.args)
	}
}
