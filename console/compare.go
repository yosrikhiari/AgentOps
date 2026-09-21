package console

// Track L — model comparison over scored runs. BuildComparison is pure:
// scenarios come from the router's own Classify (the same heuristic that
// routed the requests), verdicts from Welch's t-test at the shared n≥100 /
// p<0.05 gate. Below the gate everything reads "tied — route on cost".

import (
	"fmt"
	"sort"

	"agentops/evals"
	"agentops/router"
)

// ScoredPair is one judged answer of a model run (misses are counted, never
// averaged — the same rule as the suite score).
type ScoredPair struct {
	Question     string  `json:"question"`
	Faithfulness float64 `json:"faithfulness"`
	LatencyS     float64 `json:"latency_s"`
	Tokens       int     `json:"tokens"`
}

type ModelRunData struct {
	Model  string       `json:"model"`
	Judge  string       `json:"judge"`
	Pairs  []ScoredPair `json:"pairs"`
	Misses int          `json:"misses"`
}

type RunHistory struct {
	ID      int     `json:"id"`
	Model   string  `json:"model"`
	Judge   string  `json:"judge"`
	Score   float64 `json:"score"`
	Created string  `json:"created_at"`
}

type BenchmarkInput struct {
	Golden  string         `json:"golden_version"`
	Judge   string         `json:"judge"`
	Runs    []ModelRunData `json:"runs"`
	History []RunHistory   `json:"history"`
}

type ModelSummary struct {
	Model           string  `json:"model"`
	Scored          int     `json:"scored"`
	Misses          int     `json:"misses"`
	Faith           float64 `json:"faithfulness"`
	P50LatencyS     float64 `json:"p50_latency_s"`
	TokensPerAnswer float64 `json:"tokens_per_answer"`
}

type ScenarioModel struct {
	Model string  `json:"model"`
	Faith float64 `json:"faithfulness"`
	N     int     `json:"n"`
}

type ScenarioComparison struct {
	Scenario string          `json:"scenario"`
	Models   []ScenarioModel `json:"models"`
	Winner   string          `json:"winner"`
	Reason   string          `json:"reason"`
}

type Verdict struct {
	Winner string `json:"winner"`
	Reason string `json:"reason"`
}

type Comparison struct {
	Golden     string               `json:"golden_version"`
	Judge      string               `json:"judge"`
	Comparable bool                 `json:"comparable"`
	Note       string               `json:"note"`
	Models     []ModelSummary       `json:"models"`
	Scenarios  []ScenarioComparison `json:"scenarios"`
	Verdict    Verdict              `json:"verdict"`
}

// BuildComparison compares the latest run per model. Runs without a model tag
// (golden-answer history) never enter; differing judges invalidate, because a
// delta across judges is not drift in either model.
func BuildComparison(in BenchmarkInput) Comparison {
	c := Comparison{Golden: in.Golden, Judge: in.Judge, Models: []ModelSummary{}, Scenarios: []ScenarioComparison{}}
	if len(in.Runs) < 2 {
		c.Note = "need two scored runs (one per model) over the same golden version"
		return c
	}
	judges := map[string]bool{}
	for _, r := range in.Runs {
		j := r.Judge
		if j == "" {
			j = in.Judge
		}
		if j != "" {
			judges[j] = true
		}
	}
	if len(judges) == 0 {
		c.Note = "no judge provenance on these runs — cannot verify comparability"
		return c
	}
	if len(judges) > 1 {
		names := make([]string, 0, len(judges))
		for j := range judges {
			names = append(names, j)
		}
		sort.Strings(names)
		c.Judge = ""
		c.Note = "judge changed across runs — not comparable"
		return c
	}
	for j := range judges {
		c.Judge = j
	}
	c.Comparable = true
	for _, r := range in.Runs {
		c.Models = append(c.Models, summarize(r))
	}
	sort.Slice(c.Models, func(i, j int) bool { return c.Models[i].Model < c.Models[j].Model })
	c.Scenarios = buildScenarios(in.Runs)
	names := make([]string, 0, len(in.Runs))
	samples := make([][]float64, 0, len(in.Runs))
	ns := make([]int, 0, len(in.Runs))
	for _, r := range in.Runs {
		names = append(names, r.Model)
		var s []float64
		for _, p := range r.Pairs {
			s = append(s, p.Faithfulness)
		}
		samples = append(samples, s)
		ns = append(ns, len(s))
	}
	c.Verdict.Winner, c.Verdict.Reason = decideWinner(names, samples, ns)
	return c
}

