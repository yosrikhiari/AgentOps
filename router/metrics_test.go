package router

import (
	"strings"
	"testing"
	"time"
)

func TestMetricsExtraIsAppended(t *testing.T) {
	m := NewMetrics()
	m.Extra = func() string { return "gpu_up 1\n" }
	if !strings.Contains(m.Expose(), "gpu_up 1") {
		t.Fatalf("extra not appended:\n%s", m.Expose())
	}
}

func TestMetricsExtraNilIsSafe(t *testing.T) {
	if !strings.Contains(NewMetrics().Expose(), "router_requests_total") {
		t.Fatal("base exposition missing")
	}
}

// TestMetricsExtraRunsOutsideTheLock pins the contract that Expose never calls a
// foreign callback while holding m.mu: Extra here takes m.mu itself, so an
// implementation that appends inside the lock deadlocks and trips the timeout.
func TestMetricsExtraRunsOutsideTheLock(t *testing.T) {
	m := NewMetrics()
	m.Extra = func() string {
		m.SetBackendUp("probe", true)
		return "gpu_up 1\n"
	}
	done := make(chan string, 1)
	go func() { done <- m.Expose() }()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	select {
	case got := <-done:
		if !strings.Contains(got, "gpu_up 1") {
			t.Fatalf("extra not appended:\n%s", got)
		}
	case <-timeout.C:
		t.Fatal("Expose called Extra while holding the lock: deadlock")
	}
}
