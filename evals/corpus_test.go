package evals

import (
	"context"
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

func TestMigrateRunsSQLFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0001_a.sql"), []byte("SELECT 1"), 0644); err != nil {
		t.Fatal(err)
	}
	fx := &fakeExec{}
	if err := Migrate(context.Background(), fx, dir); err != nil {
		t.Fatal(err)
	}
	if len(fx.stmts) != 1 || !strings.Contains(fx.stmts[0], "SELECT 1") {
		t.Fatalf("got %+v", fx.stmts)
	}
}
