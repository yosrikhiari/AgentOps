package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// APIKey is one virtual key. Only the SHA-256 of the secret is ever stored; the secret
// itself is printed once at creation and never again.
type APIKey struct {
	ID           int
	Name         string
	RPM          int   // requests per minute, 0 = unlimited
	BudgetTokens int64 // lifetime token budget, 0 = unlimited
	TokensUsed   int64
	Disabled     bool
}

var (
	ErrKeyNotFound = errors.New("api key not found")
	ErrKeyDisabled = errors.New("api key disabled")
)

// KeyStore is what the gateway needs from persistence. SQLKeyStore is the real one,
// MemKeyStore serves tests.
type KeyStore interface {
	// Lookup returns the key for a secret's hash.
	Lookup(ctx context.Context, keyHash string) (APIKey, error)
	// AddUsage adds tokens to the key's lifetime counter.
	AddUsage(ctx context.Context, id int, tokens int64) error
}

// NewSecret returns a fresh secret ("ak_" + 32 hex chars) and its hash.
func NewSecret() (secret, hash string) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	secret = "ak_" + hex.EncodeToString(b[:])
	return secret, HashSecret(secret)
}

func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secret)))
	return hex.EncodeToString(sum[:])
}

// MemKeyStore is an in-memory KeyStore for tests and for the --create-key dry path.
type MemKeyStore struct {
	mu   sync.Mutex
	keys map[string]*APIKey // by hash
	next int
}

func NewMemKeyStore() *MemKeyStore { return &MemKeyStore{keys: map[string]*APIKey{}} }

// Create registers a key and returns its secret.
func (m *MemKeyStore) Create(name string, rpm int, budget int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	secret, hash := NewSecret()
	m.next++
	m.keys[hash] = &APIKey{ID: m.next, Name: name, RPM: rpm, BudgetTokens: budget}
	return secret
}

func (m *MemKeyStore) Disable(secret string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.keys[HashSecret(secret)]; ok {
		k.Disabled = true
	}
}

func (m *MemKeyStore) Lookup(ctx context.Context, keyHash string) (APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[keyHash]
	if !ok {
		return APIKey{}, ErrKeyNotFound
	}
	return *k, nil
}

func (m *MemKeyStore) AddUsage(ctx context.Context, id int, tokens int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.keys {
		if k.ID == id {
			k.TokensUsed += tokens
			return nil
		}
	}
	return ErrKeyNotFound
}

// SQLKeyStore reads api_keys through the same tiny interfaces the other packages use.
type SQLKeyStore struct {
	Exec  Execer
	Query Queryer
}

type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
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

func (s SQLKeyStore) Lookup(ctx context.Context, keyHash string) (APIKey, error) {
	rows, err := s.Query.Query(ctx,
		`SELECT id, name, rpm, budget_tokens, tokens_used, disabled FROM api_keys WHERE key_hash = $1`, keyHash)
	if err != nil {
		return APIKey{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return APIKey{}, ErrKeyNotFound
	}
	var k APIKey
	if err := rows.Scan(&k.ID, &k.Name, &k.RPM, &k.BudgetTokens, &k.TokensUsed, &k.Disabled); err != nil {
		return APIKey{}, err
	}
	return k, rows.Err()
}

func (s SQLKeyStore) AddUsage(ctx context.Context, id int, tokens int64) error {
	_, err := s.Exec.Exec(ctx, `UPDATE api_keys SET tokens_used = tokens_used + $2 WHERE id = $1`, id, tokens)
	return err
}

// Create inserts a new key and returns the secret (printed once by --create-key).
func (s SQLKeyStore) Create(ctx context.Context, name string, rpm int, budget int64) (string, error) {
	secret, hash := NewSecret()
	_, err := s.Exec.Exec(ctx,
		`INSERT INTO api_keys (name, key_hash, rpm, budget_tokens) VALUES ($1, $2, $3, $4)`, name, hash, rpm, budget)
	if err != nil {
		return "", err
	}
	return secret, nil
}

// RateLimiter is a per-key fixed window of one minute — simple, in-memory, good enough
// for one gateway process. Allow reports whether the request may proceed and, if not,
// how long until the window resets.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[int]*window
	now     func() time.Time
}

type window struct {
	start time.Time
	count int
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{windows: map[int]*window{}, now: time.Now}
}

func (r *RateLimiter) Allow(keyID, rpm int) (bool, time.Duration) {
	if rpm <= 0 {
		return true, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	w, ok := r.windows[keyID]
	if !ok || now.Sub(w.start) >= time.Minute {
		w = &window{start: now}
		r.windows[keyID] = w
	}
	if w.count >= rpm {
		return false, time.Minute - now.Sub(w.start)
	}
	w.count++
	return true, 0
}
