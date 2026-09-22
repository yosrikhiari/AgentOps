package evals

// Track Q — embedding hash cache (TDD RED: HashCache does not exist yet).

import (
	"os"
	"path/filepath"
	"testing"
)

type countingEmbed struct {
	calls int
	vec   []float32
}

func (f *countingEmbed) Embed(text string) ([]float32, error) {
	f.calls++
	return f.vec, nil
}

func TestHashCacheHits(t *testing.T) {
	fx := &countingEmbed{vec: []float32{0.1, 0.2}}
	c := NewHashCache(t.TempDir(), "m", fx)
	if _, err := c.Embed("hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Embed("hello"); err != nil {
		t.Fatal(err)
	}
	if fx.calls != 1 {
		t.Fatalf("identical text embedded %d times, want 1", fx.calls)
	}
	if c.Hits != 1 {
		t.Fatalf("hits=%d, want 1", c.Hits)
	}
}

func TestHashCacheMissOnNewText(t *testing.T) {
	fx := &countingEmbed{vec: []float32{0.1}}
	c := NewHashCache(t.TempDir(), "m", fx)
	if _, err := c.Embed("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Embed("b"); err != nil {
		t.Fatal(err)
	}
	if fx.calls != 2 || c.New != 2 {
		t.Fatalf("calls=%d new=%d, want 2/2", fx.calls, c.New)
	}
}

func TestHashCacheCorruptIsMiss(t *testing.T) {
	dir := t.TempDir()
	fx := &countingEmbed{vec: []float32{0.1, 0.2}}
	c := NewHashCache(dir, "m", fx)
	if _, err := c.Embed("hello"); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("want 1 sidecar, got %d", len(files))
	}
	if err := os.WriteFile(files[0], []byte(`{"model": "m", "vec`), 0644); err != nil {
		t.Fatal(err)
	}
	fx.calls = 0
	vec, err := c.Embed("hello")
	if err != nil {
		t.Fatal(err)
	}
	if fx.calls != 1 || c.Corrupt != 1 {
		t.Fatalf("calls=%d corrupt=%d, want 1/1", fx.calls, c.Corrupt)
	}
	if len(vec) != 2 {
		t.Fatalf("rewritten vector wrong: %v", vec)
	}
}

func TestHashCacheModelChangeIsMiss(t *testing.T) {
	dir := t.TempDir()
	fx := &countingEmbed{vec: []float32{0.1}}
	c1 := NewHashCache(dir, "m1", fx)
	if _, err := c1.Embed("hello"); err != nil {
		t.Fatal(err)
	}
	c2 := NewHashCache(dir, "m2", fx)
	fx.calls = 0
	if _, err := c2.Embed("hello"); err != nil {
		t.Fatal(err)
	}
	if fx.calls != 1 || c2.ModelChanged != 1 {
		t.Fatalf("calls=%d changed=%d, want 1/1", fx.calls, c2.ModelChanged)
	}
}

func TestHashCacheRejectsBadVector(t *testing.T) {
	dir := t.TempDir()
	fx := &countingEmbed{vec: []float32{0.1}}
	c := NewHashCache(dir, "m", fx)
	if _, err := c.Embed("hello"); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	// Declared dims disagree with the stored vector: must miss, not serve.
	if err := os.WriteFile(files[0], []byte(`{"model":"m","dims":2,"vector":[0]}`), 0644); err != nil {
		t.Fatal(err)
	}
	fx.calls = 0
	fx.vec = []float32{0.5}
	vec, err := c.Embed("hello")
	if err != nil {
		t.Fatal(err)
	}
	if fx.calls != 1 || c.Corrupt != 1 {
		t.Fatalf("dims-mismatched sidecar must miss: calls=%d corrupt=%d", fx.calls, c.Corrupt)
	}
	if len(vec) != 1 || vec[0] != 0.5 {
		t.Fatalf("rewritten vector wrong: %v", vec)
	}
}
