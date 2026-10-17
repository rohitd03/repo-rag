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
	GenElapsed   time.Duration       `json:"gen_elapsed"`
}

// pending is one generated answer waiting to be judged.
type pending struct {
	armIdx   int
	resIdx   int
	question eval.Question
	ctxText  string
	answer   string
}

// runBench compares the real system against its controls.
//
// Every arm shares the generator, the judge, the prompt and the scoring; the
// only thing that varies is how the context was chosen. Without the controls,
// "recall went up" is a claim about a number rather than about whether the
// retrieval does anything useful.
//
// Generation and judging run as two separate passes, not interleaved per
// question. On a 4GB GPU an interleaved loop evicts and reloads a model on
// every call - roughly 200 loads for a five-arm run - and Ollama eventually
// dies with "timed out waiting for llama-server to start". Batching by stage
// loads each model once.
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
	noJudge := fs.Bool("no-judge", false, "skip judging, report deterministic metrics only")
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
	ollamaURL := env("RAG_OLLAMA_URL", defaultOllamaURL)

	type armSpec struct {
		arm      bench.Arm
		genModel string
	}
	var specs []armSpec

	models := []string{}
	for _, gm := range strings.Split(*genModels, ",") {
		if gm = strings.TrimSpace(gm); gm != "" {
			models = append(models, gm)
		}
	}
	if len(models) == 0 {
		return fmt.Errorf("bench: -gen-models is empty")
	}

	// Controls run on the first generator so the comparison against it is
	// model-for-model.
	control := models[0]
	specs = append(specs,
		armSpec{bench.NoContext{}, control},
		armSpec{&bench.RandomK{Store: st, Corpus: *corpus, Model: emb.Model(), Seed: 42}, control},
		armSpec{&bench.TruncatedCorpus{
			Store: st, Corpus: *corpus, Model: emb.Model(), BudgetBytes: *budget,
		}, control},
	)
	for _, gm := range models {
		r, err := newRetriever(st, *corpus, *mode, *candidates, *lambda)
		if err != nil {
			return err
		}
		specs = append(specs, armSpec{&bench.RAG{Retriever: r, Label: "rag/" + shortModel(gm)}, gm})
	}

	filter := map[string]bool{}
	for _, name := range strings.Split(*only, ",") {
		if name = strings.TrimSpace(name); name != "" {
			filter[name] = true
		}
	}

	// ---- pass 1: generate, grouped by generator model so each loads once

	arms := make([]armResult, 0, len(specs))
	var queue []pending

	byModel := map[string][]int{}
	for _, s := range specs {
		if len(filter) > 0 && !filter[s.arm.Name()] {
			continue
		}
		arms = append(arms, armResult{
			Arm:      s.arm.Name(),
			Describe: s.arm.Describe(),
			GenModel: s.genModel,
		})
		byModel[s.genModel] = append(byModel[s.genModel], len(arms)-1)
	}
	if len(arms) == 0 {
		return fmt.Errorf("bench: -only matched no arms")
	}

	armSpecOf := map[int]bench.Arm{}
	{
		ai := 0
		for _, s := range specs {
			if len(filter) > 0 && !filter[s.arm.Name()] {
				continue
			}
			armSpecOf[ai] = s.arm
			ai++
		}
	}

	for model, armIdxs := range byModel {
		gen := answer.NewOllama(ollamaURL, model)
		for _, ai := range armIdxs {
			arm := armSpecOf[ai]
			start := time.Now()
			ctxBytes := 0

			for _, q := range g.Questions {
				hits, err := arm.Context(ctx, q.Question, *k)
				if err != nil {
					return fmt.Errorf("bench: %s: context for %s: %w", arm.Name(), q.ID, err)
				}

				paths := make([]string, len(hits))
				var ctxText strings.Builder
				for i, h := range hits {
					paths[i] = h.SourcePath
					fmt.Fprintf(&ctxText, "[%d] %s\n%s\n\n", i+1, h.Label(), h.Content)
				}
				ctxBytes += ctxText.Len()

				text, err := gen.Generate(ctx, q.Question, hits)
				if err != nil {
					return fmt.Errorf("bench: %s: generate %s: %w", arm.Name(), q.ID, err)
				}

				retrieval := eval.Score(q, paths, 0)
				arms[ai].Results = append(arms[ai].Results, eval.ScoreAnswer(q, text, retrieval.Recall, paths))

				queue = append(queue, pending{
					armIdx:   ai,
					resIdx:   len(arms[ai].Results) - 1,
					question: q,
					ctxText:  ctxText.String(),
					answer:   text,
				})

				fmt.Fprintf(os.Stderr, "\r  generate %-18s %s (%d/%d)   ",
					arm.Name(), q.ID, len(arms[ai].Results), len(g.Questions))
			}

			arms[ai].GenElapsed = time.Since(start)
			arms[ai].MeanCtxBytes = ctxBytes / len(g.Questions)
		}
	}
	fmt.Fprintln(os.Stderr)

	// ---- pass 2: judge everything, one model load

	if !*noJudge {
		j := judge.NewOllama(ollamaURL, env("RAG_JUDGE_MODEL", defaultJudgeModel))
		for n, p := range queue {
			res := &arms[p.armIdx].Results[p.resIdx]

			if v, err := j.GradeBlind(ctx, p.question.Question, p.question.GroundTruth, p.answer); err != nil {
				fmt.Fprintf(os.Stderr, "\nwarn: blind judge failed on %s/%s: %v\n",
					arms[p.armIdx].Arm, p.question.ID, err)
			} else {
				res.Judged = true
				res.JudgeCorrectness = v.Correctness
				res.JudgeReason = v.Reason
			}

			if strings.TrimSpace(p.ctxText) != "" {
				if v, err := j.GradeFaithfulness(ctx, p.question.Question, p.ctxText, p.answer); err != nil {
					fmt.Fprintf(os.Stderr, "\nwarn: faithfulness judge failed on %s/%s: %v\n",
						arms[p.armIdx].Arm, p.question.ID, err)
				} else {
					res.JudgeFaithful = v.Faithful
				}
			}

			fmt.Fprintf(os.Stderr, "\r  judge %-18s %s (%d/%d)   ",
				arms[p.armIdx].Arm, p.question.ID, n+1, len(queue))
		}
		fmt.Fprintln(os.Stderr)
	}

	for i := range arms {
		arms[i].Summary = eval.SummarizeAnswers(arms[i].Results)
	}

	judgeModel := "none"
	if !*noJudge {
		judgeModel = env("RAG_JUDGE_MODEL", defaultJudgeModel)
	}
	printBench(arms, g, judgeModel)

	if *out != "" {
		blob, err := json.MarshalIndent(arms, "", "  ")
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
	fmt.Fprintf(w, "ARM\tGEN\tCORRECT\tSCORED-2\tMISSING\tHALLUC\tFAKE-CITE\tUNFAITHFUL\tCTX-BYTES\tGEN-TIME\n")
	for _, a := range arms {
		s := a.Summary
		unfaithful := "-"
		if s.Judged > 0 {
			unfaithful = fmt.Sprintf("%d", s.Judged-s.Faithful)
		}
		fmt.Fprintf(w, "%s\t%s\t%.2f\t%d/%d\t%d\t%d\t%d\t%s\t%d\t%s\n",
			a.Arm, shortModel(a.GenModel), s.MeanCorrectness, s.Perfect, s.Judged,
			s.MissingRequired, s.Hallucinations, s.BadCitations, unfaithful,
			a.MeanCtxBytes, a.GenElapsed.Round(time.Second))
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
	fmt.Printf("\nkey comparisons (correctness delta, missing-phrase delta)\n")
	for _, a := range arms {
		if !strings.HasPrefix(a.Arm, "rag/") {
			continue
		}
		for _, ctrl := range []string{"random-k", "truncated-corpus", "no-context"} {
			c, ok := byName[ctrl]
			if !ok {
				continue
			}
			fmt.Printf("  %-14s vs %-18s %+.2f correctness, %+d missing phrases\n",
				a.Arm, ctrl,
				a.Summary.MeanCorrectness-c.Summary.MeanCorrectness,
				a.Summary.MissingRequired-c.Summary.MissingRequired)
		}
	}
}
