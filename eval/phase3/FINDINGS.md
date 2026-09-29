# Phase 3 — answer quality, measured

Retrieval scoring (Phase 2) says whether the right sources were found.
It cannot say whether the answer was any good. This phase scores the answers.

Config: `hybrid+mmr`, k=10, generator `qwen2.5-coder:3b`, judge
`qwen2.5-coder:7b`, both local. 20 questions, ~7 minutes.

---

## Headline

| | |
|---|---|
| Judge mean correctness | 1.45 / 2.00 |
| Scored 2 | 13/20 |
| Judge-rated faithful | 16/20 |
| **Hallucinations** | **1/20** |
| **Fabricated citations** | **2** |
| Answers containing any citation | 6/20 |
| Negatives correctly refused | 1/1 |

**What limited each answer:**

```
ok                   13
retrieval-limited     4
generation-limited    2
hallucination         1
```

That breakdown is the most useful output of the whole phase. Of the seven
non-ok answers, four are the retriever's fault and three are the generator's.
Effort should follow that ratio, and without attribution you would be
guessing.

---

## The judge is usable for triage and unreliable for precision

This was checked before any judge number was quoted, and the check failed —
so the 1.45 above is an **over-estimate** and should be read as such.

**Proof: q03.** The golden set deliberately encodes an off-by-one.
`processWithRetry` loops `attempt <= MaxRetries` with `MaxRetries = 3`, so a
message gets **4 total attempts**. The `why_it_is_hard` field even says
"models frequently answer 3 attempts, not 4."

The system answered:

> A failing message is retried up to 3 times with exponential backoff.

The judge scored it **2** — "accurately states the number of retries." It
should be a 1. The judge read past a numeric detail that was the entire point
of the question.

**Where the judge was right:** q11 (caught a fabricated component list), q12
(caught a genuinely wrong metric description), q06 (correctly passed an answer
that my own deterministic check wrongly failed).

**Where its reasoning was incoherent:** q09 scored 0 with the explanation
"the system's answer is correct because it accurately states that the
retrieved context does not cover the question." The *score* is right and the
*reason* contradicts it. A judge whose stated reasoning does not match its
own verdict cannot be trusted to explain itself, only to sort.

**Conclusion.** Use the judge to rank runs against each other and to flag
candidates for review. Do not quote its mean as an accuracy figure. The
deterministic checks are the ones that hold.

---

## q11 — what a hallucination actually looks like

Phase 2 recorded q11 as recall 0.12 and left it there. Answer scoring shows
what that number cost:

> 1. **HTTP POST Request** … **Component**: `internal/http/orders_handler.go` (lines 100-120)
> 2. **Order Creation** … **Component**: `internal/service/order_service.go` (lines 50-70)

`internal/http/` **does not exist in the repository.** The real path is
`internal/api/handler/order_handler.go`. The model invented a plausible
directory, a plausible filename, *and* plausible line numbers.

It also omitted Postgres, Debezium and Kafka entirely — the whole CDC middle
of the pipeline — and described a direct handler → service → Elasticsearch
path instead. Retrieval had returned only `order_repository.go` plus two
markdown files, so the model filled the gap with what such a system usually
looks like.

This is the failure mode worth fearing: internally coherent, specific down to
line numbers, and completely false. A reader who trusted it would go looking
for a file that was never written.

Both layers caught it independently — the deterministic fabricated-citation
check and the judge. That agreement is the reason to keep both.

---

## q09 — the refusal clause over-triggers

Phase 1 concluded that the grounding prompt was doing the heavy lifting on
refusals. That still holds, and this is the other side of it.

q09 asks why a failed message's offset is still committed. Recall was
**1.00** — `kafka_consumer.go` was retrieved and it contains both the
`sendToDLQ` call and the commit-anyway branch. The answer:

> The retrieved context does not cover why a message fails all retries and
> its offset is still committed.

A false refusal. The model had what it needed and declined anyway.

So refusal behaviour has a precision/recall trade-off like anything else:
the clause that stops q15 inventing a Redis cache also makes q09 give up on a
question it could have answered. Phase 1's "the refusal clause works" was
half the picture. Measuring only the negative questions would never have
revealed this, because it only shows up on an answerable one.

---

## Citations are rare and partly fake

Only **6 of 20** answers cite any file, despite the system prompt requiring a
citation per claim. **2 of those citations were fabricated** (both in q11).

So citation compliance is bad in both directions: mostly absent, and when
present, not automatically trustworthy. A citation rate reported on its own
would look like a mild instruction-following gap; paired with the fabrication
count it is a correctness problem.

This is a 3B model. Whether it is a capability limit or a prompt problem is
exactly the Phase 4 question, and it is now measurable rather than arguable.

---

## A bug in my own checker

The first run flagged q06 as missing the required phrase `sync.Once`. The
answer said:

> The `once.Do` function ensures that the `Metrics` instance is created only
> once.

Same fact, different words. The deterministic check was wrong and the judge
was right.

`must_contain` entries now accept `a|b` alternatives, so the golden set can
name the ways a fact may legitimately be phrased. It stays a substring test —
deterministic, no model involved — rather than being softened into a
judgement call.

Five golden entries were updated. The original run's "12 missing phrases"
overstated real failures; the corrected count is in `runs/02-after-fix.json`.

Worth being explicit: **an eval can be wrong, and a failing check is not
proof the system failed.** Checking the failures rather than accepting them
is what caught this one.

---

## What this changes for Phase 4

Phase 4 compares models. Phase 3 says what to compare them on, and the
answers are not the obvious ones:

- **Not mean correctness.** The judge is too lenient to rank close results.
- **Fabricated citations and hallucinations.** Deterministic, and they are
  what actually makes an answer dangerous.
- **False-refusal rate on answerable questions.** q09 shows this is a real
  axis and it is invisible if you only test the negative questions.
- **Attribution mix.** A bigger model should move `generation-limited` and
  `hallucination` toward zero while leaving `retrieval-limited` untouched.
  If a model change moves the retrieval-limited count, something is wrong
  with the experiment.

The 7B generator and the Claude backend are both already wired, so the
comparison is a configuration change rather than new code.