func summarize(r ModelRunData) ModelSummary {
	s := ModelSummary{Model: r.Model, Scored: len(r.Pairs), Misses: r.Misses}
	var sumF, sumT float64
	var lat []float64
	for _, p := range r.Pairs {
		sumF += p.Faithfulness
		sumT += float64(p.Tokens)
		lat = append(lat, p.LatencyS)
	}
	if s.Scored > 0 {
		s.Faith = sumF / float64(s.Scored)
		s.TokensPerAnswer = sumT / float64(s.Scored)
		sort.Float64s(lat)
		s.P50LatencyS = percentile(lat, 0.5)
	}
	return s
}

func buildScenarios(runs []ModelRunData) []ScenarioComparison {
	byScenario := map[string]map[string][]ScoredPair{}
	for _, r := range runs {
		for _, p := range r.Pairs {
			_, reason := router.Classify(p.Question)
			if byScenario[reason] == nil {
				byScenario[reason] = map[string][]ScoredPair{}
			}
			byScenario[reason][r.Model] = append(byScenario[reason][r.Model], p)
		}
	}
	names := make([]string, 0, len(byScenario))
	for sc := range byScenario {
		names = append(names, sc)
	}
	sort.Strings(names)
	var out []ScenarioComparison
	for _, sc := range names {
		models := make([]string, 0, len(byScenario[sc]))
		for m := range byScenario[sc] {
			models = append(models, m)
		}
		sort.Strings(models)
		var sms []ScenarioModel
		var samples [][]float64
		var ns []int
		for _, m := range models {
			ps := byScenario[sc][m]
			var sum float64
			var s []float64
			for _, p := range ps {
				sum += p.Faithfulness
				s = append(s, p.Faithfulness)
			}
			sms = append(sms, ScenarioModel{Model: m, Faith: sum / float64(len(ps)), N: len(ps)})
			samples = append(samples, s)
			ns = append(ns, len(s))
		}
		w, reason := decideWinner(models, samples, ns)
		out = append(out, ScenarioComparison{Scenario: sc, Models: sms, Winner: w, Reason: reason})
	}
	return out
}

// decideWinner names a winner only at the shared significance gate (every
// sample n≥100 and the best mean separated at p<0.05). Anything else is tied
// with the reason why — small goldens always land here, by design.
func decideWinner(names []string, samples [][]float64, ns []int) (string, string) {
	if len(names) < 2 {
		return "", "need two models to compare"
	}
	minN := ns[0]
	means := make([]float64, len(samples))
	for i, s := range samples {
		if len(s) == 0 {
			return "", "tied — a model with no scored pairs cannot win"
		}
		var sum float64
		for _, v := range s {
			sum += v
		}
		means[i] = sum / float64(len(s))
		if ns[i] < minN {
			minN = ns[i]
		}
	}
	if minN < 100 {
		return "", fmt.Sprintf("tied — route on cost (n=%d < 100)", minN)
	}
	best := 0
	for i := range means {
		if means[i] > means[best] {
			best = i
		}
	}
	minP := 1.0
	for i := range means {
		if i == best {
			continue
		}
		if p := evals.WelchPValue(samples[best], samples[i]); p < minP {
			minP = p
		}
	}
	if minP < 0.05 {
		return names[best], fmt.Sprintf("p=%.4f < 0.05 at n>=100", minP)
	}
	return "", fmt.Sprintf("tied — no significant separation (p=%.3f)", minP)
}
