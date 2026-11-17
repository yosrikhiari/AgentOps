package evals

// The judge audit answers one question the rest of the eval suite assumes: when the
// faithfulness judge says "refuted", is it right? Everything downstream - the drift
// alert, the release gate, the number on the console - inherits whatever error the
// judge has, and until this pass the project had no measurement of it at all.
//
// The unit under test is the judge, not retrieval: each item carries its own context,
// so a failed lookup cannot be mistaken for a failed judgement. The labels are written
// by reading the corpus, which is why the seed set is small and every item names its
// source document.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// AuditItem is one labelled claim/context pair. Supported is the human label, not the
// judge's: the audit exists to compare the two.
type AuditItem struct {
	ID        string `json:"id"`
	Claim     string `json:"claim"`
	Context   string `json:"context"`
	Supported bool   `json:"supported"`
	Note      string `json:"note,omitempty"`
}

// LoadAudit reads a JSONL audit set. Every field is required except the note: an item
// with no "supported" would otherwise load as unsupported by default and quietly inflate
// the very recall we are trying to measure.
func LoadAudit(r io.Reader) ([]AuditItem, error) {
	var items []AuditItem
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var wire struct {
			ID        string `json:"id"`
			Claim     string `json:"claim"`
			Context   string `json:"context"`
			Supported *bool  `json:"supported"`
			Note      string `json:"note"`
		}
		if err := json.Unmarshal([]byte(raw), &wire); err != nil {
			return nil, fmt.Errorf("judge audit line %d: %w", line, err)
		}
		if strings.TrimSpace(wire.ID) == "" {
			return nil, fmt.Errorf("judge audit line %d: id is required", line)
		}
		if strings.TrimSpace(wire.Claim) == "" {
			return nil, fmt.Errorf("judge audit line %d: claim is required", line)
		}
		if strings.TrimSpace(wire.Context) == "" {
			return nil, fmt.Errorf("judge audit line %d: context is required", line)
		}
		if wire.Supported == nil {
			return nil, fmt.Errorf("judge audit line %d: supported is required (a missing label is not a label)", line)
		}
		items = append(items, AuditItem{
			ID: wire.ID, Claim: wire.Claim, Context: wire.Context,
			Supported: *wire.Supported, Note: wire.Note,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no audit items in the file")
	}
	return items, nil
}

// Confusion counts the judge against the labels. The positive class is "unsupported",
// because that is the detector firing; FN is the dangerous cell - a hallucination the
// judge waved through - and FP is the false alarm that made a faithful paraphrase score
// 0.00 while probing this.
type Confusion struct {
	TP, TN, FP, FN int
	Errors         int // calls that returned no verdict; never folded into a cell
}

func (c Confusion) Verdicts() int { return c.TP + c.TN + c.FP + c.FN }

func (c Confusion) Precision() float64 {
	if c.TP+c.FP == 0 {
		return 0
	}
	return float64(c.TP) / float64(c.TP+c.FP)
}

func (c Confusion) Recall() float64 {
	if c.TP+c.FN == 0 {
		return 0
	}
	return float64(c.TP) / float64(c.TP+c.FN)
}

// FalseAlarmRate is FP/(FP+TN): how often a supported claim was called unsupported.
// It is reported separately from precision because the two failures cost different
// things - a false alarm blocks a good release, a miss ships a bad one.
func (c Confusion) FalseAlarmRate() float64 {
	if c.FP+c.TN == 0 {
		return 0
	}
	return float64(c.FP) / float64(c.FP+c.TN)
}

// JudgeRun is one judge's result over the whole set, plus its stability under the flip
// probe (same item, context lines reversed).
type JudgeRun struct {
	Judge string
	Confusion
	// Flips counts label changes under the context-order probe; DecisionFlips counts the
	// subset that changed supported/unsupported. A judge that moves between contradicted
	// and baseless is unstable; a judge that moves across the supported boundary is
	// unreliable, and only the second can change a release decision.
	Flips          int
	DecisionFlips  int
	FlipComparable int // items whose reversed context actually differs
	FlipErrors     int
	// FlipIDs names the items that moved, so a flip rate is actionable instead of a
	// number nobody can chase.
	FlipIDs []string
}

func (r JudgeRun) FlipRate() float64 {
	if r.FlipComparable == 0 {
		return 0
	}
	return float64(r.Flips) / float64(r.FlipComparable)
}

func (r JudgeRun) DecisionFlipRate() float64 {
	if r.FlipComparable == 0 {
		return 0
	}
	return float64(r.DecisionFlips) / float64(r.FlipComparable)
}

// Agreement is Cohen's kappa between two judges on the items both judged.
type Agreement struct {
	Pairs int
	Kappa float64
}

type AuditReport struct {
	Items     int
	Judges    []JudgeRun
	Agreement *Agreement
}

type NamedJudge struct {
	Name  string
	Judge Judge
}

// RunAudit judges every item with every judge. A failing call is counted, never turned
// into a verdict: "the judge was down" and "the judge agreed" must not look alike.
func RunAudit(ctx context.Context, items []AuditItem, judges []NamedJudge, flip bool) AuditReport {
	rep := AuditReport{Items: len(items)}
	verdicts := make([][]*Label, len(judges))
	for ji, nj := range judges {
		run := JudgeRun{Judge: nj.Name}
		verdicts[ji] = make([]*Label, len(items))
		for i, it := range items {
			label, _, err := nj.Judge.JudgeClaim(ctx, it.Claim, it.Context)
			if err != nil {
				run.Errors++
				continue
			}
			ok := label.Supported()
			verdicts[ji][i] = &label
			switch {
			case it.Supported && ok:
				run.TN++
			case it.Supported && !ok:
				run.FP++
			case !it.Supported && !ok:
				run.TP++
			default:
				run.FN++
			}
			if !flip {
				continue
			}
			flipped := flipContext(it.Context)
			if flipped == it.Context {
				continue // one line: there is no order to reverse
			}
			run.FlipComparable++
			label2, _, err := nj.Judge.JudgeClaim(ctx, it.Claim, flipped)
			if err != nil {
				run.FlipErrors++
				continue
			}
			if label2 != label {
				run.Flips++
				if label2.Supported() != label.Supported() {
					run.DecisionFlips++
				}
				run.FlipIDs = append(run.FlipIDs, fmt.Sprintf("%s: %s->%s", it.ID, label, label2))
			}
		}
		rep.Judges = append(rep.Judges, run)
	}
	if len(judges) >= 2 {
		rep.Agreement = cohenKappa(verdicts[0], verdicts[1])
	}
	return rep
}

// flipContext reverses the order of the context's lines. Retrieval returns chunks in
// similarity order, so this is the perturbation a claim-vs-context judge actually sees
// in production when the index changes - the pointwise analogue of AB/BA order swapping.
func flipContext(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

// cohenKappa measures agreement on the decision that matters - supported vs not - over
// the items both judges labelled. Two judges can agree on "not supported" while naming
// different reasons; that is not a disagreement about whether to block a release.
func cohenKappa(a, b []*Label) *Agreement {
	pairs, match, aYes, bYes := 0, 0, 0, 0
	for i := range a {
		if i >= len(b) || a[i] == nil || b[i] == nil {
			continue
		}
		pairs++
		if a[i].Supported() == b[i].Supported() {
			match++
		}
		if a[i].Supported() {
			aYes++
		}
		if b[i].Supported() {
			bYes++
		}
	}
	if pairs == 0 {
		return nil
	}
	n := float64(pairs)
	po := float64(match) / n
	pa, pb := float64(aYes)/n, float64(bYes)/n
	pe := pa*pb + (1-pa)*(1-pb)
	k := 0.0
	switch {
	case pe < 1:
		k = (po - pe) / (1 - pe)
	case po == 1:
		k = 1
	}
	return &Agreement{Pairs: pairs, Kappa: k}
}

// Verdict is the pass/fail reading, kept separate from the report so the console
// or a future policy test can reuse the thresholds.
type Verdict struct {
	Pass    bool
	Reasons []string
}

func AuditVerdict(r AuditReport, minRecall, maxFalseAlarm, maxDecisionFlip float64) Verdict {
	v := Verdict{Pass: true}
	judged := 0
	for _, j := range r.Judges {
		if j.Verdicts() == 0 {
			continue
		}
		judged++
		if rec := j.Recall(); rec < minRecall {
			v.Pass = false
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s recall %.2f below %.2f", j.Judge, rec, minRecall))
		}
		if fa := j.FalseAlarmRate(); fa > maxFalseAlarm {
			v.Pass = false
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s false alarm rate %.2f above %.2f", j.Judge, fa, maxFalseAlarm))
		}
		if df := j.DecisionFlipRate(); df > maxDecisionFlip {
			v.Pass = false
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s decision flips %.2f above %.2f", j.Judge, df, maxDecisionFlip))
		}
	}
	if judged == 0 {
		v.Pass = false
		v.Reasons = append(v.Reasons, "no verdicts: every judge errored")
	}
	return v
}
