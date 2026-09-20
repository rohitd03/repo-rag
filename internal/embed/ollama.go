package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Ollama embeds text using a locally running Ollama server.
//
// Free, private, and no API key. The trade-off is throughput - it runs on
// your CPU or GPU, so indexing a large corpus is slower than a hosted
// endpoint. Phase 4 measures exactly how much slower and whether the
// quality difference justifies paying.
type Ollama struct {
	BaseURL string
	ModelID string
	Dim     int
	HTTP    *http.Client
}

// NewOllama returns an Embedder backed by Ollama.
func NewOllama(baseURL, model string, dims int) *Ollama {
	return &Ollama{
		BaseURL: baseURL,
		ModelID: model,
		Dim:     dims,
		// Generous: a cold model load on first call can take a while.
		HTTP: &http.Client{Timeout: 5 * time.Minute},
	}
}

func (o *Ollama) Model() string { return o.ModelID }
func (o *Ollama) Dims() int     { return o.Dim }

type ollamaRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}

// Embed sends a batch of texts to /api/embed.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(ollamaRequest{Model: o.ModelID, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: post %s: %w (is ollama running?)", o.BaseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("ollama: status %d: %s", resp.StatusCode, buf.String())
	}

	var out ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("ollama: %s", out.Error)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama: asked for %d embeddings, got %d", len(texts), len(out.Embeddings))
	}

	// Catch a model/schema mismatch here rather than as an opaque Postgres
	// error several frames later.
	for i, v := range out.Embeddings {
		if len(v) != o.Dim {
			return nil, fmt.Errorf(
				"ollama: embedding %d has %d dims, schema expects %d - model %q may not match the vector(%d) column",
				i, len(v), o.Dim, o.ModelID, o.Dim,
			)
		}
	}

	return out.Embeddings, nil
}
