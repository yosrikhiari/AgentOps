package router

import "time"

// PolicyHeuristicV0 is the decision procedure the gateway has shipped since Track B:
// length > 200 characters or a complexity keyword goes to the quality tier. Phase 0 only
// records it; the label exists so a replay can attribute outcomes to the policy version
// that produced them.
const PolicyHeuristicV0 = "heuristic-v0"

// Decision is one routing decision, recorded for replay. It carries labels and model names
// only - no prompt, no answer - so it can live in a table the rest of the system already
// treats as safe (the same rule as spans).
//
// The fields mirror docs/plans/2026-11-17-routing-instrumentation.md: Phase 1 joins these
// rows to per-(query, model) outcomes to compute AIQ/PGR; until then the table answers
// "what did we choose, and why" and nothing more.
type Decision struct {
	TraceID          string
	Intent           Intent
	IntentConfidence float64
	Difficulty       float64
	Policy           string
	Tier             string
	Reason           string
	// Planned is the primary candidate the policy picked; Chosen is the model that
	// actually answered. They differ when a fallback ran, and a replay that joined
	// outcomes to Planned would credit the wrong model.
	Planned  string
	Chosen   string
	Fallback bool
	Failed   bool
	// Candidates is the whole chain that was eligible, in order.
	Candidates []string
	Sensitive  bool
	At         time.Time
}

// DecisionSink receives every routed request. Nil is a no-op (tests, stdio MCP, a build
// without Postgres). Implementations must be non-blocking: the server calls this on the
// request path, before the first backend.
type DecisionSink func(Decision)
