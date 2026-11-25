package router

import (
	"context"
	"sync"
	"testing"
	"time"
)

// blockingKeyStore is a KeyStore whose AddUsage blocks until release is closed.
type blockingKeyStore struct {
	mu      sync.Mutex
	release chan struct{}
	calls   int
}

func (b *blockingKeyStore) Lookup(ctx context.Context, keyHash string) (APIKey, error) {
	return APIKey{}, ErrKeyNotFound
}
func (b *blockingKeyStore) AddUsage(ctx context.Context, id int, tokens int64) error {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	<-b.release
	return nil
}

// chargeKey enqueues off the request path; a nil key or nil store is a no-op.
func TestChargeKeyNoopWhenNil(t *testing.T) {
	srv := NewServer("fast", "quality", nil)
	srv.chargeKey(nil, Usage{TotalTokens: 10})
	if srv.ChargeDropped() != 0 {
		t.Fatalf("expected 0 dropped, got %d", srv.ChargeDropped())
	}
	if srv.chargeCh != nil {
		t.Fatal("worker should not start when Keys is nil")
	}
}

// chargeKey hands the update to a single worker instead of spawning a goroutine.
func TestChargeKeyEnqueuesAndRecordsUsage(t *testing.T) {
	srv := NewServer("fast", "quality", nil)
	ks := NewMemKeyStore()
	secret := ks.Create("alice", 60, 1000)
	key, err := ks.Lookup(context.Background(), HashSecret(secret))
	if err != nil {
		t.Fatal(err)
	}
	srv.Keys = ks

	srv.chargeKey(&key, Usage{TotalTokens: 42})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ks.mu.Lock()
		var used int64
		for _, k := range ks.keys {
			if k.ID == key.ID {
				used = k.TokensUsed
			}
		}
		ks.mu.Unlock()
		if used == 42 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("usage was not recorded by the charge worker")
}

// The queue is bounded and drops (counted) instead of blocking the caller.
func TestChargeKeyDropsWhenQueueFull(t *testing.T) {
	srv := NewServer("fast", "quality", nil)
	ks := &blockingKeyStore{release: make(chan struct{})}
	srv.Keys = ks
	key := &APIKey{ID: 1, Name: "bob"}

	// Block the worker so the queue fills.
	// First enqueue starts the worker, which immediately blocks in AddUsage.
	srv.chargeKey(key, Usage{TotalTokens: 1})
	// Give the worker time to pick it up and block.
	time.Sleep(50 * time.Millisecond)

	// Fill the rest of the 1024-deep queue.
	for i := 0; i < 1024; i++ {
		srv.chargeKey(key, Usage{TotalTokens: 1})
	}
	// One more should drop.
	srv.chargeKey(key, Usage{TotalTokens: 1})
	if srv.ChargeDropped() == 0 {
		t.Fatal("expected a dropped update when the queue is full")
	}
	close(ks.release)
	time.Sleep(50 * time.Millisecond)
}
