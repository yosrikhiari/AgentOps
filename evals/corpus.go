package evals

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Chunk struct {
	DocID  string `json:"doc_id"`
	Hash   string `json:"hash"`
	Text   string `json:"text"`
	Source string `json:"source"`
}

func ReadClean(dir string) ([]Chunk, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []Chunk
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
		for sc.Scan() {
			var c Chunk
			if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
				fh.Close()
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			out = append(out, c)
		}
		fh.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type Embedder struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

func NewEmbedder(baseURL, model string) *Embedder {
	return &Embedder{BaseURL: baseURL, Model: model, Client: &http.Client{Timeout: 120 * time.Second}}
}

func (e *Embedder) Embed(text string) ([]float32, error) {
	body, err := json.Marshal(map[string]any{"model": e.Model, "input": text})
	if err != nil {
		return nil, err
	}
	resp, err := e.Client.Post(e.BaseURL+"/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed status %d", resp.StatusCode)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) == 0 {
		return nil, fmt.Errorf("empty embedding")
	}
	return out.Embeddings[0], nil
}

func VectorLiteral(v []float32) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}

type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
}

// Migrate applies migrations/*.sql in name order, once each: every applied file name is
// recorded in schema_migrations and skipped on later runs. The MVP relied on every statement
// being IF NOT EXISTS; tracking makes the first ALTER/DROP migration safe to write. It
// returns how many files were applied this run.
func Migrate(ctx context.Context, db Execer, q Queryer, dir string) (int, error) {
	if _, err := db.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (filename TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return 0, fmt.Errorf("schema_migrations: %w", err)
	}
	applied := map[string]bool{}
	rows, err := q.Query(ctx, `SELECT filename FROM schema_migrations`)
	if err != nil {
		return 0, fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return 0, err
		}
		applied[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return 0, err
	}
	sort.Strings(files)
	n := 0
	for _, f := range files {
		name := filepath.Base(f)
		if applied[name] {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return n, err
		}
		if _, err := db.Exec(ctx, string(raw)); err != nil {
			return n, fmt.Errorf("%s: %w", name, err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1) ON CONFLICT DO NOTHING`, name); err != nil {
			return n, fmt.Errorf("record %s: %w", name, err)
		}
		n++
	}
	return n, nil
}

func Ingest(ctx context.Context, db Execer, chunks []Chunk, embed *Embedder) (int, error) {
	done := 0
	for _, c := range chunks {
		vec, err := embed.Embed(c.Text)
		if err != nil {
			return done, fmt.Errorf("embed %s: %w", c.Hash, err)
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO docs (id, source) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
			c.DocID, c.Source); err != nil {
			return done, err
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO chunks (doc_id, hash, text, source, embedding)
			 VALUES ($1, $2, $3, $4, $5::vector)
			 ON CONFLICT (hash) DO UPDATE SET text = EXCLUDED.text, embedding = EXCLUDED.embedding`,
			c.DocID, c.Hash, c.Text, c.Source, VectorLiteral(vec)); err != nil {
			return done, fmt.Errorf("upsert %s: %w", c.Hash, err)
		}
		done++
	}
	// Full-sync semantics: a chunk that is no longer produced by the cleaner (doc edited or
	// removed) must stop being retrievable, or v1 text would keep answering v2 questions.
	hashes := make([]string, 0, len(chunks))
	for _, c := range chunks {
		hashes = append(hashes, c.Hash)
	}
	if _, err := db.Exec(ctx, `DELETE FROM chunks WHERE NOT (hash = ANY($1))`, hashes); err != nil {
		return done, fmt.Errorf("prune stale chunks: %w", err)
	}
	return done, nil
}

type Row interface {
	Scan(dest ...any) error
}

type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

func Search(ctx context.Context, db Queryer, embed *Embedder, query string, topK int) ([]Chunk, error) {
	vec, err := embed.Embed(query)
	if err != nil {
		return nil, err
	}
	return SearchVector(ctx, db, vec, topK)
}

type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

func SearchVector(ctx context.Context, db Queryer, vec []float32, topK int) ([]Chunk, error) {
	rows, err := db.Query(ctx,
		`SELECT doc_id, hash, text, source FROM chunks
		 ORDER BY embedding <=> $1::vector LIMIT $2`,
		VectorLiteral(vec), topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.DocID, &c.Hash, &c.Text, &c.Source); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
