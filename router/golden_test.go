package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The routing golden set.
//
// These are the questions an operator actually asks a gateway about itself. Unlike
// the eval golden set (a frozen corpus of document questions scored by a judge), the
// expected answer names the model this deployment really used, so it can only be
// checked against the router that chose it. Each case must be answered by the router,
// name the model that last answered and the reason it was chosen — never a guess.

func TestReasoningPromptsGoToQualityEvenWhenShort(t *testing.T) {
	cases := []struct {
		prompt     string
		wantTier   string
		wantReason string
	}{
		{"why are you using this model", "quality", GatewayReason},
		{"which models do you have", "quality", GatewayReason},
		{"why does this function panic", "quality", ReasoningReason},
		{"explain what changed", "quality", ReasoningReason},
		{"compare the two offers", "quality", ReasoningReason},
		{"what is the reason for the outage", "quality", ReasoningReason},
		{"hi", "fast", FastReason},
		{"thanks!", "fast", FastReason},
		// A model question that is not about this deployment must reach a model.
		{"which model should I use for a classifier", "fast", FastReason},
	}
	for _, c := range cases {
		tier, reason := Classify(c.prompt)
		if tier != c.wantTier || reason != c.wantReason {
			t.Errorf("Classify(%q) = %s/%s, want %s/%s", c.prompt, tier, reason, c.wantTier, c.wantReason)
		}
	}
}

// A follow-up carries no noun: "why not another one" is a gateway question only
// because the previous user turn was one. The thread is the only context a stateless
// gateway has, so that is what the detector reads.
func TestGatewayFollowUpNeedsTheThread(t *testing.T) {
	thread := []Message{
		{Role: "user", Content: "why are you using this model"},
		{Role: "assistant", Content: "Last model turn: fast-m ..."},
		{Role: "user", Content: "why not another one"},
	}
	if !IsGatewayQuestion("why not another one", thread) {
		t.Fatal("follow-up after a gateway question must stay a gateway question")
	}
	ordinary := []Message{
		{Role: "user", Content: "the deploy failed last night"},
		{Role: "assistant", Content: "what did the logs say?"},
		{Role: "user", Content: "why did it fail"},
	}
	if IsGatewayQuestion("why did it fail", ordinary) {
		t.Fatal("an ordinary why-question must reach a model, not the router")
	}
}

// The golden set proper: every question is answered by the router, names the model of
// the last real turn and the reason it was routed there, and never calls a backend.
func TestRouterGoldenMetaQuestions(t *testing.T) {
	be := localFake("bonjour", 10)
	srv := NewServer("fast-m", "quality-m", be)
	srv.AddModel(ModelRef{Tier: "cloud", Model: "cloud-m", Backend: &fakeBackend{name: "groq"}})
	if _, err := srv.Chat("hi", 0); err != nil { // one real turn, so there is a last trace
		t.Fatal(err)
	}
	callsAfterWarmUp := be.calls

	cases := []struct {
		name    string
		history []Message
		prompt  string
	}{
		{name: "which models", prompt: "which models do you have"},
		{name: "why this model", prompt: "why are you using this model"},
		{name: "why not another", prompt: "why not another one", history: []Message{
			{Role: "user", Content: "why are you using this model"},
			{Role: "assistant", Content: "Last model turn: fast-m on the fast tier via ollama, because short-simple-prompt."},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msgs := append(append([]Message{}, c.history...), Message{Role: "user", Content: c.prompt})
			out, err := srv.Complete(context.Background(), ChatRequest{Messages: msgs}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if out.AnsweredBy != "router" {
				t.Fatalf("answered_by = %q, want router", out.AnsweredBy)
			}
			if out.Reason != GatewayReason {
				t.Fatalf("reason = %q, want %q", out.Reason, GatewayReason)
			}
			if out.Tier != "quality" {
				t.Fatalf("tier = %q, want quality", out.Tier)
			}
			// Names the real model of the last turn and that turn's reason.
			if !strings.Contains(out.Text, "fast-m") {
				t.Fatalf("answer does not name the model that answered:\n%s", out.Text)
			}
			if !strings.Contains(out.Text, FastReason) {
				t.Fatalf("answer does not name the routing reason:\n%s", out.Text)
			}
			// Names the whole roster, with tiers, including the model this turn would use.
			for _, want := range []string{"fast-m · fast · local", "quality-m · quality · local", "cloud-m · cloud · cloud"} {
				if !strings.Contains(out.Text, want) {
					t.Fatalf("answer missing roster line %q:\n%s", want, out.Text)
				}
			}
			if !strings.Contains(out.Text, "no model was called") {
				t.Fatalf("answer does not say it was answered by the router:\n%s", out.Text)
			}
			if be.calls != callsAfterWarmUp {
				t.Fatalf("a gateway question called the model %d extra time(s)", be.calls-callsAfterWarmUp)
			}
		})
	}
}

// The roster and this turn's route travel in the system prompt, so a model answers
// "why this model?" from facts instead of inventing a policy.
func TestSystemPromptNamesRosterAndRoute(t *testing.T) {
	be := localFake("ok", 4)
	srv := NewServer("fast-m", "quality-m", be)
	srv.AddModel(ModelRef{Tier: "cloud", Model: "cloud-m", Backend: &fakeBackend{name: "groq"}})
	if _, err := srv.Chat("hi", 0); err != nil {
		t.Fatal(err)
	}
	if len(be.lastMsgs) == 0 || be.lastMsgs[0].Role != "system" {
		t.Fatalf("no system prompt injected: %+v", be.lastMsgs)
	}
	got := be.lastMsgs[0].Content
	for _, want := range []string{
		"You are the assistant behind Tower, a self-hosted LLM gateway.",
		"fast-m · fast · local",
		"quality-m · quality · local",
		"cloud-m · cloud · cloud",
		"This turn was routed to fast-m because short-simple-prompt.",
		"Do not offer generic help.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, got)
		}
	}
}

