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

// evalSeed fixes the sampler so repeated runs are comparable. Any constant
// works; what matters is that it does not change between runs being
// compared.
const evalSeed = 42

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
	// Hold the model in VRAM between requests. Without it, a 4GB GPU
	// alternating between a 3B generator and a 7B judge evicts and reloads
	// on every call, and eventually Ollama fails with "timed out waiting
	// for llama-server to start".
	KeepAlive string      `json:"keep_alive,omitempty"`
	Options   chatOptions `json:"options"`
}

type chatOptions struct {
	// Greedy decoding. This is a grounded extraction task, not a creative
	// one, and the eval depends on reproducibility.
	//
	// This was 0.1 until two identical eval runs produced 15 different
	// answers out of 20 and moved mean correctness by 0.25 - more than any
	// change being measured. "Near-greedy" is not greedy.
	Temperature float64 `json:"temperature"`

	// Fixed seed. Temperature 0 alone still leaves ties broken at random,
	// and the sampler is seeded per request.
	Seed int `json:"seed"`

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
	// No short-circuit on empty hits. The benchmark's no-context arm needs
	// the model to actually attempt the question with nothing retrieved, so
	// that what it answers from its own priors is measurable. Callers that
	// want the friendly "nothing indexed" message check for themselves.

	body, err := json.Marshal(chatRequest{
		Model: o.ModelID,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: buildPrompt(question, hits)},
		},
		Stream:    false,
		KeepAlive: "15m",
		Options:   chatOptions{Temperature: 0, Seed: evalSeed, NumCtx: 8192},
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
