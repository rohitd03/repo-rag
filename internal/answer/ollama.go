package answer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rohitd03/repo-rag/internal/store"
)

// Ollama is a Generator backed by a local model served by Ollama.
//
// Free and private, with two costs worth measuring rather than assuming:
// a small local model is weaker at multi-hop reasoning than a frontier
// model, and it is slower per token on consumer hardware. Phase 4 puts
// numbers on both by running the same golden set through this and through
// Claude.
type Ollama struct {
	BaseURL string
	ModelID string
	HTTP    *http.Client
}

// NewOllama returns a Generator backed by a local Ollama model.
func NewOllama(baseURL, model string) *Ollama {
	return &Ollama{
		BaseURL: baseURL,
		ModelID: model,
		// A cold model load plus generation on a laptop GPU is slow. This
		// is a backstop against a hang, not a latency target.
		HTTP: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (o *Ollama) Model() string { return o.ModelID }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Options  chatOptions   `json:"options"`
}

type chatOptions struct {
	// Near-greedy decoding. This is a grounded extraction task, not a
	// creative one - the answer should be reproducible across runs, or the
	// eval measures sampling noise as much as it measures the model.
	Temperature float64 `json:"temperature"`

	// The context window the model is loaded with. Five chunks of ~83 lines
	// plus the system prompt runs well past the 2048 default, and Ollama
	// silently truncates from the start rather than erroring - which would
	// quietly drop the retrieved context and leave the question behind.
	NumCtx int `json:"num_ctx"`
}

type chatResponse struct {
	Message chatMessage `json:"message"`
	Error   string      `json:"error,omitempty"`
}

// Generate asks the local model to answer strictly from hits.
func (o *Ollama) Generate(ctx context.Context, question string, hits []store.Hit) (string, error) {
	if len(hits) == 0 {
		return noContext, nil
	}

	body, err := json.Marshal(chatRequest{
		Model: o.ModelID,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: buildPrompt(question, hits)},
		},
		Stream:  false,
		Options: chatOptions{Temperature: 0.1, NumCtx: 8192},
	})
	if err != nil {
		return "", fmt.Errorf("answer: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("answer: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("answer: post %s: %w (is ollama running?)", o.BaseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		msg := buf.String()
		if resp.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("answer: model %q not pulled - run `ollama pull %s`: %s",
				o.ModelID, o.ModelID, msg)
		}
		return "", fmt.Errorf("answer: ollama status %d: %s", resp.StatusCode, msg)
	}

	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("answer: decode response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("answer: ollama: %s", out.Error)
	}

	return strings.TrimSpace(out.Message.Content), nil
}
