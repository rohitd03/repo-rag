package eval

import (
	"sort"
	"strings"
)

// Result is one question's retrieval score.
type Result struct {
	Question Question

	// Retrieved holds the source paths returned, in rank order, deduplicated.
	// Chunks are the retrieval unit but sources are what the golden set
	// names, so several chunks from one file collapse to a single entry at
	// its best rank.
	Retrieved []string

	Found   []string // expected sources that were retrieved
	Missing []string // expected sources that were not

	// Recall is Found / expected. Undefined for negative questions.
	Recall float64

	// RR is the reciprocal rank of the first expected source, 0 if none was
	// retrieved. Recall says whether the right chunk was in the list; RR
	// says how near the top it was, which matters because the model reads
	// the top of the context most reliably.
	RR float64

	// TopScore is the best similarity in the result set. For negative
	// questions this is the signal of interest - if irrelevant results
	// score reliably lower, a floor can detect unanswerable questions
	// before generation runs.
	TopScore float64
}

// Score compares one question's retrieved sources against its expected ones.
//
// retrievedPaths must be in rank order, best first, and may contain repeats.
func Score(q Question, retrievedPaths []string, topScore float64) Result {
	r := Result{Question: q, TopScore: topScore}

	// Best rank per source path.
	rankOf := make(map[string]int, len(retrievedPaths))
	for i, p := range retrievedPaths {
		if _, seen := rankOf[p]; !seen {
			rankOf[p] = i + 1
			r.Retrieved = append(r.Retrieved, p)
		}
	}

	if q.Negative() {
		return r
	}

	best := 0
	for _, want := range q.ExpectedSources {
		rank, ok := rankOf[normalize(want)]
		if !ok {
			r.Missing = append(r.Missing, want)
			continue
		}
		r.Found = append(r.Found, want)
		if best == 0 || rank < best {
			best = rank
		}
	}

	r.Recall = float64(len(r.Found)) / float64(len(q.ExpectedSources))
	if best > 0 {
		r.RR = 1 / float64(best)
	}
	return r
}

func normalize(p string) string {
	return strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "./")
}

// Summary aggregates results across a run.
type Summary struct {
	K              int
	Scored         int // answerable questions, the denominator for Recall/MRR
	Negatives      int
	Recall         float64 // mean recall@k over answerable questions
	MRR            float64 // mean reciprocal rank over answerable questions
	PerfectRecall  int     // answerable questions where every source was found
	ZeroRecall     int     // answerable questions where none was found
	ByCategory     map[string]float64
	NegTopScoreMax float64 // highest top-score among negative questions
	PosTopScoreMin float64 // lowest top-score among answerable questions
}

// Summarize aggregates per-question results.
func Summarize(results []Result, k int) Summary {
	s := Summary{K: k, ByCategory: map[string]float64{}}

	catSum := map[string]float64{}
	catN := map[string]int{}
	s.PosTopScoreMin = 1.0

	for _, r := range results {
		if r.Question.Negative() {
			s.Negatives++
			if r.TopScore > s.NegTopScoreMax {
				s.NegTopScoreMax = r.TopScore
			}
			continue
		}

		s.Scored++
		s.Recall += r.Recall
		s.MRR += r.RR
		switch {
		case r.Recall == 1:
			s.PerfectRecall++
		case r.Recall == 0:
			s.ZeroRecall++
		}
		if r.TopScore < s.PosTopScoreMin {
			s.PosTopScoreMin = r.TopScore
		}

		catSum[r.Question.Category] += r.Recall
		catN[r.Question.Category]++
	}

	if s.Scored > 0 {
		s.Recall /= float64(s.Scored)
		s.MRR /= float64(s.Scored)
	}
	for c, sum := range catSum {
		s.ByCategory[c] = sum / float64(catN[c])
	}
	return s
}

// Categories returns the summary's categories in a stable order.
func (s Summary) Categories() []string {
	out := make([]string, 0, len(s.ByCategory))
	for c := range s.ByCategory {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// SeparationHolds reports whether every answerable question scored higher
// than every negative one.
//
// When true, a similarity floor between the two could reject unanswerable
// questions before generation. It is a hypothesis to track across runs, not
// a mechanism to rely on - one overlapping pair falsifies it.
func (s Summary) SeparationHolds() bool {
	return s.Negatives > 0 && s.Scored > 0 && s.PosTopScoreMin > s.NegTopScoreMax
}
