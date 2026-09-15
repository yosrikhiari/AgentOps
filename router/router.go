package router

import (
	"fmt"
	"strings"
)

const (
	FastReason    = "short-simple-prompt"
	QualityReason = "long-or-complex-prompt"
	// ExplicitReason is used when the client named a model instead of "auto".
	ExplicitReason = "explicit-model"
)

var complexKeywords = []string{
	"analyze", "compare", "prove", "contract", "summarize",
	"analyse", "comparer", "résume", "contrat", "facture",
}

// DefaultSensitiveKeywords flag a prompt as data that must never leave the box. The list is
// deliberately small and obvious; the header X-AgentOps-Sensitive is the reliable signal.
var DefaultSensitiveKeywords = []string{
	"password", "mot de passe", "iban", "passport", "passeport", "ssn", "social security",
	"credit card", "carte bancaire", "confidential", "confidentiel", "secret", "api key", "cin ",
}

func Classify(prompt string) (tier string, reason string) {
	p := strings.TrimSpace(prompt)
	lower := strings.ToLower(p)
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
		if chosen == nil {
			return route, ErrUnknownModel{Model: requested}
		}
		if sensitive && !chosen.local() {
			return route, ErrSensitiveCloud{Model: requested}
		}
		route.Tier = chosen.Tier
		route.Reason = ExplicitReason
		route.Candidates = []ModelRef{*chosen}
		return route, nil
	}
	route.Tier, route.Reason = Classify(prompt)
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
