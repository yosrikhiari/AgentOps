package main

// Track J follow-up: Ollama's own OLLAMA_MODELS means "models directory", so a
// path value must never be registered as a servable model.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"tower/gpu"
	"tower/tracker"
)

// TestBuildInfoNamesTheBuild: a release binary must be able to say which commit
// it is, because that is what the cosign identity and the provenance attestation
// point at. Version comes from the linker, Go and module from the toolchain.
func TestBuildInfoNamesTheBuild(t *testing.T) {
	bi := buildInfoOf("v1.2.3", "", "")
	if bi.Version != "v1.2.3" {
		t.Fatalf("version = %q", bi.Version)
	}
	if !strings.HasPrefix(bi.Go, "go") {
		t.Fatalf("go = %q, want the toolchain version", bi.Go)
	}
	if bi.Module == "" {
		t.Fatal("module is empty; a release build must name itself")
	}
}

// TestBuildInfoLinkerStampsWin: -X flags are what the release workflow sets, so
// they must beat whatever VCS metadata the local checkout happened to carry.
func TestBuildInfoLinkerStampsWin(t *testing.T) {
	bi := buildInfoOf("v9.9.9", "cafebabe", "2026-11-16T10:00:00Z")
	if bi.Commit != "cafebabe" {
		t.Fatalf("commit = %q, want the linker stamp", bi.Commit)
	}
	if bi.Date != "2026-11-16T10:00:00Z" {
		t.Fatalf("date = %q, want the linker stamp", bi.Date)
	}
}

// TestBuildInfoJSONIsOneLine: --version-json is piped into jq and into the
// release checklist, so it must be exactly one JSON object, not pretty-printed
// and not mixed with a log line.
func TestBuildInfoJSONIsOneLine(t *testing.T) {
	raw, err := json.Marshal(buildInfoOf("dev", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\n") {
		t.Fatalf("not one line: %q", raw)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, raw)
	}
	for _, k := range []string{"version", "go", "module"} {
		if got[k] == nil || got[k] == "" {
			t.Fatalf("%s missing from %s", k, raw)
		}
	}
}

func TestExtraModelsSkipsPaths(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", `C:\Users\yosri\.ollama,qwen3:8b`)
	t.Setenv("GROQ_API_KEY", "")
	srv, _ := buildBackends(Config{OllamaURL: "http://127.0.0.1:1", FastModel: "fast-m", QualityModel: "quality-m"})
	for _, ref := range srv.Models {
		if strings.ContainsAny(ref.Model, `/\`) {
			t.Fatalf("path registered as model: %q", ref.Model)
		}
	}
	found := false
	for _, ref := range srv.Models {
		if ref.Model == "qwen3:8b" {
			found = true
		}
	}
	if !found {
		t.Fatal("real extra model qwen3:8b missing")
	}
}

// TestTraceJSONIncludesNearestGPUSample: a span set plus a nearest-sample
// reader becomes one body. gpu_ok is the flag the page keys on: a zero sample
// must never be distinguishable from a real 0% reading by omission alone.
func TestTraceJSONIncludesNearestGPUSample(t *testing.T) {
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	spans := []tracker.Span{
		{TraceID: "t1", SpanID: "s1", Name: "chat", StartedAt: start},
		{TraceID: "t1", SpanID: "s2", Name: "model.generate", StartedAt: start.Add(time.Second)},
	}
	out := traceJSON(context.Background(), "t1", spans, func(_ context.Context, at time.Time) (gpu.Sample, bool, error) {
		if !at.Equal(start) {
			t.Fatalf("correlated on %v, want the first span's start %v", at, start)
		}
		return gpu.Sample{At: start, Name: "GTX 1650", UtilPct: 39, MemUsedBytes: 77 << 20}, true, nil
	})
	if out["gpu_ok"] != true {
		t.Fatalf("gpu_ok = %v", out["gpu_ok"])
	}
	g, ok := out["gpu"].(gpu.Sample)
	if !ok || g.UtilPct != 39 {
		t.Fatalf("gpu = %#v", out["gpu"])
	}
}

// TestTraceJSONOmitsGPUWithoutASample: no sample in the window is not "0%".
// The keys must be absent so the page renders no chip at all.
func TestTraceJSONOmitsGPUWithoutASample(t *testing.T) {
	out := traceJSON(context.Background(), "t1", []tracker.Span{{TraceID: "t1", StartedAt: time.Now()}},
		func(context.Context, time.Time) (gpu.Sample, bool, error) {
			return gpu.Sample{}, false, nil
		})
	if _, ok := out["gpu"]; ok {
		t.Fatal("gpu present without a sample")
	}
	if _, ok := out["gpu_ok"]; ok {
		t.Fatal("gpu_ok present without a sample")
	}
}

// TestAuditJudgeBackends: the second judge's backend is explicit. A cloud model name is
// meaningless to Ollama, and a missing key must fail loudly instead of silently judging
// with the local model twice - which would produce a perfect kappa that means nothing.
func TestAuditJudgeBackends(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "")
	if _, name, err := auditJudge("phi4-mini", "", "http://localhost:11434"); err != nil || name != "phi4-mini" {
		t.Fatalf("ollama default: name=%q err=%v", name, err)
	}
	if _, _, err := auditJudge("llama-3.1-8b-instant", "groq", "http://localhost:11434"); err == nil {
		t.Fatal("groq without a key must fail")
	}
	t.Setenv("GROQ_API_KEY", "test-key")
	if _, name, err := auditJudge("llama-3.1-8b-instant", "groq", "http://localhost:11434"); err != nil || name != "groq/llama-3.1-8b-instant" {
		t.Fatalf("groq backend: name=%q err=%v", name, err)
	}
	if _, _, err := auditJudge("m", "nope", "http://localhost:11434"); err == nil {
		t.Fatal("unknown backend must fail")
	}
}

// TestEvalTallyKeepsNoClaimPairsOutOfTheMean: an answer too short to split into
// claims never reaches the judge, so it is unmeasured, not a zero. Counting it as
// 0.0 depressed the suite score and could fail the gate (audit 2026-11-18 C4).
func TestEvalTallyKeepsNoClaimPairsOutOfTheMean(t *testing.T) {
	var tl evalTally
	tl.add(1.0, 3, false, false, 1, 1) // fully faithful
	tl.add(0.0, 0, false, false, 1, 1) // no scorable claim -> unmeasured
	tl.add(0.0, 2, false, true, 1, 1)  // refuted by the judge
	tl.add(0.0, 0, true, true, 0, 0)   // retrieval miss

	if tl.scored != 2 {
		t.Fatalf("scored = %d, want 2 (the faithful pair and the refuted one)", tl.scored)
	}
	if tl.sumF != 1.0 {
		t.Fatalf("sumF = %v, want 1.0 - the no-claim pair must not add a zero", tl.sumF)
	}
	if tl.noClaims != 1 || tl.misses != 1 || tl.refuted != 1 || tl.unsolvable != 2 {
		t.Fatalf("tally = %+v", tl)
	}
}
