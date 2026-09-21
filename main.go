package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"agentops/advisor"
	"agentops/console"
	"agentops/evals"
	"agentops/mcp"
	"agentops/router"
	"agentops/tracker"
)

// version is stamped by the release build (-ldflags "-X main.version=v1.2.3").
var version = "dev"

var errEmptyDraft = errors.New("empty draft")

// pgDB is the slice of pgx shared by *pgx.Conn, *pgxpool.Pool and pgx.Tx.
type pgDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
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

func runMigrate(dsn string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)
	n, err := evals.Migrate(ctx, pgAdapter{conn}, pgAdapter{conn}, "migrations")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("migrations: applied %d new file(s)", n)
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
	return map[string]any{"trace_id": traceID, "spans": spans}, nil
}

type spanRecord struct {
	traceID, spanID, parentID, name, attrs string
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
			`INSERT INTO spans (trace_id, span_id, parent_id, name, attrs) VALUES ($1, $2, $3, $4, $5::jsonb) ON CONFLICT (trace_id, span_id) DO NOTHING`,
			rec.traceID, rec.spanID, rec.parentID, rec.name, rec.attrs)
		cancel()
		if err != nil && w.dropped.Add(1) == 1 {
			log.Printf("span sink: first write failure (later ones are silent): %v", err)
		}
		if w.Hub != nil {
			w.Hub.Publish(rec) // live view even when the database is down
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
		case w.queue <- spanRecord{traceID, spanID, parentID, name, attrs}:
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
// fit against the 8 GB budget, presence, and the co-residency verdict. It
// changes nothing and serves nothing.
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
	fmt.Print(advisor.Advise(names, pulled).String())
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
		return strings.Join(parts, "\n---\n"), nil
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
	final, err := tracker.RunToy(ctx, store, workflowID, input, researcher, drafter, reviewer)
	if err != nil {
		log.Fatalf("tracker workflow %s: %v", workflowID, err)
	}
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

// buildBackends wires the local Ollama backend plus an optional OpenAI-compatible cloud
// backend (Groq by default) from the environment.
func buildBackends(cfg Config) (*router.Server, []router.Backend) {
	local := router.NewOllamaClient(cfg.OllamaURL)
	srv := router.NewServer(cfg.FastModel, cfg.QualityModel, local)
	backends := []router.Backend{local}
	if key := os.Getenv("GROQ_API_KEY"); key != "" {
		model := os.Getenv("CLOUD_MODEL")
		if model == "" {
			model = "llama-3.1-8b-instant"
		}
		base := os.Getenv("CLOUD_BASE_URL")
		if base == "" {
			base = "https://api.groq.com/openai/v1"
		}
		name := os.Getenv("CLOUD_BACKEND_NAME")
		if name == "" {
			name = "groq"
		}
		cloud := router.NewOpenAIBackend(name, base, key)
		srv.AddModel(router.ModelRef{Tier: "cloud", Model: model, Backend: cloud})
		backends = append(backends, cloud)
	}
	// OLLAMA_MODELS: extra local models a client may name explicitly (`"model": "qwen2.5:3b-instruct"`).
	// A multi-agent client places different roles on different models; without this the
	// gateway would answer unknown_model for anything but the two tier models.
	for _, m := range extraModels(cfg.FastModel, cfg.QualityModel) {
		srv.AddModel(router.ModelRef{Tier: "local", Model: m, Backend: local})
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

func scoreOnce(dsn, ollamaURL, goldenPath, goldenVersion, scoreModel string) error {
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout())
	defer cancel()
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		return err
	}
	var pairs []evals.Pair
	for i, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var p evals.Pair
		if err := json.Unmarshal(line, &p); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
		pairs = append(pairs, p)
	}
	if len(pairs) == 0 {
		return fmt.Errorf("%s: no pairs", goldenPath)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
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
		judge = evals.NewGroqJudge("llama-3.1-8b-instant", key)
		judgeName = "groq/llama-3.1-8b-instant"
	} else {
		judge = evals.NewOllamaJudge(ollamaURL, judgeName)
	}
	topK := 5
	var sumF, sumP, sumR float64
	var scored, misses int
	var results []evals.PairResult
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
	for i, p := range pairs {
		var res evals.PairResult
		var err error
		if answer != nil {
			res, err = evals.ScoreGeneratedPair(ctx, p, search, judge, answer, topK)
		} else {
			res, err = evals.ScorePair(ctx, p, search, judge, topK)
		}
		if err != nil {
			return fmt.Errorf("pair %d: %w", i+1, err)
		}
		if res.RetrievalMiss {
			misses++
		} else {
			sumF += res.Faithfulness
			scored++
		}
		sumP += res.Precision
		sumR += res.Recall
		results = append(results, res)
		log.Printf("pair %d miss=%v faith=%.2f p=%.2f r=%.2f q=%.60q", i+1, res.RetrievalMiss, res.Faithfulness, res.Precision, res.Recall, res.Question)
	}
	n := float64(len(pairs))
	avgF := 0.0
	if scored > 0 {
		avgF = sumF / float64(scored) // faith where retrieved; misses excluded
	}
	log.Printf("suite n=%d scored=%d misses=%d judge=%s prompt=%s faith=%.3f precision=%.3f recall=%.3f",
		len(pairs), scored, misses, judgeName, evals.JudgePromptVersion, avgF, sumP/n, sumR/n)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	saver := pgAdapter{tx}
	runID, err := evals.RecordRun(ctx, saver, saver, goldenVersion, judgeName, scoreModel, avgF, results)
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("record run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit run: %w", err)
	}
	threshold := evalThreshold()
	rep, err := evals.DriftReportFor(ctx, pgAdapter{conn}, goldenVersion, threshold)
	if err != nil {
		return fmt.Errorf("drift report: %w", err)
	}
	log.Printf("eval_run id=%d golden=%s faith=%.3f threshold=%.2f alert=%v runs=%d delta=%.3f misses_now=%d",
		runID, goldenVersion, avgF, threshold, rep.Alert, rep.Runs, rep.Delta, rep.MissesNow)
	return nil
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
	n, err := evals.Ingest(ctx, pgAdapter{conn}, chunks, evals.NewEmbedder(ollamaURL, embedModel))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("ingested %d chunks", n)
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
	scheduleEvals := flag.String("schedule-evals", "", "run eval suite every INTERVAL (e.g. 24h) forever; empty disables")
	runTrackerFlag := flag.Bool("run-tracker", false, "run toy Researcher-Drafter-Reviewer workflow and exit")
	resumeTracker := flag.String("resume-tracker", "", "resume toy workflow ID and exit")
	trackerInput := flag.String("tracker-input", "what does agentops do?", "input question for --run-tracker")
	traceID := flag.String("trace", "", "print redacted spans for TRACE_ID and exit")
	advise := flag.Bool("advise", false, "print the static model advisor report (Track K) and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	createKey := flag.String("create-key", "", "create an API key with NAME, print the secret once, and exit")
	keyRPM := flag.Int("key-rpm", 0, "requests per minute for --create-key (0 = unlimited)")
	keyBudget := flag.Int64("key-budget", 0, "lifetime token budget for --create-key (0 = unlimited)")
	flag.Parse()
	if *showVersion {
		fmt.Println("agentops " + version)
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
	if *advise {
		runAdvise(cfg)
		return
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("postgres dsn: %v", err)
	}
	defer pool.Close()
	srv, backends := buildBackends(cfg)
	spans := newSpanWriter(pool)
	srv.SpanSink = spans.Sink()
	events := newEventHub()
	spans.Hub = events
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
	if v, ok := latestEvalScore(pool, *driftGolden); ok {
		srv.Metrics.SetEvalFaithfulness(v)
	}
	// Retention: threads idle 90+ days are purged at boot, best-effort.
	// See PRIVACY.md and migrations/0005_conversations.sql.
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		store := console.SQLStore{Query: pgAdapter{pool}, Exec: pgAdapter{pool}, Row: pgAdapter{pool}}
		if n, err := store.PurgeConversations(ctx, 90); err != nil {
			log.Printf("conversation purge skipped: %v", err)
		} else if n > 0 {
			log.Printf("conversation purge: dropped %d idle thread(s)", n)
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
		log.Printf("agentops mcp stdio fast=%s quality=%s", cfg.FastModel, cfg.QualityModel)
		if err := mcpSrv.Run(os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	mux := http.NewServeMux()
	// Tower console: UI at / plus its read endpoints and two actions, all on the pool.
	runWorkflow := func(id, input string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			store, researcher, drafter, reviewer := toyWorkflow(pool, cfg.OllamaURL, cfg.FastModel)
			if _, err := tracker.RunToy(ctx, store, id, input, researcher, drafter, reviewer); err != nil {
				log.Printf("workflow %s: %v", id, err)
				return
			}
			log.Printf("workflow %s done", id)
		}()
	}
	ui := console.New(console.Deps{
		Store:         console.SQLStore{Query: pgAdapter{pool}, Exec: pgAdapter{pool}, Row: pgAdapter{pool}},
		Router:        srv,
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
			runWorkflow(id, "")
			return nil
		},
	})
	for _, pattern := range ui.Routes() {
		mux.Handle(pattern, ui)
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
		if srv.RequireKey {
			secret := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if secret == "" {
				mcpErr(http.StatusUnauthorized, "missing_api_key", "Authorization: Bearer <api key> is required")
				return
			}
			key, err := srv.Keys.Lookup(r.Context(), router.HashSecret(secret))
			if err != nil {
				mcpErr(http.StatusUnauthorized, "invalid_api_key", "api key not recognised")
				return
			}
			if key.Disabled {
				mcpErr(http.StatusForbidden, "api_key_disabled", "api key disabled")
				return
			}
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			mcpErr(http.StatusBadRequest, "bad_request", "unreadable body")
			return
		}
		raw, status := mcpSrv.HandleOne(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
	})
	mux.Handle("/", srv)
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	// Graceful shutdown: stop accepting, let in-flight chats finish (up to the request
	// timeout), then flush queued spans and close the pool.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	log.Printf("agentops %s router on %s fast=%s quality=%s backends=%d auth=%v", version, cfg.Addr, cfg.FastModel, cfg.QualityModel, len(backends), srv.RequireKey)
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
		log.Print("bye")
	}
}
