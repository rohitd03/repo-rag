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
	"github.com/rohitd03/repo-rag/internal/eval"
	"github.com/rohitd03/repo-rag/internal/judge"
	"github.com/rohitd03/repo-rag/internal/store"
)

// runAnswerEval scores generated answers, not just retrieval.
//
// Phase 2's harness measured whether the right sources were found. That is
// necessary and not sufficient: q12 showed a 2/3 recall producing a fully
// correct answer, and q07 showed a faithful answer that was still incomplete
// because retrieval was. Only scoring both stages tells you which to fix.
func runAnswerEval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("answer-eval", flag.ExitOnError)
	corpus := fs.String("corpus", "", "corpus name (required)")
	goldens := fs.String("goldens", "eval/goldens/cdc-pipeline.yaml", "path to the golden set")
	k := fs.Int("k", 10, "how many chunks to retrieve")
	mode := fs.String("mode", defaultMode, "retrieval mode")
	candidates := fs.Int("candidates", defaultCandidates, "candidate pool depth")
	lambda := fs.Float64("lambda", defaultLambda, "MMR relevance vs diversity")
	noJudge := fs.Bool("no-judge", false, "deterministic checks only, skip the model judge")
	only := fs.String("only", "", "comma-separated question ids, for spot checks")
	out := fs.String("out", "", "write full results to this JSON file")
	label := fs.String("label", "", "label for this run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *corpus == "" {
		fs.Usage()
		return fmt.Errorf("answer-eval: -corpus is required")
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

	r, err := newRetriever(st, *corpus, *mode, *candidates, *lambda)
	if err != nil {
		return err
	}

	gen, err := newGenerator()
	if err != nil {
		return err
	}

	var j judge.Judge
	if !*noJudge {
		j = judge.NewOllama(
			env("RAG_OLLAMA_URL", defaultOllamaURL),
			env("RAG_JUDGE_MODEL", defaultJudgeModel),
		)
	}

	filter := map[string]bool{}
	for _, id := range strings.Split(*only, ",") {
		if id = strings.TrimSpace(id); id != "" {
			filter[id] = true
		}
	}

	start := time.Now()
	var results []eval.AnswerResult

	for _, q := range g.Questions {
		if len(filter) > 0 && !filter[q.ID] {
			continue
		}

		hits, err := r.Retrieve(ctx, q.Question, *k)
		if err != nil {
			return fmt.Errorf("answer-eval: retrieve %s: %w", q.ID, err)
		}

		paths := make([]string, len(hits))
		var contextText strings.Builder
		for i, h := range hits {
			paths[i] = h.SourcePath
			fmt.Fprintf(&contextText, "[%d] %s\n%s\n\n", i+1, h.Label(), h.Content)
		}

		text, err := gen.Generate(ctx, q.Question, hits)
		if err != nil {
			return fmt.Errorf("answer-eval: generate %s: %w", q.ID, err)
		}

		retrieval := eval.Score(q, paths, 0)
		res := eval.ScoreAnswer(q, text, retrieval.Recall, paths)

		if j != nil {
			v, err := j.Grade(ctx, q.Question, q.GroundTruth, contextText.String(), text)
			if err != nil {
				// A judge failure must not discard the deterministic
				// results, which are the trustworthy half anyway.
				fmt.Fprintf(os.Stderr, "warn: judge failed on %s: %v\n", q.ID, err)
			} else {
				res.Judged = true
				res.JudgeCorrectness = v.Correctness
				res.JudgeFaithful = v.Faithful
				res.JudgeReason = v.Reason
			}
		}

		results = append(results, res)
		fmt.Fprintf(os.Stderr, "\r  %s (%d/%d)   ", q.ID, len(results), len(g.Questions))
	}
	fmt.Fprintln(os.Stderr)

	if *label == "" {
		*label = fmt.Sprintf("mode=%s k=%d gen=%s", *mode, *k, gen.Model())
	}
	judgeModel := "none"
	if j != nil {
		judgeModel = j.Model()
	}
	printAnswerReport(results, eval.SummarizeAnswers(results), *label, gen.Model(), judgeModel, time.Since(start))

	if *out != "" {
		blob, err := json.MarshalIndent(results, "", "  ")
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

func printAnswerReport(results []eval.AnswerResult, s eval.AnswerSummary, label, genModel, judgeModel string, took time.Duration) {
	fmt.Printf("run          %s\n", label)
	fmt.Printf("generator    %s\n", genModel)
	fmt.Printf("judge        %s\n", judgeModel)
	fmt.Printf("elapsed      %s\n\n", took.Round(time.Millisecond))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tCAT\tRECALL\tCORRECT\tFAITHFUL\tCITED\tATTRIBUTION\tNOTE\n")
	for _, r := range results {
		correct, faithful := "-", "-"
		if r.Judged {
			correct = fmt.Sprintf("%d", r.JudgeCorrectness)
			faithful = "no"
			if r.JudgeFaithful {
				faithful = "yes"
			}
		}
		cited := "no"
		if len(r.Citations) > 0 {
			cited = "yes"
		}
		recall := fmt.Sprintf("%.2f", r.Recall)
		if r.Question.Negative() {
			recall = "n/a"
		}

		var notes []string
		if len(r.Violations) > 0 {
			notes = append(notes, "FORBIDDEN:"+strings.Join(r.Violations, ","))
		}
		if len(r.BadCitations) > 0 {
			notes = append(notes, "FAKE-CITE:"+strings.Join(r.BadCitations, ","))
		}
		if len(r.MissingRequired) > 0 {
			notes = append(notes, "missing:"+strings.Join(r.MissingRequired, ","))
		}
		if r.Question.Negative() && r.Refused {
			notes = append(notes, "refused")
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Question.ID, r.Question.Category, recall, correct, faithful, cited,
			r.Attribution(), strings.Join(notes, " "))
	}
	w.Flush()

	fmt.Printf("\ndeterministic (always true)\n")
	fmt.Printf("  hallucinations       %d/%d\n", s.Hallucinations, s.Total)
	fmt.Printf("  fabricated citations %d\n", s.BadCitations)
	fmt.Printf("  answers with a cite  %d/%d\n", s.AnswersWithCite, s.Total)
	fmt.Printf("  cited a right source %d/%d\n", s.CitedCorrectly, s.Scored)
	fmt.Printf("  missing required     %d phrases\n", s.MissingRequired)
	fmt.Printf("  negatives refused    %d/%d\n", s.NegativesRefused, s.Negatives)

	if s.Judged > 0 {
		fmt.Printf("\njudge (a model, verify before trusting)\n")
		fmt.Printf("  mean correctness     %.2f / 2.00\n", s.MeanCorrectness)
		fmt.Printf("  scored 2             %d/%d\n", s.Perfect, s.Judged)
		fmt.Printf("  faithful             %d/%d\n", s.Faithful, s.Judged)
	}

	fmt.Printf("\nwhat limited each answer\n")
	for _, a := range eval.AttributionOrder {
		if n := s.ByAttribution[a]; n > 0 {
			fmt.Printf("  %-20s %d\n", a, n)
		}
	}
}

// answerGenerator is satisfied by both backends; kept so the compiler
// catches a backend that drifts from the interface.
var _ answer.Generator = (*answer.Ollama)(nil)
