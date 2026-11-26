package router

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestStreamLimiter_AcquireRelease(t *testing.T) {
	sl := NewStreamLimiter(2)
	if sl.Capacity() != 2 {
		t.Fatalf("capacity: got %d, want 2", sl.Capacity())
	}
	if err := sl.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if sl.InUse() != 1 {
		t.Fatalf("inUse: got %d, want 1", sl.InUse())
	}
	if err := sl.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if sl.InUse() != 2 {
		t.Fatalf("inUse: got %d, want 2", sl.InUse())
	}
	// third acquire should block; use a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := sl.Acquire(ctx)
	if err == nil {
		t.Fatalf("expected error on third acquire, got nil")
	}
	sl.Release()
	sl.Release()
	if sl.InUse() != 0 {
		t.Fatalf("inUse after release: got %d, want 0", sl.InUse())
	}
}

func TestStreamLimiter_EnvVar(t *testing.T) {
	t.Setenv("MAX_CONCURRENT_STREAMS", "7")
	sl := NewStreamLimiter(0)
	if sl.Capacity() != 7 {
		t.Fatalf("capacity from env: got %d, want 7", sl.Capacity())
	}
}

func TestStreamLimiter_Concurrent(t *testing.T) {
	sl := NewStreamLimiter(4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sl.Acquire(context.Background()); err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			time.Sleep(10 * time.Millisecond)
			sl.Release()
		}()
	}
	wg.Wait()
	if sl.InUse() != 0 {
		t.Fatalf("inUse: got %d, want 0", sl.InUse())
	}
}
