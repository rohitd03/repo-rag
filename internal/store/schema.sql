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
