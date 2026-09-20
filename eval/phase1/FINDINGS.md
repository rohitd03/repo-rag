# Phase 1 — naive RAG, measured

First real run against `cdc-pipeline` (commit `78ab775`), 2026-09-20.

Config: fixed-size line windows (3200 B target, 400 B overlap) ·
`nomic-embed-text` (768-dim) via local Ollama · pgvector HNSW, cosine · plain
top-k, k=5 · no reranking, no hybrid search.

---

## Indexing

| | |
|---|---|
| Files | 48 (exact parity with `pack-corpus.sh`) |
| Chunks | 107 |
| Full index | **64.9 s** |
| Re-index, nothing changed | **7 ms**, 0 embedded, 107 skipped |

The hash cache turns a 65-second re-index into 7 milliseconds — about
9,000×. Worth noting *why* that ratio is so extreme: embedding is the only
slow step in the whole pipeline. Everything else is I/O and Postgres.

## Query latency

| | |
|---|---|
| First query after Ollama starts | 8,183 ms |
| Every query after that | **~55 ms** |

The first call pays for loading the model into memory. Steady-state retrieval
is 55 ms, which is Postgres doing an HNSW lookup plus one local embedding
call. For comparison, the no-RAG baseline has to ship ~62,900 tokens over the
network on every single question.

## Chunk shape — the thing to fix first

| | |
|---|---|
| Chunks per file | 2.2 avg, 13 max |
| Lines per chunk | **82.7 avg**, 139 max |
| Files that are a single chunk | 21 of 48 |

**Chunks are far too coarse.** At ~83 lines, a chunk is most of a small Go
file. Retrieval is effectively working at *file* granularity — it can tell you
`lag_monitor.go` is relevant, but not *which part*. Almost half the corpus is
one indivisible blob.

That is the fixed-size chunker doing exactly what it was built to do, and it
sets up Phase 2: AST chunking should take the average to one function per
chunk, roughly 10–25 lines, giving retrieval something to actually discriminate
between.

---

## Retrieval results

Scored as recall@5 against `expected_sources` in the golden set. Retrieval
only — no generation, so this measures the retriever alone.

### q04 — "which file calculates consumer lag?" · recall **1/1**

```
[1] 0.645  internal/consumer/lag_monitor.go:1-124   ← correct
[2] 0.636  CDC_Pipeline_Project.md:550-653
[3] 0.619  alerts.yml:126-147
[4] 0.615  alerts.yml:69-136
[5] 0.600  internal/metrics/prometheus.go:94-166
```

Works. Easy locational questions are what naive vector search is good at.

### q07 — "why a `kafkaQuerier` interface?" · recall **1/3**

```
[1] 0.710  internal/consumer/lag_monitor.go:1-124   ← correct
[2] 0.612  alerts.yml:126-147
[3] 0.610  internal/consumer/kafka_consumer.go:1-121
[4] 0.604  docs/alerting.md:75-144
[5] 0.598  docker/debezium/register-connector.sh:1-52
```

**Missing `decisions.md` and `lag_monitor_test.go`** — the two sources that
carry the *other half* of the answer. `decisions.md` states the convention
("interfaces defined in the package that uses them") and the test file is the
proof that testability was the motive.

This is the predicted failure, but the diagnosis needs splitting in two,
because the two misses have different causes and different fixes.

`lag_monitor_test.go` contains the literal string `kafkaQuerier` and is still
not retrieved — a rare identifier has no neighbours in embedding space, so
cosine similarity cannot see it. BM25 ranks it first instantly. **That is the
hybrid-search argument, with a number attached.**

`decisions.md` is harder, and an earlier draft of this document got it wrong
by claiming it also contains the identifier. It does not. The relevant line is
just:

> Interfaces defined in the package that uses them, not the package that
> implements them

No `kafkaQuerier`, no `lag`, no `kafka`. Keyword search will not find it from
this question and neither will vector search, because a terse one-line
convention in a 7-line file is a weak embedding target however it is chunked.

So hybrid search should take q07 from 1/3 to 2/3, not 3/3. Getting to 3/3
needs something else — query expansion, or accepting that repo-wide
conventions are a different retrieval problem from code lookup. Worth stating
the expected ceiling before making the change, so the result can be judged
against a prediction rather than rationalised afterwards.

### q12 — "difference between the two lag metrics?" · recall **2/3**

```
[1] 0.582  internal/consumer/lag_monitor.go:1-124   ← correct
[2] 0.576  internal/metrics/prometheus.go:1-108     ← correct
[3] 0.570  alerts.yml:126-147
[4] 0.562  README.md:169-289
[5] 0.544  internal/api/middleware/metrics.go:1-47
```

**Missing `debezium_handler.go`**, which is where `pipeline_lag_seconds` is
actually computed. So the retriever found one half of a comparison question
and not the other — the answer will be confidently lopsided.

Note the scores are the flattest of any question (0.582 → 0.544). Everything
looks equally "about lag" and nothing stands out.

### q15 — "how does it use Redis?" · negative case

```
[1] 0.537  postman/.postman/resources.yaml:1-10
[2] 0.528  .github/workflows/ci.yml:1-54
[3] 0.514  CDC_Pipeline_Project.md:1-72
[4] 0.514  CDC_Pipeline_Project.md:374-479
[5] 0.506  CDC_Pipeline_Project.md:280-389
```

