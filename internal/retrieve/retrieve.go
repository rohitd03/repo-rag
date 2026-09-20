// Package retrieve selects which chunks go into the prompt.
//
// Separating this from the store keeps each strategy switchable at runtime,
// which is what makes them comparable: the eval harness runs the same golden
// set through every mode and the only thing that changed is the mode.
package retrieve

import (
	"context"
	"fmt"
	"math"

	"github.com/rohitd03/repo-rag/internal/embed"
	"github.com/rohitd03/repo-rag/internal/store"
)

// Mode selects a retrieval strategy.
type Mode string

const (
	// ModeVector is plain top-k cosine similarity. The Phase 1 baseline.
	ModeVector Mode = "vector"

	// ModeHybrid fuses vector and full-text rankings with RRF.
	ModeHybrid Mode = "hybrid"

	// ModeMMR is vector retrieval followed by Maximal Marginal Relevance,
	// which trades a little relevance for diversity.
	ModeMMR Mode = "mmr"

	// ModeHybridMMR fuses first, then diversifies the fused list.
	ModeHybridMMR Mode = "hybrid+mmr"
)

// Modes lists every mode, in the order the eval harness sweeps them.
var Modes = []Mode{ModeVector, ModeHybrid, ModeMMR, ModeHybridMMR}

// ParseMode validates a mode name.
func ParseMode(s string) (Mode, error) {
	for _, m := range Modes {
		if Mode(s) == m {
			return m, nil
		}
	}
	return "", fmt.Errorf("unknown retrieval mode %q: want one of %v", s, Modes)
}

// Retriever runs one retrieval strategy against a corpus.
type Retriever struct {
	Store    *store.Store
	Embedder embed.Embedder
	Corpus   string
	Mode     Mode

	// Candidates is how deep to go before fusing or diversifying. Ignored
	// by ModeVector.
	Candidates int

	// Lambda balances MMR: 1.0 is pure relevance (equivalent to no MMR),
	// 0.0 is pure diversity. Ignored unless the mode uses MMR.
	Lambda float64
}

// Retrieve returns the k chunks to put in the prompt.
func (r *Retriever) Retrieve(ctx context.Context, question string, k int) ([]store.Hit, error) {
	vecs, err := r.Embedder.Embed(ctx, []string{question})
	if err != nil {
		return nil, err
	}
	qvec := vecs[0]
	model := r.Embedder.Model()

	candidates := r.Candidates
	if candidates < k {
		candidates = k * 5
	}

	switch r.Mode {
	case ModeVector:
		return r.Store.Search(ctx, r.Corpus, model, qvec, k)

	case ModeHybrid:
		return r.Store.SearchHybrid(ctx, r.Corpus, model, qvec, question, k, candidates)

	case ModeMMR:
		hits, vecs, err := r.Store.SearchCandidates(ctx, r.Corpus, model, qvec, candidates)
		if err != nil {
			return nil, err
		}
		rel := make([]float64, len(hits))
		for i := range hits {
			rel[i] = cosine(vecs[i], qvec)
		}
		return MMR(hits, vecs, rel, k, r.Lambda), nil

	case ModeHybridMMR:
		// Fuse first so keyword-only matches can enter the pool, then
		// diversify. Running MMR before fusion would discard exactly the
		// rare-identifier hits that hybrid search exists to recover.
		hits, err := r.Store.SearchHybrid(ctx, r.Corpus, model, qvec, question, candidates, candidates)
		if err != nil {
			return nil, err
		}
		if len(hits) <= k {
			return hits, nil
		}
		hitVecs, err := r.Store.EmbeddingsFor(ctx, r.Corpus, model, hits)
		if err != nil {
			return nil, err
		}
		// Relevance comes from the fused RRF score, not from recomputed
		// cosine similarity. Using cosine here was a real bug: it threw
		// away the fusion and re-sorted by the vector ranking alone, so a
		// chunk that only keyword search found — exactly what hybrid
		// exists to recover — was demoted right back out of the results.
		// It made hybrid+mmr score identically to plain mmr on 18 of 19
		// questions, and lose hybrid's q14 gain on the 19th.
		rel := make([]float64, len(hits))
		for i, h := range hits {
			rel[i] = h.Score
		}
		return MMR(hits, hitVecs, rel, k, r.Lambda), nil
	}

	return nil, fmt.Errorf("retrieve: unhandled mode %q", r.Mode)
}

// MMR selects k hits balancing relevance to the query against dissimilarity
// to what has already been picked.
//
// The problem it solves is visible in golden question q12, which asks for the
// difference between two lag metrics: plain top-k returns five chunks about
// the *same* metric, because they are all individually the best match. MMR
// penalises a candidate for resembling an already-selected chunk, so the
// second concept gets a place.
//
// relevance is supplied by the caller rather than recomputed here, so the
// ranking that produced the candidates is the ranking MMR diversifies. It is
// min-max normalised to [0,1] internally: cosine similarity runs ~0.5-0.9
// while RRF scores run ~0.016-0.033, and without normalisation the same
// lambda would mean something completely different in each mode.
//
// lambda weights the two terms. At 1.0 this degenerates to plain top-k.
func MMR(hits []store.Hit, vecs [][]float32, relevance []float64, k int, lambda float64) []store.Hit {
	if lambda <= 0 || lambda > 1 {
		lambda = 0.7
	}
	if len(hits) == 0 || k <= 0 {
		return nil
	}
	if len(hits) <= k {
		return hits
	}

	relevance = normalize01(relevance)

	selected := make([]int, 0, k)
	taken := make([]bool, len(hits))

	for len(selected) < k {
		best, bestScore := -1, math.Inf(-1)

		for i := range hits {
			if taken[i] {
				continue
			}

			maxSim := 0.0
			for _, j := range selected {
				if s := cosine(vecs[i], vecs[j]); s > maxSim {
					maxSim = s
				}
			}

			score := lambda*relevance[i] - (1-lambda)*maxSim
			if score > bestScore {
				best, bestScore = i, score
			}
		}

		if best < 0 {
			break
		}
		taken[best] = true
		selected = append(selected, best)
	}

	out := make([]store.Hit, 0, len(selected))
	for _, i := range selected {
		out = append(out, hits[i])
	}
	return out
}

// normalize01 min-max scales a slice into [0,1]. An all-equal slice maps to
// all-1.0, which makes MMR fall back to pure diversity - correct, since a
// ranker that scores everything the same has no preference to honour.
func normalize01(xs []float64) []float64 {
	if len(xs) == 0 {
		return xs
	}
	lo, hi := xs[0], xs[0]
	for _, x := range xs {
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	out := make([]float64, len(xs))
	if hi == lo {
		for i := range out {
			out[i] = 1
		}
		return out
	}
	for i, x := range xs {
		out[i] = (x - lo) / (hi - lo)
	}
	return out
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
