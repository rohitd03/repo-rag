# Phase 2 — retrieval quality, measured

Four levers applied to the Phase 1 pipeline, each measured against the same
20-question golden set on the same index.

| | recall | MRR | zero-recall | context |
|---|---|---|---|---|
| **Phase 1** — fixed chunks, vector, k=5 | 0.629 | 0.741 | 2/19 | ~414 lines |
| **Phase 2** — AST + enrichment + hybrid + MMR, k=10 | **0.726** | **0.829** | **0/19** | ~220 lines |

Better retrieval on roughly **half the context**. The context number matters as
much as the recall number: fewer tokens per query is the only axis on which
RAG can beat the no-RAG baseline for a corpus this size.

---

## What changed

1. **AST chunking** — Go files split at declaration boundaries via `go/ast`,
   with doc comments attached to the declaration they document. Markdown
   splits at headings, fence-aware. Everything else keeps fixed windows.
2. **Context enrichment** — every chunk is embedded with a provenance header
   (`path · package X · func (*T) Name`) prepended. The stored content stays
   raw so line numbers still match the file.
3. **Hybrid search** — a generated `tsvector` column and GIN index alongside
   the vector column, fused with Reciprocal Rank Fusion (k=60).
4. **MMR** — diversity reranking over the candidate pool.

Chunk shape, before and after:

| | Phase 1 | Phase 2 |
|---|---|---|
| Chunks | 107 | **359** |
| Lines per chunk | 82.7 | **22.0** |
| Files that are one chunk | 21/48 | 16/48 |

---

## The measurement error I made first

The obvious comparison — Phase 1 at k=5 against Phase 2 at k=5 — says AST
chunking changed nothing:

```
recall@5   0.629  →  0.629
```

That comparison is wrong, and it is worth recording why. At k=5 Phase 1
supplied 5 × 82.7 ≈ **414 lines** of context and Phase 2 supplied 5 × 22 ≈
**110 lines**. Holding *k* fixed while chunk size drops by 4× does not hold
the experiment fixed — it quietly cuts the context to a quarter and calls the
difference a chunking result.

The category breakdown gave it away, because it moved sharply in both
directions at once:

| category | Phase 1 | Phase 2, k=5 |
|---|---|---|
| factual | 0.333 | **0.667** |
| reasoning | 0.806 | 0.694 |
| cross-file | 0.281 | 0.198 |

Finer chunks helped the questions answerable from one place and hurt the ones
needing several, which is exactly what less total context predicts. **The
right unit of comparison is context volume, not k.**

---

## The k sweep

Re-running every mode across k:

| k | vector | hybrid | mmr | hybrid+mmr | zero-recall (hybrid) |
|---|---|---|---|---|---|
| 5 | 0.629 | 0.603 | 0.629 | 0.577 | 2 |
| **10** | 0.700 | **0.726** | 0.700 | **0.726** | **0** |
| 15 | 0.700 | 0.726 | 0.700 | 0.726 | 0 |
| 20 | 0.717 | 0.743 | 0.717 | 0.743 | 0 |

k=5 → k=10 is where the gain is. k=10 → k=15 buys **nothing at all**, and
k=20 buys 0.017 for double the tokens. With 22-line chunks, k=10 is the knee.

Note the inversion at k=5: MMR and hybrid both score *worse* than plain
vector there. At k=5 there is no room to spend a slot on diversity, and
hybrid's keyword hits displace vector hits that were already correct.
**These techniques need headroom to pay off** — a detail that would be
invisible from a single-k measurement.

---

## Per-lever verdicts

### Hybrid search — the clear win

Cross-file recall **0.198 → 0.323** at every k, and it is the only change that
takes zero-recall questions to 0.

The prediction from Phase 1 held exactly: q07 went from 1/3 to **2/3**.
`lag_monitor_test.go` was recovered by keyword match on `kafkaQuerier`;
`decisions.md` was not, because it does not contain the identifier and its
one-line convention is a weak target for either ranker. Predicting the
ceiling before making the change is what makes this a result rather than a
rationalisation.

q14 also went 0.00 → 0.50.

### AST chunking + enrichment — a win, but on efficiency

Factual recall doubled, 0.333 → 0.667. q02 went from 0.00 to 1.00 — the Kafka
topic defaults were previously buried inside a single 100-line config blob
and are now their own chunk with a `package config` header.

Against Phase 1 at comparable context volume (Phase 1 k=5 ≈ 414 lines,
Phase 2 k=10 ≈ 220 lines), it delivers higher recall on half the tokens.
Its contribution is density, not raw recall.

### MMR — marginal, and it was buggy

MMR alone is **identical to vector on every question at k≥10**. Its only
contribution is MRR: 0.794 → 0.829 combined with hybrid. It moves correct
chunks higher without changing which chunks are present.

The first implementation had a real bug worth recording. `hybrid+mmr` scored
identically to plain `mmr` on 18 of 19 questions and *lost* hybrid's q14 gain
on the 19th. Cause: MMR recomputed relevance as cosine-to-query, which threw
away the RRF fusion and re-sorted by the vector ranking alone — demoting the
keyword-only hits that hybrid exists to recover. Fused-then-reranked is not
fused if the reranker ignores the fusion.

The fix: MMR now takes relevance from the caller and min-max normalises it,
since cosine (~0.5–0.9) and RRF (~0.016–0.033) are on incomparable scales and
the same lambda would otherwise mean different things per mode.

Honest consequence: correcting the bug made the k=5 numbers *worse*
(0.647 → 0.577). The earlier figure was a measurement artifact.

---

## Recommended configuration

```
mode=hybrid+mmr  k=10  candidates=40  lambda=0.7
recall@10 0.726 · MRR 0.829 · zero-recall 0/19 · ~700ms for all 20 questions
```

Plain `hybrid` at k=10 reaches the same recall with slightly worse MRR
(0.794 vs 0.829) and is simpler. If MMR's ordering gain does not survive
Phase 3's answer-quality scoring, drop it.

---

## The similarity-floor hypothesis

Phase 1 noticed unanswerable questions scoring lower than answerable ones.
It still holds in vector mode at k=10:

```
lowest answerable  0.5980
highest negative   0.5375
```

A margin of 0.06 across 19 questions. Enough to keep tracking, not enough to
build on.

**It cannot be measured at all in hybrid mode**, and the harness reporting
"BROKEN" there is a bug in the report, not a finding. RRF scores are derived
from ranks, so nearly every top hit scores exactly 1/(60+1) = 0.0164 whether
the question is answerable or not. The number is not a similarity and
comparing it across questions is meaningless.

---

## What is still broken

**q11 sits at 0.12** and has never moved. It needs 8 source files to trace a
request end to end, and no single-shot retrieval at any k or mode has found
more than one of them. This is the structural limit the golden set was built
to expose: some questions need iterative retrieval, not a better ranker.
Phase 5's agentic retrieval is the honest answer, and until then q11 is a
standing 0.12 that should not be explained away.

**Cross-file is 0.323** against locational's 1.000. Multi-hop is simply a
different problem from lookup.

**`decisions.md` is unreachable** from q07's phrasing by either ranker. A
seven-line file of terse conventions is a bad embedding target and contains
none of the question's keywords. Query expansion might reach it; nothing in
Phase 2 does.

---

## Next

Reranking is the one Phase 2 lever not yet built — retrieve ~40, have a model
score each candidate, keep ~8. It targets the sticky-chunk problem from
Phase 1 and should improve precision at low k, which is where hybrid and MMR
both currently hurt. It costs a model call per query, so it belongs alongside
the Phase 4 ROI comparison rather than being adopted on faith.
