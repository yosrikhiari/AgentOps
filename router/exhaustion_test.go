package router

import (
	"errors"
	"testing"
	"time"
)

func TestExhaustionTrackerBenchedExpiry(t *testing.T) {
	tr := NewExhaustionTracker(nil)
	tr.Bench("groq", time.Now().Add(50*time.Millisecond), "quota")
	ok, until := tr.Benched("groq")
	if !ok || until.IsZero() {
		t.Fatal("expected benched")
	}
	time.Sleep(80 * time.Millisecond)
	ok2, _ := tr.Benched("groq")
	if ok2 {
		t.Fatal("expected expired")
	}
	// lazy GC removed the entry
	tr.mu.RLock()
	_, present := tr.benched["groq"]
	tr.mu.RUnlock()
	if present {
		t.Fatal("expected entry removed after expiry")
	}
}

func TestExhaustionTrackerConcurrency(t *testing.T) {
	tr := NewExhaustionTracker(nil)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				tr.Bench("groq", time.Now().Add(time.Second), "quota")
				tr.Benched("groq")
				tr.Snapshot()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func TestIsExhaustion(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&ExhaustionError{Status: 429}, true},
		{&HTTPStatusError{Status: 429}, true},
		{&HTTPStatusError{Status: 402}, true},
		{&HTTPStatusError{Status: 403}, true},
		{&HTTPStatusError{Status: 500}, false},
		{&HTTPStatusError{Status: 404}, false},
		{errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := isExhaustion(c.err); got != c.want {
			t.Errorf("isExhaustion(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestCooldownForDaily(t *testing.T) {
	err := &HTTPStatusError{Status: 429, Body: "Daily request limit reached"}
	d, kind := CooldownFor(err)
	if kind != "quota" {
		t.Fatalf("kind = %q", kind)
	}
	if d < 5*time.Minute || d > 24*time.Hour {
		t.Fatalf("daily cooldown out of range: %s", d)
	}
}

func TestCooldownForRetryAfter(t *testing.T) {
	err := &HTTPStatusError{Status: 429, RetryAfter: 90 * time.Second}
	d, _ := CooldownFor(err)
	if d != 90*time.Second {
		t.Fatalf("got %s, want 90s", d)
	}
}

func TestCooldownForTransientNoHint(t *testing.T) {
	err := &HTTPStatusError{Status: 429, Body: ""}
	d, _ := CooldownFor(err)
	if d != 60*time.Second {
		t.Fatalf("got %s, want 60s", d)
	}
}