// A sensitivity decision is made on what the client sent, before the gateway's own
// prompt is prepended: the injected text must never be able to flip a prompt to
// fail-closed (or, worse, make a sensitive prompt look safe).
func TestSystemPromptDoesNotChangeSensitivity(t *testing.T) {
	be := localFake("ok", 4)
	srv := NewServer("fast-m", "quality-m", be)
	if _, err := srv.Chat("my password is hunter2, summarise it", 0); err != nil {
		t.Fatal(err)
	}
	if got := be.lastMsgs[0].Content; strings.Contains(strings.ToLower(got), "password") {
		t.Fatalf("sensitive prompt leaked into the injected system prompt:\n%s", got)
	}
}

// The picker's four choices are tiers, and the server resolves them so the console does
// not need to know which model serves which tier.
func TestTierAliasRouting(t *testing.T) {
	local := localFake("x", 1)
	cloud := &fakeBackend{name: "groq", text: "y", tokens: 2}
	srv := NewServer("fast-m", "quality-m", local)
	srv.AddModel(ModelRef{Tier: "cloud", Model: "cloud-m", Backend: cloud})

	out, err := srv.Complete(context.Background(), ChatRequest{Model: "quality", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Model != "quality-m" || out.Tier != "quality" || out.Reason != ExplicitTierReason {
		t.Fatalf("quality alias: model=%q tier=%q reason=%q", out.Model, out.Tier, out.Reason)
	}

	out, err = srv.Complete(context.Background(), ChatRequest{Model: "cloud", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Model != "cloud-m" {
		t.Fatalf("cloud alias: %q", out.Model)
	}

	// Fail-closed survives the alias: asking a cloud tier for a sensitive prompt is
	// refused, not silently downgraded to a local model.
	_, err = srv.Complete(context.Background(), ChatRequest{Model: "cloud", Sensitive: true,
		Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	var sens ErrSensitiveCloud
	if !errors.As(err, &sens) {
		t.Fatalf("sensitive + cloud alias: got %v, want ErrSensitiveCloud", err)
	}

	// A model really named like a tier still wins over the alias.
	srv2 := NewServer("fast-m", "quality-m", localFake("z", 1))
	srv2.AddModel(ModelRef{Tier: "local", Model: "quality", Backend: local})
	out, err = srv2.Complete(context.Background(), ChatRequest{Model: "quality", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Model != "quality" || out.Reason != ExplicitReason {
		t.Fatalf("exact model name must win over the tier alias: model=%q reason=%q", out.Model, out.Reason)
	}

	// A tier this deployment does not serve is the client's mistake, not a dead
	// backend: 400 unknown_model, matched by the one error shape.
	srv3 := NewServer("fast-m", "quality-m", localFake("z", 1))
	rec := post(srv3, `{"model":"cloud","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown_model") {
		t.Fatalf("unconfigured tier: %d %s", rec.Code, rec.Body.String())
	}
}

// Every chat trace has three spans. A failed request used to emit only two, which is
// indistinguishable from a lost telemetry write; router.respond now says the router
// answered - with an error.
func TestFailurePathEmitsRespond(t *testing.T) {
	be := localFake("", 0)
	be.err = errors.New("down")
	srv := NewServer("fast-m", "quality-m", be)
	var calls []spanCall
	srv.SpanSink = func(traceID, spanID, parentID, name, attrs string) {
		calls = append(calls, spanCall{traceID, spanID, parentID, name})
	}
	if _, err := srv.Chat("hi", 0); err == nil {
		t.Fatal("want an error")
	}
	if len(calls) != 3 {
		t.Fatalf("want 3 spans on the failure path, got %d: %+v", len(calls), calls)
	}
	if calls[2].name != "router.respond" {
		t.Fatalf("last span = %q, want router.respond", calls[2].name)
	}
	if calls[2].parentID != calls[1].spanID {
		t.Fatal("respond span must hang off the generate span")
	}
}

// A gateway question produces three spans too, and no model.generate: a span must not
// claim a model produced text that no model produced.
func TestGatewayQuestionTraceShape(t *testing.T) {
	be := localFake("ok", 4)
	srv := NewServer("fast-m", "quality-m", be)
	var calls []spanCall
	srv.SpanSink = func(traceID, spanID, parentID, name, attrs string) {
		calls = append(calls, spanCall{traceID, spanID, parentID, name})
	}
	if _, err := srv.Chat("which models do you have", 0); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(calls))
	for _, c := range calls {
		got = append(got, c.name)
	}
	if strings.Join(got, ",") != "route.decide,router.answer,router.respond" {
		t.Fatalf("span names = %v", got)
	}
	if be.calls != 0 {
		t.Fatalf("gateway question called the model %d time(s)", be.calls)
	}
}

// The response carries what the footer and the popover need, so the console never has
// to re-derive a decision.
func TestResponseCarriesTierAndCandidates(t *testing.T) {
	srv := NewServer("fast-m", "quality-m", localFake("ok", 4))
	rec := post(srv, `{"messages":[{"role":"user","content":"hi"}]}`, nil)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["tier"] != "fast" {
		t.Fatalf("tier = %v", out["tier"])
	}
	cands, ok := out["candidates"].([]any)
	if !ok || len(cands) != 2 {
		t.Fatalf("candidates = %v", out["candidates"])
	}
	first, _ := cands[0].(map[string]any)
	if first["model"] != "fast-m" || first["tier"] != "fast" || first["local"] != true {
		t.Fatalf("first candidate = %v", cands[0])
	}
}
