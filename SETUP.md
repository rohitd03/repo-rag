# Setup

Three things must be running before `rag ask` works. Do them in this order —
each step is checkable on its own, so a failure tells you which piece is wrong.

---

## 1. Ollama (embeddings)

Not currently installed. Either:

**Native** — download from [ollama.com](https://ollama.com/download), then:

```bash
ollama pull nomic-embed-text
```

**Docker:**

```bash
docker run -d -v ollama:/root/.ollama -p 11434:11434 --name ollama ollama/ollama
docker exec ollama ollama pull nomic-embed-text
```

`nomic-embed-text` is ~275 MB and produces 768-dimensional vectors, which is
what `vector(768)` in the schema expects. Swapping to a model with different
dimensions means changing the column type *and* re-indexing.

Check it:

```bash
curl -s http://localhost:11434/api/embed \
  -d '{"model":"nomic-embed-text","input":"hello"}' | head -c 120
```

You want a JSON array of floats back.

---

## 2. Postgres with pgvector

Docker Desktop is installed but was not running. Start it, then:

```bash
make up
```

This runs `pgvector/pgvector:pg16` on **port 5433** — deliberately not 5432,
so it can coexist with the `cdc-pipeline` database.

Check it:

```bash
docker compose exec postgres psql -U rag -d rag -c "SELECT extname FROM pg_extension;"
```

You want `vector` in the list. The schema is applied automatically by the
`rag` binary on first connect, so there is no separate migrate step.

---

## 3. A generation model

Generation defaults to a **local model via Ollama** — no API key, no spend:

```bash
ollama pull qwen2.5-coder:7b
```

Your RTX 4050 has 4 GB of VRAM, so the 7B (~4.7 GB at Q4) partially offloads
to CPU and runs slower. `qwen2.5-coder:3b` (~1.9 GB) fits entirely on the GPU
and is much faster to iterate with:

```bash
ollama pull qwen2.5-coder:3b
RAG_GEN_MODEL=qwen2.5-coder:3b ./bin/rag ask -corpus cdc-pipeline "..."
```

Phase 4 compares them, so pulling both is worthwhile.

### Optional: Claude instead

A Claude subscription does **not** include API access — they are separately
billed products. If you want to compare against a frontier model, create a key
at [console.anthropic.com](https://console.anthropic.com) and:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
export RAG_GEN_BACKEND=claude
```

A full 20-question eval run costs roughly $0.12 (Haiku 4.5) to $0.55 (Opus 5).
Nothing else in the project needs a key.

---

## Verify, in order

```bash
make test      # chunker unit tests - no services needed
make index     # needs Ollama + Postgres
make stats     # should report 48 files
make smoke     # retrieval only, no API key, no tokens spent
```

If `make smoke` prints five chunks with similarity scores, everything except
generation is working. Then (no API key needed):

```bash
./bin/rag ask -corpus cdc-pipeline -show-sources \
  "why does the lag monitor define a kafkaQuerier interface?"
```

---

## Troubleshooting

| Symptom | Cause |
|---|---|
| `is ollama running?` | Ollama not started, or on a non-default port — set `RAG_OLLAMA_URL` |
| `embedding 0 has N dims, schema expects 768` | Wrong embedding model pulled |
| `is the postgres container up?` | `make up`, or Docker Desktop isn't running |
| `type "vector" does not exist` | Using a plain `postgres` image instead of `pgvector/pgvector` |
| `model "X" not pulled` | `ollama pull X` |
| `unauthorized` | `RAG_GEN_BACKEND=claude` with no `ANTHROPIC_API_KEY` |
| `no chunks found for corpus` | `make index` hasn't been run, or `-corpus` name is misspelled |

---

## Configuration

| Variable | Default |
|---|---|
| `RAG_DATABASE_URL` | `postgres://rag:rag@localhost:5433/rag?sslmode=disable` |
| `RAG_OLLAMA_URL` | `http://localhost:11434` |
| `RAG_EMBED_MODEL` | `nomic-embed-text` |
| `RAG_GEN_BACKEND` | `ollama` (or `claude`) |
| `RAG_GEN_MODEL` | `qwen2.5-coder:7b` for ollama, `claude-opus-5` for claude |

`RAG_GEN_MODEL` and `RAG_EMBED_MODEL` are the knobs Phase 4 sweeps. Changing
the embedding model writes a separate set of rows rather than overwriting the
existing ones — `embed_model` is part of the uniqueness key — so two models can
be compared without destroying the index in between.
