# Phase 3 — answer quality, measured

Retrieval scoring (Phase 2) says whether the right sources were found. It
cannot say whether the answer was any good. This phase scores the answers —
and the first thing it found was that the harness itself was not yet
trustworthy.

Config: `hybrid+mmr`, k=10, generator `qwen2.5-coder:3b`, judge
`qwen2.5-coder:7b`, both local. 20 questions, ~7 minutes per run.

---

## First: the results were not reproducible, and that invalidated the first two runs

Two runs of an identical configuration produced **15 different answers out of
20**, and three questions flipped their judge score. Mean correctness moved
from 1.45 to 1.70 — a larger swing than any change being measured.

An earlier version of this document reported that 1.45 → 1.70 as evidence
that a fix had helped. It was not. It was noise, and the claim was wrong.

**Cause.** The generator ran at `temperature: 0.1` and Ollama seeds its
sampler per request. Near-greedy is not greedy, and an unseeded sampler is
not reproducible even at temperature 0, because ties still get broken at
random.

**Fix.** `temperature: 0` and a fixed seed on both the generator and the
judge. Verified by running the identical config twice and diffing:

```
identical answers  20/20
judge score flips  0
```

Everything below comes from the deterministic runs. The earlier ones are kept
in `runs/` as the evidence for this section, not as results.

**Why this is the most important finding in the phase:** Phase 4 exists to
compare models. If run-to-run noise moves the headline metric by 0.25, a
genuine 0.2 improvement from a better model is unmeasurable. The comparison
would have produced confident numbers that meant nothing.

---

## Results

| | |
|---|---|
| Judge mean correctness | 1.60 / 2.00 |
| Judge scored 2 | 14/20 |
| Judge-rated faithful | 16/20 |
| Hallucinations | 0/20 |
| Fabricated citations | 0 |
| Answers containing a citation | 5/20 |
| Missing required phrases | 8 |
| Negatives correctly refused | 1/1 |

**What limited each answer:**

```
ok                   14
retrieval-limited     4
generation-limited    2
```

That breakdown is the most actionable output of the phase. Of six imperfect
answers, four are the retriever's fault and two the generator's. Effort
should follow that ratio.

---

## The judge is usable for triage and not for scoring

Checked before quoting any judge number, and it failed twice. Both failures
are cases where the **deterministic check was right and the judge was wrong**.

### q03 — misses a numeric detail that is the whole question

The golden set deliberately encodes an off-by-one: `processWithRetry` loops
`attempt <= MaxRetries` with `MaxRetries = 3`, so a message gets **4 total
attempts**. The `why_it_is_hard` field says so explicitly.

The answer: *"retried up to 3 times."* The judge scored it **2** —
"accurately states the number of retries."

### q11 — scores 2 on an answer missing two-thirds of the pipeline

q11 asks to trace an order from HTTP POST to Elasticsearch. The answer names
the handler, the repository and the Elasticsearch client. It never mentions
**Postgres, Debezium or Kafka** — the entire CDC middle of the system that
the project is named after.

The judge scored it **2** and marked it **faithful**. The deterministic check
caught it immediately: `missing: [Debezium, Kafka]`.

### Where the layers disagree, so far the deterministic check has won

```
judge said 2 but a required phrase is absent:  q05, q11
```

**Conclusion.** Use the judge to rank runs against each other and to flag
candidates for a human look. Do not quote `1.60` as an accuracy figure — it
is an over-estimate, and two of the 14 answers it scored 2 demonstrably are
not.

---

## q09 — the refusal clause over-triggers

Phase 1 concluded the grounding prompt was doing the heavy lifting on
refusals. True, and here is the other side.

q09 asks why a failed message's offset is still committed. Recall was
**1.00** — `kafka_consumer.go` was retrieved, containing both the `sendToDLQ`
call and the commit-anyway branch. The answer:

> The retrieved context does not cover why a message fails all retries and
> its offset is still committed.

A false refusal on a question it had everything to answer.

So refusal has a precision/recall trade-off like anything else: the clause
that stops q15 inventing a Redis cache also makes q09 give up. Phase 1's
"the refusal clause works" was half the picture, and testing only the
negative questions would never have shown the other half.

---

## Citations are rare

**5 of 20** answers cite any file, despite the system prompt requiring a
citation per claim.

At temperature 0 there were **zero fabricated citations**. The earlier runs
had 2 and then 4 — so the fabrication rate was itself sampling noise, which
is its own argument for the determinism fix. Under greedy decoding this model
either cites correctly or does not cite at all.

Whether low citation compliance is a capability limit or a prompt problem is
a Phase 4 question, and it is now measurable rather than arguable.

---

## A bug in my own checker

The first run flagged q06 for missing `sync.Once` when the answer said
`once.Do` — the same fact in different words. The checker was wrong and the
judge was right.

`must_contain` entries now accept `a|b` alternatives, so the golden set can
name legitimate phrasings while staying a deterministic substring test rather
than being softened into a judgement call. Five entries updated.

**An eval can be wrong, and a failing check is not proof the system failed.**
Investigating the failures rather than accepting them is what caught this.

---

## What Phase 4 must measure

Phase 3 says what to compare models on, and the answers are not the obvious
ones:

- **Not mean correctness.** The judge is too lenient to rank close results,
  and q03 and q11 prove it.
- **Missing required phrases.** Deterministic, and it caught what the judge
  missed on both failures.
- **Hallucinations and fabricated citations.** Deterministic, and they are
  what makes an answer dangerous rather than merely incomplete.
- **False-refusal rate on answerable questions.** q09 shows this is a real
  axis, invisible if you only test negatives.
- **Attribution mix.** A better generator should push `generation-limited`
  toward zero while leaving `retrieval-limited` untouched. If a model swap
  moves the retrieval-limited count, the experiment is broken.

And a precondition, now satisfied: **every configuration must be reproducible
before any two are compared.**
