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

## 3. Anthropic API key (generation only)

```bash
export ANTHROPIC_API_KEY=sk-ant-...
```

Or run `ant auth login` — the SDK picks up the stored profile with no env var.

Only `rag ask` needs this. Indexing and retrieval work without it, which is
why `make smoke` exists.

---

## Verify, in order

```bash
make test      # chunker unit tests - no services needed
make index     # needs Ollama + Postgres
make stats     # should report 48 files
make smoke     # retrieval only, no API key, no tokens spent
```

If `make smoke` prints five chunks with similarity scores, everything except
generation is working. Then:

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
| `unauthorized` | No `ANTHROPIC_API_KEY` and no `ant auth login` profile |
| `no chunks found for corpus` | `make index` hasn't been run, or `-corpus` name is misspelled |

---

## Configuration

| Variable | Default |
|---|---|
| `RAG_DATABASE_URL` | `postgres://rag:rag@localhost:5433/rag?sslmode=disable` |
| `RAG_OLLAMA_URL` | `http://localhost:11434` |
| `RAG_EMBED_MODEL` | `nomic-embed-text` |
| `RAG_GEN_MODEL` | `claude-opus-5` |

`RAG_GEN_MODEL` and `RAG_EMBED_MODEL` are the knobs Phase 4 sweeps. Changing
the embedding model writes a separate set of rows rather than overwriting the
existing ones — `embed_model` is part of the uniqueness key — so two models can
be compared without destroying the index in between.
