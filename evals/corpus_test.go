package evals

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadClean(t *testing.T) {
	chunks, err := ReadClean("corpus/clean")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 15 {
		t.Fatalf("want >=15 chunks, got %d", len(chunks))
	}
	seen := map[string]bool{}
	for _, c := range chunks {
		if c.DocID == "" || c.Hash == "" || c.Text == "" {
			t.Fatalf("bad chunk %+v", c)
		}
		if seen[c.Hash] {
			t.Fatalf("dup hash %s", c.Hash)
		}
		seen[c.Hash] = true
	}
}

func TestVectorLiteral(t *testing.T) {
	got := VectorLiteral([]float32{0.5, -1.25, 0})
	if got != "[0.5,-1.25,0]" {
		t.Fatalf("got %s", got)
	}
}

type fakeExec struct {
	stmts []string
}

func (f *fakeExec) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	f.stmts = append(f.stmts, sql)
	return 1, nil
}

// fakeRows replays one string column; fakeQueryer serves it for the schema_migrations read.
type fakeRows struct {
	vals []string
	i    int
}

func (r *fakeRows) Next() bool {
	if r.i >= len(r.vals) {
		return false
	}
	r.i++
	return true
}
func (r *fakeRows) Scan(dest ...any) error { *(dest[0].(*string)) = r.vals[r.i-1]; return nil }
func (r *fakeRows) Err() error             { return nil }
func (r *fakeRows) Close()                 {}

type fakeQueryer struct{ applied []string }

func (f *fakeQueryer) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return &fakeRows{vals: f.applied}, nil
}

func TestMigrateRunsSQLFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0001_a.sql"), []byte("SELECT 1"), 0644); err != nil {
		t.Fatal(err)
	}
	fx := &fakeExec{}
	n, err := Migrate(context.Background(), fx, &fakeQueryer{}, dir)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// create table, the migration itself, the record insert
	if len(fx.stmts) != 3 || !strings.Contains(fx.stmts[1], "SELECT 1") || !strings.Contains(fx.stmts[2], "INSERT INTO schema_migrations") {
		t.Fatalf("got %+v", fx.stmts)
	}
}

// A file already listed in schema_migrations must not run again — that is the whole point
// of tracking, and what makes a future ALTER migration safe.
func TestMigrateSkipsApplied(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"0001_a.sql", "0002_b.sql"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("SELECT '"+f+"'"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	fx := &fakeExec{}
	n, err := Migrate(context.Background(), fx, &fakeQueryer{applied: []string{"0001_a.sql"}}, dir)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for _, s := range fx.stmts {
		if strings.Contains(s, "0001_a.sql'") {
			t.Fatalf("0001_a.sql ran again: %+v", fx.stmts)
		}
	}
	if !strings.Contains(strings.Join(fx.stmts, "\n"), "0002_b.sql") {
		t.Fatalf("0002_b.sql did not run: %+v", fx.stmts)
	}
}

// Ingest must prune chunks the cleaner no longer emits, otherwise an edited doc leaves its
// old text retrievable next to the new one.
func TestIngestPrunesStaleChunks(t *testing.T) {
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"embeddings":[[0.1,0.2]]}`))
	}))
	defer embed.Close()
	fx := &fakeExec{}
	n, err := Ingest(context.Background(), fx, []Chunk{{DocID: "d", Hash: "h1", Text: "t", Source: "d.md"}}, NewEmbedder(embed.URL, "m"))
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	last := fx.stmts[len(fx.stmts)-1]
	if !strings.Contains(last, "DELETE FROM chunks WHERE NOT (hash = ANY($1))") {
		t.Fatalf("last statement should prune stale chunks, got %q", last)
	}
}
