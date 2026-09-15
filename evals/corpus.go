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

func Migrate(ctx context.Context, db Execer, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, string(raw)); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
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
