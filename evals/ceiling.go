package evals

// RouteCeiling answers the question a routing experiment must answer before it starts:
// how much is there to win? Two numbers, both about the roster rather than the router.
//
//   - Beta is the co-failure rate: the share of questions on which EVERY rung scores below
//     the floor. No policy that returns one model's answer can beat 1-beta, so a high beta
//     means the bottleneck is the roster, not the classifier.
//   - FastOptimal is the share of questions where the fast rung is at least as good as every
//     other rung. If most questions are already answered best by the cheapest model, there
//     is little for a router to improve.
//
// Both are computed only over questions every rung answered: comparing a model on the
// questions it happened to answer is how routing gains get invented.

import "sort"

// RungOutcome is one model's result on one question. A retrieval miss is a failure to
// answer, never a zero score to be averaged in.
type RungOutcome struct {
	Question     string
	Model        string
	Faithfulness float64
	Miss         bool
}

type Ceiling struct {
	Rungs       []string
	Questions   int
	Beta        float64
	FastOptimal float64
	MeanFaith   map[string]float64
}

// solvable is the floor for "this answer is usable". Half the claims supported is a
// deliberate, stated threshold - a routing decision that ships a mostly-unsupported answer
// is not a win, however cheap it was.
const solvableFloor = 0.5

func RouteCeiling(outcomes []RungOutcome, fastModel string) Ceiling {
	c := Ceiling{MeanFaith: map[string]float64{}}
	models := map[string]bool{}
	perQuestion := map[string]map[string]RungOutcome{}
	for _, o := range outcomes {
		models[o.Model] = true
		if perQuestion[o.Question] == nil {
			perQuestion[o.Question] = map[string]RungOutcome{}
		}
		perQuestion[o.Question][o.Model] = o
	}
	for m := range models {
		c.Rungs = append(c.Rungs, m)
	}
	sort.Strings(c.Rungs)
	if len(c.Rungs) < 2 {
		return c
	}
	var sums = map[string]float64{}
	var counts = map[string]int{}
	beta, fastOpt, questions := 0, 0, 0
	for q, byModel := range perQuestion {
		if len(byModel) != len(c.Rungs) {
			continue // a question not every rung answered is not comparable
		}
		questions++
		allFail, fastBest := true, true
		var fastFaith float64
		if o, ok := byModel[fastModel]; ok {
			fastFaith = o.Faithfulness
			if o.Miss {
				fastFaith = 0
			}
		}
		for m, o := range byModel {
			f := o.Faithfulness
			if o.Miss {
				f = 0
			}
			if f >= solvableFloor {
				allFail = false
			}
			if m != fastModel && f > fastFaith {
				fastBest = false
			}
			if !o.Miss {
				sums[m] += o.Faithfulness
				counts[m]++
			}
		}
		if allFail {
			beta++
		}
		if fastBest {
			fastOpt++
		}
		_ = q
	}
	c.Questions = questions
	if questions == 0 {
		return c
	}
	c.Beta = float64(beta) / float64(questions)
	c.FastOptimal = float64(fastOpt) / float64(questions)
	for m := range models {
		if counts[m] > 0 {
			c.MeanFaith[m] = sums[m] / float64(counts[m])
		}
	}
	return c
}
