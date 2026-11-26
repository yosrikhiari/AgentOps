package router

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
)

// StreamLimiter caps concurrent streaming requests to prevent resource
// exhaustion. It is a semaphore with a configurable capacity.
type StreamLimiter struct {
	mu       sync.Mutex
	sem      chan struct{}
	capacity int
	inUse    int
}

// NewStreamLimiter creates a limiter with the given capacity. If capacity <= 0,
// it reads MAX_CONCURRENT_STREAMS from the environment (default 10).
func NewStreamLimiter(capacity int) *StreamLimiter {
	if capacity <= 0 {
		capacity = 10
		if v := os.Getenv("MAX_CONCURRENT_STREAMS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				capacity = n
			}
		}
	}
	return &StreamLimiter{
		sem:      make(chan struct{}, capacity),
		capacity: capacity,
	}
}

// Capacity returns the configured capacity.
func (sl *StreamLimiter) Capacity() int { return sl.capacity }

// Acquire blocks until a slot is available or the context is cancelled.
// Returns an error if the context is cancelled or the limiter is full and
// the wait exceeds a reasonable timeout.
func (sl *StreamLimiter) Acquire(ctx context.Context) error {
	select {
	case sl.sem <- struct{}{}:
		sl.mu.Lock()
		sl.inUse++
		sl.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees a slot.
func (sl *StreamLimiter) Release() {
	select {
	case <-sl.sem:
	default:
	}
	sl.mu.Lock()
	if sl.inUse > 0 {
		sl.inUse--
	}
	sl.mu.Unlock()
}

// InUse returns the number of currently held slots.
func (sl *StreamLimiter) InUse() int {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	return sl.inUse
}

// ErrTooManyStreams is returned when the stream limit is reached.
var ErrTooManyStreams = fmt.Errorf("too many concurrent streams")
