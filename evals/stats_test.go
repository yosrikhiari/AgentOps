package evals

// Track L — verdict statistics (TDD RED: WelchPValue does not exist yet).

import (
	"testing"
)

func TestWelchPValueSeparated(t *testing.T) {
	a := make([]float64, 120)
	b := make([]float64, 120)
	for i := range a {
		a[i] = 1.0
		b[i] = 0.0
	}
	if p := WelchPValue(a, b); p >= 0.05 {
		t.Fatalf("perfectly separated means must be significant, p=%v", p)
	}
}

func TestWelchPValueIdentical(t *testing.T) {
	a := make([]float64, 72)
	b := make([]float64, 72)
	for i := range a {
		if i%2 == 0 {
			a[i], b[i] = 1.0, 1.0
		} else {
			a[i], b[i] = 0.0, 0.0
		}
	}
	if p := WelchPValue(a, b); p < 0.05 {
		t.Fatalf("identical samples must not be significant, p=%v", p)
	}
}

func TestWelchPValueNeedsData(t *testing.T) {
	if p := WelchPValue(nil, []float64{1}); p != 1 {
		t.Fatalf("empty sample must read p=1 (no evidence), got %v", p)
	}
}
