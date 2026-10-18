# Decisions

Each decision here is recorded with what it was measured against and what
would change it. Where a decision rests on an opinion rather than a number,
it says so.

Measurements come from `eval/phase4/runs/`, 20 golden questions over the
`cdc-pipeline` corpus (48 files, ~59k tokens).

---

## The benchmark these decisions rest on

Five arms. Identical generator, prompt and scoring; only context selection
varies.

| arm | missing phrases | hallucinations | fabricated citations | context bytes | 20q gen time |
|---|---:|---:|---:|---:|---:|
| no-context | 20 | 3 | 3 | 0 | 9 s |
| random-k | 16 | 1 | 1 | 7,172 | 75 s |
| truncated-corpus | 16 | 4 | 2 | 102,255 | 65 s |
| **rag/3b** | **8** | **0** | **0** | 6,992 | 38 s |
| rag/7b | 10 | 0 | 0 | 6,992 | 133 s |

"Missing phrases" counts required facts absent from the answer, checked by
exact substring with permitted alternatives. It is deterministic and it is
the primary ranking metric — see the judge decision below for why.

---

## Retrieval is required here, not an optimization

Phase 0 measured the corpus at ~59k tokens and concluded it fits in a modern
context window, making retrieval merely a cost optimization. That was true of
a 200k frontier model and false of the models actually in use.

`qwen2.5-coder` holds **32,768 tokens**. The corpus is roughly **1.8× larger**.
The truncated-corpus arm fits 118 of 359 chunks — a third of the repository.

So for a locally hosted model there is no "just paste it all in" option. The
choice is retrieval or an incomplete corpus.

**What would change it:** a local model with a 128k+ window, or a corpus under
~25k tokens. At that point the truncated arm becomes a real competitor and
this should be re-measured rather than assumed.

---

## More context made the answers worse

The sharpest result in the set. truncated-corpus sends **14.6× more context**
than rag/3b and produces twice the missing phrases and the **most
hallucinations of any arm** — including, while holding 102 KB of this
repository:

- naming `sarama` as the Kafka library (it is `segmentio/kafka-go`)
- citing `internal/consumer/consumer.go` (does not exist)
- asserting a `go-redis` dependency (there is no Redis anywhere in the corpus)

rag/3b, given 7 KB of focused context, answered the Redis question correctly
by declining.

This is the "lost in the middle" effect, measured rather than cited. For a
small model, context volume is not a proxy for context quality, and padding
the window actively degrades grounding.

---

## Ranking is worth a halving of the error rate at equal cost

random-k is the control that justifies the retrieval stack. It holds context
volume essentially constant — 7,172 bytes against 6,992 — and removes **only**
the ranking.

```
random-k   16 missing phrases, 1 hallucination, 1 fabricated citation
rag/3b      8 missing phrases, 0 hallucinations, 0 fabricated citations
```

Same budget, half the errors. Without this arm, "recall improved from 0.63 to
0.73" is a statement about a number; with it, the ranking is shown to be
load-bearing.

**What would change it:** a corpus where most chunks are relevant to most
questions. The gap narrows as the corpus becomes more homogeneous.

---

## Generation model: `qwen2.5-coder:3b`, not the 7B

The ROI decision, and it went against expectation.

| | missing | hallucinations | 20q gen time |
|---|---:|---:|---:|
| rag/3b | 8 | 0 | 38 s |
| rag/7b | 10 | 0 | 133 s |

The 7B is **3.5× slower and shows no improvement**. On this hardware — an RTX
4050 with 4 GB of VRAM — the 3B fits entirely in VRAM while the 7B partially
offloads to CPU, which is most of the latency difference.

Stated carefully: 8 versus 10 on 20 questions is a small difference from a
single run of the 7B, so the honest claim is *no measurable improvement*, not
*worse*. Either way it does not earn 3.5× the latency.

**What would change it:** more VRAM, so the 7B runs fully on GPU; or a
question set with harder multi-hop reasoning, where capacity should matter
more than it does on mostly-lookup questions.

---

## No hosted model, and the cost is not what the pricing page says

A hosted frontier model was deliberately excluded. The direct cost would have
been roughly $0.12–$0.55 per 20-question run — genuinely small.

The finding is that the local option's real cost is not accuracy. It is that
**4 GB of VRAM cannot hold a generator and a judge at the same time**. The
first benchmark implementation interleaved generate-then-judge per question;
Ollama evicted and reloaded a model on every call, about 200 loads, and died
at question 13 with `timed out waiting for llama-server to start`.

The fix was to restructure into two passes — generate everything, then judge
everything — cutting model loads from ~200 to 3, plus `keep_alive` to hold a
model resident within a pass.

That architectural constraint is the price of running locally, and no pricing
comparison surfaces it.

**What would change it:** a GPU that holds both models, which removes the
constraint entirely and makes the two-pass structure an optimisation rather
than a requirement.

---

## Rank on deterministic metrics, not on the judge

The LLM judge is used for triage and is **not** the basis of any decision
here. It was validated against known-correct answers before being trusted,
and it failed twice:

- **q03** encodes a deliberate off-by-one — `processWithRetry` loops
  `attempt <= MaxRetries`, so 3 retries is 4 attempts. The answer said "up to
  3 times" and the judge scored it 2 of 2.
