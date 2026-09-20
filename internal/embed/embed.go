// Package embed turns text into vectors.
//
// Everything here sits behind the Embedder interface from day one. That is
// not speculative generality - Phase 4 has to swap implementations and
// measure the difference in recall, indexing time and cost, and you cannot
// compare two models if the calling code is welded to one of them.
package embed

import "context"

// Embedder converts text into fixed-length vectors.
//
// The same implementation must embed both the documents at index time and
// the query at search time. Mixing models across those two paths produces
// vectors in different spaces, and the search silently returns nonsense
// rather than failing.
type Embedder interface {
	// Embed returns one vector per input, in the same order.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Model identifies which model produced the vectors. Stored on every
	// row so a model swap is detectable rather than silently corrupting
	// the index.
	Model() string

	// Dims is the vector length. Must match the column type in the schema.
	Dims() int
}
