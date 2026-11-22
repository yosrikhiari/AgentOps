package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"tower/advisor"
	"tower/audit"
	"tower/console"
	"tower/evals"
	"tower/gpu"
	"tower/mcp"
	"tower/router"
	"tower/tracker"
)

// version is stamped by the release build (-ldflags "-X main.version=v1.2.3").
// commit and buildDate are stamped by the same command, so a downloaded binary
// can name the exact revision its signature and SBOM are for.
var (
	version   = "dev"
	commit    = ""
	buildDate = ""
)

// buildInfo is what a binary knows about itself. The linker stamps win; a plain
// `go build` still reports the VCS metadata the toolchain embeds.
type buildInfo struct {
	Version     string `json:"version"`
	Commit      string `json:"commit,omitempty"`
	Date        string `json:"date,omitempty"`
	Go          string `json:"go"`
	Module      string `json:"module"`
	VCSModified bool   `json:"vcs_modified,omitempty"`
}

// buildInfoOf takes the stamps as arguments instead of reading the package
// globals, so it is testable without mutating state.
func buildInfoOf(ver, commitStamp, dateStamp string) buildInfo {
	bi := buildInfo{Version: ver, Go: runtime.Version(), Module: "tower"}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Path != "" {
			bi.Module = info.Main.Path
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				bi.Commit = s.Value
			case "vcs.time":
				bi.Date = s.Value
			case "vcs.modified":
				bi.VCSModified = s.Value == "true"
			}
		}
	}
	if commitStamp != "" {
		bi.Commit = commitStamp
	}
	if dateStamp != "" {
		bi.Date = dateStamp
	}
	return bi
}

// shortCommit keeps --version to one readable line; --version-json has the full
// hash for verification.
func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

var errEmptyDraft = errors.New("empty draft")

// pgDB is the slice of pgx shared by *pgx.Conn, *pgxpool.Pool and pgx.Tx.
type pgDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// pgAdapter narrows pgDB to the tiny interfaces evals/ and tracker/ declare, so those
// packages never import pgx.
type pgAdapter struct {
	db pgDB
}

func (p pgAdapter) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := p.db.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (p pgAdapter) QueryRow(ctx context.Context, sql string, args ...any) evals.Row {
	return p.db.QueryRow(ctx, sql, args...)
}

