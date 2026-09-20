// Command rag indexes a repository and answers questions about it.
//
//	rag index -corpus cdc-pipeline -root ../cdc-pipeline
//	rag ask   -corpus cdc-pipeline "why does the lag monitor exist?"
//	rag stats -corpus cdc-pipeline
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rohitd03/repo-rag/internal/answer"
	"github.com/rohitd03/repo-rag/internal/chunk"
	"github.com/rohitd03/repo-rag/internal/embed"
	"github.com/rohitd03/repo-rag/internal/store"
)

const (
	defaultDSN        = "postgres://rag:rag@localhost:5433/rag?sslmode=disable"
	defaultOllamaURL  = "http://localhost:11434"
	defaultEmbedModel = "nomic-embed-text"
	defaultEmbedDims  = 768
	defaultGenModel   = "claude-opus-5"

	// Ollama handles batches fine and this cuts HTTP round-trips
	// substantially versus embedding one chunk at a time.
	embedBatchSize = 32
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "index":
		err = runIndex(ctx, os.Args[2:])
	case "ask":
		err = runAsk(ctx, os.Args[2:])
	case "stats":
		err = runStats(ctx, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `rag - ask questions about a source repository

  rag index -corpus NAME -root PATH    chunk, embed and store a repository
  rag ask   -corpus NAME [-k N] QUESTION
  rag stats -corpus NAME               what is currently indexed

environment:
  RAG_DATABASE_URL   default `+defaultDSN+`
  RAG_OLLAMA_URL     default `+defaultOllamaURL+`
  RAG_EMBED_MODEL    default `+defaultEmbedModel+`
  RAG_GEN_MODEL      default `+defaultGenModel+`
  ANTHROPIC_API_KEY  required for `+"`ask`"+`
`)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func newEmbedder() embed.Embedder {
	return embed.NewOllama(
		env("RAG_OLLAMA_URL", defaultOllamaURL),
		env("RAG_EMBED_MODEL", defaultEmbedModel),
		defaultEmbedDims,
	)
}

// ---------------------------------------------------------------- index

func runIndex(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	corpus := fs.String("corpus", "", "corpus name (required)")
	root := fs.String("root", "", "path to the repository (required)")
	force := fs.Bool("force", false, "re-embed every chunk, ignoring hashes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *corpus == "" || *root == "" {
		fs.Usage()
		return fmt.Errorf("index: -corpus and -root are required")
	}

	emb := newEmbedder()

	st, err := store.Open(ctx, env("RAG_DATABASE_URL", defaultDSN))
	if err != nil {
		return err
	}
	defer st.Close()

	files, err := chunk.Walk(*root)
	if err != nil {
		return fmt.Errorf("index: walk %s: %w", *root, err)
	}
	fmt.Printf("corpus %q: %d files under %s\n", *corpus, len(files), *root)

	known, err := st.ExistingHashes(ctx, *corpus, emb.Model())
	if err != nil {
		return err
	}

	start := time.Now()
	var (
		pending  []chunk.Chunk // chunks whose text changed, awaiting embedding
		counts   = make(map[string]int)
		total    int
		skipped  int
		embedded int
	)

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		texts := make([]string, len(pending))
		for i, c := range pending {
			texts[i] = c.Content
		}
		vecs, err := emb.Embed(ctx, texts)
		if err != nil {
			return err
		}
		if err := st.Upsert(ctx, *corpus, emb.Model(), pending, vecs); err != nil {
			return err
		}
		embedded += len(pending)
		fmt.Printf("\r  embedded %d chunks...", embedded)
		pending = pending[:0]
		return nil
	}

	for _, rel := range files {
		chunks, err := chunk.File(*root, rel)
		if err != nil {
			return err
		}
		counts[rel] = len(chunks)
		total += len(chunks)

		for _, c := range chunks {
			// The hash check is what makes re-indexing cheap. Embedding
			// is the only slow step, so an unchanged chunk costs nothing.
			if !*force && known[store.Key{SourcePath: c.SourcePath, Index: c.Index}] == c.Hash {
				skipped++
				continue
			}
			pending = append(pending, c)
			if len(pending) >= embedBatchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if embedded > 0 {
		fmt.Println()
	}

	deleted, err := st.DeleteStale(ctx, *corpus, emb.Model(), counts)
	if err != nil {
		return err
	}

	fmt.Printf("\nindexed in %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Printf("  chunks total  %d\n", total)
	fmt.Printf("  embedded      %d\n", embedded)
	fmt.Printf("  unchanged     %d (skipped, no embedding call)\n", skipped)
	fmt.Printf("  stale deleted %d\n", deleted)
	fmt.Printf("  embed model   %s\n", emb.Model())
	return nil
}

// ------------------------------------------------------------------ ask

func runAsk(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	corpus := fs.String("corpus", "", "corpus name (required)")
	k := fs.Int("k", 5, "how many chunks to retrieve")
	showSources := fs.Bool("show-sources", false, "print the retrieved chunks")
	retrieveOnly := fs.Bool("retrieve-only", false, "skip generation, just show what was retrieved")
	if err := fs.Parse(args); err != nil {
		return err
	}
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if *corpus == "" || question == "" {
		fs.Usage()
		return fmt.Errorf("ask: -corpus and a question are required")
	}

	emb := newEmbedder()

	st, err := store.Open(ctx, env("RAG_DATABASE_URL", defaultDSN))
	if err != nil {
		return err
	}
	defer st.Close()

	// The query goes through the same embedder as the documents did. Using
	// a different model here would put the query in a different vector
	// space and return confident nonsense rather than an error.
	tRetrieve := time.Now()
	qvecs, err := emb.Embed(ctx, []string{question})
	if err != nil {
		return err
	}
	hits, err := st.Search(ctx, *corpus, emb.Model(), qvecs[0], *k)
	if err != nil {
		return err
	}
	retrieveMS := time.Since(tRetrieve).Milliseconds()

	if len(hits) == 0 {
		return fmt.Errorf("ask: no chunks found for corpus %q - run `rag index` first", *corpus)
	}

	if *showSources || *retrieveOnly {
		fmt.Printf("retrieved %d chunks in %dms\n\n", len(hits), retrieveMS)
		for i, h := range hits {
			fmt.Printf("  [%d] %.3f  %s:%d-%d\n", i+1, h.Score, h.SourcePath, h.StartLine, h.EndLine)
		}
		fmt.Println()
	}
	if *retrieveOnly {
		return nil
	}

	genModel := env("RAG_GEN_MODEL", defaultGenModel)
	tGen := time.Now()
	text, err := answer.NewClaude(genModel).Generate(ctx, question, hits)
	if err != nil {
		return err
	}

	fmt.Println(text)
	fmt.Printf("\n---\nretrieval %dms · generation %dms · k=%d · %s\n",
		retrieveMS, time.Since(tGen).Milliseconds(), *k, genModel)
	return nil
}

// ---------------------------------------------------------------- stats

func runStats(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	corpus := fs.String("corpus", "", "corpus name (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *corpus == "" {
		fs.Usage()
		return fmt.Errorf("stats: -corpus is required")
	}

	emb := newEmbedder()

	st, err := store.Open(ctx, env("RAG_DATABASE_URL", defaultDSN))
	if err != nil {
		return err
	}
	defer st.Close()

	chunks, files, err := st.Stats(ctx, *corpus, emb.Model())
	if err != nil {
		return err
	}
	fmt.Printf("corpus      %s\n", *corpus)
	fmt.Printf("embed model %s\n", emb.Model())
	fmt.Printf("files       %d\n", files)
	fmt.Printf("chunks      %d\n", chunks)
	return nil
}
