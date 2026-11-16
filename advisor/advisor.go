// Package advisor is Track K: a startup report from static metadata plus
// cached public numbers. It informs the operator and changes nothing at
// runtime — its output is never a routing input.
package advisor

import (
	"fmt"
	"sort"
	"strings"
)

// BoxVRAMGB is the fallback budget for --advise when no driver answers: the
// demo box described in docs/VRAM.md (RTX 4060, 8 GB, one chat model resident at
// a time). A measured total from nvidia-smi wins over it — see AdviseWithBudget.
const BoxVRAMGB = 8

// ModelSpec is what we statically know about a model. VRAMGB is the working
// estimate with a modest context window (docs/VRAM.md), not a benchmark.
type ModelSpec struct {
	Name        string
	SizeParams  string
	Quant       string
	ContextNote string
	License     string
	VRAMGB      float64
	Chat        bool // Chat=false: always-resident infrastructure (embeddings)
}

func knownModels() []ModelSpec {
	return []ModelSpec{
		{Name: "qwen2.5:3b-instruct", SizeParams: "3B", Quant: "Q4 (Ollama default)",
			ContextNote: "2–4K operating window (docs/VRAM.md)", License: "Apache-2.0",
			VRAMGB: 2, Chat: true},
		{Name: "qwen2.5:7b-instruct-q4_K_M", SizeParams: "7B", Quant: "Q4_K_M",
			ContextNote: "2–4K operating window; 7B-Q4 + 4K ctx is the 8 GB ceiling (docs/VRAM.md)",
			License:     "Apache-2.0", VRAMGB: 4.5, Chat: true},
		{Name: "qwen3:8b", SizeParams: "8B", Quant: "Q4 (Ollama default)",
			ContextNote: "judge/drafter fallback on this box; 5.6 GB resident (docs/VRAM.md)",
			License:     "Apache-2.0", VRAMGB: 5.6, Chat: true},
		{Name: "nomic-embed-text", SizeParams: "137M", Quant: "full",
			ContextNote: "embeddings only", License: "Apache-2.0", VRAMGB: 0.3},
	}
}

// Rumor is one cached public number. Every field is required: a number
// without a source and a date is gossip, and TestCachedRumorsCarryProvenance
// fails the build if one is added. Empty table is honest — it means no public
// number found today was defensible enough to freeze.
type Rumor struct {
	Source string
	Date   string
	Model  string
	Metric string
	Value  string
}

var cachedRumors = []Rumor{}

type Verdict struct {
	Name        string
	SizeParams  string
	Quant       string
	ContextNote string
	License     string
	VRAMGB      float64
	Presence    string // pulled | not pulled | unknown
	FitsAlone   bool
	Veto        bool
	Verdict     string
	Rumors      []Rumor
}

type Report struct {
	Models      []Verdict
	CoResidency string
	RumorsNote  string
	// BudgetGB and BudgetSource are what the verdicts above were judged
	// against, and where that number came from ("measured: NVIDIA GeForce GTX
	// 1650" vs "docs/VRAM.md"). String() prints them, so a 4 GB 1650 and an
	// 8 GB 4060 never produce the same sentence by accident.
	BudgetGB     float64
	BudgetSource string
}

// Advise reports on named models against the docs/VRAM.md budget.
func Advise(names []string, pulled map[string]bool) Report {
	return AdviseWithBudget(names, pulled, BoxVRAMGB, "docs/VRAM.md")
}

// AdviseWithBudget answers the same question against a budget measured on the
// machine that will run the model. pulled==nil means presence is unknown
// (Ollama unreachable) — reported, never assumed. The source is required: a
// number without a provenance is exactly the gossip this package refuses to
// print.
func AdviseWithBudget(names []string, pulled map[string]bool, budgetGB float64, source string) Report {
	byName := map[string]ModelSpec{}
	for _, s := range knownModels() {
		byName[s.Name] = s
	}
	specs := make([]ModelSpec, 0, len(names))
	for _, n := range names {
		if s, ok := byName[n]; ok {
			specs = append(specs, s)
		} else {
			specs = append(specs, ModelSpec{Name: n})
		}
	}
	return adviseSpecs(specs, pulled, budgetGB, source)
}

