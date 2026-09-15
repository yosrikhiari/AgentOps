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

// ModelRef is a model on a backend, with the tier it serves.
type ModelRef struct {
	Tier    string
	Model   string
	Backend Backend
}

func (r ModelRef) local() bool { return r.Backend != nil && isLocal(r.Backend) }

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
	for _, r := range refs {
		if r.Tier == route.Tier {
			route.Candidates = append(route.Candidates, r)
			break
		}
	}
	if len(route.Candidates) == 0 {
		return route, fmt.Errorf("no model serves tier %q", route.Tier)
	}
	// Fallbacks: other local tiers first, then cloud — cloud only when nothing is sensitive.
	for _, r := range refs {
		if r.Model == route.Candidates[0].Model && r.Backend == route.Candidates[0].Backend {
			continue
		}
		if r.local() {
			route.Candidates = append(route.Candidates, r)
		}
	}
	if !sensitive {
		for _, r := range refs {
			if !r.local() {
				route.Candidates = append(route.Candidates, r)
			}
		}
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
