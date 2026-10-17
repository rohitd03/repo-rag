# Glossary

Plain-language definitions of the terms used across this repo's findings.

---

## Measuring retrieval

**Recall** — of the sources that *should* have been found, what fraction were?
A shopping list of 10 items, you come home with 7: recall 0.7. Written
`recall@k`, where k is how many results you looked at.

**MRR** (Mean Reciprocal Rank) — *how high up* the right answer appeared.
Position 1 scores 1.0, position 2 scores 0.5, position 4 scores 0.25, then
averaged across questions. Separate from recall because models read the top of
their context most reliably — found at position 10 is technically found and
practically ignored.

**k** — how many chunks to retrieve. Choosing how many search results to read.

**Zero-recall** — questions where nothing correct was retrieved at all. More
useful than the mean: a mean of 0.7 with two total misses is a different
system from a mean of 0.7 with none.

---

## Cutting up the corpus

**Chunk** — one retrievable piece of a file. The unit everything else operates
on.

**AST** (Abstract Syntax Tree) — the structure a compiler sees when it parses
code, so it knows where each function begins and ends. *AST chunking* cuts at
those boundaries.

> Cutting a book: the naive way is every 50 pages, landing mid-sentence. The
> structured way is at chapter breaks.

Phase 1 cut every 3,200 characters; Phase 2 cuts at declarations. 107 chunks
of 83 lines became 359 of 22.

**Context enrichment** — prefixing a chunk with where it came from
(`path · package consumer · func (*lagMonitor) updateLag`) before embedding
it. A bare function body is anonymous; the header makes it identifiable.

---

## Finding things

**Embedding** — a model converts text into a list of numbers (768 of them
here). Text with similar *meaning* produces nearby numbers, which is what
makes "why do we track lag?" match code that never uses those words.

**Vector search** — find the stored chunks whose numbers are closest to the
question's numbers. One SQL query: `ORDER BY embedding <=> $1 LIMIT 10`.

**Keyword search** (BM25 / Postgres full-text) — classic word matching. Finds
exact identifiers; misses anything phrased differently.

**Hybrid search** — run both and merge. They fail in *opposite* places:
vector search cannot find `kafkaQuerier` because a rare identifier has no
semantic neighbours, while keyword search finds it instantly. That asymmetry
is the entire argument for using both.

**RRF** (Reciprocal Rank Fusion) — how to merge two ranked lists whose scores
are not comparable.

> Two critics rate films. One scores out of 10, the other gives stars out of
> 5. Averaging 7.5 and 3 is meaningless. But *positions* combine fine — he
> ranked it 2nd, she ranked it 4th.

Cosine similarity and `ts_rank_cd` are exactly that mismatch. RRF discards
both scores and uses only ranks: each ranker contributes `1/(60 + rank)`.

**MMR** (Maximal Marginal Relevance) — diversity reranking. Pick the best
result, then repeatedly pick the best thing *unlike* what you already have.

> Packing a suitcase: not the five best t-shirts, but a shirt, then trousers,
> then shoes.

Needed because a comparison question ("the difference between these two
metrics") otherwise returns five chunks about the same one — each is
individually the closest match.

**pgvector** — the Postgres extension adding a `vector` column type and
distance operators. Lets vector search be a normal SQL query, joinable and
filterable alongside existing tables.

**HNSW** — the index type that makes nearest-neighbour search fast without
scanning every row. The approximate-search equivalent of a B-tree.

---

## Judging answers

**Faithfulness** — is every claim in the answer supported by the retrieved
context? Independent of correctness: an answer can be faithful and wrong (the
context was bad), or correct and unfaithful (it guessed right).

**Hallucination** — stating something the context does not support. The
dangerous version here is a **fabricated citation**: a cited file path that
was never retrieved, which reads as evidence. See
`eval/phase3/FINDINGS.md` for the real example.

**LLM-as-judge** — using a model to grade answers. Cheaper than hand-scoring
and less reliable, so it is validated against hand-scored examples before its
numbers are quoted. Here it failed that validation twice and is used for
ranking runs, not for reporting accuracy.

**Attribution** — which stage caused a bad answer:

| | Meaning | Fix |
|---|---|---|
| `retrieval-limited` | recall < 1, answer incomplete | better search |
| `generation-limited` | full context, wrong answer | better model or prompt |
| `hallucination` | unsupported claim or fake citation | grounding |

The most useful output of the eval, and the thing a single score destroys.
Incomplete context and a weak model need opposite fixes.

**Negative question** — one whose answer is "the project doesn't do that."
`q15` asks how the project uses Redis; there is no Redis. Retrieval always
returns k chunks regardless, so these test whether the system refuses rather
than inventing something from the least-irrelevant results.

**False refusal** — declining a question the context *did* answer. The
opposite failure, and invisible if you only test negative questions. See
`q09`.

---

## Running experiments

**Golden set** — hand-written questions with verified answers, expected source
files, and forbidden phrases. The thing that makes "did this change help?"
answerable.

**Baseline** — how well the simplest approach works. Without it, no
improvement can be claimed. Here: the whole repo pasted into one context
window, no retrieval.

**Test set contamination** — letting the system see the answer key. The golden
file lives in `eval/`, which `chunk.Walk` refuses to index, with a named test
enforcing it. A chunk containing the question text is a perfect semantic
match, so indexing it would score near-perfect while measuring nothing.

**Determinism** — identical inputs producing identical outputs. A precondition
for comparing two configurations, not a nicety: two runs here once differed on
15 of 20 answers, which was larger than any effect being measured.

**ROI** — for a given job, is a model or technique worth what it costs in
money, latency and complexity? The question this project exists to answer,
and the reason the whole corpus fitting in one context window is recorded up
front rather than hidden.