// AdviseSpecs is Advise over explicit specs (lets tests inject a 70B veto
// case without polluting the static table). It assumes the fallback budget.
func AdviseSpecs(specs []ModelSpec, pulled map[string]bool) Report {
	return adviseSpecs(specs, pulled, BoxVRAMGB, "docs/VRAM.md")
}

func adviseSpecs(specs []ModelSpec, pulled map[string]bool, budgetGB float64, source string) Report {
	rep := Report{BudgetGB: budgetGB, BudgetSource: source}
	var chats []string
	for _, s := range specs {
		v := Verdict{Name: s.Name, SizeParams: s.SizeParams, Quant: s.Quant,
			ContextNote: s.ContextNote, License: s.License, VRAMGB: s.VRAMGB}
		known := s.SizeParams != ""
		switch {
		case !known:
			v.Presence = presenceOf(s.Name, pulled)
			v.Verdict = "no static data — excluded from Track L until measured"
		default:
			v.Presence = presenceOf(s.Name, pulled)
			v.Veto = s.VRAMGB > budgetGB
			v.FitsAlone = !v.Veto
			switch {
			case v.Presence == "not pulled":
				v.FitsAlone = false
				v.Verdict = "not pulled — pull first (Track J)"
			case v.Veto:
				v.Verdict = fmt.Sprintf("VETO: cannot fit %g GB (needs ~%g GB)", budgetGB, s.VRAMGB)
			default:
				v.Verdict = fmt.Sprintf("fits alone (~%g GB of %g GB)", s.VRAMGB, budgetGB)
			}
			for _, r := range cachedRumors {
				if r.Model == s.Name {
					v.Rumors = append(v.Rumors, r)
				}
			}
		}
		if known && s.Chat && !v.Veto {
			chats = append(chats, s.Name)
		}
		rep.Models = append(rep.Models, v)
	}
	sort.Strings(chats)
	switch {
	case len(chats) >= 2:
		rep.CoResidency = fmt.Sprintf("one chat model resident at a time — %s co-resident: no (swap rule, docs/VRAM.md); nomic-embed-text always resident",
			strings.Join(chats, " + "))
	case len(chats) == 1:
		rep.CoResidency = "one chat model resident at a time (swap rule, docs/VRAM.md); nomic-embed-text always resident"
	default:
		rep.CoResidency = "no servable chat models in this lineup"
	}
	if len(cachedRumors) == 0 {
		rep.RumorsNote = "no cached public scores — every verdict above is local metadata, not a leaderboard claim"
	} else {
		rep.RumorsNote = fmt.Sprintf("%d cached public score(s), each with source + date (rumor, not measurement)", len(cachedRumors))
	}
	return rep
}

func presenceOf(name string, pulled map[string]bool) string {
	if pulled == nil {
		return "unknown"
	}
	if pulled[name] {
		return "pulled"
	}
	return "not pulled"
}

func (r Report) String() string {
	var sb strings.Builder
	budget, source := r.BudgetGB, r.BudgetSource
	if budget == 0 { // a Report built without adviseSpecs still prints a budget
		budget, source = BoxVRAMGB, "docs/VRAM.md"
	}
	fmt.Fprintf(&sb, "advisor: %g GB VRAM budget (%s)\n", budget, source)
	for _, m := range r.Models {
		size := m.SizeParams
		if size == "" {
			size = "?"
		}
		fmt.Fprintf(&sb, "- %s [%s, %s, %s] VRAM~%gGB presence=%s: %s\n",
			m.Name, size, m.Quant, m.License, m.VRAMGB, m.Presence, m.Verdict)
		for _, ru := range m.Rumors {
			fmt.Fprintf(&sb, "    rumor %s %s (%s, %s)\n", ru.Metric, ru.Value, ru.Source, ru.Date)
		}
	}
	sb.WriteString("coresidency: " + r.CoResidency + "\n")
	sb.WriteString("public scores: " + r.RumorsNote + "\n")
	return sb.String()
}
