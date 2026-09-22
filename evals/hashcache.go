package evals

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
)

// TextEmbedder embeds one text. *Embedder implements it; HashCache wraps it;
// Ingest takes the interface so tests inject a counting fake.
type TextEmbedder interface {
	Embed(text string) ([]float32, error)
}

type sidecar struct {
	Model  string    `json:"model"`
	Dims   int       `json:"dims"`
	Vector []float32 `json:"vector"`
}

// HashCache is a read-through embedding cache: one {shorthash}.json sidecar
// per chunk text under dir. Re-ingest embeds only what changed; anything
// unreadable, cross-model, dims-mismatched or non-finite is a miss (re-embed
// + overwrite), never an error. Order of operations per chunk — embed, write
// sidecar, upsert — converges on re-run: a crash between any two steps just
// repeats the cheap one.
//
// The payload carries the model name, so a model bump misses on the same
// paths and overwrites them: no hand-rm, no orphans, no silently stale evals.
// Counters classify every miss for the ingest log (new / corrupt /
// model-changed).
type HashCache struct {
	dir          string
	model        string
	embed        TextEmbedder
	Hits         int
	New          int
	Corrupt      int
	ModelChanged int
}

func NewHashCache(dir, model string, embed TextEmbedder) *HashCache {
	return &HashCache{dir: dir, model: model, embed: embed}
}

func (c *HashCache) key(text string) string {
	// Text-only namespace: a model bump re-reads the same paths and misses on
	// the payload model check, then overwrites — self-cleaning, no orphans.
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

func (c *HashCache) path(text string) string {
	return filepath.Join(c.dir, c.key(text)+".json")
}

func valid(model string, s sidecar) bool {
	if s.Model != model || s.Dims != len(s.Vector) || len(s.Vector) == 0 {
		return false
	}
	for _, v := range s.Vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return false
		}
	}
	return true
}

// Embed returns the cached vector on hit, else embeds, persists, and returns.
// Filesystem errors on read are misses; a write failure is returned (the
// caller fails loudly rather than ingesting uncached silently forever).
func (c *HashCache) Embed(text string) ([]float32, error) {
	raw, err := os.ReadFile(c.path(text))
	if err == nil {
		var s sidecar
		if json.Unmarshal(raw, &s) == nil {
			switch {
			case s.Model != "" && s.Model != c.model:
				c.ModelChanged++
			case valid(c.model, s):
				c.Hits++
				return s.Vector, nil
			default:
				c.Corrupt++
			}
		} else {
			c.Corrupt++
		}
	} else if !os.IsNotExist(err) {
		c.Corrupt++
	} else {
		c.New++
	}
	vec, err := c.embed.Embed(text)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return nil, err
	}
	out, err := json.Marshal(sidecar{Model: c.model, Dims: len(vec), Vector: vec})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(c.path(text), out, 0644); err != nil {
		return nil, err
	}
	return vec, nil
}
