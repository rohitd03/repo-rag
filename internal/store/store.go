// Package store persists chunks and their vectors in Postgres via pgvector.
package store

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/rohitd03/repo-rag/internal/chunk"
)

//go:embed schema.sql
var schemaSQL string

// Store is a pgvector-backed chunk store.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects and applies the schema.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping %s: %w (is the postgres container up?)", dsn, err)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// ExistingHashes returns hash-by-chunk-key for a corpus, so the indexer can
// skip embedding chunks whose text has not changed. Embedding is the only
// expensive step, so this is what turns a full re-index into a fast one.
func (s *Store) ExistingHashes(ctx context.Context, corpus, model string) (map[Key]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT source_path, chunk_index, content_hash
		   FROM chunks
		  WHERE corpus = $1 AND embed_model = $2`,
		corpus, model)
	if err != nil {
		return nil, fmt.Errorf("store: load hashes: %w", err)
	}
	defer rows.Close()

	out := make(map[Key]string)
	for rows.Next() {
		var k Key
		var h string
		if err := rows.Scan(&k.SourcePath, &k.Index, &h); err != nil {
			return nil, fmt.Errorf("store: scan hash: %w", err)
		}
		out[k] = h
	}
	return out, rows.Err()
}

// Key identifies a chunk within a corpus and embedding model.
type Key struct {
	SourcePath string
	Index      int
}

// Upsert writes chunks and their vectors. len(vectors) must equal len(chunks).
func (s *Store) Upsert(ctx context.Context, corpus, model string, chunks []chunk.Chunk, vectors [][]float32) error {
	if len(chunks) != len(vectors) {
		return fmt.Errorf("store: %d chunks but %d vectors", len(chunks), len(vectors))
	}
	if len(chunks) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for i, c := range chunks {
		batch.Queue(
			`INSERT INTO chunks
			   (corpus, source_path, chunk_index, start_line, end_line,
			    content, content_hash, embed_model, embedding)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			 ON CONFLICT (corpus, source_path, chunk_index, embed_model)
			 DO UPDATE SET
			   start_line   = EXCLUDED.start_line,
			   end_line     = EXCLUDED.end_line,
			   content      = EXCLUDED.content,
			   content_hash = EXCLUDED.content_hash,
			   embedding    = EXCLUDED.embedding,
			   indexed_at   = NOW()`,
			corpus, c.SourcePath, c.Index, c.StartLine, c.EndLine,
			c.Content, c.Hash, model, pgvector.NewVector(vectors[i]),
		)
	}

	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range chunks {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("store: upsert: %w", err)
		}
	}
	return nil
}

// DeleteStale removes rows for chunks that no longer exist.
//
// Two cases, and forgetting either one leaves the index describing code that
// is gone: a file was deleted outright, or a file shrank so its trailing
// chunks no longer exist. keep maps each live file to its current chunk count.
func (s *Store) DeleteStale(ctx context.Context, corpus, model string, keep map[string]int) (int64, error) {
	paths := make([]string, 0, len(keep))
	counts := make([]int32, 0, len(keep))
	for p, n := range keep {
		paths = append(paths, p)
		counts = append(counts, int32(n))
	}

	tag, err := s.pool.Exec(ctx,
		`WITH live AS (
		     SELECT unnest($3::text[]) AS source_path,
		            unnest($4::int[])  AS chunk_count
		 )
		 DELETE FROM chunks c
		  WHERE c.corpus = $1
		    AND c.embed_model = $2
		    AND NOT EXISTS (
		          SELECT 1 FROM live l
		           WHERE l.source_path = c.source_path
		             AND c.chunk_index < l.chunk_count
		        )`,
		corpus, model, paths, counts)
	if err != nil {
		return 0, fmt.Errorf("store: delete stale: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Hit is one retrieved chunk.
type Hit struct {
	SourcePath string
	StartLine  int
	EndLine    int
	Content    string
	Score      float64 // cosine similarity, 1.0 == identical
}

// Search returns the k chunks nearest to the query vector.
//
// <=> is pgvector's cosine distance operator, and the ORDER BY is what lets
// the HNSW index serve the query. Wrapping the distance in a function, or
// filtering on it in WHERE, silently drops back to a sequential scan.
func (s *Store) Search(ctx context.Context, corpus, model string, query []float32, k int) ([]Hit, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT source_path, start_line, end_line, content,
		        1 - (embedding <=> $3) AS score
		   FROM chunks
		  WHERE corpus = $1 AND embed_model = $2
		  ORDER BY embedding <=> $3
		  LIMIT $4`,
		corpus, model, pgvector.NewVector(query), k)
	if err != nil {
		return nil, fmt.Errorf("store: search: %w", err)
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.SourcePath, &h.StartLine, &h.EndLine, &h.Content, &h.Score); err != nil {
			return nil, fmt.Errorf("store: scan hit: %w", err)
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// Stats reports what is currently indexed.
func (s *Store) Stats(ctx context.Context, corpus, model string) (chunks, files int, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT source_path)
		   FROM chunks WHERE corpus = $1 AND embed_model = $2`,
		corpus, model).Scan(&chunks, &files)
	if err != nil {
		return 0, 0, fmt.Errorf("store: stats: %w", err)
	}
	return chunks, files, nil
}
