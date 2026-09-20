package answer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/rohitd03/repo-rag/internal/store"
)

// Claude is a Generator backed by the Anthropic Messages API.
type Claude struct {
	client  anthropic.Client
	modelID string
}

// NewClaude builds a generator. The SDK reads ANTHROPIC_API_KEY itself, and
// also picks up an `ant auth login` profile when no key is set.
func NewClaude(modelID string) *Claude {
	return &Claude{client: anthropic.NewClient(), modelID: modelID}
}

func (c *Claude) Model() string { return c.modelID }

// Generate asks the model to answer strictly from hits.
func (c *Claude) Generate(ctx context.Context, question string, hits []store.Hit) (string, error) {
	if len(hits) == 0 {
		return noContext, nil
	}

	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.modelID),
		MaxTokens: 16000,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(buildPrompt(question, hits))),
		},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 401:
				return "", fmt.Errorf("answer: unauthorized - set ANTHROPIC_API_KEY or run `ant auth login`: %w", err)
			case 404:
				return "", fmt.Errorf("answer: model %q not found: %w", c.modelID, err)
			case 429:
				return "", fmt.Errorf("answer: rate limited, retry shortly: %w", err)
			}
		}
		return "", fmt.Errorf("answer: generate: %w", err)
	}

	// A safety decline arrives as HTTP 200 with this stop reason, so it has
	// to be checked before reading content.
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", fmt.Errorf("answer: model declined (%s): %s",
			resp.StopDetails.Category, resp.StopDetails.Explanation)
	}

	var sb strings.Builder
	for _, block := range resp.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			sb.WriteString(text.Text)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}
