# Phase 0 Baseline — cdc-pipeline, no RAG

The control group. Everything built in Phases 1–4 gets compared against this
sheet. If a later phase cannot beat these numbers, it did not earn its
complexity.

Corpus snapshot: commit `78ab775`, captured 2026-09-20.

---

## Corpus economics

Measured by `scripts/pack-corpus.sh ../cdc-pipeline`:

| | |
|---|---|
| Files | 48 |
| Lines | 8,080 |
| Bytes | 251,474 |
| Tokens (est. 4 B/token) | **~62,900** |

**The whole repository fits in a single 200k-token context window, roughly
three times over.**

Write that down, because it frames the entire project. For this corpus,
retrieval is not required — it is an *optimization*. Which means every RAG
change from here has to justify itself against a baseline that already has
perfect recall by construction. That is a hard baseline to beat on accuracy,
and an easy one to beat on cost and latency.

Per-query cost of the no-RAG approach is `~62,900 input tokens × model input
rate`, on every single question, with no caching. A RAG query sends roughly
2–4k tokens instead. Fill in the actual rates in Phase 4 — the ratio is the
part that matters and it is about 20:1.

---

## Protocol

Run this **before writing any retrieval code**. It takes about an hour and it
is the phase most people skip.

1. Open a fresh chat with a long-context model. One conversation per question —
   do **not** let earlier answers contaminate later ones.
2. Paste the entire contents of `eval/baseline/corpus.txt`, then ask exactly the
   `question` field from `eval/goldens/cdc-pipeline.yaml`. Do not add hints.
3. Score the answer against `ground_truth` in the golden file:

   | Score | Meaning |
   |---|---|
   | **2** | Correct and complete. Would satisfy a reviewer. |
   | **1** | Directionally right but missing a key part, or hedged into uselessness. |
   | **0** | Wrong, or fabricated something not in the repo. |

4. A `must_not_contain` hit is an automatic **0**, regardless of how good the
   rest of the answer reads. Hallucination is not partial credit.
5. Record *how* it failed in Notes, not just that it failed. The failure mode
   is the data you will use in Phase 2.

---

## Scores

| ID | Category | Difficulty | Score | Notes — what it got right / how it failed |
|----|----------|-----------|:-----:|--------------------------------------------|
| q01 | factual | easy | | |
| q02 | factual | easy | | |
| q03 | factual | medium | | *watch for the 3-vs-4 attempts off-by-one* |
| q04 | locational | easy | | |
| q05 | locational | medium | | |
| q06 | locational | medium | | *did it find `sync.Once`?* |
| q07 | reasoning | medium | | *did it cite `decisions.md` as well as the comment?* |
| q08 | reasoning | medium | | *did it connect the .sql file to the Go handler?* |
| q09 | reasoning | hard | | *did it name the DLQ-write-fails edge case?* |
| q10 | reasoning | medium | | |
| q11 | cross-file | hard | | *how many of the 8 components did it name?* |
| q12 | cross-file | hard | | *did it distinguish messages-behind from seconds-behind?* |
| q13 | cross-file | hard | | *did it notice retrying a parse failure is pointless?* |
| q14 | cross-file | medium | | *did it use the word CQRS unprompted?* |
| q15 | negative | easy | | *did it refuse, or invent Redis?* |
| q16 | negative | medium | | *did it assert auth exists somewhere?* |
| q17 | negative | medium | | *did it conflate the admin client with the group member?* |
| q18 | reasoning | hard | | |
| q19 | reasoning | hard | | |
| q20 | factual | medium | | *did it spot there is no version table?* |

---

## Totals

```
Total score            __ / 40
Strict accuracy        __ / 20    (score == 2 only)
Hallucination count    __         (any must_not_contain hit)

By category                       By difficulty
  factual      __ / 8               easy    __ / 8
  locational   __ / 6               medium  __ / 20
  reasoning    __ / 12              hard    __ / 12
  cross-file   __ / 8
  negative     __ / 6
```

Mean latency per question: `__ s`

---

## Interpretation guide

Fill this in after scoring. What you conclude here decides whether Phases 1–4
are worth doing, and it is the honest framing you will use when describing the
project to someone else.

**If strict accuracy ≥ 18/20** — context-stuffing already works on this corpus.
RAG's win is cost and latency, not quality, and you should say so plainly.
That is still a completely valid engineering result; "we measured and chose the
simpler thing" is a stronger story than a RAG built on faith.

**If 12–17/20** — there is real headroom. Look at *which* categories failed.
Cross-file failures usually mean the model got lost in a large context (the
"lost in the middle" effect), and good retrieval genuinely fixes that by
raising signal density.

**If ≤ 11/20** — check the questions before blaming the model. Ambiguous
wording is the likelier culprit. Rewrite the question, do not lower the bar.

**Watch specifically for:** negative questions (q15–q17) scoring worse than
factual ones. If the model fabricates a Redis cache when handed the entire
repo that provably contains none, that is a *prompt and grounding* problem, and
retrieval alone will not fix it. Knowing which failures retrieval can and
cannot fix is the whole point of running this phase first.

---

## Conclusion

> Write 3–5 sentences here once scored. State whether RAG is justified for this
> corpus and on what grounds — accuracy, cost, latency, or none. This paragraph
> is the thing you will actually say out loud in an interview, so make it
> defensible rather than flattering.
