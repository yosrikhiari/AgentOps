package advisor

// Track K — static advisor (TDD RED: nothing here exists yet).

import (
	"strings"
	"testing"
)

func TestAdviseKnownTiersFit(t *testing.T) {
	rep := Advise([]string{"qwen2.5:3b-instruct", "qwen2.5:7b-instruct-q4_K_M"},
		map[string]bool{"qwen2.5:3b-instruct": true, "qwen2.5:7b-instruct-q4_K_M": true})
	if len(rep.Models) != 2 {
		t.Fatalf("models=%d, want 2", len(rep.Models))
	}
	for _, m := range rep.Models {
		if !m.FitsAlone || m.Veto {
			t.Fatalf("%s should fit alone without veto: %+v", m.Name, m)
		}
		if m.Presence != "pulled" {
			t.Fatalf("%s presence=%q, want pulled", m.Name, m.Presence)
		}
	}
	if !strings.Contains(rep.CoResidency, "swap") {
		t.Fatalf("two chat models must name the swap rule: %q", rep.CoResidency)
	}
}

func TestAdviseVetoesCannotFit(t *testing.T) {
	rep := AdviseSpecs([]ModelSpec{{Name: "hypothetical-70b", SizeParams: "70B", VRAMGB: 40}}, nil)
	if len(rep.Models) != 1 || !rep.Models[0].Veto {
		t.Fatalf("40 GB model on an 8 GB box must be vetoed: %+v", rep.Models)
	}
}

func TestAdviseUnknownModel(t *testing.T) {
	rep := Advise([]string{"mystery:model"}, map[string]bool{"mystery:model": true})
	if len(rep.Models) != 1 {
		t.Fatalf("models=%d, want 1", len(rep.Models))
	}
	m := rep.Models[0]
	if m.Veto || m.FitsAlone {
		t.Fatalf("unknown model must be no-data, not a verdict: %+v", m)
	}
	if !strings.Contains(m.Verdict, "no static data") {
		t.Fatalf("verdict=%q", m.Verdict)
	}
}

func TestAdviseSkipsUnpulled(t *testing.T) {
	rep := Advise([]string{"qwen2.5:3b-instruct", "qwen2.5:7b-instruct-q4_K_M"},
		map[string]bool{"qwen2.5:3b-instruct": true})
	for _, m := range rep.Models {
		if m.Name == "qwen2.5:7b-instruct-q4_K_M" && m.Presence != "not pulled" {
			t.Fatalf("missing model presence=%q", m.Presence)
		}
	}
}

func TestCachedRumorsCarryProvenance(t *testing.T) {
	for _, r := range cachedRumors {
		if r.Source == "" || r.Date == "" || r.Metric == "" || r.Value == "" {
			t.Fatalf("rumor without provenance: %+v", r)
		}
	}
}

func TestReportPrintsTable(t *testing.T) {
	rep := Advise([]string{"qwen2.5:3b-instruct"}, map[string]bool{"qwen2.5:3b-instruct": true})
	out := rep.String()
	for _, want := range []string{"qwen2.5:3b-instruct", "VRAM", "8 GB", "swap"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
}
