CREATE EXTENSION IF NOT EXISTS vector;

-- One row per chunk of source text.
--
-- embed_model is part of the identity, not just metadata. Vectors from
-- different embedding models live in different number spaces and cannot be
-- compared, so swapping models means a fresh set of rows rather than an
-- update. Keeping the model in the key lets two models coexist during a
-- Phase 4 comparison instead of forcing a destructive re-index.
CREATE TABLE IF NOT EXISTS chunks (
    id           BIGSERIAL PRIMARY KEY,
    corpus       TEXT        NOT NULL,
    source_path  TEXT        NOT NULL,
    chunk_index  INT         NOT NULL,
    start_line   INT         NOT NULL,
    end_line     INT         NOT NULL,
    content      TEXT        NOT NULL,

    -- sha256 of content. Lets re-indexing skip the embedding call, which is
    -- the only slow part of indexing.
    content_hash TEXT        NOT NULL,

    -- Provenance, used to enrich the embedded text and to display results.
    package      TEXT        NOT NULL DEFAULT '',
    kind         TEXT        NOT NULL DEFAULT '',
    symbol       TEXT        NOT NULL DEFAULT '',

    embed_model  TEXT        NOT NULL,
    embedding    vector(768) NOT NULL,
    indexed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (corpus, source_path, chunk_index, embed_model)
);

-- HNSW with cosine distance. nomic-embed-text is trained for cosine
-- similarity, so vector_cosine_ops is the matching operator class - using
-- L2 here would quietly degrade recall rather than fail.
CREATE INDEX IF NOT EXISTS idx_chunks_embedding
    ON chunks USING hnsw (embedding vector_cosine_ops);

-- Supports the delete-stale-rows pass at the end of indexing.
CREATE INDEX IF NOT EXISTS idx_chunks_lookup
    ON chunks (corpus, embed_model, source_path);

-- Added after the initial schema; ALTER keeps an existing database usable.
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS package TEXT NOT NULL DEFAULT '';
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS kind    TEXT NOT NULL DEFAULT '';
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS symbol  TEXT NOT NULL DEFAULT '';

-- Keyword half of hybrid search.
--
-- Vector search cannot find a rare identifier: "kafkaQuerier" has no
-- semantic neighbours, so cosine similarity ranks it no higher than any
-- other Kafka code. Full-text search matches it exactly. The two fail in
-- different places, which is why fusing them beats either alone.
--
-- The symbol and path are included in the document so a query naming a
-- function finds that function's chunk even when the body never repeats
-- the name.
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS tsv tsvector
    GENERATED ALWAYS AS (
        to_tsvector('english',
            coalesce(symbol, '') || ' ' ||
            replace(coalesce(source_path, ''), '/', ' ') || ' ' ||
            coalesce(content, ''))
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_chunks_tsv ON chunks USING gin (tsv);
