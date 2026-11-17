package evals

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// scriptJudge answers from a map keyed by claim, so a test can assert the
// confusion matrix without a model. absent claims read as unsupported.
type scriptJudge struct {
	verdict map[string]bool
	errOn   map[string]bool
	calls   int
}

func (s *scriptJudge) JudgeClaim(_ context.Context, claim, _ string) (Label, string, error) {
	s.calls++
	if s.errOn[claim] {
		return Baseless, "", errors.New("judge down")
	}
	if s.verdict[claim] {
		return Entailed, "because", nil
	}
	return Baseless, "because", nil
}

// markJudge answers from the first line of the context, so reversing the context
// changes the verdict - exactly the instability the flip probe is meant to catch.
type markJudge struct{}

func (markJudge) JudgeClaim(_ context.Context, _ /*claim*/, contextText string) (Label, string, error) {
	first, _, _ := strings.Cut(contextText, "\n")
	if strings.Contains(first, "MARK") {
		return Entailed, "first line decides", nil
	}
	return Contradicted, "first line decides", nil
}

// labelOnlyJudge moves between the two negative labels when the context is reversed:
// the supported/unsupported decision never changes, so only the label-flip counter may
// record it.
type labelOnlyJudge struct{}

func (labelOnlyJudge) JudgeClaim(_ context.Context, _ /*claim*/, contextText string) (Label, string, error) {
	first, _, _ := strings.Cut(contextText, "\n")
	if strings.Contains(first, "MARK") {
		return Baseless, "absent", nil
	}
	return Contradicted, "opposite", nil
}

