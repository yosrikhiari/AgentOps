package router

import (
	"fmt"
	"strings"
	"time"
)

// Answering questions about the gateway.
//
// A gateway whose assistant invents its own routing is worse than one that says
// "no model ran; here is what the router decided". Everything in this file works
// from labels the router already records (docs/RULES.md §2): model names, tiers,
// reasons and latencies. No prompt, no answer, no database read on the request path.

// Candidate is one registered model as the API and the console see it: a fact, not a
// state, so it is a chip, never a pill.
type Candidate struct {
	Model string `json:"model"`
	Tier  string `json:"tier"`
	Local bool   `json:"local"`
}

// LastRoute is the last turn a model actually answered. It is held in memory, on the
// single gateway process, because the gateway is stateless by ADR-0010 and has no
// store of its own; the console is the only reader that wants more history and it
// reads spans. Labels only — never the prompt or the answer.
type LastRoute struct {
	Model            string
	Backend          string
	Tier             string
	Reason           string
	LatencyS         float64
	PromptTokens     int
	CompletionTokens int
	At               time.Time
}

// rememberRoute records the turn a model answered. Mutex-protected because requests
// are served concurrently; the lock is held for a struct copy, not for any I/O, so it
// cannot move the router-overhead budget.
func (s *Server) rememberRoute(r LastRoute) {
	s.lastMu.Lock()
	s.lastRoute = &r
	s.lastMu.Unlock()
}

// LastTurn returns a copy of the last model-answered turn, or nil when no model has
// answered in this process yet. A copy, so a caller cannot read a struct that another
// request is replacing field by field.
func (s *Server) LastTurn() *LastRoute {
	s.lastMu.RLock()
	defer s.lastMu.RUnlock()
	if s.lastRoute == nil {
		return nil
	}
	cp := *s.lastRoute
	return &cp
}

// gatewayQuestionTerms are phrases that can only be about this gateway's models or
// routing. Deliberately phrases, not the bare word "model": "which model should I use
// for a classifier" is a question about models, but "the gateway is down, how do I
// debug it" must still reach a model.
var gatewayQuestionTerms = []string{
	"which model", "what model", "which models", "what models",
	"why this model", "why that model", "why are you using", "why did you use",
	"why are you answering", "why is this model", "why is that model",
	"how does routing", "how do you route", "how is routing", "how are you routed",
	"your routing", "routing rule", "routing rules", "routing decision",
	"why not another", "why not a different model", "why not the other",
	"what other models", "other models do you",
}

// IsGatewayQuestion reports whether this turn asks the gateway about itself. The
// thread is read because a stateless gateway has no other context: "why not another
// one" carries no noun of its own and is only a gateway question when the previous
// user turn was one. Nothing is stored — the conversation as sent is the context.
func IsGatewayQuestion(prompt string, msgs []Message) bool {
	if isDirectGatewayQuestion(prompt) {
		return true
	}
	if !isGatewayFollowUp(prompt) {
		return false
	}
	prev := previousUserPrompt(prompt, msgs)
	return prev != "" && isDirectGatewayQuestion(prev)
}

func isDirectGatewayQuestion(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	for _, t := range gatewayQuestionTerms {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// isGatewayFollowUp recognises the shapes a follow-up takes. It is only consulted
// when the previous user turn was a gateway question, so a short "why?" in an
// ordinary conversation still reaches a model.
func isGatewayFollowUp(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if len([]rune(lower)) > 120 {
		return false
	}
	if containsWord(lower, "why") || containsWord(lower, "how") {
		return true
	}
	for _, t := range []string{"what about", "how come", "instead", "another one"} {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// previousUserPrompt is the user turn before the current one. msgs is the router's
// whole input (the current prompt is normally its last user message); an assistant
// or system turn in between is skipped, and an exact repeat is not the previous turn.
func previousUserPrompt(current string, msgs []Message) string {
	cur := strings.TrimSpace(current)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" && msgs[i].Role != "" {
			continue
		}
		if strings.TrimSpace(msgs[i].Content) == cur {
			continue
		}
		return msgs[i].Content
	}
	return ""
}

// Roster is the registered models in registration order, de-duplicated: the same
// model named through two refs is one fact, not two.
func Roster(refs []ModelRef) []Candidate {
	seen := map[string]bool{}
	out := make([]Candidate, 0, len(refs))
	for _, r := range refs {
		key := r.Backend.Name() + "/" + r.Model
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Candidate{Model: r.Model, Tier: r.Tier, Local: r.Local()})
	}
	return out
}

// locality is the word the system prompt uses: a fact about where the model runs,
// which matters because sensitive prompts never leave the box.
func locality(local bool) string {
	if local {
		return "local"
	}
	return "cloud"
}

// routingLine is the one sentence both the system prompt and the router's own answer
// state, so a model and the router cannot describe the policy differently.
const routingLine = "Routing: short/simple prompts go to the fast tier; long or complex prompts and " +
	"reasoning prompts go to the quality tier; cloud is fallback only and never used for sensitive prompts."

// SystemPrompt is built server-side for every request that reaches a model, and is
// never stored: the client's thread is client-held (ADR-0010), so an injected prompt
// that were persisted would be resent and duplicated every turn.
//
// model and reason are the turn's own route, so the model answers "why this model?"
// from what actually happened rather than from the roster in the abstract.
func SystemPrompt(roster []Candidate, model, reason string) string {
	var b strings.Builder
	b.WriteString("You are the assistant behind Tower, a self-hosted LLM gateway.\n")
	b.WriteString("Registered models:\n")
	for _, c := range roster {
		fmt.Fprintf(&b, "%s · %s · %s\n", c.Model, c.Tier, locality(c.Local))
	}
	b.WriteString(routingLine)
	b.WriteString("\n")
	fmt.Fprintf(&b, "This turn was routed to %s because %s.\n", model, reason)
	b.WriteString("If asked about the model or routing, answer from this information and do not guess. ")
	b.WriteString("Answer directly. Do not offer generic help.")
	return b.String()
}

// withSystemPrompt puts the gateway's instructions first and keeps the client's own
// system message, if any, behind it: the client asked for a persona, the gateway has
// facts the persona must not contradict.
func withSystemPrompt(msgs []Message, system string) []Message {
	out := make([]Message, 0, len(msgs)+1)
	out = append(out, Message{Role: "system", Content: system})
	out = append(out, msgs...)
	return out
}

// MetaAnswer is the reply to a gateway question. It reports the last model turn
// (model, tier, reason, latency) and the roster with tiers, and says plainly that no
// model was called for this reply. It never guesses: with no previous turn it says so
// and still names every registered model.
func MetaAnswer(tier, reason string, roster []Candidate, last *LastRoute) string {
	var b strings.Builder
	if last == nil {
		b.WriteString("No request has been answered by a model in this process yet, so there is no previous routing decision to report.\n")
	} else {
		fmt.Fprintf(&b, "Last model turn: %s on the %s tier via %s, because %s, answered in %.2f s.\n",
			last.Model, last.Tier, last.Backend, last.Reason, last.LatencyS)
	}
	fmt.Fprintf(&b, "This turn was routed to the %s tier because %s; no model was called — the router answered it.\n",
		tier, reason)
	b.WriteString("Registered models:\n")
	for _, c := range roster {
		fmt.Fprintf(&b, "%s · %s · %s\n", c.Model, c.Tier, locality(c.Local))
	}
	b.WriteString(routingLine)
	return b.String()
}

// lastMu and lastRoute are declared on Server (server.go), next to the rest of the
// request-path state they guard.
