# repo-rag

A retrieval-augmented question-answering CLI for source repositories, written
in Go, storing embeddings in PostgreSQL via `pgvector`.

Built as a measured experiment rather than a demo: every retrieval change is
scored against a hand-written golden question set, and the choice of embedding
model, generation model and retrieval strategy is justified with numbers rather
than vibes.

First target corpus: [`cdc-pipeline`](../cdc-pipeline) — a Postgres → Debezium →
Kafka → Elasticsearch CDC pipeline.

---

## Why this exists

Most RAG projects skip the two steps that make the technique worth anything:

1. **A baseline.** If you never measure how well the naive approach works, you
   cannot claim retrieval helped.
2. **An eval set.** Without one, "tuning" a RAG pipeline is just changing
   numbers and trusting your gut on a handful of cherry-picked questions.

This repo does both first, then builds the pipeline.

---

## Status

| Phase | What it delivers | State |
|---|---|:---:|
| **0** | Golden question set + no-RAG baseline | ✅ built, awaiting scoring |
| **1** | Naive RAG end to end — fixed chunks, one model, top-k | ✅ built, awaiting run |
| **2** | Retrieval quality — AST chunking, hybrid search, MMR | ✅ recall 0.629 → 0.726 |
| 3 | Answer-quality harness — faithfulness judge, citation rate | ⬜ |
| 4 | Model/approach ROI comparison + written decision | ⬜ |

---

## Phase 0 artifacts

```
eval/
  goldens/cdc-pipeline.yaml   20 questions with verified answers,
                              expected source files, and hallucination guards
  baseline/RESULTS.md         scoring protocol + results sheet (fill this in)
  baseline/corpus.txt         whole repo packed into one file (generated)
scripts/
  pack-corpus.sh              packs a repo + reports its token count
```

### The headline number

The entire `cdc-pipeline` corpus is **48 files / ~62,900 tokens**. It fits in a
single 200k context window about three times over.

So for *this* corpus, retrieval is an optimization, not a requirement. RAG has
to win on cost (~20× fewer tokens per query) and latency, because it cannot win
on recall against a baseline that sees everything by construction.

Stating that up front is the point. The interesting engineering question is not
"can I build a RAG" but "at what corpus size does retrieval start paying for
itself, and how would I know?"

### Question set design

20 questions across five categories, each chosen to stress a different part of
the pipeline:

| Category | n | Stresses |
|---|:-:|---|
| factual | 4 | basic lookup — should never fail |
| locational | 3 | file-level routing |
| reasoning | 7 | "why" questions; answers live in comments and conventions |
| cross-file | 4 | multi-hop retrieval; `q11` needs 8 files at once |
| negative | 3 | refusal behaviour — the answer is "it doesn't do that" |

Notable cases:

- **q07** asks why `kafkaQuerier` exists. `kafkaQuerier` is a rare identifier
  with no semantic neighbours, so pure vector search is weak on it. This is the
  argument for hybrid BM25 + vector search.
- **q11** needs eight source files in context simultaneously. At k=5 that is
  structurally impossible — a built-in demonstration of where single-shot
  retrieval breaks.
- **q12** asks the difference between two metrics that both embed to "lag".
  Naive top-k returns five chunks about the same one. This motivates MMR.
- **q15** asks how the project uses Redis. It doesn't. There is no Redis
  anywhere in the repo. A system that answers this question confidently is
  broken regardless of how it scores elsewhere.

---

## Running the baseline

```bash
./scripts/pack-corpus.sh ../cdc-pipeline eval/baseline/corpus.txt
```

Then follow the protocol in [`eval/baseline/RESULTS.md`](eval/baseline/RESULTS.md):
one fresh conversation per question, paste `corpus.txt`, ask the question
verbatim, score 0/1/2 against `ground_truth`.

---

## Planned stack

| Concern | Choice | Why |
|---|---|---|
| Language | Go | matches the corpus; single static binary |
| Vector store | Postgres + `pgvector` | no new database; hybrid search with `tsvector` in the same query |
| Infrastructure | Terraform | `terraform/local` is applied and in use; `terraform/aws` is validated but never applied |
| Embeddings | Ollama (`nomic-embed-text`), local | free, private, swappable behind an interface |
| Generation | Ollama (`qwen2.5-coder`) local, Claude optional | local by default, no API key; Phase 4 compares local vs frontier |
| Chunking | `go/ast` for Go, heading-aware for Markdown | chunks align to semantic units |

The embedding and generation layers sit behind interfaces from day one,
specifically so Phase 4 can swap implementations and measure the difference.
