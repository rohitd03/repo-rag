package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rohitd03/repo-rag/internal/answer"
	"github.com/rohitd03/repo-rag/internal/bench"
	"github.com/rohitd03/repo-rag/internal/eval"
	"github.com/rohitd03/repo-rag/internal/judge"
	"github.com/rohitd03/repo-rag/internal/store"
)

// armResult is one arm's score over the whole golden set.
type armResult struct {
	Arm          string              `json:"arm"`
	Describe     string              `json:"describe"`
	GenModel     string              `json:"gen_model"`
	Results      []eval.AnswerResult `json:"results"`
	Summary      eval.AnswerSummary  `json:"summary"`
	MeanCtxBytes int                 `json:"mean_context_bytes"`
	Elapsed      time.Duration       `json:"elapsed"`
}

// runBench compares the real system against its controls.
//
// Every arm shares the generator, the judge, the prompt and the scoring; the
// only thing that varies is how the context was chosen. Without the controls,
// "recall went up" is a claim about a number rather than about whether the
// retrieval does anything useful.
func runBench(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	corpus := fs.String("corpus", "", "corpus name (required)")
	goldens := fs.String("goldens", "eval/goldens/cdc-pipeline.yaml", "path to the golden set")
	k := fs.Int("k", 10, "chunks per question for the retrieval and random arms")
	mode := fs.String("mode", defaultMode, "retrieval mode for the RAG arms")
	candidates := fs.Int("candidates", defaultCandidates, "candidate pool depth")
	lambda := fs.Float64("lambda", defaultLambda, "MMR relevance vs diversity")
	genModels := fs.String("gen-models", "qwen2.5-coder:3b", "comma-separated generator models for the RAG arms")
	budget := fs.Int("truncate-bytes", 90000, "context budget for the truncated-corpus arm, in bytes")
	only := fs.String("only", "", "comma-separated arm names, for a partial run")
	out := fs.String("out", "", "write full results to this JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *corpus == "" {
		fs.Usage()
		return fmt.Errorf("bench: -corpus is required")
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

	emb := newEmbedder()
	j := judge.NewOllama(env("RAG_OLLAMA_URL", defaultOllamaURL), env("RAG_JUDGE_MODEL", defaultJudgeModel))

	// Build the arms. Controls first, so a run interrupted partway still
	// has the baselines it needs for the comparison to mean anything.
	type armSpec struct {
		arm      bench.Arm
		genModel string
	}
	var specs []armSpec

	genModel := strings.Split(*genModels, ",")[0]
	specs = append(specs,
		armSpec{bench.NoContext{}, genModel},
		armSpec{&bench.RandomK{Store: st, Corpus: *corpus, Model: emb.Model(), Seed: 42}, genModel},
		armSpec{&bench.TruncatedCorpus{
			Store: st, Corpus: *corpus, Model: emb.Model(), BudgetBytes: *budget,
		}, genModel},
	)

	for _, gm := range strings.Split(*genModels, ",") {
		gm = strings.TrimSpace(gm)
		if gm == "" {
			continue
		}
		r, err := newRetriever(st, *corpus, *mode, *candidates, *lambda)
		if err != nil {
			return err
		}
		specs = append(specs, armSpec{
			&bench.RAG{Retriever: r, Label: "rag/" + shortModel(gm)},
			gm,
		})
	}

	filter := map[string]bool{}
	for _, name := range strings.Split(*only, ",") {
		if name = strings.TrimSpace(name); name != "" {
			filter[name] = true
		}
	}

	var armResults []armResult

	for _, spec := range specs {
		if len(filter) > 0 && !filter[spec.arm.Name()] {
			continue
		}

		gen := answer.NewOllama(env("RAG_OLLAMA_URL", defaultOllamaURL), spec.genModel)

		start := time.Now()
		var (
			results  []eval.AnswerResult
			ctxBytes int
		)

		for _, q := range g.Questions {
			hits, err := spec.arm.Context(ctx, q.Question, *k)
			if err != nil {
				return fmt.Errorf("bench: %s: context for %s: %w", spec.arm.Name(), q.ID, err)
			}

			paths := make([]string, len(hits))
			var contextText strings.Builder
			for i, h := range hits {
				paths[i] = h.SourcePath
				fmt.Fprintf(&contextText, "[%d] %s\n%s\n\n", i+1, h.Label(), h.Content)
			}
			ctxBytes += contextText.Len()

			text, err := gen.Generate(ctx, q.Question, hits)
			if err != nil {
				return fmt.Errorf("bench: %s: generate %s: %w", spec.arm.Name(), q.ID, err)
			}

			retrieval := eval.Score(q, paths, 0)
			res := eval.ScoreAnswer(q, text, retrieval.Recall, paths)

			// Correctness is graded blind - the judge never sees which arm
			// produced the answer. Faithfulness needs the context, so it is
			// a separate call.
			if v, err := j.GradeBlind(ctx, q.Question, q.GroundTruth, text); err != nil {
				fmt.Fprintf(os.Stderr, "\nwarn: blind judge failed on %s/%s: %v\n", spec.arm.Name(), q.ID, err)
			} else {
				res.Judged = true
				res.JudgeCorrectness = v.Correctness
				res.JudgeReason = v.Reason
			}

			if len(hits) > 0 {
				if v, err := j.GradeFaithfulness(ctx, q.Question, contextText.String(), text); err != nil {
					fmt.Fprintf(os.Stderr, "\nwarn: faithfulness judge failed on %s/%s: %v\n", spec.arm.Name(), q.ID, err)
				} else {
					res.JudgeFaithful = v.Faithful
				}
			}

			results = append(results, res)
			fmt.Fprintf(os.Stderr, "\r  %-18s %s (%d/%d)   ",
				spec.arm.Name(), q.ID, len(results), len(g.Questions))
		}

		armResults = append(armResults, armResult{
			Arm:          spec.arm.Name(),
			Describe:     spec.arm.Describe(),
			GenModel:     spec.genModel,
			Results:      results,
			Summary:      eval.SummarizeAnswers(results),
			MeanCtxBytes: ctxBytes / len(g.Questions),
			Elapsed:      time.Since(start),
		})
	}
	fmt.Fprintln(os.Stderr)

	printBench(armResults, g, j.Model())

	if *out != "" {
		blob, err := json.MarshalIndent(armResults, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*out, blob, 0o644); err != nil {
			return err
		}
		fmt.Printf("\nfull results written to %s\n", *out)
	}
	return nil
}

func shortModel(m string) string {
	if i := strings.Index(m, ":"); i >= 0 {
		return m[i+1:]
	}
	return m
}

func printBench(arms []armResult, g *eval.Golden, judgeModel string) {
	fmt.Printf("judge        %s (correctness graded blind - no context shown)\n", judgeModel)
	fmt.Printf("questions    %d\n\n", len(g.Questions))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ARM\tGEN\tCORRECT\tSCORED-2\tMISSING\tHALLUC\tFAKE-CITE\tREFUSED\tCTX-BYTES\tELAPSED\n")
	for _, a := range arms {
		s := a.Summary
		fmt.Fprintf(w, "%s\t%s\t%.2f\t%d/%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			a.Arm, shortModel(a.GenModel), s.MeanCorrectness, s.Perfect, s.Judged,
			s.MissingRequired, s.Hallucinations, s.BadCitations,
			s.ByAttribution["generation-limited"]+s.NegativesRefused,
			a.MeanCtxBytes, a.Elapsed.Round(time.Second))
	}
	w.Flush()

	fmt.Printf("\nwhat each arm controls for\n")
	for _, a := range arms {
		fmt.Printf("  %-18s %s\n", a.Arm, a.Describe)
	}

	// The deltas that matter. A retrieval system has to beat random chunks
	// on the same budget, or the ranking is decorative.
	byName := map[string]armResult{}
	for _, a := range arms {
		byName[a.Arm] = a
	}
	fmt.Printf("\nkey comparisons\n")
	for _, a := range arms {
		if !strings.HasPrefix(a.Arm, "rag/") {
			continue
		}
		for _, ctrl := range []string{"random-k", "truncated-corpus", "no-context"} {
			if c, ok := byName[ctrl]; ok {
				d := a.Summary.MeanCorrectness - c.Summary.MeanCorrectness
				sign := "+"
				if d < 0 {
					sign = ""
				}
				fmt.Printf("  %-14s vs %-18s %s%.2f correctness, %+d missing phrases\n",
					a.Arm, ctrl, sign, d,
					a.Summary.MissingRequired-c.Summary.MissingRequired)
			}
		}
	}
}
