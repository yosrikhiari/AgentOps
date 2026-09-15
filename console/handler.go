package console

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentops/router"
)

//go:embed static
var staticFS embed.FS

// Deps is everything the console needs from the rest of the binary. Every field but
// Store and Router is optional; missing actions answer 501.
type Deps struct {
	Store         Store
	Router        *router.Server
	Drift         func(ctx context.Context, golden string) (any, error)
	RunEval       func(ctx context.Context, golden string) error
	StartWorkflow func(ctx context.Context, input string) (string, error)
	ResumeWork    func(ctx context.Context, id string) error
	GoldenVersion string
	Version       string
}

// Handler serves the UI at / and the console JSON under /v1/. It nests router endpoints
// nowhere — main.go mounts both on one mux.
type Handler struct {
	deps Deps
	mux  *http.ServeMux

	mu       sync.Mutex
	evalBusy bool
	evalLast evalStatus
}

type evalStatus struct {
	Running    bool      `json:"running"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Error      string    `json:"error,omitempty"`
	Golden     string    `json:"golden_version,omitempty"`
}

func New(deps Deps) *Handler {
	h := &Handler{deps: deps, mux: http.NewServeMux()}
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	h.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	h.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, sub, "index.html")
	})
	h.mux.HandleFunc("GET /v1/overview", h.overview)
	h.mux.HandleFunc("GET /v1/requests", h.requests)
	h.mux.HandleFunc("GET /v1/evals/runs", h.evalRuns)
	h.mux.HandleFunc("GET /v1/evals/status", h.evalStatus)
	h.mux.HandleFunc("POST /v1/evals/run", h.evalRun)
	h.mux.HandleFunc("GET /v1/workflows", h.workflows)
	h.mux.HandleFunc("POST /v1/workflows", h.startWorkflow)
	h.mux.HandleFunc("POST /v1/workflows/{id}/resume", h.resumeWorkflow)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// Routes lists the paths this handler owns, so main.go can mount them individually
// next to the router's own /v1 endpoints.
func (h *Handler) Routes() []string {
	return []string{"GET /{$}", "GET /static/", "GET /v1/overview", "GET /v1/requests", "GET /v1/evals/runs",
		"GET /v1/evals/status", "POST /v1/evals/run", "GET /v1/workflows", "POST /v1/workflows", "POST /v1/workflows/{id}/resume"}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

func limitParam(r *http.Request, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ov, err := h.deps.Store.Overview(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "store_unavailable", "span store unavailable")
		return
	}
	out := map[string]any{"version": h.deps.Version, "golden_version": h.deps.GoldenVersion, "traffic": ov,
		"judge_cost_usd": 0.0}
	if h.deps.Router != nil {
		models := make([]map[string]any, 0, len(h.deps.Router.Models))
		for _, ref := range h.deps.Router.Models {
			m := map[string]any{"model": ref.Model, "tier": ref.Tier, "backend": ref.Backend.Name()}
			if h.deps.Router.Prober != nil {
				m["up"] = h.deps.Router.Prober.Up(ref.Backend.Name())
			}
			models = append(models, m)
		}
		out["models"] = models
		if h.deps.Router.Prober != nil {
			out["backends"] = h.deps.Router.Prober.Statuses()
		}
		snap := h.deps.Router.Metrics.Snapshot()
		totals := map[string]any{}
		for name, s := range snap {
			totals[name] = map[string]uint64{"requests": s.Requests, "errors": s.Errors, "tokens": s.Tokens}
		}
		out["since_start"] = totals
	}
	if h.deps.Drift != nil {
		if rep, err := h.deps.Drift(ctx, h.deps.GoldenVersion); err == nil {
			out["drift"] = rep
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) requests(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	reqs, err := h.deps.Store.RecentRequests(ctx, limitParam(r, 30, 200))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "store_unavailable", "span store unavailable")
		return
	}
	if reqs == nil {
		reqs = []Request{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": reqs})
}

func (h *Handler) evalRuns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	golden := r.URL.Query().Get("golden")
	if golden == "" {
		golden = h.deps.GoldenVersion
	}
	runs, err := h.deps.Store.EvalRuns(ctx, golden, limitParam(r, 50, 500))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "store_unavailable", "eval store unavailable")
		return
	}
	if runs == nil {
		runs = []EvalRun{}
	}
	out := map[string]any{"golden_version": golden, "runs": runs}
	if h.deps.Drift != nil {
		if rep, err := h.deps.Drift(ctx, golden); err == nil {
			out["drift"] = rep
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) evalStatus(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	st := h.evalLast
	st.Running = h.evalBusy
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, st)
}

// evalRun starts one suite run in the background; a second request while one is running
// gets 409 rather than a second judge storm on the same GPU.
func (h *Handler) evalRun(w http.ResponseWriter, r *http.Request) {
	if h.deps.RunEval == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured", "eval runner not configured")
		return
	}
	var body struct {
		Golden string `json:"golden_version"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Golden == "" {
		body.Golden = h.deps.GoldenVersion
	}
	h.mu.Lock()
	if h.evalBusy {
		h.mu.Unlock()
		writeErr(w, http.StatusConflict, "eval_running", "an eval run is already in progress")
		return
	}
	h.evalBusy = true
	h.evalLast = evalStatus{Running: true, StartedAt: time.Now(), Golden: body.Golden}
	h.mu.Unlock()
	go func(golden string) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		err := h.deps.RunEval(ctx, golden)
		h.mu.Lock()
		h.evalBusy = false
		h.evalLast.Running = false
		h.evalLast.FinishedAt = time.Now()
		if err != nil {
			h.evalLast.Error = err.Error()
			log.Printf("console: eval run failed: %v", err)
		}
		h.mu.Unlock()
	}(body.Golden)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "golden_version": body.Golden})
}

func (h *Handler) workflows(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	wfs, err := h.deps.Store.Workflows(ctx, limitParam(r, 20, 200))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "store_unavailable", "workflow store unavailable")
		return
	}
	if wfs == nil {
		wfs = []Workflow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": wfs})
}

func (h *Handler) startWorkflow(w http.ResponseWriter, r *http.Request) {
	if h.deps.StartWorkflow == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured", "workflow runner not configured")
		return
	}
	var body struct {
		Input string `json:"input"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Input) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "input is required")
		return
	}
	id, err := h.deps.StartWorkflow(r.Context(), body.Input)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "workflow_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "running"})
}

var ErrNotFound = errors.New("workflow not found")

func (h *Handler) resumeWorkflow(w http.ResponseWriter, r *http.Request) {
	if h.deps.ResumeWork == nil {
		writeErr(w, http.StatusNotImplemented, "not_configured", "workflow runner not configured")
		return
	}
	id := r.PathValue("id")
	if err := h.deps.ResumeWork(r.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "workflow_not_found", "no workflow with that id")
			return
		}
		writeErr(w, http.StatusBadGateway, "workflow_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "resuming"})
}
