package router

import (
	"fmt"
	"strings"
)

const (
	FastReason    = "short-simple-prompt"
	QualityReason = "long-or-complex-prompt"
	// ReasoningReason is a short prompt that asks for reasoning ("why", "explain",
	// "compare", "reason"). Length is the wrong signal for these: a one-line "why"
	// is exactly the case where the quality tier earns its cost.
	ReasoningReason = "reasoning-prompt"
	// GatewayReason marks a question about this gateway's models or routing. The
	// router answers it from recorded facts instead of calling a model (router/meta.go).
	GatewayReason = "gateway-question"
	// ExplicitReason is used when the client named a model instead of "auto".
	ExplicitReason = "explicit-model"
	// ExplicitTierReason is used when the client named a tier ("fast", "quality",
	// "cloud") rather than a model — what the console's model picker sends.
	ExplicitTierReason = "explicit-tier"
)

var complexKeywords = []string{
	"analyze", "compare", "prove", "contract", "summarize",
	"analyse", "comparer", "résume", "contrat", "facture",
}

// reasoningWords are the signals that a prompt asks for judgement rather than a
// lookup. They are matched as whole words so "showcase" is not "why" and
// "reasonably" is not "reason".
var reasoningWords = []string{
	"why", "explain", "explains", "explaining", "reason", "reasons", "reasoning",
	"compare", "compares", "comparing", "comparison", "trade-off", "tradeoff",
	"justify", "analyse", "analyze", "analysis",
	"pourquoi", "expliquer", "comparer", "raison",
}

// containsWord reports whether word occurs in s delimited by non-letters. Lowercase
// both inputs first; the caller already has the lowercased prompt.
func containsWord(s, word string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !isLetterByte(s[j-1])
		end := j + len(word)
		after := end == len(s) || !isLetterByte(s[end])
		if before && after {
			return true
		}
		i = end
	}
}

func isLetterByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// IsTierName reports whether s is a tier the model picker may address by name.
// "local" is deliberately absent: it labels OLLAMA_MODELS extras, not a choice.
func IsTierName(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fast", "quality", "cloud":
		return true
	}
	return false
}

// DefaultSensitiveKeywords flag a prompt as data that must never leave the box. The list is
// deliberately small and obvious; the header X-Tower-Sensitive is the reliable signal.
var DefaultSensitiveKeywords = []string{
	"password", "mot de passe", "iban", "passport", "passeport", "ssn", "social security",
	"credit card", "carte bancaire", "confidential", "confidentiel", "secret", "api key", "cin ",
}

// Classify is ClassifyThread for callers that have only the prompt. It cannot see a
// follow-up such as "why not another one", which needs the thread to be understood.
func Classify(prompt string) (tier string, reason string) {
	return ClassifyThread(prompt, nil)
}

// ClassifyThread decides the tier for one turn. msgs is the whole conversation as the
// client sent it (the router's own input convention); it is read only to recognise a
// short follow-up to an earlier gateway question, and only labels are kept.
//
// Order matters: a gateway question is answered by the router rather than a model, so
// it is named before reasoning; reasoning before length because "why" at 30 characters
// is still a reasoning prompt.
func ClassifyThread(prompt string, msgs []Message) (tier string, reason string) {
	p := strings.TrimSpace(prompt)
	lower := strings.ToLower(p)
	if IsGatewayQuestion(p, msgs) {
		return "quality", GatewayReason
	}
	for _, w := range reasoningWords {
		if containsWord(lower, w) {
			return "quality", ReasoningReason
		}
	}
	if len(p) > 200 {
		return "quality", QualityReason
	}
	for _, kw := range complexKeywords {
		if strings.Contains(lower, kw) {
			return "quality", QualityReason
		}
	}
	return "fast", FastReason
}

// IsSensitive reports whether any message contains a sensitive keyword.
func IsSensitive(msgs []Message, keywords []string) bool {
	for _, m := range msgs {
		lower := strings.ToLower(m.Content)
		for _, kw := range keywords {
			if strings.Contains(lower, kw) {
				return true
			}
		}
	}
	return false
}

// ModelRef is a model on a backend, with the tier it serves. Cloud, when set, marks the
// model as leaving the machine even though its backend is local — since 2026 Ollama serves
// hosted models (":cloud" suffix) through the same local API, so locality is a property of
// the model, not only of the backend.
type ModelRef struct {
	Tier    string
	Model   string
	Backend Backend
	Cloud   bool
}

// IsCloudModel recognises Ollama's hosted-model naming ("name:cloud", "name:cloud-…").
func IsCloudModel(model string) bool {
	i := strings.LastIndex(model, ":")
	return i >= 0 && strings.HasPrefix(model[i+1:], "cloud")
}

