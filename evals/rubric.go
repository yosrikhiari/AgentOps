package evals

// Content grading for a workflow's final answer.
//
// The trajectory metric says a run completed cleanly; it says nothing about whether the
// answer was any good. This is the smallest honest thing that does: one rubric call, a
// 1-5 score and a one-sentence reason. It is NOT faithfulness - the workflow does not
// persist the chunks its researcher retrieved, so there is no source to check the answer
// against. Call it quality, and keep it apart from the grounded faithfulness number.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RubricPromptVersion names the rubric, stored beside every grade for the same reason the
// faithfulness prompt version is stored: a different question is a different judge.
const RubricPromptVersion = "answer-quality-v1"

// RubricScore is one grade. Value 0 means "unscored" - a reply with no parseable number is
// not a 1, exactly as a missing verdict is not "supported".
type RubricScore struct {
	Value  int
	Reason string
}

func rubricPrompt(question, answer string) string {
	return fmt.Sprintf(`Score how well the ANSWER addresses the QUESTION, on a scale of 1 to 5.
5 = directly answers it, complete and specific.
3 = partially answers it, or is vague.
1 = does not answer it, or is off-topic.
Judge only whether it answers the question; do not reward length or confidence.
First write one sentence of justification. Then on the last line write exactly: SCORE: <n>

QUESTION:
%s

ANSWER:
%s`, question, answer)
}

var rubricScoreRE = regexp.MustCompile(`(?i)SCORE:\s*([1-5])`)

// parseRubric reads the last SCORE: <n> and clamps nothing: an out-of-range or missing
// number is 0 (unscored), never silently rounded into a grade.
func parseRubric(raw string) RubricScore {
	out := RubricScore{Reason: strings.TrimSpace(raw)}
	idx := strings.LastIndex(strings.ToUpper(raw), "SCORE:")
	if idx < 0 {
		return out
	}
	out.Reason = strings.TrimSpace(raw[:idx])
	m := rubricScoreRE.FindStringSubmatch(raw[idx:])
	if m == nil {
		return out
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 || n > 5 {
		return out
	}
	out.Value = n
	return out
}

// Completer is the one method rubric grading needs; *OllamaGenerator satisfies it.
type Completer interface {
	Complete(prompt string) (string, error)
}

// GradeAnswer asks the model for a rubric score. A failure to grade is an error, not a 0:
// "we could not ask" and "the answer is bad" must not look alike.
func GradeAnswer(ctx context.Context, gen Completer, question, answer string) (RubricScore, error) {
	if strings.TrimSpace(answer) == "" {
		return RubricScore{}, fmt.Errorf("nothing to grade: the workflow produced no answer")
	}
	var raw string
	err := WithRetry(ctx, 3, time.Second, func() error {
		r, err := gen.Complete(rubricPrompt(question, answer))
		if err != nil {
			return err
		}
		raw = r
		return nil
	})
	if err != nil {
		return RubricScore{}, err
	}
	return parseRubric(raw), nil
}
