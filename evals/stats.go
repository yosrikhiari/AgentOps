package evals

import "math"

// WelchPValue is the two-sided p-value of Welch's t-test on two samples of
// per-pair faithfulness scores, with the t statistic referred to a standard
// normal (valid at the n≥100 significance gate Track F/L/M share; below it
// callers report "tied" without asking). It returns 1 when either sample has
// fewer than 2 observations or zero pooled variance — no evidence either way.
func WelchPValue(a, b []float64) float64 {
	ma, va, na := meanVar(a)
	mb, vb, nb := meanVar(b)
	if na < 2 || nb < 2 {
		return 1
	}
	se := math.Sqrt(va/float64(na) + vb/float64(nb))
	if se == 0 {
		// No spread at all: identical means are tied, differing means are
		// perfectly separated.
		if ma == mb {
			return 1
		}
		return 0
	}
	t := math.Abs(ma-mb) / se
	return 2 * (1 - normalCDF(t))
}

func meanVar(xs []float64) (mean, variance float64, n int) {
	n = len(xs)
	if n == 0 {
		return 0, 0, 0
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(n)
	if n < 2 {
		return mean, 0, n
	}
	for _, x := range xs {
		d := x - mean
		variance += d * d
	}
	return mean, variance / float64(n-1), n
}

// normalCDF is Abramowitz–Stegun 7.1.26 (|ε| < 7.5e-8): plenty for a 0.05 gate.
func normalCDF(x float64) float64 {
	t := 1 / (1 + 0.2316419*math.Abs(x))
	poly := t * (0.319381530 + t*(-0.356563782+t*(1.781477937+t*(-1.821255978+t*1.330274429))))
	p := 1 - math.Exp(-x*x/2)/math.Sqrt(2*math.Pi)*poly
	if x < 0 {
		return 1 - p
	}
	return p
}
