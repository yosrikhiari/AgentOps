package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

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

func main() {
	mcpMode := flag.Bool("mcp", false, "run as MCP stdio server instead of HTTP router")
	migrate := flag.Bool("migrate", false, "apply SQL migrations and exit")
	ingest := flag.Bool("ingest", false, "embed clean corpus into pgvector and exit")
	search := flag.String("search", "", "cosine-search the corpus for QUERY and exit")
	draftGolden := flag.Bool("draft-golden", false, "draft golden QA pairs with local LLM into evals/golden/<golden-version>_draft.jsonl and exit")
	freezeGolden := flag.Bool("freeze-golden", false, "validate evals/golden/<golden-version>.jsonl and freeze its hash")
	score := flag.Bool("score", false, "run faithfulness + retrieval suite over golden file and exit")
	scoreModel := flag.String("score-model", "", "model under test for --score (Track L): generate each answer with MODEL at temperature 0 instead of scoring frozen answers; the run is tagged with the model")
	crossCheck := flag.Bool("score-cross-check", false, "run --score with a second judge (Groq) and report agreement between the two")
	scoreJudge2 := flag.String("score-judge2-model", "", "second judge model for --score cross-check (default groq/openai/gpt-oss-20b)")
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
	workflowVerify := flag.String("workflow-verify", "", "run the verifier (if any) for WORKFLOW_ID, store the result, and exit")
	exportRouting := flag.String("export-routing-data", "", "write route_decisions as JSONL to PATH and exit")
	judgePrompt := flag.String("judge-prompt", "v2", "prompt variant for --judge-audit: v2 (justification then verdict) or v3 (verdict first, then evidence)")
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
		if *crossCheck {
			log.Printf("cross-check: running primary judge")
			if err := scoreOnce(dsn, cfg.OllamaURL, *goldenPath, *goldenVersion, *scoreModel); err != nil {
				log.Fatal(err)
			}
			j2 := *scoreJudge2
			if j2 == "" {
				j2 = envOr("GROQ_JUDGE_MODEL", defaultGroqModel)
			}
			log.Printf("cross-check: running Groq judge (%s)", j2)
			os.Setenv("JUDGE_BACKEND", "groq")
			os.Setenv("GROQ_JUDGE_MODEL", j2)
			if err := scoreOnce(dsn, cfg.OllamaURL, *goldenPath, *goldenVersion, *scoreModel); err != nil {
				log.Printf("cross-check Groq run failed: %v", err)
			}
			log.Printf("cross-check: both runs complete; compare in the console (#/evals) or via --drift")
			return
		}
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
		runJudgeAudit(cfg.OllamaURL, *judgeModelFlag, *judge2Model, *judge2Backend, *auditFile, *judgePrompt)
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
	if *workflowVerify != "" {
		runWorkflowVerify(dsn, *workflowVerify)
		return
	}
	if *exportRouting != "" {
		runExportRoutingData(dsn, *exportRouting)
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
	runWorkflow := func(id, input, verifier string) {
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
			// Outcome-based verification (docs/WORKFLOW-EVAL-RESEARCH.md): if a
			// verifier is attached, run it and record the result. The workflow's
			// 'done' status reflects completion of its steps; 'verified' reflects
			// whether the environment actually changed as requested.
			if verifier != "" {
				out, verr := runVerifier(ctx, verifier)
				verified := verr == nil
				if _, err := pool.Exec(ctx, `UPDATE workflows SET verifier=$1, verified=$2, verifier_output=$3, verified_at=now() WHERE id=$4`, verifier, verified, out, id); err != nil {
					log.Printf("workflow %s: store verifier result: %v", id, err)
				}
				if verified {
					log.Printf("workflow %s verified: %s", id, strings.TrimSpace(out))
				} else {
					_, _ = pool.Exec(ctx, `UPDATE workflows SET status='failed' WHERE id=$1`, id)
					auditNote(audit.WorkflowFailed(id, "verification_failed"))
					log.Printf("workflow %s NOT verified: %v (%s)", id, verr, strings.TrimSpace(out))
				}
			}
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
		StartWorkflow: func(ctx context.Context, input, verifier string) (string, error) {
			id := tracker.NewWorkflowID()
			auditNote(audit.WorkflowCreated(id, "toy"))
			runWorkflow(id, input, verifier)
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
			runWorkflow(id, "", "")
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