// Local reports whether a request to this model stays on the machine: the backend must be
// local AND the model must not be a hosted one.
func (r ModelRef) Local() bool {
	return r.Backend != nil && isLocal(r.Backend) && !r.Cloud && !IsCloudModel(r.Model)
}

func (r ModelRef) local() bool { return r.Local() }

// Route is the plan for one request: the primary model, the fallbacks to try in order if
// the primary fails, and why. The fail-closed rule is applied here, once: a sensitive
// request never has a non-local candidate, whatever the client asked for.
type Route struct {
	Tier       string
	Reason     string
	Sensitive  bool
	Candidates []ModelRef
}

// ErrUnknownModel is returned when the client names a model the gateway does not serve.
type ErrUnknownModel struct{ Model string }

func (e ErrUnknownModel) Error() string { return fmt.Sprintf("unknown model %q", e.Model) }

// ErrSensitiveCloud is returned when the client explicitly asks a cloud model to handle
// sensitive data.
type ErrSensitiveCloud struct{ Model string }

func (e ErrSensitiveCloud) Error() string {
	return fmt.Sprintf("model %q is not local; sensitive requests never leave this machine", e.Model)
}

// ErrModelNotPulled is returned when the client names — or the classified tier
// needs — a model the gateway serves on paper but Ollama does not have on
// disk. The pull hint names the fix; path-like entries (never pullable) get no
// hint.
type ErrModelNotPulled struct{ Model string }

func (e ErrModelNotPulled) Error() string {
	if strings.ContainsAny(e.Model, `/\`) {
		return fmt.Sprintf("model %q is not pulled", e.Model)
	}
	return fmt.Sprintf("model %q is not pulled — run: ollama pull %s", e.Model, e.Model)
}

// Plan decides where a request goes. requested is the client's model field ("" or "auto"
// = let the router decide). refs are all models the gateway serves; the first with
// Tier=="fast" and "quality" are the auto tiers; every non-local ref is a fallback for
// non-sensitive requests.
func Plan(requested string, msgs []Message, sensitive bool, refs []ModelRef) (Route, error) {
	prompt := lastUserContent(msgs)
	var route Route
	route.Sensitive = sensitive
	if requested != "" && requested != "auto" {
		var chosen *ModelRef
		for i := range refs {
			if refs[i].Model == requested || refs[i].Backend.Name()+"/"+refs[i].Model == requested {
				chosen = &refs[i]
				break
			}
		}
		// A tier name is a request for "the model that serves this tier" — what the
		// console's picker sends. An exact model name matching a tier string still
		// wins above, so this cannot shadow a real model called "fast".
		reason := ExplicitReason
		if chosen == nil && IsTierName(requested) {
			for i := range refs {
				if refs[i].Tier == strings.ToLower(strings.TrimSpace(requested)) {
					chosen = &refs[i]
					break
				}
			}
			if chosen == nil {
				// No registered model serves this tier. That is a client asking for
				// something this deployment does not have — 400 unknown_model, not a
				// 502 "backend unavailable". A tier whose model exists but is not
				// pulled is caught earlier, as 503 model_not_pulled.
				return route, ErrUnknownModel{Model: requested}
			}
			reason = ExplicitTierReason
		}
		if chosen == nil {
			return route, ErrUnknownModel{Model: requested}
		}
		if sensitive && !chosen.local() {
			return route, ErrSensitiveCloud{Model: requested}
		}
		route.Tier = chosen.Tier
		route.Reason = reason
		route.Candidates = []ModelRef{*chosen}
		return route, nil
	}
	route.Tier, route.Reason = ClassifyThread(prompt, msgs)
	// Candidate order: the classified tier, then the other local models, then non-local
	// ones. A sensitive request then drops every non-local candidate — primary included, so
	// a quality tier that happens to be a hosted model is skipped, not merely de-prioritised.
	var primary []ModelRef
	var locals, remote []ModelRef
	for _, r := range refs {
		switch {
		case r.Tier == route.Tier && len(primary) == 0:
			primary = append(primary, r)
		case r.local():
			locals = append(locals, r)
		default:
			remote = append(remote, r)
		}
	}
	if len(primary) == 0 {
		return route, fmt.Errorf("no model serves tier %q", route.Tier)
	}
	chain := append(append(primary, locals...), remote...)
	for _, r := range chain {
		if sensitive && !r.local() {
			continue
		}
		route.Candidates = append(route.Candidates, r)
	}
	if len(route.Candidates) == 0 {
		return route, ErrSensitiveCloud{Model: primary[0].Model}
	}
	return route, nil
}

func lastUserContent(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" || msgs[i].Role == "" {
			return msgs[i].Content
		}
	}
	if len(msgs) > 0 {
		return msgs[len(msgs)-1].Content
	}
	return ""
}