func (p pgAdapter) Query(ctx context.Context, sql string, args ...any) (evals.Rows, error) {
	rows, err := p.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// Begin opens a pgx transaction wrapped so it satisfies console.Tx (and, by
// method set, evals.Execer). Used by console.SQLStore.AppendMessage.
func (p pgAdapter) Begin(ctx context.Context) (console.Tx, error) {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgTx{tx: tx}, nil
}

type pgTx struct {
	tx pgx.Tx
}

func (t pgTx) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := t.tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (t pgTx) Commit(ctx context.Context) error { return t.tx.Commit(ctx) }

func (t pgTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

// routerAdapter serves router.SQLKeyStore, which declares its own Rows type.
type routerAdapter struct {
	pgAdapter
}

func (r routerAdapter) Query(ctx context.Context, sql string, args ...any) (router.Rows, error) {
	rows, err := r.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// trackerAdapter exists only because tracker.Rows and evals.Rows are distinct named types.
type trackerAdapter struct {
	pgAdapter
}

func (t trackerAdapter) Query(ctx context.Context, sql string, args ...any) (tracker.Rows, error) {
	rows, err := t.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// gpuAdapter exists only because gpu.Rows is a distinct named type.
type gpuAdapter struct {
	pgAdapter
}

func (g gpuAdapter) Query(ctx context.Context, sql string, args ...any) (gpu.Rows, error) {
	rows, err := g.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func runMigrate(dsn string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)
	// Advisory lock: two concurrent `--migrate` processes (or a migrate racing a
	// boot schema check) must not both run Migrate. pg_advisory_lock blocks until
	// the lock is free; the session-level lock is released on close/defer.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(0x746f776572)`); err != nil {
		log.Fatalf("migrate: advisory lock: %v", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(0x746f776572)`) }()
	n, err := evals.Migrate(ctx, pgAdapter{conn}, pgAdapter{conn}, "migrations")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("migrations: applied %d new file(s)", n)
}

// checkSchema verifies at boot that every migration shipped with this binary has
// been applied. A missing migration means the operator has not run --migrate (or
// ran an older binary); serving on a half-migrated schema is worse than refusing
// to start. Returns nil when the schema is current or when the migrations
// directory cannot be found (nothing to compare against).
func checkSchema(pool *pgxpool.Pool) error {
	files, err := filepath.Glob(filepath.Join("migrations", "*.sql"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		log.Printf("schema check: no migrations directory found; skipping")
		return nil
	}
	expected := map[string]bool{}
	for _, f := range files {
		expected[filepath.Base(f)] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx, `SELECT filename FROM schema_migrations`)
	if err != nil {
		// Table missing or DB unreachable: treat as "not migrated".
		return fmt.Errorf("schema_migrations unreadable (%v); run: tower --migrate", err)
	}
	defer rows.Close()
	applied := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var missing []string
	for name := range expected {
		if !applied[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("schema out of date: missing migrations %v; run: tower --migrate", missing)
	}
	log.Printf("schema check: %d migration(s) applied", len(applied))
	return nil
}

func evalThreshold() float64 {
	raw := strings.TrimSpace(os.Getenv("EVAL_THRESHOLD"))
	if raw == "" {
		return 0.7
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0.7
	}
	return v
}

func queryDrift(db pgDB, goldenVersion string, threshold float64) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rep, err := evals.DriftReportFor(ctx, pgAdapter{db}, goldenVersion, threshold)
	if err != nil {
		return nil, err
	}
	return rep, nil
}

// withConn runs fn on one short-lived connection — the CLI shape (connect, do, exit).
func withConn(dsn string, timeout time.Duration, fn func(ctx context.Context, conn *pgx.Conn) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return fn(ctx, conn)
}

// runRouteReplay prints the decision mix recorded by Phase 0. It deliberately does not
// print a quality number: AIQ/PGR need per-(query, model) outcomes, which Phase 1 records
// in shadow mode. Claiming a router beats random before that data exists is exactly the
// mistake the 2026 audit found in commercial routers.
func runRouteReplay(dsn, window, fastModel string) {
	d, err := time.ParseDuration(window)
	if err != nil || d <= 0 {
		d = 720 * time.Hour
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx,
		`SELECT intent, tier, count(*)::int,
		        round(avg(intent_confidence)::numeric, 2)::float8,
		        round(avg(difficulty)::numeric, 2)::float8
		 FROM route_decisions
		 WHERE created_at >= now() - make_interval(secs => $1)
		 GROUP BY intent, tier ORDER BY count(*) DESC`, d.Seconds())
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	log.Printf("route replay: last %s, policy %s", window, router.PolicyHeuristicV0)
	log.Printf("  %-10s %-8s %7s %6s %6s", "intent", "tier", "count", "conf", "diff")
	var total int
	for rows.Next() {
		var intent, tier string
		var n int
		var conf, diff float64
		if err := rows.Scan(&intent, &tier, &n, &conf, &diff); err != nil {
			log.Fatal(err)
		}
		total += n
		log.Printf("  %-10s %-8s %7d %6.2f %6.2f", intent, tier, n, conf, diff)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
	if total == 0 {
		log.Print("  no decisions in the window — send a few requests first")
		return
	}
	var fast, quality, explicit, sensitive, fellBack, failed int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE tier = 'fast')::int,
		        count(*) FILTER (WHERE tier = 'quality')::int,
		        count(*) FILTER (WHERE reason = 'explicit-model')::int,
		        count(*) FILTER (WHERE sensitive)::int,
		        count(*) FILTER (WHERE fallback)::int,
		        count(*) FILTER (WHERE failed)::int
		 FROM route_decisions WHERE created_at >= now() - make_interval(secs => $1)`,
		d.Seconds()).Scan(&fast, &quality, &explicit, &sensitive, &fellBack, &failed); err != nil {
		log.Fatal(err)
	}
	pct := func(n int) int { return n * 100 / total }
	log.Printf("  mix: fast %d (%d%%), quality %d (%d%%), explicit %d, sensitive %d",
		fast, pct(fast), quality, pct(quality), explicit, sensitive)
	log.Printf("  outcomes: %d answered after a fallback, %d failed; always-fast %d%%, always-quality %d%%",
		fellBack, failed, pct(fast), pct(quality))
	log.Printf("  quality numbers: none yet — AIQ/PGR and the random-at-matched-cost comparison need Phase 1 shadow labels")

	// The ceiling: how much of the suite no router could rescue, and whether the roster
	// can even answer the routing question. Every line is either measured from a complete
	// run or says "undefined" - never a zero that reads like a measurement.
	var golden string
	var runID int
	err = conn.QueryRow(ctx,
		`SELECT id, golden_version FROM eval_runs
		 WHERE model = '' AND status = 'complete'
		   AND EXISTS (SELECT 1 FROM eval_pair_scores p WHERE p.run_id = eval_runs.id AND p.faithfulness IS NOT NULL)
		 ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&runID, &golden)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		log.Printf("  ceiling: no complete judged golden run yet — unsolvable share undefined")
	case err != nil:
		log.Fatal(err)
	default:
		var pairs, misses, noClaims, refuted int
		if err := conn.QueryRow(ctx,
			`SELECT count(*)::int,
			        count(*) FILTER (WHERE retrieval_miss)::int,
			        count(*) FILTER (WHERE NOT retrieval_miss AND claims = 0)::int,
			        count(*) FILTER (WHERE NOT retrieval_miss AND claims > 0 AND faithfulness = 0)::int
			 FROM eval_pair_scores WHERE run_id = $1`, runID).Scan(&pairs, &misses, &noClaims, &refuted); err != nil {
			log.Fatal(err)
		}
		unsolvable := misses + refuted
		log.Printf("  ceiling: %d/%d unsolvable (%d%%) on %s run %d — %d retrieval misses, %d refuted, %d no-claims. No router can rescue these",
			unsolvable, pairs, unsolvable*100/maxInt(pairs, 1), golden, runID, misses, refuted, noClaims)
	}
	// β (co-failure) and fast-optimal need per-model outcomes: the latest complete Track L
	// run for each model. Golden-answer runs are the reference, not a rung.
	if runID != 0 {
		crows, err := conn.Query(ctx,
			`WITH latest AS (
			   SELECT DISTINCT ON (model) id, model FROM eval_runs
			   WHERE golden_version = $1 AND model <> '' AND status = 'complete'
			   ORDER BY model, created_at DESC, id DESC
			 )
			 SELECT p.question, l.model, p.retrieval_miss, p.faithfulness
			 FROM latest l JOIN eval_pair_scores p ON p.run_id = l.id`, golden)
		if err != nil {
			log.Fatal(err)
		}
		var outcomes []evals.RungOutcome
		for crows.Next() {
			var o evals.RungOutcome
			var f *float64
			if err := crows.Scan(&o.Question, &o.Model, &o.Miss, &f); err != nil {
				crows.Close()
				log.Fatal(err)
			}
			if f != nil {
				o.Faithfulness = *f
			}
			outcomes = append(outcomes, o)
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			log.Fatal(err)
		}
		ceil := evals.RouteCeiling(outcomes, fastModel)
		if len(ceil.Rungs) < 2 || ceil.Questions == 0 {
			log.Printf("  routing headroom: undefined — %d rung(s) with outcomes; β and fast-optimal need ≥2 models judged on the same questions (see docs/EVALS.md)", len(ceil.Rungs))
		} else {
			log.Printf("  routing headroom: %d rungs (%s), %d questions answered by all", len(ceil.Rungs), strings.Join(ceil.Rungs, ", "), ceil.Questions)
			beta := int(ceil.Beta*100 + 0.5)
			log.Printf("  beta (every rung fails): %d%% — no single-answer policy can beat %d%%", beta, 100-beta)
			log.Printf("  fast-optimal: %d%% — the fast rung is at least as good on this share", int(ceil.FastOptimal*100+0.5))
			for _, m := range ceil.Rungs {
				log.Printf("    %s mean faithfulness %.3f", m, ceil.MeanFaith[m])
			}
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func runDrift(dsn, goldenVersion string, threshold float64) {
	var data any
	err := withConn(dsn, 15*time.Second, func(ctx context.Context, conn *pgx.Conn) error {
		var err error
		data, err = queryDrift(conn, goldenVersion, threshold)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}
	raw, _ := json.MarshalIndent(data, "", "  ")
	log.Printf("drift %s:\n%s", goldenVersion, string(raw))
}

// runReleaseGate is the Track T pre-tag check: the last k golden-answer runs
// for a golden version must all clear today's threshold on one judge.
// Anything else prints the reason and exits 1 (log.Fatal) — no tag.
func runReleaseGate(dsn, golden string, k int, threshold float64) {
	var res evals.GateResult
	var gpuLines []string
	err := withConn(dsn, 20*time.Second, func(ctx context.Context, conn *pgx.Conn) error {
		var err error
		res, err = evals.ReleaseGate(ctx, pgAdapter{conn}, golden, k, threshold)
		if err != nil {
			return err
		}
		// What the card was doing during each run in the window. Evidence about
		// comparability, not a gate: a swap-heavy run next to a quiet one is the
		// most common way a green window still means nothing.
		gs := gpu.SQLStore{Query: gpuAdapter{pgAdapter{conn}}}
		for _, r := range res.Runs {
			sum, serr := gs.Summary(ctx, r.StartedAt, r.FinishedAt)
			switch {
			case serr != nil:
				gpuLines = append(gpuLines, fmt.Sprintf("  run %d gpu: unavailable (%v)", r.ID, serr))
			case sum.Samples == 0:
				gpuLines = append(gpuLines, fmt.Sprintf("  run %d gpu: no samples (prober off) - comparability unknown", r.ID))
			default:
				line := fmt.Sprintf("  run %d gpu: mean %.0f%% util, peak %.1f of %.1f GB",
					r.ID, sum.UtilMean, float64(sum.MemPeakBytes)/(1<<30), float64(sum.MemTotalBytes)/(1<<30))
				if sum.MaxTempC > 0 {
					line += fmt.Sprintf(", peak %.0fC", sum.MaxTempC)
				}
				line += fmt.Sprintf(", %d model swap(s)", sum.Swaps)
				if sum.Swaps > 0 {
					line += " - swaps make this run not directly comparable with a quiet one"
				}
				if sum.ThrottledSamples > 0 {
					line += fmt.Sprintf("; thermally throttled in %d/%d samples", sum.ThrottledSamples, sum.Samples)
				}
				gpuLines = append(gpuLines, line)
			}
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("release gate %s (k=%d):\n%s", golden, k, res.Report)
	for _, line := range gpuLines {
		log.Printf("%s", line)
	}
	if !res.Pass {
		log.Fatalf("release gate %s: not releasable", golden)
	}
}

// runJudgeAudit measures the judge itself. Every score, drift alert and release gate
// inherits this error, and until this pass nobody had measured it: a faithful
// paraphrase scoring 0.00 was the symptom, not a bad answer.
// defaultGroqModel is the cloud model used when the deployment does not name one. Groq
// retired llama-3.1-8b-instant in 2026 (404 model_not_found), so the default has to be a
// model that exists today; CLOUD_MODEL and GROQ_JUDGE_MODEL override it.
const defaultGroqModel = "openai/gpt-oss-20b"

// auditJudge builds the second judge. A cloud judge is how the audit measures agreement
// without a bigger card, so the backend is explicit rather than inferred from the name:
// a model called "llama-3.1-8b-instant" means nothing to Ollama.
func auditJudge(name, backend, ollamaURL string) (evals.Judge, string, error) {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "", "ollama":
		return evals.NewOllamaJudge(ollamaURL, name), name, nil
	case "groq":
		key := strings.TrimSpace(os.Getenv("GROQ_API_KEY"))
		if key == "" {
			return nil, "", errors.New("--judge2-backend groq needs GROQ_API_KEY in the environment")
		}
		return evals.NewGroqJudge(name, key), "groq/" + name, nil
	}
	return nil, "", fmt.Errorf("unknown judge backend %q (ollama|groq)", backend)
}

func runJudgeAudit(ollamaURL, model, model2, backend2, path string) {
	if model == "" {
		model = envOr("JUDGE_MODEL", "qwen3:8b")
	}
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("judge audit: %v", err)
	}
	defer f.Close()
	items, err := evals.LoadAudit(f)
	if err != nil {
		log.Fatalf("judge audit: %v", err)
	}
	judges := []evals.NamedJudge{{Name: model, Judge: evals.NewOllamaJudge(ollamaURL, model)}}
	if model2 != "" {
		judge, name, err := auditJudge(model2, backend2, ollamaURL)
		if err != nil {
			log.Fatalf("judge audit: %v", err)
		}
		judges = append(judges, evals.NamedJudge{Name: name, Judge: judge})
	}
	rep := evals.RunAudit(context.Background(), items, judges, true)
	log.Printf("judge audit: %d labelled items, prompt=%s (positive class = unsupported)", rep.Items, evals.JudgePromptVersion)
	log.Printf("  %-24s %3s %3s %3s %3s %5s %6s %10s %7s %7s %7s", "judge", "tp", "fp", "fn", "tn", "prec", "recall", "false-alrm", "lflip", "dflip", "stable")
	for _, j := range rep.Judges {
		flips := "n/a"
		if j.FlipComparable > 0 {
			flips = fmt.Sprintf("%d/%d", j.Flips, j.FlipComparable)
		}
		dflips := "n/a"
		if j.FlipComparable > 0 {
			dflips = fmt.Sprintf("%d/%d", j.DecisionFlips, j.FlipComparable)
		}
		log.Printf("  %-24s %3d %3d %3d %3d %5.2f %6.2f %10.2f %7s %7s %6.2f",
			j.Judge, j.TP, j.FP, j.FN, j.TN, j.Precision(), j.Recall(), j.FalseAlarmRate(), flips, dflips, j.StableRate())
		if j.Errors > 0 {
			log.Printf("  %-24s %d call(s) returned no verdict (counted as errors, not agreement): %s", j.Judge, j.Errors, j.FirstError)
		}
		if len(j.FlipIDs) > 0 {
			log.Printf("  %-24s moved on reorder: %s", j.Judge, strings.Join(j.FlipIDs, ", "))
		}
	}
	if rep.Agreement != nil {
		log.Printf("  agreement: n=%d kappa=%.2f", rep.Agreement.Pairs, rep.Agreement.Kappa)
	} else {
		log.Printf("  agreement: single judge - pass --judge2-model to measure it")
	}
	total := 0
	for _, j := range rep.Judges {
		total += j.Verdicts()
	}
	if total == 0 {
		log.Printf("judge audit: no verdicts at all - is %q pulled and Ollama reachable at %s?", model, ollamaURL)
		os.Exit(2)
	}
	if v := evals.AuditVerdict(rep, 0.80, 0.20, 0.10); !v.Pass {
		log.Printf("FAIL: %s", strings.Join(v.Reasons, "; "))
		os.Exit(1)
	}
	log.Printf("PASS: recall >= 0.80, false-alarm <= 0.20, decision flips <= 0.10 over %d items. n is small - read the table, not just the verdict.", rep.Items)
}

func latestEvalScore(db pgDB, goldenVersion string) (float64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rep, err := evals.DriftReportFor(ctx, pgAdapter{db}, goldenVersion, evalThreshold())
	if err != nil || rep.Runs == 0 {
		return 0, false
	}
	return rep.ScoreNow, true
}

func queryTrace(db pgDB, traceID string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ad := trackerAdapter{pgAdapter{db}}
	store := tracker.SQLStore{Exec: ad, Query: ad}
	spans, err := store.ListSpans(ctx, traceID)
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 {
		return nil, tracker.ErrTraceNotFound
	}
	gpuStore := gpu.SQLStore{Query: gpuAdapter{pgAdapter{db}}}
	return traceJSON(ctx, traceID, spans, func(ctx context.Context, at time.Time) (gpu.Sample, bool, error) {
		return gpuStore.Nearest(ctx, at, 15*time.Second)
	}), nil
}

// traceJSON is the /v1/traces/{id} body. The GPU sample is the one nearest the
// first span's start — correlation, not attribution: nvidia-smi sees one ollama
// process, so nothing here claims a request owned the card. When no sample is
// in the window the keys are absent, because "unmeasured" and "0%" are different
// facts and only one of them is a reading. ctx is the caller's 10 s bound, so a
// wedged sample query cannot outlive the request.
func traceJSON(ctx context.Context, traceID string, spans []tracker.Span, near func(context.Context, time.Time) (gpu.Sample, bool, error)) map[string]any {
	out := map[string]any{"trace_id": traceID, "spans": spans}
	if len(spans) == 0 || near == nil {
		return out
	}
	// A dead gpu_samples table must not cost you the trace you came to read.
	if s, ok, err := near(ctx, spans[0].StartedAt); err == nil && ok {
		out["gpu"] = s
		out["gpu_ok"] = true
	}
	return out
}

type spanRecord struct {
	traceID, spanID, parentID, name, attrs string
	at                                     time.Time
}

// spanWriter drains router spans to Postgres on one goroutine, in emit order, off the
// request path. A dead database costs the request nothing; a full queue drops spans
// (counted) rather than blocking a chat.
type spanWriter struct {
	db      pgDB
	queue   chan spanRecord
	dropped atomic.Uint64
	done    chan struct{}
	once    sync.Once
	// Hub, when set, receives every span for live viewers. Lossy by design:
	// a slow browser drops spans there; the queue above is unaffected.
	Hub *eventHub
	// Exporter, when set, also ships each span over OTLP. Off the request path and
	// lossy, like the database write.
	Exporter *otlpExporter
}

// otlpExporter batches spans to an OTLP/HTTP collector. Strictly lossy: a collector that
// is down must never slow a chat, and Postgres remains the source of truth. The endpoint is
// the full traces URL (e.g. http://localhost:4318/v1/traces); empty disables export.
type otlpExporter struct {
	endpoint string
	client   *http.Client
	queue    chan tracker.ExportedSpan
	sent     atomic.Uint64
	dropped  atomic.Uint64
	done     chan struct{}
	once     sync.Once
}

func newOTLPExporter(endpoint string) *otlpExporter {
	e := &otlpExporter{
		endpoint: endpoint,
		client:   &http.Client{Timeout: 3 * time.Second},
		queue:    make(chan tracker.ExportedSpan, 1024),
		done:     make(chan struct{}),
	}
	go e.loop()
	return e
}

// Add hands one span to the exporter. It never blocks: the queue drops and counts, and the
// store's redaction runs again here because an exporter is a new surface.
func (e *otlpExporter) Add(rec spanRecord) {
	var attrs map[string]any
	if err := json.Unmarshal([]byte(tracker.RedactAttrs(rec.attrs)), &attrs); err != nil {
		e.dropped.Add(1)
		return
	}
	span := tracker.ExportedSpan{TraceID: rec.traceID, SpanID: rec.spanID, ParentID: rec.parentID, Name: rec.name, At: rec.at, Attrs: attrs}
	select {
	case e.queue <- span:
	default:
		e.dropped.Add(1)
	}
}

func (e *otlpExporter) loop() {
	defer close(e.done)
	batch := make([]tracker.ExportedSpan, 0, 64)
	tick := time.NewTimer(time.Hour)
	if !tick.Stop() {
		<-tick.C
	}
	defer tick.Stop()
	for {
		select {
		case span, ok := <-e.queue:
			if !ok {
				e.flush(batch)
				return
			}
			batch = append(batch, span)
			if len(batch) >= 64 {
				e.flush(batch)
				batch = batch[:0]
				continue
			}
			if len(batch) == 1 {
				tick.Reset(250 * time.Millisecond)
			}
		case <-tick.C:
			e.flush(batch)
			batch = batch[:0]
		}
	}
}

func (e *otlpExporter) flush(batch []tracker.ExportedSpan) {
	if len(batch) == 0 {
		return
	}
	body, err := tracker.OTLPPayload(batch, "tower")
	if err != nil {
		e.dropped.Add(uint64(len(batch)))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, strings.NewReader(string(body)))
	if err != nil {
		e.dropped.Add(uint64(len(batch)))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		if e.dropped.Add(uint64(len(batch))) == uint64(len(batch)) {
			log.Printf("otlp export: first failure (later ones are silent): %v", err)
		}
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		e.dropped.Add(uint64(len(batch)))
		return
	}
	e.sent.Add(uint64(len(batch)))
}

// Status reports the exporter's queue depth and counters for the /v1/otlp/status
// endpoint. QueueLen is a snapshot of a buffered channel and is safe to read.
func (e *otlpExporter) Status() map[string]any {
	if e == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":  true,
		"endpoint": e.endpoint,
		"queued":   len(e.queue),
		"capacity": cap(e.queue),
		"sent":     e.sent.Load(),
		"dropped":  e.dropped.Load(),
	}
}

// Close stops accepting spans, flushes what is queued, and returns after timeout.
func (e *otlpExporter) Close(timeout time.Duration) {
	e.once.Do(func() { close(e.queue) })
	select {
	case <-e.done:
	case <-time.After(timeout):
		log.Printf("otlp export: %d spans still queued at shutdown", len(e.queue))
	}
}

func newSpanWriter(db pgDB) *spanWriter {
	w := &spanWriter{db: db, queue: make(chan spanRecord, 1024), done: make(chan struct{})}
	go w.loop()
	return w
}

// Close stops accepting spans, drains the queue and returns once the last one is written
// (or after timeout). Called on graceful shutdown so in-flight traces are not lost.
func (w *spanWriter) Close(timeout time.Duration) {
	w.once.Do(func() { close(w.queue) })
	select {
	case <-w.done:
	case <-time.After(timeout):
		log.Printf("span sink: %d spans still queued at shutdown", len(w.queue))
	}
}

func (w *spanWriter) loop() {
	defer close(w.done)
	for rec := range w.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := w.db.Exec(ctx,
			`INSERT INTO spans (trace_id, span_id, parent_id, name, attrs, started_at) VALUES ($1, $2, $3, $4, $5::jsonb, $6) ON CONFLICT (trace_id, span_id) DO NOTHING`,
			rec.traceID, rec.spanID, rec.parentID, rec.name, rec.attrs, rec.at)
		cancel()
		if err != nil && w.dropped.Add(1) == 1 {
			log.Printf("span sink: first write failure (later ones are silent): %v", err)
		}
		if w.Hub != nil {
			w.Hub.Publish(rec) // live view even when the database is down
		}
		if w.Exporter != nil {
			w.Exporter.Add(rec)
		}
	}
}

// decisionWriter drains routing decisions to Postgres on one goroutine, off the request
// path. Same contract as spanWriter: a dead database costs the request nothing, a full
// queue drops the decision and counts it.
type decisionWriter struct {
	db      pgDB
	queue   chan router.Decision
	dropped atomic.Uint64
	done    chan struct{}
	once    sync.Once
}

func newDecisionWriter(db pgDB) *decisionWriter {
	w := &decisionWriter{db: db, queue: make(chan router.Decision, 512), done: make(chan struct{})}
	go w.loop()
	return w
}

func (w *decisionWriter) Close(timeout time.Duration) {
	w.once.Do(func() { close(w.queue) })
	select {
	case <-w.done:
	case <-time.After(timeout):
		log.Printf("decision sink: %d decisions still queued at shutdown", len(w.queue))
	}
}

func (w *decisionWriter) loop() {
	defer close(w.done)
	for d := range w.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := w.db.Exec(ctx,
			`INSERT INTO route_decisions
			 (trace_id, intent, intent_confidence, difficulty, policy, tier, reason, planned, chosen, fallback, failed, candidates, sensitive, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			d.TraceID, string(d.Intent), d.IntentConfidence, d.Difficulty, d.Policy,
			d.Tier, d.Reason, d.Planned, d.Chosen, d.Fallback, d.Failed, d.Candidates, d.Sensitive, d.At)
		cancel()
		if err != nil && w.dropped.Add(1) == 1 {
			log.Printf("decision sink: first write failure (later ones are silent): %v", err)
		}
	}
}

func (w *decisionWriter) Sink() router.DecisionSink {
	return func(d router.Decision) {
		defer func() {
			if recover() != nil { // send on closed queue during shutdown: drop, count
				w.dropped.Add(1)
			}
		}()
		select {
		case w.queue <- d:
		default:
			w.dropped.Add(1)
		}
	}
}

func (w *spanWriter) Sink() router.SpanSink {
	return func(traceID, spanID, parentID, name, attrs string) {
		defer func() {
			if recover() != nil { // send on closed queue during shutdown: drop, count
				w.dropped.Add(1)
			}
		}()
		select {
		case w.queue <- spanRecord{traceID: traceID, spanID: spanID, parentID: parentID, name: name, attrs: attrs, at: time.Now().UTC()}:
		default:
			w.dropped.Add(1)
		}
	}
}

func runTrace(dsn, traceID string) {
	var data any
	err := withConn(dsn, 15*time.Second, func(ctx context.Context, conn *pgx.Conn) error {
		var err error
		data, err = queryTrace(conn, traceID)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}
	raw, _ := json.MarshalIndent(data, "", "  ")
	log.Printf("trace %s:\n%s", traceID, string(raw))
}

// runTrajectory prints what one workflow run did: steps, attempts, retries, and whether it
// was clean. Read-only, so it is safe against a running workflow (it will look unfinished).
func runTrajectory(dsn, workflowID string) {
	var t tracker.Trajectory
	err := withConn(dsn, 15*time.Second, func(ctx context.Context, conn *pgx.Conn) error {
		var err error
		t, err = tracker.TrajectoryFor(ctx, trackerAdapter{pgAdapter{conn}}, workflowID)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("trajectory %s", t.Summary())
}

// runWorkflowRepeats measures reliability, not quality: the same input N times, then pass^k
// (the chance all k trials succeed, estimated from the clean ones). The toy workflow has no
// ground truth for its content, so "clean" means every step landed first time - which is the
// property a durable-workflow pitch actually claims.
func runWorkflowRepeats(dsn, ollamaURL, model, input string, runs int) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(runs)*10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("postgres dsn: %v", err)
	}
	defer pool.Close()
	store, researcher, drafter, reviewer := toyWorkflow(pool, ollamaURL, model)
	db := trackerAdapter{pgAdapter{pool}}
	clean := 0
	for i := 1; i <= runs; i++ {
		id := tracker.NewWorkflowID()
		start := time.Now()
		if _, err := tracker.RunToy(ctx, store, id, input, researcher, drafter, reviewer); err != nil {
			log.Printf("run %d/%d %s failed: %v", i, runs, id, err)
			continue
		}
		t, err := tracker.TrajectoryFor(ctx, db, id)
		if err != nil {
			log.Printf("run %d/%d %s: trajectory unavailable: %v", i, runs, id, err)
			continue
		}
		if t.Clean {
			clean++
		}
		log.Printf("run %d/%d %s %s (%s)", i, runs, id, t.Summary(), time.Since(start).Round(time.Millisecond))
	}
	for k := 1; k <= runs; k++ {
		v, ok := tracker.PassK(clean, runs, k)
		if !ok {
			continue
		}
		log.Printf("pass^%d = %.3f (%d/%d runs clean)", k, v, clean, runs)
	}
	if clean == 0 {
		log.Printf("no clean runs: nothing here says the workflow is unreliable, only that none of %d finished first-try - read the per-run lines", runs)
	}
}

// runWorkflowGrade scores a workflow's final answer on a rubric and stores it. This is
// quality, not faithfulness: the workflow does not persist the chunks its researcher
// retrieved, so there is no source to check the answer against.
func runWorkflowGrade(dsn, ollamaURL, workflowID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)
	var question string
	if err := conn.QueryRow(ctx, `SELECT input FROM workflows WHERE id = $1`, workflowID).Scan(&question); err != nil {
		log.Fatalf("workflow %s: %v", workflowID, err)
	}
	var answer string
	err = conn.QueryRow(ctx,
		`SELECT output FROM steps WHERE workflow_id = $1 AND status = 'done' ORDER BY seq DESC LIMIT 1`,
		workflowID).Scan(&answer)
	if err != nil {
		log.Fatalf("workflow %s has no completed step to grade: %v", workflowID, err)
	}
	model := envOr("JUDGE_MODEL", "qwen3:8b")
	gen := evals.NewOllamaGenerator(ollamaURL, model)
	score, err := evals.GradeAnswer(ctx, gen, question, answer)
	if err != nil {
		log.Fatalf("grade %s: %v", workflowID, err)
	}
	if score.Value == 0 {
		log.Printf("grade %s: UNSCORED - the judge returned no SCORE: 1-5 line; reason: %s", workflowID, firstLine(score.Reason))
		os.Exit(2)
	}
	if err := evals.SaveWorkflowGrade(ctx, pgAdapter{conn}, evals.WorkflowGrade{
		WorkflowID: workflowID, Judge: model, Prompt: evals.RubricPromptVersion,
		Score: score.Value, Reason: score.Reason,
	}); err != nil {
		log.Fatalf("store grade: %v", err)
	}
	log.Printf("grade %s: %d/5 by %s (%s) - %s", workflowID, score.Value, model, evals.RubricPromptVersion, firstLine(score.Reason))
}

// firstLine keeps a judge's one-sentence reason on one line in a log.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// extraModels is the cleaned OLLAMA_MODELS list: named models a client may
// address explicitly. Path-like entries are skipped (Ollama itself uses
// OLLAMA_MODELS for its models directory — Track J).
func extraModels(fastModel, qualityModel string) []string {
	var out []string
	for _, m := range strings.Split(os.Getenv("OLLAMA_MODELS"), ",") {
		m = strings.TrimSpace(m)
		if m == "" || m == fastModel || m == qualityModel {
			continue
		}
		if strings.ContainsAny(m, `/\`) {
			log.Printf("OLLAMA_MODELS entry %q looks like a path, not a model — skipping", m)
			continue
		}
		out = append(out, m)
	}
	return out
}

// runAdvise prints the Track K static advisor report: per-model size, VRAM
// fit against the budget, presence, and the co-residency verdict. It changes
// nothing and serves nothing. The budget is the VRAM nvidia-smi reports when a
// driver answers, and docs/VRAM.md's 8 GB otherwise — the header always says
// which, because a veto on a 4 GB card and a pass on an 8 GB one are otherwise
// indistinguishable.
func runAdvise(cfg Config) {
	names := []string{cfg.FastModel, cfg.QualityModel}
	names = append(names, extraModels(cfg.FastModel, cfg.QualityModel)...)
	names = append(names, "nomic-embed-text")
	var pulled map[string]bool
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if set, err := router.NewOllamaClient(cfg.OllamaURL).PulledModels(ctx); err == nil {
		pulled = set
	} else {
		log.Printf("advise: presence unknown (%v) — reporting configured lineup", err)
	}
	budget, source := float64(advisor.BoxVRAMGB), "docs/VRAM.md"
	if s, err := gpu.NewNvidiaSMI(envOr("GPU_SMI", "nvidia-smi")).Read(ctx); err == nil && s.MemTotalBytes > 0 {
		budget, source = s.TotalGB(), "measured: "+s.Name
	}
	fmt.Print(advisor.AdviseWithBudget(names, pulled, budget, source).String())
}

// toyWorkflow builds the Researcher → Drafter → Reviewer step functions over any pgDB
// (a CLI connection or the server's pool) so the CLI and the console share one agent.
func toyWorkflow(db pgDB, ollamaURL, fastModel string) (tracker.Store, tracker.StepFunc, tracker.StepFunc, tracker.StepFunc) {
	ad := trackerAdapter{pgAdapter{db}}
	store := tracker.SQLStore{Exec: ad, Query: ad}
	embedModel := os.Getenv("EMBED_MODEL")
	if embedModel == "" {
		embedModel = "nomic-embed-text"
	}
	embedder := evals.NewEmbedder(ollamaURL, embedModel)
	client := router.NewOllamaClient(ollamaURL)
	researcher := func(ctx context.Context, q string) (string, error) {
		vec, err := embedder.Embed(q)
		if err != nil {
			return "", err
		}
		rows, err := db.Query(ctx,
			`SELECT doc_id, hash, text, source FROM chunks ORDER BY embedding <=> $1::vector LIMIT 3`,
			evals.VectorLiteral(vec))
		if err != nil {
			return "", err
		}
		defer rows.Close()
		var parts []string
		for rows.Next() {
			var doc, hash, text, source string
			if err := rows.Scan(&doc, &hash, &text, &source); err != nil {
				return "", err
			}
			parts = append(parts, text)
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		if len(parts) == 0 {
			return "no corpus context; answer from input: " + q, nil
		}
		// Track R: the researcher context is machine-assembled, so it is
		// budget-trimmed like the judge context (relevance order from the
		// similarity query — head survives). The trim is logged, and the
		// step's output snippet already shows the trimmed text.
		kept, n := evals.TrimToBudget(parts, evals.DefaultBudgetTokens)
		if n > 0 {
			log.Printf("researcher: budget trimmed %d chunk(s)", n)
		}
		return strings.Join(kept, "\n---\n"), nil
	}
	drafter := func(ctx context.Context, contextText string) (string, error) {
		text, _, err := client.Generate(ctx, fastModel, []router.Message{{Role: "user", Content: "Draft a 3-sentence brief from this context:\n" + contextText}}, 0)
		return text, err
	}
	reviewer := func(ctx context.Context, draft string) (string, error) {
		trimmed := strings.TrimSpace(draft)
		if trimmed == "" {
			return "", errEmptyDraft
		}
		if len(trimmed) > 800 {
			trimmed = trimmed[:800]
		}
		return "approved: " + trimmed, nil
	}
	return store, researcher, drafter, reviewer
}

func runTracker(dsn, ollamaURL, resumeID, input string, fastModel string) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)
	store, researcher, drafter, reviewer := toyWorkflow(conn, ollamaURL, fastModel)
	workflowID := resumeID
	if workflowID == "" {
		workflowID = tracker.NewWorkflowID()
	}
	// Track S: the transition trail is best-effort telemetry — a failed audit
	// write is logged loudly and never aborts the workflow.
	auditLog := audit.SQLStore{Exec: pgAdapter{conn}, Query: pgAdapter{conn}}
	auditNote := func(e audit.Entry) {
		if err := auditLog.Append(ctx, e); err != nil {
			log.Printf("audit: %v", err)
		}
	}
	if resumeID == "" {
		auditNote(audit.WorkflowCreated(workflowID, "toy"))
	} else {
		auditNote(audit.WorkflowResumed(workflowID))
	}
	final, err := tracker.RunToy(ctx, store, workflowID, input, researcher, drafter, reviewer)
	if err != nil {
		auditNote(audit.WorkflowFailed(workflowID, "run_error"))
		log.Fatalf("tracker workflow %s: %v", workflowID, err)
	}
	auditNote(audit.WorkflowCompleted(workflowID))
	log.Printf("tracker workflow %s done: %.120q", workflowID, final)
}

func runDraftGolden(ollamaURL, version string) {
	genModel := os.Getenv("DRAFT_MODEL")
	if genModel == "" {
		genModel = "qwen3:8b"
	}
	chunks, err := evals.ReadClean("evals/corpus/clean")
	if err != nil {
		log.Fatal(err)
	}
	pairs, err := evals.DraftPairs(evals.NewOllamaGenerator(ollamaURL, genModel), chunks)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll("evals/golden", 0755); err != nil {
		log.Fatal(err)
	}
	fh, err := os.Create("evals/golden/" + version + "_draft.jsonl")
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(fh)
	for _, p := range pairs {
		if err := enc.Encode(p); err != nil {
			fh.Close()
			log.Fatal(err)
		}
	}
	fh.Close()
	log.Printf("drafted %d pairs from %d chunks → evals/golden/%s_draft.jsonl", len(pairs), len(chunks), version)
}

func runFreezeGolden(version string) {
	path := "evals/golden/" + version + ".jsonl"
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("create %s from the reviewed draft first: %v", path, err)
	}
	chunks, err := evals.ReadClean("evals/corpus/clean")
	if err != nil {
		log.Fatal(err)
	}
	known := map[string]bool{}
	for _, c := range chunks {
		known[c.DocID] = true
	}
	n, err := evals.ValidateGolden(raw, known)
	if err != nil {
		log.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if err := os.WriteFile("evals/golden/"+version+".sha256", []byte(hex.EncodeToString(sum[:])+"  "+version+".jsonl\n"), 0644); err != nil {
		log.Fatal(err)
	}
	log.Printf("frozen %s: %d pairs sha256=%s…", version, n, hex.EncodeToString(sum[:8]))
}

// runCreateKey mints an API key, stores its hash, and prints the secret exactly once.
func runCreateKey(dsn, name string, rpm int, budget int64) {
	if strings.TrimSpace(name) == "" {
		log.Fatal("--create-key needs a name")
	}
	var secret string
	err := withConn(dsn, 15*time.Second, func(ctx context.Context, conn *pgx.Conn) error {
		var err error
		secret, err = router.SQLKeyStore{Exec: pgAdapter{conn}, Query: routerAdapter{pgAdapter{conn}}}.Create(ctx, name, rpm, budget)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("api key %q created (rpm=%d budget_tokens=%d)\n%s\n", name, rpm, budget, secret)
	fmt.Println("store it now — only its SHA-256 is kept")
}

// buildBackends wires the local Ollama backend plus cloud backends from the environment.
// CLOUD_LADDER (preferred) is a comma-separated list of name:key:url:model rungs, best
// first. When it is unset, the legacy GROQ_API_KEY / CLOUD_* variables form a single
// rung, so existing deployments keep working unchanged.
func buildBackends(cfg Config) (*router.Server, []router.Backend) {
	local := router.NewOllamaClient(cfg.OllamaURL)
	srv := router.NewServer(cfg.FastModel, cfg.QualityModel, local)
	backends := []router.Backend{local}
	tracker := router.NewExhaustionTracker(srv.Metrics)
	srv.Exhaustion = tracker

	addCloud := func(name, key, base, model string) {
		if strings.TrimSpace(key) == "" {
			return
		}
		if strings.TrimSpace(base) == "" {
			base = "https://api.groq.com/openai/v1"
		}
		if strings.TrimSpace(name) == "" {
			name = "cloud"
		}
		if strings.TrimSpace(model) == "" {
			model = defaultGroqModel
		}
		cloud := router.NewOpenAIBackend(name, base, key)
		srv.AddModel(router.ModelRef{Tier: "cloud", Model: model, Backend: cloud})
		backends = append(backends, cloud)
	}

	if ladder := os.Getenv("CLOUD_LADDER"); strings.TrimSpace(ladder) != "" {
		for _, rung := range strings.Split(ladder, ",") {
			rung = strings.TrimSpace(rung)
			if rung == "" {
				continue
			}
			// Fields are separated by "|" (URLs and model names contain colons).
			parts := strings.Split(rung, "|")
			if len(parts) != 4 {
				log.Printf("CLOUD_LADDER: skipping malformed rung %q (want name|key|url|model)", rung)
				continue
			}
			addCloud(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]),
				strings.TrimSpace(parts[2]), strings.TrimSpace(parts[3]))
		}
	} else if key := os.Getenv("GROQ_API_KEY"); key != "" {
		model := os.Getenv("CLOUD_MODEL")
		base := os.Getenv("CLOUD_BASE_URL")
		name := os.Getenv("CLOUD_BACKEND_NAME")
		addCloud(name, key, base, model)
	}
	// OLLAMA_MODELS: extra local models a client may name explicitly (`"model": "qwen2.5:3b-instruct"`).
	// A multi-agent client places different roles on different models; without this the
	// gateway would answer unknown_model for anything but the two tier models.
	for _, m := range extraModels(cfg.FastModel, cfg.QualityModel) {
		srv.AddModel(router.ModelRef{Tier: "local", Model: m, Backend: local})
	}
	// ROUTER_POLICY: "heuristic" (default) uses ClassifyThread; "learned" asks a
	// small local model to classify the tier, falling back to the heuristic on
	// error/timeout. Only meaningful when Ollama is reachable.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("ROUTER_POLICY")), "learned") {
		cm := strings.TrimSpace(os.Getenv("ROUTER_CLASSIFIER_MODEL"))
		if cm == "" {
			cm = cfg.FastModel
		}
		srv.RouterPolicy = "learned"
		srv.Classifier = router.OllamaClassifier(local, cm)
		log.Printf("router policy: learned (classifier model %s)", cm)
		// Pre-warm the classifier model so the first real request doesn't pay
		// the cold-load penalty (several seconds on a 4 GB card).
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if _, _, err := srv.Classifier(ctx, "warmup"); err != nil {
				log.Printf("classifier warmup: %v (first request may be slow)", err)
			} else {
				log.Printf("classifier warmup: ok")
			}
		}()
	} else {
		srv.RouterPolicy = "heuristic"
	}
	if kws := os.Getenv("SENSITIVE_KEYWORDS"); kws != "" {
		srv.SensitiveKeywords = strings.Split(strings.ToLower(kws), ",")
	}
	if t := os.Getenv("REQUEST_TIMEOUT"); t != "" {
		if d, err := time.ParseDuration(t); err == nil && d > 0 {
			srv.RequestTimeout = d
		}
	}
	return srv, backends
}

func runScore(dsn, ollamaURL, goldenPath, goldenVersion, scoreModel string) {
	if err := scoreOnce(dsn, ollamaURL, goldenPath, goldenVersion, scoreModel); err != nil {
		log.Fatal(err)
	}
}

func runScheduler(dsn, ollamaURL, goldenPath, goldenVersion, scoreModel, every string) {
	d, err := time.ParseDuration(every)
	if err != nil {
		log.Fatalf("bad interval %q: %v", every, err)
	}
	if d <= 0 {
		log.Fatalf("interval must be positive, got %q", every)
	}
	for {
		if err := scoreOnce(dsn, ollamaURL, goldenPath, goldenVersion, scoreModel); err != nil {
			log.Printf("scheduled eval failed: %v", err)
		}
		log.Printf("next scheduled eval in %s", d)
		time.Sleep(d)
	}
}

func float64Ptr(f float64) *float64 { return &f }

// evalTimeout bounds one suite run. 48 pairs take ~8 min on an idle 4060 but well over 30
// when the judge shares the GPU with live traffic (every request swaps models), so the
// default is generous and EVAL_TIMEOUT overrides it.
func evalTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("EVAL_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return 90 * time.Minute
}

// evalTally is the suite summary. A pair whose answer yields no scorable claim is
// not a zero: the judge was never asked anything, so it is counted as unmeasured
// and kept out of the mean (audit 2026-11-18 C4). Misses and unmeasured pairs are
// different facts and stay separate.
type evalTally struct {
	sumF, sumP, sumR                              float64
	scored, misses, refuted, noClaims, unsolvable int
}

func (t *evalTally) add(faithfulness float64, claims int, miss, unsolvable bool, precision, recall float64) {
	switch {
	case miss:
		t.misses++
	case claims == 0:
		t.noClaims++
	default:
		t.sumF += faithfulness
		t.scored++
		if faithfulness == 0 {
			t.refuted++
		}
	}
	if unsolvable {
		t.unsolvable++
	}
	t.sumP += precision
	t.sumR += recall
}

func scoreOnce(dsn, ollamaURL, goldenPath, goldenVersion, scoreModel string) error {
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout())
	defer cancel()
	// The window is this run, measured rather than inferred: the prober that
	// keeps sampling is usually the server's, in another process, which is
	// exactly why the counter is persisted.
	runStart := time.Now()
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	// The doc ids the validator must accept come from the ingested corpus, which
	// is the same set retrieval can return. Reading them from the database rather
	// than the clean/ folder keeps a binary-only deployment scoring.
	known := map[string]bool{}
	docRows, err := conn.Query(ctx, `SELECT DISTINCT doc_id FROM chunks`)
	if err != nil {
		return fmt.Errorf("read corpus doc ids: %w", err)
	}
	for docRows.Next() {
		var id string
		if err := docRows.Scan(&id); err != nil {
			docRows.Close()
			return fmt.Errorf("read corpus doc ids: %w", err)
		}
		known[id] = true
	}
	docRows.Close()
	if err := docRows.Err(); err != nil {
		return fmt.Errorf("read corpus doc ids: %w", err)
	}
	pairs, err := evals.LoadGolden(raw, known)
	if err != nil {
		return fmt.Errorf("%s: %w (is the corpus ingested? run --ingest)", goldenPath, err)
	}
	embedModel := os.Getenv("EMBED_MODEL")
	if embedModel == "" {
		embedModel = "nomic-embed-text"
	}
	embedder := evals.NewEmbedder(ollamaURL, embedModel)
	search := func(ctx context.Context, q string, k int) ([]evals.Chunk, error) {
		return evals.Search(ctx, pgAdapter{conn}, embedder, q, k)
	}
	var judge evals.Judge
	judgeName := os.Getenv("JUDGE_MODEL")
	if judgeName == "" {
		judgeName = "qwen3:8b"
	}
	if os.Getenv("JUDGE_BACKEND") == "groq" {
		key := os.Getenv("GROQ_API_KEY")
		if key == "" {
			return errors.New("GROQ_API_KEY is empty")
		}
		model := envOr("GROQ_JUDGE_MODEL", defaultGroqModel)
		judge = evals.NewGroqJudge(model, key)
		judgeName = "groq/" + model
	} else {
		judge = evals.NewOllamaJudge(ollamaURL, judgeName)
	}
	topK := 5
	var tally evalTally
	auditLog := audit.SQLStore{Exec: pgAdapter{conn}, Query: pgAdapter{conn}}
	if err := auditLog.Append(ctx, audit.EvalStarted(goldenVersion, scoreModel, len(pairs))); err != nil {
		log.Printf("audit: %v", err)
	}
	// Track L: with --score-model the answers come from the named model (same
	// retrieval, same judge); without it the frozen golden answers are scored
	// exactly as before.
	var answer evals.Answerer
	if scoreModel != "" {
		gen := router.NewOllamaClient(ollamaURL)
		answer = func(ctx context.Context, question, contextText string) (string, int, error) {
			text, usage, err := gen.GenerateWith(ctx, scoreModel,
				[]router.Message{{Role: "user", Content: "Answer the question using only the context below. Reply in one or two full sentences.\n\nContext:\n" + contextText + "\n\nQuestion: " + question}},
				router.GenParams{MaxTokens: 256, Temperature: float64Ptr(0)})
			if err != nil {
				return "", 0, err
			}
			return text, usage.CompletionTokens, nil
		}
	}
	run, err := evals.StartOrResumeRun(ctx, pgAdapter{conn}, goldenVersion, judgeName, scoreModel, evals.JudgePromptVersion, runStart)
	if err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	done := make(map[string]bool, len(run.Pairs))
	for _, p := range run.Pairs {
		done[p.Question] = true
		// Fold what an earlier attempt already judged into this run's summary, so the
		// number at the end covers the whole suite rather than only the tail.
		tally.add(p.Faithfulness, p.Claims, p.RetrievalMiss, p.Unsolvable(), p.Precision, p.Recall)
	}
	if run.Resumed {
		log.Printf("resuming run %d: %d/%d pairs already judged (each pair is stored as it lands; a crash no longer loses the run)", run.ID, len(run.Pairs), len(pairs))
	}
	for i, p := range pairs {
		if done[p.Question] {
			continue
		}
		var res evals.PairResult
		var err error
		if answer != nil {
			res, err = evals.ScoreGeneratedPair(ctx, p, search, judge, answer, topK)
		} else {
			res, err = evals.ScorePair(ctx, p, search, judge, topK)
		}
		if err != nil {
			// Mark the run failed but keep every pair already judged: the next attempt
			// resumes from here instead of re-judging the whole suite.
			if ferr := evals.FailRun(ctx, pgAdapter{conn}, run.ID); ferr != nil {
				log.Printf("could not mark run %d failed: %v", run.ID, ferr)
			}
			return fmt.Errorf("pair %d (run %d kept %d/%d pairs): %w", i+1, run.ID, len(done), len(pairs), err)
		}
		if err := evals.AddPair(ctx, pgAdapter{conn}, run.ID, res); err != nil {
			if ferr := evals.FailRun(ctx, pgAdapter{conn}, run.ID); ferr != nil {
				log.Printf("could not mark run %d failed: %v", run.ID, ferr)
			}
			return fmt.Errorf("store pair %d (run %d): %w", i+1, run.ID, err)
		}
		done[res.Question] = true
		tally.add(res.Faithfulness, len(res.Claims), res.RetrievalMiss, evals.Unsolvable(res), res.Precision, res.Recall)
		log.Printf("pair %d miss=%v faith=%.2f p=%.2f r=%.2f trimmed=%d q=%.60q", i+1, res.RetrievalMiss, res.Faithfulness, res.Precision, res.Recall, res.Trimmed, res.Question)
	}
	n := float64(len(pairs))
	avgF := 0.0
	if tally.scored > 0 {
		avgF = tally.sumF / float64(tally.scored) // faith where retrieved; misses and unmeasured pairs excluded
	} else {
		// Nothing was judged, so there is no faithfulness measurement. The run is still
		// recorded (its miss rows are evidence about retrieval), but drift and the gate
		// ignore it - a 0.000 here would read as "everything was hallucinated" when the
		// truth is "nothing was asked of the judge".
		log.Printf("warning: no judged pairs - all %d were retrieval misses; is the corpus ingested? run --ingest. This run is evidence about retrieval, not a faithfulness score", tally.misses)
	}
	// unsolvable = misses + fully refuted: the share of the suite no router could have
	// rescued, which is the ceiling any routing policy is measured against. It is printed
	// next to the score so a low faith number is never read as a routing opportunity.
	log.Printf("suite n=%d scored=%d misses=%d refuted=%d no-claims=%d unsolvable=%d judge=%s prompt=%s faith=%.3f precision=%.3f recall=%.3f",
		len(pairs), tally.scored, tally.misses, tally.refuted, tally.noClaims, tally.unsolvable, judgeName, evals.JudgePromptVersion, avgF, tally.sumP/n, tally.sumR/n)
	if err := evals.FinishRun(ctx, pgAdapter{conn}, run.ID, avgF); err != nil {
		return fmt.Errorf("finish run %d: %w", run.ID, err)
	}
	runID := run.ID
	threshold := evalThreshold()
	rep, err := evals.DriftReportFor(ctx, pgAdapter{conn}, goldenVersion, threshold)
	if err != nil {
		return fmt.Errorf("drift report: %w", err)
	}
	if err := auditLog.Append(ctx, audit.EvalFinished(runID, avgF, rep.Alert)); err != nil {
		log.Printf("audit: %v", err)
	}
	log.Printf("eval_run id=%d golden=%s faith=%.3f threshold=%.2f alert=%v runs=%d delta=%.3f misses_now=%d",
		runID, goldenVersion, avgF, threshold, rep.Alert, rep.Runs, rep.Delta, rep.MissesNow)
	logRunGPU(ctx, conn, runStart, time.Now())
	return nil
}

// logRunGPU prints what the card did during this run. It never fails the run:
// the measurement is evidence about comparability, not a gate. No samples means
// the prober was off, and that is said plainly instead of being read as a quiet
// GPU (a zero here would be a claim nobody measured).
func logRunGPU(ctx context.Context, db pgDB, from, to time.Time) {
	sum, err := gpu.SQLStore{Query: gpuAdapter{pgAdapter{db}}}.Summary(ctx, from, to)
	if err != nil {
		log.Printf("gpu during this run: unavailable (%v)", err)
		return
	}
	if sum.Samples == 0 {
		log.Printf("gpu during this run: no samples - the prober was not running, so comparability is unknown")
		return
	}
	msg := fmt.Sprintf("gpu during this run: %d samples, mean %.0f%% util, peak %.1f of %.1f GB VRAM",
		sum.Samples, sum.UtilMean,
		float64(sum.MemPeakBytes)/(1<<30), float64(sum.MemTotalBytes)/(1<<30))
	if sum.MaxTempC > 0 {
		msg += fmt.Sprintf(", peak %.0fC", sum.MaxTempC)
	}
	msg += fmt.Sprintf(", %d model swap(s)", sum.Swaps)
	if sum.Swaps > 0 {
		msg += " - a swap unloads one model to load another, so this run is not directly comparable with a quiet one"
	}
	if sum.ThrottledSamples > 0 {
		msg += fmt.Sprintf("; thermally throttled in %d/%d samples - clocks were cut, so latency is not comparable either", sum.ThrottledSamples, sum.Samples)
	}
	log.Printf("%s", msg)
}

func runSearch(dsn, ollamaURL, query string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)
	embedModel := os.Getenv("EMBED_MODEL")
	if embedModel == "" {
		embedModel = "nomic-embed-text"
	}
	hits, err := evals.Search(ctx, pgAdapter{conn}, evals.NewEmbedder(ollamaURL, embedModel), query, 5)
	if err != nil {
		log.Fatal(err)
	}
	for i, h := range hits {
		snip := h.Text
		if len(snip) > 120 {
			snip = snip[:120] + "…"
		}
		log.Printf("#%d [%s] %s", i+1, h.DocID, snip)
	}
}

func runIngest(dsn, ollamaURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)
	chunks, err := evals.ReadClean("evals/corpus/clean")
	if err != nil {
		log.Fatal(err)
	}
	embedModel := os.Getenv("EMBED_MODEL")
	if embedModel == "" {
		embedModel = "nomic-embed-text"
	}
	// Track Q: sidecars live beside the clean corpus (never inside it, so the
	// cleaner cannot wipe them); a re-ingest embeds only changed chunks.
	cache := evals.NewHashCache("evals/corpus/cache", embedModel, evals.NewEmbedder(ollamaURL, embedModel))
	n, err := evals.Ingest(ctx, pgAdapter{conn}, chunks, cache)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("ingested %d chunks (cache hits=%d new=%d corrupt=%d model_changed=%d)",
		n, cache.Hits, cache.New, cache.Corrupt, cache.ModelChanged)
}

// isLoopbackRemote reports whether a request came from this machine. It parses
// the peer address rather than trusting a header, because a header is the thing
// a remote client controls.
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// consoleGuard keeps the operator surface on the machine that runs it. The
// console returns stored prompts verbatim and can start GPU work, so a peer that
// is not loopback is refused unless the operator set CONSOLE_REMOTE=true
// deliberately. The refusal uses the same error envelope as every other gateway
// error, so a client parses one shape.
func consoleGuard(allowRemote bool, next http.Handler) http.Handler {
	if allowRemote {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLoopbackRemote(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code":     "console_local_only",
			"message":  "the console is reachable from this machine only; set CONSOLE_REMOTE=true to allow remote clients",
			"trace_id": "",
		}})
	})
}

// securityHeaders is the console's browser contract: no framing, no MIME
// sniffing, no referrer leakage, and no script or style the binary did not ship.
// The two font hosts are the console's only third-party origins.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"style-src 'self' https://fonts.googleapis.com; " +
		"font-src https://fonts.gstatic.com; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// Every response carries the id the API reference promises. Chat and
		// gateway error handlers overwrite it with the id that names their spans;
		// console and trace reads keep this one, which identifies the request
		// even though it has no span of its own.
		if h.Get("X-Trace-ID") == "" {
			h.Set("X-Trace-ID", tracker.NewWorkflowID())
		}
		next.ServeHTTP(w, r)
	})
}

// withCrossOriginProtection rejects state-changing browser requests from another
// origin (Sec-Fetch-Site, falling back to Origin vs Host). Non-browser clients —
// curl, an SDK, an agent — send neither header and are unaffected.
func withCrossOriginProtection(next http.Handler) http.Handler {
	return http.NewCrossOriginProtection().Handler(next)
}

// purger is the one method the retention sweep needs, so the sweep is testable
// without a database.
type purger interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
}

// tableRetention is one table's window. table and column are constants chosen by
// the caller, never client input; the cutoff is a typed interval below.
type tableRetention struct {
	table, column string
	keep          time.Duration
}

// purgeTelemetry deletes rows older than each window. See RULES.md §7 and
// PRIVACY.md: before this sweep existed the policy was prose and three tables
// grew forever.
func purgeTelemetry(ctx context.Context, db purger, windows ...tableRetention) (map[string]int64, error) {
	out := make(map[string]int64, len(windows))
	for _, w := range windows {
		rows, err := db.Exec(ctx,
			`DELETE FROM `+w.table+` WHERE `+w.column+` < now() - make_interval(secs => $1)`,
			int64(w.keep/time.Second))
		if err != nil {
			return out, fmt.Errorf("%s: %w", w.table, err)
		}
		out[w.table] = rows
	}
	return out, nil
}

func main() {
	mcpMode := flag.Bool("mcp", false, "run as MCP stdio server instead of HTTP router")
	migrate := flag.Bool("migrate", false, "apply SQL migrations and exit")
	ingest := flag.Bool("ingest", false, "embed clean corpus into pgvector and exit")
	search := flag.String("search", "", "cosine-search the corpus for QUERY and exit")
	draftGolden := flag.Bool("draft-golden", false, "draft golden QA pairs with local LLM into evals/golden/<golden-version>_draft.jsonl and exit")
	freezeGolden := flag.Bool("freeze-golden", false, "validate evals/golden/<golden-version>.jsonl and freeze its hash")
	score := flag.Bool("score", false, "run faithfulness + retrieval suite over golden file and exit")
	scoreModel := flag.String("score-model", "", "model under test for --score (Track L): generate each answer with MODEL at temperature 0 instead of scoring frozen answers; the run is tagged with the model")
	goldenPath := flag.String("golden", "", "golden file for --score (default evals/golden/<golden-version>.jsonl)")
	goldenVersion := flag.String("golden-version", "v3", "golden version: names the draft/frozen files and tags eval_runs")
	driftFlag := flag.Bool("drift", false, "print drift report for golden version and exit")
	driftGolden := flag.String("drift-golden", "v3", "golden version for --drift and report endpoint")
	releaseGate := flag.Bool("release-gate", false, "pre-tag release check (Track T): fail unless the last --gate-k golden runs pass the release bar, then exit")
	judgeAudit := flag.Bool("judge-audit", false, "measure the faithfulness judge against a labelled claim/context set (precision, recall, agreement, order stability) and exit")
	auditFile := flag.String("audit-file", "evals/golden/judge-audit.jsonl", "labelled JSONL set for --judge-audit: id, claim, context, supported")
	judgeModelFlag := flag.String("judge-model", "", "judge model for --judge-audit (default JUDGE_MODEL, else qwen3:8b)")
	judge2Model := flag.String("judge2-model", "", "second judge for --judge-audit; when set, Cohen's kappa between the two is reported")
	judge2Backend := flag.String("judge2-backend", "ollama", "backend for --judge2-model: ollama (local) or groq (needs GROQ_API_KEY)")
	routeReplay := flag.Bool("route-replay", false, "print the routing-decision mix from route_decisions (intent, tier, chosen) and exit")
	routeWindow := flag.String("route-window", "720h", "how far back --route-replay looks")
	gateK := flag.Int("gate-k", 3, "consecutive green runs --release-gate requires (fewer runs, mixed judges, or a sub-threshold score all fail)")
	scheduleEvals := flag.String("schedule-evals", "", "run eval suite every INTERVAL (e.g. 24h) forever; empty disables")
	runTrackerFlag := flag.Bool("run-tracker", false, "run toy Researcher-Drafter-Reviewer workflow and exit")
	resumeTracker := flag.String("resume-tracker", "", "resume toy workflow ID and exit")
	trackerInput := flag.String("tracker-input", "what does tower do?", "input question for --run-tracker")
	traceID := flag.String("trace", "", "print redacted spans for TRACE_ID and exit")
	trajectoryID := flag.String("trajectory", "", "print trajectory metrics (steps, attempts, retries, clean) for WORKFLOW_ID and exit")
	workflowRepeats := flag.Int("workflow-repeats", 0, "run the toy workflow N times on --tracker-input and print pass^k reliability, then exit")
	workflowGrade := flag.String("workflow-grade", "", "grade WORKFLOW_ID's final answer on a 1-5 rubric, store it, and exit")
	advise := flag.Bool("advise", false, "print the static model advisor report (Track K) and exit")
	showVersion := flag.Bool("version", false, "print version, commit and build date, then exit")
	versionJSON := flag.Bool("version-json", false, "print the build info as one line of JSON and exit")
	createKey := flag.String("create-key", "", "create an API key with NAME, print the secret once, and exit")
	keyRPM := flag.Int("key-rpm", 0, "requests per minute for --create-key (0 = unlimited)")
	keyBudget := flag.Int64("key-budget", 0, "lifetime token budget for --create-key (0 = unlimited)")
	flag.Parse()
	if *showVersion {
		bi := buildInfoOf(version, commit, buildDate)
		line := "tower " + bi.Version
		if bi.Commit != "" {
			line += " (" + shortCommit(bi.Commit) + ")"
		}
		if bi.Date != "" {
			line += " built " + bi.Date
		}
		fmt.Println(line)
		return
	}
	if *versionJSON {
		raw, err := json.Marshal(buildInfoOf(version, commit, buildDate))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(string(raw))
		return
	}
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if *goldenPath == "" {
		*goldenPath = "evals/golden/" + *goldenVersion + ".jsonl"
	}
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		dsn = defaultDSN()
	}
	if *migrate {
		runMigrate(dsn)
		return
	}
	if *routeReplay {
		runRouteReplay(dsn, *routeWindow, cfg.FastModel)
		return
	}
	if *createKey != "" {
		runCreateKey(dsn, *createKey, *keyRPM, *keyBudget)
		return
	}
	if *ingest {
		runIngest(dsn, cfg.OllamaURL)
		return
	}
	if *search != "" {
		runSearch(dsn, cfg.OllamaURL, *search)
		return
	}
	if *draftGolden {
		runDraftGolden(cfg.OllamaURL, *goldenVersion)
		return
	}
	if *freezeGolden {
		runFreezeGolden(*goldenVersion)
		return
	}
	if *score {
		runScore(dsn, cfg.OllamaURL, *goldenPath, *goldenVersion, *scoreModel)
		return
	}
	if *driftFlag {
		runDrift(dsn, *driftGolden, evalThreshold())
		return
	}
	if *releaseGate {
		runReleaseGate(dsn, *goldenVersion, *gateK, evalThreshold())
		return
	}
	if *judgeAudit {
		runJudgeAudit(cfg.OllamaURL, *judgeModelFlag, *judge2Model, *judge2Backend, *auditFile)
		return
	}
	if *scheduleEvals != "" {
		runScheduler(dsn, cfg.OllamaURL, *goldenPath, *goldenVersion, *scoreModel, *scheduleEvals)
		return
	}
	if *runTrackerFlag {
		runTracker(dsn, cfg.OllamaURL, "", *trackerInput, cfg.FastModel)
		return
	}
	if *resumeTracker != "" {
		runTracker(dsn, cfg.OllamaURL, *resumeTracker, *trackerInput, cfg.FastModel)
		return
	}
	if *traceID != "" {
		runTrace(dsn, *traceID)
		return
	}
	if *trajectoryID != "" {
		runTrajectory(dsn, *trajectoryID)
		return
	}
	if *workflowRepeats > 0 {
		runWorkflowRepeats(dsn, cfg.OllamaURL, cfg.FastModel, *trackerInput, *workflowRepeats)
		return
	}
	if *workflowGrade != "" {
		runWorkflowGrade(dsn, cfg.OllamaURL, *workflowGrade)
		return
	}
	if *advise {
		runAdvise(cfg)
		return
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("postgres dsn: %v", err)
	}
	defer pool.Close()
	if err := checkSchema(pool); err != nil {
		log.Fatal(err)
	}
	srv, backends := buildBackends(cfg)
	spans := newSpanWriter(pool)
	srv.SpanSink = spans.Sink()
	decisions := newDecisionWriter(pool)
	srv.DecisionSink = decisions.Sink()
	events := newEventHub()
	spans.Hub = events
	var otlpExp *otlpExporter
	if endpoint := strings.TrimSpace(os.Getenv("OTLP_ENDPOINT")); endpoint != "" {
		otlpExp = newOTLPExporter(endpoint)
		spans.Exporter = otlpExp
		log.Printf("otlp export: spans also go to %s (dual-named gen_ai.*, prompts never exported)", endpoint)
	}
	srv.Keys = router.SQLKeyStore{Exec: pgAdapter{pool}, Query: routerAdapter{pgAdapter{pool}}}
	srv.RequireKey = strings.EqualFold(os.Getenv("REQUIRE_API_KEY"), "true")
	srv.Prober = router.NewHealthProber(srv.Metrics, backends...)
	// Track J: the gateway never advertises an unpulled model. Presence is
	// refreshed on every probe tick; gaps are logged (once per model) at boot
	// and on change.
	srv.Prober.AfterProbe = func(ctx context.Context) { srv.RefreshPulled(ctx) }
	probeCtx, stopProbe := context.WithCancel(context.Background())
	defer stopProbe()
	go srv.Prober.Run(probeCtx, 15*time.Second)
	{
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		srv.RefreshPulled(ctx)
		cancel()
	}
	// GPU sampling is best-effort: a dead Postgres must not stop local
	// measurement, so the prober drops insert errors and the boot purge is
	// skipped when the database is unreachable. The prober never runs on a
	// request path — nvidia-smi costs ~100ms and is read from the last snapshot.
	gpuStore := gpu.SQLStore{Exec: pgAdapter{pool}, Query: gpuAdapter{pgAdapter{pool}}}
	gpuProber := gpu.NewProber(
		gpu.NewNvidiaSMI(envOr("GPU_SMI", "nvidia-smi")),
		gpu.OllamaPS{URL: cfg.OllamaURL},
		gpuStore,
	)
	go gpuProber.Run(probeCtx, gpuPollInterval())
	srv.Metrics.Extra = gpuProber.Expose
	// Retention: samples beyond GPU_HISTORY_KEEP are dropped at boot. Bounded so
	// an unreachable database delays startup by seconds, not by the TCP timeout.
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		gpuKeep := gpuHistoryKeep()
		if n, err := gpuStore.Purge(ctx, gpuKeep); err != nil {
			log.Printf("gpu purge skipped: %v", err)
		} else if n > 0 {
			log.Printf("gpu: dropped %d samples older than %s", n, gpuKeep)
		}
	}()
	if v, ok := latestEvalScore(pool, *driftGolden); ok {
		srv.Metrics.SetEvalFaithfulness(v)
	}
	// Retention: threads idle 90+ days are purged at boot, best-effort.
	// See PRIVACY.md and migrations/0005_conversations.sql.
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		store := console.SQLStore{Query: pgAdapter{pool}, Exec: pgAdapter{pool}, Row: pgAdapter{pool}, Begin: pgAdapter{pool}}
		if n, err := store.PurgeConversations(ctx, 90); err != nil {
			log.Printf("conversation purge skipped: %v", err)
		} else if n > 0 {
			log.Printf("conversation purge: dropped %d idle thread(s)", n)
		}
	}()
	// Retention for the three tables that grow with traffic. Before this, the
	// policy existed only in RULES.md/PRIVACY.md; the sweep runs at boot and then
	// daily, because a long-lived container never reboots (audit 2026-11-18 A1).
	purgeWindows := []tableRetention{
		{"spans", "started_at", spanKeep()},
		{"route_decisions", "created_at", decisionKeep()},
		{"audit_log", "ts", auditKeep()},
	}
	sweep := func(when string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n, err := purgeTelemetry(ctx, pgAdapter{pool}, purgeWindows...)
		if err != nil {
			log.Printf("telemetry retention (%s): %v", when, err)
			return
		}
		for table, rows := range n {
			if rows > 0 {
				log.Printf("retention (%s): dropped %d row(s) from %s", when, rows, table)
			}
		}
	}
	sweep("boot")
	go func() {
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-probeCtx.Done():
				return
			case <-t.C:
				sweep("daily")
			}
		}
	}()

	mcpSrv := mcp.NewServer(srv)
	mcpSrv.SpanSink = spans.Sink() // MCP tool calls land in the same span store
	mcpSrv.TraceLookup = func(id string) (any, error) {
		return queryTrace(pool, id)
	}
	mcpSrv.DriftLookup = func() (any, error) {
		return queryDrift(pool, *driftGolden, evalThreshold())
	}
	if *mcpMode {
		log.Printf("tower mcp stdio fast=%s quality=%s", cfg.FastModel, cfg.QualityModel)
		if err := mcpSrv.Run(os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	mux := http.NewServeMux()
	// Tower console: UI at / plus its read endpoints and two actions, all on the pool.
	auditTrail := audit.SQLStore{Exec: pgAdapter{pool}, Query: pgAdapter{pool}}
	auditNote := func(e audit.Entry) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := auditTrail.Append(ctx, e); err != nil {
			log.Printf("audit: %v", err)
		}
	}
	runWorkflow := func(id, input string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			store, researcher, drafter, reviewer := toyWorkflow(pool, cfg.OllamaURL, cfg.FastModel)
			if _, err := tracker.RunToy(ctx, store, id, input, researcher, drafter, reviewer); err != nil {
				auditNote(audit.WorkflowFailed(id, "run_error"))
				log.Printf("workflow %s: %v", id, err)
				return
			}
			auditNote(audit.WorkflowCompleted(id))
			log.Printf("workflow %s done", id)
		}()
	}
	ui := console.New(console.Deps{
		Store:         console.SQLStore{Query: pgAdapter{pool}, Exec: pgAdapter{pool}, Row: pgAdapter{pool}, GPU: gpuAdapter{pgAdapter{pool}}, Begin: pgAdapter{pool}},
		Router:        srv,
		GPU:           gpuProber.Snapshot,
		GoldenVersion: *driftGolden,
		Version:       version,
		Drift: func(ctx context.Context, golden string) (any, error) {
			return queryDrift(pool, golden, evalThreshold())
		},
		RunEval: func(ctx context.Context, golden string) error {
			return scoreOnce(dsn, cfg.OllamaURL, "evals/golden/"+golden+".jsonl", golden, "")
		},
		StartWorkflow: func(ctx context.Context, input string) (string, error) {
			id := tracker.NewWorkflowID()
			auditNote(audit.WorkflowCreated(id, "toy"))
			runWorkflow(id, input)
			return id, nil
		},
		ResumeWork: func(ctx context.Context, id string) error {
			var status string
			if err := pool.QueryRow(ctx, `SELECT status FROM workflows WHERE id = $1`, id).Scan(&status); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return console.ErrNotFound
				}
				return err
			}
			if status == tracker.StatusDone {
				return fmt.Errorf("workflow %s is already done", id)
			}
			auditNote(audit.WorkflowResumed(id))
			runWorkflow(id, "")
			return nil
		},
	})
	// The console reads stored prompts and starts GPU work, so it is local-only
	// unless the operator opts in. ADDR being loopback makes this a second lock,
	// not the only one.
	consoleRemote := strings.EqualFold(os.Getenv("CONSOLE_REMOTE"), "true")
	consoleUI := consoleGuard(consoleRemote, ui)
	for _, pattern := range ui.Routes() {
		mux.Handle(pattern, consoleUI)
	}
	mux.HandleFunc("GET /v1/drift/report", func(w http.ResponseWriter, r *http.Request) {
		data, err := queryDrift(pool, *driftGolden, evalThreshold())
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "eval_unavailable", "message": "eval store unavailable"}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		raw, err := json.Marshal(data)
		if err != nil {
			// Marshal first: an unencodable report (NaN metric) must be a loud
			// 502, never 200 headers with an empty body.
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "eval_unavailable", "message": "eval store unavailable"}})
			return
		}
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /v1/traces/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "bad_request", "message": "trace id required"}})
			return
		}
		data, err := queryTrace(pool, id)
		if errors.Is(err, tracker.ErrTraceNotFound) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "trace_not_found", "message": "no spans for trace id", "trace_id": id}})
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "trace_unavailable", "message": "trace store unavailable", "trace_id": id}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(data)
	})
	// Live ticket rail for Tower #/live: redacted spans as server-sent events.
	mux.Handle("GET /v1/events", events)
	// MCP over HTTP for remote agents: the same five tools as --mcp stdio, one
	// request per POST, answered by the same handle() via HandleOne — the two
	// transports cannot drift. Model calls still travel the fail-closed
	// Router.Chat path (sensitive never leaves the box). RPM/budget accounting
	// stays on the chat path; this gate only checks presence and validity.
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		traceID := tracker.NewWorkflowID()
		mcpErr := func(status int, code, msg string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg, "trace_id": traceID}})
		}
		// The same gate as the chat path — validity, disabled, budget and RPM —
		// rather than a weaker copy that only checked that the key existed.
		key, ok := srv.Authenticate(w, r, traceID)
		if !ok {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			mcpErr(http.StatusBadRequest, "bad_request", "unreadable body")
			return
		}
		raw, status, tokens := mcpSrv.HandleOneHTTPUsage(body, r.Header.Get("MCP-Protocol-Version"))
		// A tool that called a model is charged like a chat call; leaving it out
		// made /mcp an unmetered door around the budget.
		if key != nil && srv.Keys != nil && tokens > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := srv.Keys.AddUsage(ctx, key.ID, tokens); err != nil {
				log.Printf("mcp usage for key %d: %v", key.ID, err)
			}
			cancel()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
	})
	if otlpExp != nil {
		mux.HandleFunc("GET /v1/otlp/status", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(otlpExp.Status())
		})
	}
	mux.Handle("/", srv)
	// ReadTimeout closes a slow-trickled body; WriteTimeout is the model budget
	// plus a margin, so a streaming answer is not cut off mid-flight; IdleTimeout
	// reaps keep-alive connections that are doing nothing.
	writeTimeout := srv.RequestTimeout + 30*time.Second
	if srv.RequestTimeout <= 0 {
		writeTimeout = 0
	}
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           securityHeaders(withCrossOriginProtection(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       120 * time.Second,
	}
	// Graceful shutdown: stop accepting, let in-flight chats finish (up to the request
	// timeout), then flush queued spans and close the pool.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	consoleScope := "local-only"
	if consoleRemote {
		consoleScope = "remote-allowed"
	}
	log.Printf("tower %s router on %s fast=%s quality=%s backends=%d auth=%v console=%s", version, cfg.Addr, cfg.FastModel, cfg.QualityModel, len(backends), srv.RequireKey, consoleScope)
	select {
	case err := <-errCh:
		log.Fatal(err)
	case sig := <-stop:
		log.Printf("%s: draining in-flight requests", sig)
		ctx, cancel := context.WithTimeout(context.Background(), srv.RequestTimeout)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		stopProbe()
		spans.Close(5 * time.Second)
		if spans.Exporter != nil {
			spans.Exporter.Close(5 * time.Second)
		}
		decisions.Close(2 * time.Second)
		log.Print("bye")
	}
}