- **q11** asks to trace an order end to end. The answer never mentions
  Postgres, Debezium or Kafka. The judge scored it 2 and called it faithful;
  the deterministic check caught it immediately as missing `[Debezium, Kafka]`.

Both times the layers disagreed, the deterministic check was right. So
`missing phrases`, `hallucinations` and `fabricated citations` rank the arms,
and judge scores are kept separate rather than averaged in.

Where the judge is used, correctness is graded **blind** — question, ground
truth and answer, never the context. An answer built from 118 chunks versus
none at all is identifiable at a glance, and a judge that can tell the arms
apart is not blind.

---

## Reproducibility holds in aggregate, not per answer

Temperature 0 and a fixed seed on both generator and judge. Two identical
runs in Phase 3 produced 20/20 identical answers.

Across runs with **different sets of models resident**, that weakens: 3–5 of
20 answers differ per arm, most likely because GPU layer offload changes when
another model shares the device, altering floating-point reduction order.

The aggregate metrics are stable regardless — missing-phrase counts moved by
at most 1 (21→20, 16→16, 16→16, 8→8).

So: rank configurations on aggregate deterministic metrics. Do not read
meaning into a single question flipping between runs. Before this was fixed,
two identical runs differed on 15 of 20 answers and moved mean correctness by
0.25 — larger than any effect being measured.

---

## Vector store: Postgres with pgvector

No dedicated vector database. The corpus is ~359 chunks; pgvector handles
several orders of magnitude more.

The decisive factor is not scale but that hybrid search needs both rankings in
**one query**. The keyword half is a generated `tsvector` column on the same
table, fused with the vector ranking via RRF in a single statement. Splitting
the two across separate systems would mean two round trips and
application-side fusion for no benefit.

It also means permission filtering could happen inside the query rather than
after it — the usual failure mode in multi-tenant retrieval — though this
project has no such requirement.

**What would change it:** roughly 10M+ vectors, or a requirement for
distributed sharding.

---

## Chunking: AST boundaries, not fixed size

Fixed-size windows averaged 82.7 lines and made 21 of 48 files a single
indivisible chunk; retrieval could only discriminate between files. Splitting
Go at declaration boundaries and Markdown at headings gives 359 chunks of 22
lines.

Measured effect: factual recall 0.333 → 0.667, and q02 went 0.00 → 1.00
because the Kafka defaults were no longer buried inside a 100-line config
blob.

A second effect was not the goal and matters more operationally: chunks
anchored to declarations are **stable under edits elsewhere in the file**.
Editing a doc comment near the top of a 10-chunk file re-embeds 1 of 331
chunks. Under fixed-size chunking every boundary below the edit shifted and
the whole file re-embedded.

---

## Retrieval mode: hybrid + MMR at k=10

| k | vector | hybrid | hybrid+mmr |
|---|---|---|---|
| 5 | 0.629 | 0.603 | 0.577 |
| **10** | 0.700 | 0.726 | **0.726** |
| 15 | 0.700 | 0.726 | 0.726 |
| 20 | 0.717 | 0.743 | 0.743 |

k=10 is the knee. k=15 buys nothing; k=20 buys 0.017 recall for double the
tokens.

Hybrid search is the real contributor — cross-file recall 0.198 → 0.323 at
every k, and the only change that eliminates zero-recall questions. MMR is
marginal, identical to plain vector on every question at k≥10, and improves
only ordering (MRR 0.794 → 0.829).

Note the inversion at k=5: both hybrid and MMR score *worse* than plain
vector. These techniques need headroom, which a single-k measurement would
have hidden.

**MMR is the weakest-justified choice here.** It survives on an ordering
improvement that the answer-quality metrics do not clearly reward. Plain
hybrid at k=10 reaches the same recall and is simpler.

---

## Infrastructure: Terraform, not docker-compose

Compose describes a desired state but keeps no record of what it created, so
it cannot report what changed and cannot reliably clean up. Terraform keeps
state, which is what makes `plan` meaningful and `destroy` complete.

Verified on the full lifecycle, not just apply: `destroy` leaves no container,
volume or image, and a re-apply plus re-index reproduces the metrics exactly.

The AWS module is written and validates but **has never been applied** — there
is no AWS account attached to this project. It is labelled that way in its own
header and in `terraform/README.md` rather than left for a reader to assume.

---

## When this would not be worth building

Recorded because a decision log that only justifies what was built is
advocacy.

- **Corpus under ~25k tokens.** It fits in a local context window with room
  to spare, and the truncated arm stops being handicapped. Paste it in.
- **A frontier model with a 200k+ window and a budget.** Phase 0's original
  conclusion applies again — retrieval becomes a cost optimisation worth
  roughly 20× fewer tokens per query, not a correctness requirement.
- **Questions needing many files at once.** q11 requires 8 source files and
  sits at 0.12 recall under every mode and every k tried. Single-shot
  retrieval cannot do multi-hop, and no amount of ranking fixes it. That needs
  agentic retrieval, which is not built.
- **A corpus of mostly-relevant chunks.** The random-k gap is what justifies
  the ranking; it narrows as the corpus becomes homogeneous.
