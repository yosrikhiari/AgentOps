package router

import (
	"errors"
	"testing"
)

// TestAnalyseLabelsRepresentativePrompts pins the taxonomy against prompts the repo
// actually sees. A label is a hint for the policy, so one mislabel is cheap - but the
// table is what later phases will measure against, so it must not drift silently.
func TestAnalyseLabelsRepresentativePrompts(t *testing.T) {
	cases := []struct {
		prompt string
		want   Intent
	}{
		{"Return the invoice number and the due date as JSON.", IntentExtract},
		{"Extract every party named in this contract.", IntentExtract},
		{"Summarise this report in three bullets.", IntentSummarise},
		{"Résume ce document en deux phrases.", IntentSummarise},
		{"Is this review positive or negative?", IntentClassify},
		{"Classify each ticket into billing or technical.", IntentClassify},
		{"Why does this function panic when the slice is empty?", IntentCode},
		{"Fix the stack trace and rewrite the SQL query.", IntentCode},
		{"Calculate the interest over 18 months at 4.2%.", IntentMath},
		{"Prove that the sum of two even numbers is even.", IntentMath},
		{"Compare the two offers and explain the trade-offs step by step.", IntentReason},
		{"Draft a polite follow-up email to the supplier.", IntentWrite},
		{"Translate this paragraph into French.", IntentWrite},
		{"hello", IntentChat},
		{"thanks!", IntentChat},
	}
	for _, c := range cases {
		got := Analyse(c.prompt)
		if got.Intent != c.want {
			t.Errorf("Analyse(%q).Intent = %q, want %q", c.prompt, got.Intent, c.want)
		}
		if got.Confidence < 0.5 || got.Confidence > 0.95 {
			t.Errorf("%q: confidence %v outside [0.5,0.95]", c.prompt, got.Confidence)
		}
		if got.Method != IntentMethodHeuristic {
			t.Errorf("%q: method %q", c.prompt, got.Method)
		}
	}
}

// TestDifficultyRisesWithLengthAndStructure: difficulty is a separate scalar because the
// 2026 audit found it - not topic - is what predicts a strong-model win. It must be
// monotonic in the signals we can compute locally.
func TestDifficultyRisesWithLengthAndStructure(t *testing.T) {
	short := Analyse("hi").Difficulty
	long := Analyse("Please read the following and answer carefully: " +
		"the contract was signed in March, amended in June, and terminated in September, " +
		"with three parties disagreeing about the notice period.").Difficulty
	if !(long > short) {
		t.Fatalf("long (%v) must be harder than short (%v)", long, short)
	}
	structured := Analyse("Compare the offers and return a numbered list of trade-offs.").Difficulty
	plain := Analyse("Compare the offers.").Difficulty
	if !(structured > plain) {
		t.Fatalf("structured (%v) must be harder than plain (%v)", structured, plain)
	}
	for _, p := range []string{"", "hi", longPrompt()} {
		if d := Analyse(p).Difficulty; d < 0 || d > 1 {
			t.Fatalf("difficulty %v out of range for %q", d, p)
		}
	}
}

func longPrompt() string {
	s := ""
	for len(s) < 3000 {
		s += "The supplier shall deliver the goods without undue delay. "
	}
	return s
}

// TestDecisionSinkRecordsWhatTheRouterChose: Phase 0 records the decision it already makes.
// The sink must see the intent, the chosen model and the candidate chain, and the response
// must be unchanged.
func TestDecisionSinkRecordsWhatTheRouterChose(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("ok", 4))
	var got []Decision
	srv.DecisionSink = func(d Decision) { got = append(got, d) }

	rec := post(srv, `{"messages":[{"role":"user","content":"Fix this stack trace and the SQL query."}]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("decisions = %d, want 1", len(got))
	}
	d := got[0]
	if d.Intent != IntentCode {
		t.Fatalf("intent = %q, want code", d.Intent)
	}
	if d.Policy != PolicyHeuristicV0 || d.Tier != "fast" || d.Reason != FastReason {
		t.Fatalf("decision = %+v", d)
	}
	if d.Chosen != "ollama/fast-m" {
		t.Fatalf("chosen = %q", d.Chosen)
	}
	if d.Planned != "ollama/fast-m" || d.Fallback || d.Failed {
		t.Fatalf("planned=%q fallback=%v failed=%v", d.Planned, d.Fallback, d.Failed)
	}
	if len(d.Candidates) == 0 || d.TraceID == "" || d.Sensitive {
		t.Fatalf("decision = %+v", d)
	}
}

// TestDecisionSinkRecordsExplicitAndSensitive: an explicit model keeps its reason, and a
// sensitive request records that fact without ever recording prompt text.
func TestDecisionSinkRecordsExplicitAndSensitive(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("ok", 4))
	var got []Decision
	srv.DecisionSink = func(d Decision) { got = append(got, d) }

	rec := post(srv, `{"model":"quality-m","messages":[{"role":"user","content":"password hunter2"}]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	d := got[0]
	if d.Reason != ExplicitReason || d.Chosen != "ollama/quality-m" {
		t.Fatalf("decision = %+v", d)
	}
	if !d.Sensitive {
		t.Fatal("sensitive keyword not recorded")
	}
	// The record is labels and model names only; nothing prompt-shaped may leak.
	if d.Intent == "" || len(d.Intent) > 20 {
		t.Fatalf("intent looks prompt-shaped: %q", d.Intent)
	}
}

// TestDecisionRecordsTheModelThatAnsweredNotThePlan: with the local backend down, the
// cloud fallback answers. The decision must say so - a replay that credited the planned
// model would learn the wrong lesson from every fallback.
func TestDecisionRecordsTheModelThatAnsweredNotThePlan(t *testing.T) {
	srv, _, cloud := twoBackends(errors.New("ollama down"))
	var got []Decision
	srv.DecisionSink = func(d Decision) { got = append(got, d) }
	rec := post(srv, `{"prompt":"hi"}`, nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("decisions = %d", len(got))
	}
	d := got[0]
	if !d.Fallback || d.Failed {
		t.Fatalf("fallback=%v failed=%v", d.Fallback, d.Failed)
	}
	if d.Planned != "ollama/fast-m" {
		t.Fatalf("planned = %q", d.Planned)
	}
	if d.Chosen != cloud.Name()+"/llama-8b" {
		t.Fatalf("chosen = %q, want the cloud model that answered", d.Chosen)
	}
}

func TestDecisionSinkNilIsSafe(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("ok", 4))
	if rec := post(srv, `{"prompt":"hi"}`, nil); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}