Pure noise, as it should be — there is no Redis to find. The retriever
cannot refuse; it returns five chunks no matter what. Whether this question
is answered correctly rests entirely on the grounding instructions in the
system prompt.

**But look at the scores.** Top hit 0.537, versus 0.645–0.710 for the
answerable questions. Across this sample, everything answerable scored ≥0.58
and this one peaked at 0.537. A similarity floor might detect
"nothing relevant here" before generation even runs — cheap, and worth
testing properly in Phase 3 rather than trusting four data points.

---

## Generation — local `qwen2.5-coder:3b`

Same five retrieved chunks, answered by a 3B model on the laptop GPU. Scored
0/1/2 against `ground_truth`, by hand, on the same five questions.

| Q | Score | Notes |
|---|:---:|---|
| q04 | **2** | Correct, and cited `internal/consumer/lag_monitor.go` |
| q07 | 1 | Correct as far as the context went, but see below |
| q12 | **2** | Both metrics named, messages-vs-seconds distinction right |
| q15 | **2** | Clean refusal |
| q16 | 1 | Right conclusion, did not name the three middlewares |

**8/10 from a 3B model running locally.** Small sample, hand-scored, but not
what the "small models hallucinate" prior would predict.

### The negative cases held

q15 asked how the project uses Redis. Retrieval handed over five irrelevant
chunks — postman config, CI yaml, project docs. The answer:

> The retrieved context does not cover Redis for caching.

No fabrication, no `must_not_contain` hit. q16 likewise concluded there is no
auth rather than inventing a middleware.

This is the result to be careful about interpreting. It suggests grounding
behaviour is driven more by the system prompt than by model size — the refusal
clause is doing the work. Worth testing properly in Phase 3 by running the
same questions with the refusal clause removed, rather than assuming.

### q07 — a retrieval failure wearing a generation failure's clothes

> The lag monitor defines a kafkaQuerier interface instead of using
> kafka.Client directly to make the monitor unit-testable without a real
> broker.

Correct, and incomplete — it misses the `decisions.md` convention. But
retrieval never surfaced `decisions.md`, so the model answered exactly what
its context supported. **The generation is faithful; the retriever is at
fault.** Telling these two apart is the single most useful diagnostic skill in
this project, and it is invisible unless you record what was retrieved
alongside what was answered.

### q12 — recall is not answerability

Retrieval scored 2/3, missing `debezium_handler.go` where
`pipeline_lag_seconds` is computed. The predicted outcome was a lopsided
answer. Instead the model got it right, inferring the relationship from the
`Help` strings in `prometheus.go` — and the inference checks out, because
`PipelineLastEventTimestamp` is set to exactly `event.TsMs / 1000`.

So a recall miss did not produce an answer miss, because the missing source
was partly redundant with a retrieved one. **Retrieval metrics alone would
have predicted a failure that did not happen.** Phase 3 needs retrieval
metrics *and* answer metrics; either on its own misleads.

### Citation compliance is inconsistent

The system prompt requires a source citation per claim. q04 complied, q07 did
not. Small-model instruction-following, and cheap to measure — Phase 3 should
track citation rate as its own metric rather than folding it into the score.

### Generation latency

| | |
|---|---|
| Cold, first call (model load into VRAM) | 69,476 ms |
| Warm, short answer | 661 ms |
| Warm, longer answer | 2,078–10,174 ms |

The 3B fits entirely in the RTX 4050's 4 GB, so once loaded it is fast enough
to iterate with. Identical output across two runs of q07 confirms
`temperature: 0.1` is giving reproducibility, which the eval harness depends
on.

---

## Cross-cutting observations

**A sticky chunk.** `alerts.yml:126-147` appeared in q04, q07 *and* q12 — three
of four queries, none of which were about alerting. It is a short chunk dense
with the words "lag", "kafka" and "consumer", so it matches anything in the
neighbourhood without contributing an answer. Short, keyword-dense chunks
punch above their weight in cosine similarity. Reranking should demote it.

**Two distinct failure modes, and they need different fixes:**

| Failure | Seen in | Fix |
|---|---|---|
| Rare identifier has no semantic neighbours | q07 | hybrid search (BM25 + vector) |
| Redundant hits crowd out the second half of a comparison | q12 | MMR / diversity |

Worth separating, because a single "improve retrieval" change would fix one
and not the other, and without this breakdown you would not know which.

---

## Phase 2 priorities, in order

1. **AST-aware chunking.** 83-line chunks are the root cause of the coarse
   granularity. Biggest single lever.
2. **Hybrid search.** Postgres `tsvector` alongside the vector column, fused
   with Reciprocal Rank Fusion. Directly targets q07.
3. **Context enrichment.** Prepend file path, package and symbol name before
   embedding, so a chunk carries its own identity.
4. **MMR / diversity.** Targets q12.
5. **Reranking.** Retrieve ~25, score for relevance, keep ~6. Demotes sticky
   chunks like `alerts.yml`.

Each lands as its own change, measured against these numbers.

---

## Caveat

This is 4 questions out of 20, scored by hand, retrieval only. It is enough to
point Phase 2 in the right direction and not enough to claim anything. The
Phase 3 harness runs all 20 automatically and produces the numbers that
actually count.
