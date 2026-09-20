package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/rohitd03/repo-rag/internal/eval"
	"github.com/rohitd03/repo-rag/internal/store"
)

// runEval scores retrieval against the golden set.
//
// No generation, so this is free, deterministic and fast enough to run after
// every change. That is the point - a retrieval change you cannot measure in
// under a minute is a change you will not measure.
func runEval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	corpus := fs.String("corpus", "", "corpus name (required)")
	goldens := fs.String("goldens", "eval/goldens/cdc-pipeline.yaml", "path to the golden set")
	k := fs.Int("k", 5, "how many chunks to retrieve")
	verbose := fs.Bool("v", false, "print per-question detail")
	label := fs.String("label", "", "label for this run, printed in the header")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *corpus == "" {
		fs.Usage()
		return fmt.Errorf("eval: -corpus is required")
	}

	g, err := eval.Load(*goldens)
	if err != nil {
		return err
	}

	emb := newEmbedder()
	st, err := store.Open(ctx, env("RAG_DATABASE_URL", defaultDSN))
	if err != nil {
		return err
	}
	defer st.Close()

	start := time.Now()
	results := make([]eval.Result, 0, len(g.Questions))

	for _, q := range g.Questions {
		vecs, err := emb.Embed(ctx, []string{q.Question})
		if err != nil {
			return fmt.Errorf("eval: embed %s: %w", q.ID, err)
		}
		hits, err := st.Search(ctx, *corpus, emb.Model(), vecs[0], *k)
		if err != nil {
			return fmt.Errorf("eval: search %s: %w", q.ID, err)
		}

		paths := make([]string, len(hits))
		for i, h := range hits {
			paths[i] = h.SourcePath
		}
		top := 0.0
		if len(hits) > 0 {
			top = hits[0].Score
		}
		results = append(results, eval.Score(q, paths, top))
	}

	printReport(results, eval.Summarize(results, *k), *label, emb.Model(), time.Since(start), *verbose)
	return nil
}

func printReport(results []eval.Result, s eval.Summary, label, embedModel string, took time.Duration, verbose bool) {
	if label != "" {
		fmt.Printf("run         %s\n", label)
	}
	fmt.Printf("embed model %s\n", embedModel)
	fmt.Printf("k           %d\n", s.K)
	fmt.Printf("questions   %d scored, %d negative (excluded from recall)\n", s.Scored, s.Negatives)
	fmt.Printf("elapsed     %s\n\n", took.Round(time.Millisecond))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tCAT\tRECALL\tRR\tTOP\tMISSING\n")
	for _, r := range results {
		missing := "-"
		if len(r.Missing) > 0 {
			missing = fmt.Sprintf("%v", r.Missing)
		}
		if r.Question.Negative() {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.3f\t%s\n",
				r.Question.ID, r.Question.Category, "n/a", "n/a", r.TopScore, "(negative)")
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%.2f\t%.2f\t%.3f\t%s\n",
			r.Question.ID, r.Question.Category, r.Recall, r.RR, r.TopScore, missing)
	}
	w.Flush()

	fmt.Printf("\nrecall@%d        %.3f\n", s.K, s.Recall)
	fmt.Printf("MRR             %.3f\n", s.MRR)
	fmt.Printf("perfect recall  %d/%d\n", s.PerfectRecall, s.Scored)
	fmt.Printf("zero recall     %d/%d\n", s.ZeroRecall, s.Scored)

	fmt.Printf("\nrecall by category\n")
	for _, c := range s.Categories() {
		fmt.Printf("  %-12s %.3f\n", c, s.ByCategory[c])
	}

	// Tracks the Phase 1 hypothesis that unanswerable questions score
	// measurably lower, which would make a similarity floor a cheap refusal
	// signal. Reported every run so it can be falsified rather than assumed.
	fmt.Printf("\nscore separation\n")
	fmt.Printf("  lowest answerable  %.3f\n", s.PosTopScoreMin)
	fmt.Printf("  highest negative   %.3f\n", s.NegTopScoreMax)
	if s.SeparationHolds() {
		fmt.Printf("  holds: a floor between them would reject every negative\n")
	} else {
		fmt.Printf("  BROKEN: they overlap, a similarity floor would misfire\n")
	}

	if verbose {
		fmt.Printf("\nper-question detail\n")
		for _, r := range results {
			fmt.Printf("\n%s [%s] %s\n", r.Question.ID, r.Question.Category, r.Question.Question)
			for i, p := range r.Retrieved {
				mark := " "
				for _, f := range r.Found {
					if f == p {
						mark = "*"
					}
				}
				fmt.Printf("   %s %d. %s\n", mark, i+1, p)
			}
			if len(r.Missing) > 0 {
				fmt.Printf("     missing: %v\n", r.Missing)
			}
		}
	}
}
