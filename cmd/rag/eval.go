package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/rohitd03/repo-rag/internal/eval"
	"github.com/rohitd03/repo-rag/internal/retrieve"
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
	mode := fs.String("mode", defaultMode, "retrieval mode: vector | hybrid | mmr | hybrid+mmr")
	candidates := fs.Int("candidates", defaultCandidates, "candidate pool depth before fusion/MMR")
	lambda := fs.Float64("lambda", defaultLambda, "MMR relevance vs diversity")
	sweep := fs.Bool("sweep", false, "run every mode and print a comparison table")
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

	st, err := store.Open(ctx, env("RAG_DATABASE_URL", defaultDSN))
	if err != nil {
		return err
	}
	defer st.Close()

	if *sweep {
		return runSweep(ctx, st, g, *corpus, *k, *candidates, *lambda)
	}

	r, err := newRetriever(st, *corpus, *mode, *candidates, *lambda)
	if err != nil {
		return err
	}

	start := time.Now()
	results, err := scoreAll(ctx, r, g, *k)
	if err != nil {
		return err
	}

	if *label == "" {
		*label = fmt.Sprintf("mode=%s k=%d candidates=%d lambda=%.2f",
			*mode, *k, *candidates, *lambda)
	}
	printReport(results, eval.Summarize(results, *k), *label,
		r.Embedder.Model(), time.Since(start), *verbose)
	return nil
}

func scoreAll(ctx context.Context, r *retrieve.Retriever, g *eval.Golden, k int) ([]eval.Result, error) {
	results := make([]eval.Result, 0, len(g.Questions))
	for _, q := range g.Questions {
		hits, err := r.Retrieve(ctx, q.Question, k)
		if err != nil {
			return nil, fmt.Errorf("eval: %s: %w", q.ID, err)
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
	return results, nil
}

// runSweep scores every mode against the same golden set and prints one
// comparison table. Running them in a single process against one index is
// what keeps the comparison honest - nothing varies but the mode.
func runSweep(ctx context.Context, st *store.Store, g *eval.Golden, corpus string, k, candidates int, lambda float64) error {
	type row struct {
		mode string
		s    eval.Summary
		took time.Duration
		res  []eval.Result
	}
	var rows []row

	for _, m := range retrieve.Modes {
		r, err := newRetriever(st, corpus, string(m), candidates, lambda)
		if err != nil {
			return err
		}
		start := time.Now()
		res, err := scoreAll(ctx, r, g, k)
		if err != nil {
			return err
		}
		rows = append(rows, row{string(m), eval.Summarize(res, k), time.Since(start), res})
	}

	fmt.Printf("sweep · k=%d · candidates=%d · lambda=%.2f · %d questions (%d scored)\n\n",
		k, candidates, lambda, len(g.Questions), rows[0].s.Scored)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "MODE\tRECALL@%d\tMRR\tPERFECT\tZERO\tFACTUAL\tCROSS-FILE\tREASONING\tLOCATIONAL\tELAPSED\n", k)
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%.3f\t%.3f\t%d/%d\t%d\t%.3f\t%.3f\t%.3f\t%.3f\t%s\n",
			r.mode, r.s.Recall, r.s.MRR, r.s.PerfectRecall, r.s.Scored, r.s.ZeroRecall,
			r.s.ByCategory["factual"], r.s.ByCategory["cross-file"],
			r.s.ByCategory["reasoning"], r.s.ByCategory["locational"],
			r.took.Round(time.Millisecond))
	}
	w.Flush()

	// Per-question deltas against the vector baseline, so a mode that lifts
	// the mean by breaking one question is visible rather than hidden in
	// the average.
	fmt.Printf("\nper-question recall, vs vector baseline\n\n")
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tCAT")
	for _, r := range rows {
		fmt.Fprintf(w, "\t%s", r.mode)
	}
	fmt.Fprintf(w, "\n")
	for i, q := range g.Questions {
		if q.Negative() {
			continue
		}
		fmt.Fprintf(w, "%s\t%s", q.ID, q.Category)
		base := rows[0].res[i].Recall
		for j, r := range rows {
			cell := fmt.Sprintf("%.2f", r.res[i].Recall)
			if j > 0 {
				switch d := r.res[i].Recall - base; {
				case d > 0.001:
					cell += " +"
				case d < -0.001:
					cell += " -"
				}
			}
			fmt.Fprintf(w, "\t%s", cell)
		}
		fmt.Fprintf(w, "\n")
	}
	w.Flush()

	fmt.Printf("\nscore separation (answerable floor vs negative ceiling)\n")
	for _, r := range rows {
		status := "holds"
		if !r.s.SeparationHolds() {
			status = "BROKEN, they overlap"
		}
		fmt.Printf("  %-12s %.4f vs %.4f  %s\n",
			r.mode, r.s.PosTopScoreMin, r.s.NegTopScoreMax, status)
	}
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
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.4f\t%s\n",
				r.Question.ID, r.Question.Category, "n/a", "n/a", r.TopScore, "(negative)")
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%.2f\t%.2f\t%.4f\t%s\n",
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
	fmt.Printf("  lowest answerable  %.4f\n", s.PosTopScoreMin)
	fmt.Printf("  highest negative   %.4f\n", s.NegTopScoreMax)
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
