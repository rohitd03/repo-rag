// Package bench defines the arms of the Phase 4 comparison.
//
// An arm is one way of choosing what context to hand the model. Having the
// real system and its controls behind the same interface is what makes the
// comparison trustworthy: the generator, the judge and the scoring are
// identical across arms, and the only thing that varies is context selection.
package bench

import (
	"context"
	"fmt"
	"math/rand"
	"sort"

	"github.com/rohitd03/repo-rag/internal/retrieve"
	"github.com/rohitd03/repo-rag/internal/store"
)

// Arm selects the context for one question.
type Arm interface {
	// Name identifies the arm in the results table.
	Name() string

	// Context returns the chunks to put in the prompt. An empty slice is
	// valid and means "answer with no context at all".
	Context(ctx context.Context, question string, k int) ([]store.Hit, error)

	// Describe is one line explaining what the arm is controlling for.
	Describe() string
}

// ---------------------------------------------------------------- no context

// NoContext answers from the model's own priors, with nothing retrieved.
//
// The control that catches a question answered from general knowledge about
// how CDC pipelines are usually built rather than from this repository. If an
// arm with real context cannot beat this, the retrieval contributed nothing.
type NoContext struct{}

func (NoContext) Name() string { return "no-context" }

func (NoContext) Describe() string {
	return "no retrieval at all - measures what the model knows from priors"
}

func (NoContext) Context(context.Context, string, int) ([]store.Hit, error) {
	return nil, nil
}

// ------------------------------------------------------------------- random k

// RandomK returns k chunks chosen at random from the corpus.
//
// The sharpest control in the set. It holds the amount of context constant
// and removes only the ranking, so the gap between this and the real
// retriever is exactly what ranking is worth. A system that barely beats
// random chunks has an expensive no-op in the middle of it.
type RandomK struct {
	Store  *store.Store
	Corpus string
	Model  string
	Seed   int64

	all []store.Hit
}

func (r *RandomK) Name() string { return "random-k" }

func (r *RandomK) Describe() string {
	return "k random chunks - isolates how much the ranking is worth"
}

func (r *RandomK) Context(ctx context.Context, question string, k int) ([]store.Hit, error) {
	if r.all == nil {
		all, err := r.Store.AllChunks(ctx, r.Corpus, r.Model)
		if err != nil {
			return nil, err
		}
		r.all = all
	}
	if len(r.all) == 0 {
		return nil, fmt.Errorf("bench: corpus %q is empty", r.Corpus)
	}

	// Seeded per question, so the same question always draws the same
	// chunks. An unseeded draw would make this arm's score move between
	// runs and reintroduce exactly the noise Phase 3 removed.
	rng := rand.New(rand.NewSource(r.Seed + int64(len(question))))
	idx := rng.Perm(len(r.all))
	if k > len(idx) {
		k = len(idx)
	}

	picked := idx[:k]
	sort.Ints(picked)
	out := make([]store.Hit, 0, k)
	for _, i := range picked {
		out = append(out, r.all[i])
	}
	return out, nil
}

// ------------------------------------------------------- truncated corpus

// TruncatedCorpus fills the context window with the repository in file
// order, ignoring the question entirely.
//
// This is the honest local version of "just paste the whole repo in". The
// corpus is roughly 63k tokens and these models hold 32k, so the whole repo
// does not fit - which is itself the finding. Phase 0 recorded that the
// corpus fits in a 200k frontier context window and concluded retrieval was
// only an optimization. For the models actually in use, it is not optional.
type TruncatedCorpus struct {
	Store  *store.Store
	Corpus string
	Model  string

	// BudgetBytes is how much context to fill. Roughly 4 bytes per token,
	// left well under the 32k window so the question, system prompt and
	// answer still fit.
	BudgetBytes int

	all []store.Hit
}

func (t *TruncatedCorpus) Name() string { return "truncated-corpus" }

func (t *TruncatedCorpus) Describe() string {
	return "repo in file order until the context window is full - the no-RAG baseline"
}

func (t *TruncatedCorpus) Context(ctx context.Context, _ string, _ int) ([]store.Hit, error) {
	if t.all == nil {
		all, err := t.Store.AllChunks(ctx, t.Corpus, t.Model)
		if err != nil {
			return nil, err
		}
		t.all = all
	}

	budget := t.BudgetBytes
	if budget <= 0 {
		budget = 90000
	}

	var (
		out  []store.Hit
		used int
	)
	for _, h := range t.all {
		n := len(h.Content)
		if used+n > budget {
			break
		}
		out = append(out, h)
		used += n
	}
	return out, nil
}

// Coverage reports how much of the corpus the budget actually admits. The
// shortfall is the point of this arm.
func (t *TruncatedCorpus) Coverage(ctx context.Context) (included, total int, err error) {
	hits, err := t.Context(ctx, "", 0)
	if err != nil {
		return 0, 0, err
	}
	return len(hits), len(t.all), nil
}

// ------------------------------------------------------------------- rag

// RAG is the real system.
type RAG struct {
	Retriever *retrieve.Retriever
	Label     string
}

func (r *RAG) Name() string { return r.Label }

func (r *RAG) Describe() string {
	return fmt.Sprintf("hybrid retrieval, mode=%s", r.Retriever.Mode)
}

func (r *RAG) Context(ctx context.Context, question string, k int) ([]store.Hit, error) {
	return r.Retriever.Retrieve(ctx, question, k)
}