func TestLoadAuditParsesItems(t *testing.T) {
	items, err := LoadAudit(strings.NewReader(`{"id":"a","claim":"c","context":"x","supported":true}
{"id":"b","claim":"d","context":"x\ny","supported":false,"note":"contradiction"}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "a" || !items[0].Supported || items[1].Supported {
		t.Fatalf("items = %+v", items)
	}
	if items[1].Note != "contradiction" {
		t.Fatalf("note lost: %+v", items[1])
	}
}

// TestLoadAuditRejectsMalformed: a labelled set with a missing field is worse than
// no set - an absent "supported" would silently become false and inflate recall.
func TestLoadAuditRejectsMalformed(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"missing context", `{"id":"a","claim":"c","supported":true}`, "context"},
		{"missing id", `{"claim":"c","context":"x","supported":true}`, "id"},
		{"missing label", `{"id":"a","claim":"c","context":"x"}`, "supported is required"},
		{"bad line", `{`, "line 1"},
		{"empty", "  \n", "no audit items"},
	}
	for _, c := range cases {
		_, err := LoadAudit(strings.NewReader(c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// TestAuditConfusionMatrix: the positive class is "unsupported", because that is the
// detector firing. One of each cell, so every derived rate is a known fraction.
func TestAuditConfusionMatrix(t *testing.T) {
	items := []AuditItem{
		{ID: "tn", Claim: "tn", Context: "x", Supported: true},
		{ID: "fp", Claim: "fp", Context: "x", Supported: true},
		{ID: "tp", Claim: "tp", Context: "x", Supported: false},
		{ID: "fn", Claim: "fn", Context: "x", Supported: false},
	}
	j := &scriptJudge{verdict: map[string]bool{"tn": true, "fp": false, "tp": false, "fn": true}}
	rep := RunAudit(context.Background(), items, []NamedJudge{{Name: "j", Judge: j}}, false)
	run := rep.Judges[0]
	if run.TP != 1 || run.TN != 1 || run.FP != 1 || run.FN != 1 {
		t.Fatalf("confusion = %+v", run.Confusion)
	}
	if run.Verdicts() != 4 || run.Errors != 0 {
		t.Fatalf("verdicts/errors = %d/%d", run.Verdicts(), run.Errors)
	}
	if run.Precision() != 0.5 || run.Recall() != 0.5 || run.FalseAlarmRate() != 0.5 {
		t.Fatalf("rates = %.2f/%.2f/%.2f", run.Precision(), run.Recall(), run.FalseAlarmRate())
	}
}

// TestAuditAgreementKappa: two judges on the same items. Perfect agreement is 1;
// inverted agreement is -1, because a judge that is reliably wrong is not "close".
func TestAuditAgreementKappa(t *testing.T) {
	items := []AuditItem{
		{ID: "1", Claim: "1", Context: "x"},
		{ID: "2", Claim: "2", Context: "x"},
		{ID: "3", Claim: "3", Context: "x"},
		{ID: "4", Claim: "4", Context: "x"},
	}
	agree := RunAudit(context.Background(), items, []NamedJudge{
		{Name: "a", Judge: &scriptJudge{verdict: map[string]bool{"1": true, "2": true, "3": false, "4": false}}},
		{Name: "b", Judge: &scriptJudge{verdict: map[string]bool{"1": true, "2": true, "3": false, "4": false}}},
	}, false)
	if agree.Agreement == nil || agree.Agreement.Pairs != 4 || agree.Agreement.Kappa != 1 {
		t.Fatalf("agreement = %+v", agree.Agreement)
	}
	opposite := RunAudit(context.Background(), items, []NamedJudge{
		{Name: "a", Judge: &scriptJudge{verdict: map[string]bool{"1": true, "2": true, "3": false, "4": false}}},
		{Name: "b", Judge: &scriptJudge{verdict: map[string]bool{"1": false, "2": false, "3": true, "4": true}}},
	}, false)
	if opposite.Agreement == nil || opposite.Agreement.Kappa > -0.9 {
		t.Fatalf("inverted agreement kappa = %+v", opposite.Agreement)
	}
	// One judge: no pairwise statistic, and no fabricated 1.0.
	single := RunAudit(context.Background(), items, []NamedJudge{{Name: "a", Judge: &scriptJudge{}}}, false)
	if single.Agreement != nil {
		t.Fatalf("single judge agreement = %+v", single.Agreement)
	}
}

// TestAuditFlipProbe: the same judge, context lines reversed. A verdict that moves
// is position sensitivity, and it is counted, not hidden in an average.
func TestAuditFlipProbe(t *testing.T) {
	items := []AuditItem{
		{ID: "multi", Claim: "c", Context: "MARK here\nother", Supported: true},
		{ID: "single", Claim: "d", Context: "MARK only", Supported: true},
	}
	rep := RunAudit(context.Background(), items, []NamedJudge{{Name: "m", Judge: markJudge{}}}, true)
	run := rep.Judges[0]
	if run.Flips != 1 || run.FlipComparable != 1 {
		t.Fatalf("flips = %d/%d, want 1 comparable flip", run.Flips, run.FlipComparable)
	}
	if run.DecisionFlips != 1 {
		t.Fatalf("entailed vs contradicted crosses the supported boundary: %+v", run)
	}
	if run.FlipErrors != 0 {
		t.Fatalf("flip errors = %d", run.FlipErrors)
	}
	// A label-only flip (baseless vs contradicted) is instability but not a different
	// decision: it must be reported, and must not fail the decision-flip floor.
	labelOnly := RunAudit(context.Background(), []AuditItem{{ID: "x", Claim: "c", Context: "MARK\nd", Supported: false}},
		[]NamedJudge{{Name: "lo", Judge: labelOnlyJudge{}}}, true)
	lo := labelOnly.Judges[0]
	if lo.Flips != 1 || lo.DecisionFlips != 0 {
		t.Fatalf("label-only flip = %+v", lo)
	}
	// Without the probe nothing is claimed.
	plain := RunAudit(context.Background(), items, []NamedJudge{{Name: "m", Judge: markJudge{}}}, false)
	if plain.Judges[0].FlipComparable != 0 {
		t.Fatalf("flip probe ran when disabled: %+v", plain.Judges[0])
	}
}

// TestAuditCountsErrorsNotVerdicts: a judge that is down must not look like a judge
// that agreed, and one failing item must not abort the whole audit.
func TestAuditCountsErrorsNotVerdicts(t *testing.T) {
	items := []AuditItem{
		{ID: "ok", Claim: "ok", Context: "x", Supported: false},
		{ID: "dead", Claim: "dead", Context: "x", Supported: false},
	}
	j := &scriptJudge{
		verdict: map[string]bool{"ok": false},
		errOn:   map[string]bool{"dead": true},
	}
	rep := RunAudit(context.Background(), items, []NamedJudge{{Name: "j", Judge: j}}, false)
	run := rep.Judges[0]
	if run.Verdicts() != 1 || run.Errors != 1 || run.TP != 1 {
		t.Fatalf("run = %+v", run)
	}
}

func TestAuditVerdictThresholds(t *testing.T) {
	good := AuditReport{Judges: []JudgeRun{{Judge: "a", Confusion: Confusion{TP: 8, TN: 6, FP: 0, FN: 0}}}}
	if v := AuditVerdict(good, 0.8, 0.2, 0.1); !v.Pass {
		t.Fatalf("good run failed: %+v", v)
	}
	missed := AuditReport{Judges: []JudgeRun{{Judge: "a", Confusion: Confusion{TP: 5, TN: 6, FP: 0, FN: 3}}}}
	v := AuditVerdict(missed, 0.8, 0.2, 0.1)
	if v.Pass || !strings.Contains(strings.Join(v.Reasons, " "), "recall") {
		t.Fatalf("low recall passed: %+v", v)
	}
	alarm := AuditReport{Judges: []JudgeRun{{Judge: "a", Confusion: Confusion{TP: 8, TN: 4, FP: 2, FN: 0}}}}
	v = AuditVerdict(alarm, 0.8, 0.2, 0.1)
	if v.Pass || !strings.Contains(strings.Join(v.Reasons, " "), "false alarm") {
		t.Fatalf("false alarms passed: %+v", v)
	}
	flaky := AuditReport{Judges: []JudgeRun{{Judge: "a", Confusion: Confusion{TP: 8, TN: 6},
		DecisionFlips: 2, FlipComparable: 10}}}
	v = AuditVerdict(flaky, 0.8, 0.2, 0.1)
	if v.Pass || !strings.Contains(strings.Join(v.Reasons, " "), "decision flips") {
		t.Fatalf("decision flips passed: %+v", v)
	}
	empty := AuditReport{Judges: []JudgeRun{{Judge: "a", Confusion: Confusion{Errors: 14}}}}
	if v := AuditVerdict(empty, 0.8, 0.2, 0.1); v.Pass {
		t.Fatalf("an audit with no verdicts passed: %+v", v)
	}
}

// TestAuditItemRoundTrip keeps the seed file honest: every line must load back into
// the struct, so a hand-edited label cannot silently disappear.
func TestAuditItemRoundTrip(t *testing.T) {
	in := AuditItem{ID: "x", Claim: "c", Context: "a\nb", Supported: true, Note: "n"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	items, err := LoadAudit(strings.NewReader(string(raw) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0] != in {
		t.Fatalf("round trip = %+v", items)
	}
}
