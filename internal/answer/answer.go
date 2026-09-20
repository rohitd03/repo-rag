// Package answer turns retrieved chunks into a grounded answer.
package answer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/rohitd03/repo-rag/internal/store"
)

// systemPrompt is the grounding contract.
//
// The refusal instruction is not politeness, it is the whole reason the
// negative questions (q15-q17) are in the golden set. Retrieval always
// returns k chunks, even when the answer is not in the corpus at all - ask
// about Redis and you get the five least-irrelevant chunks, which look
// plausible enough for a model to build a story from. Without an explicit
// licence to say "not in the context", the model will.
const systemPrompt = `You answer questions about a source code repository using only the context provided.

Rules:
- Use only the numbered context below. Do not use general knowledge about how
  similar projects are usually built.
- Cite the source file for each claim, like (internal/consumer/lag_monitor.go).
- If the context does not contain the answer, say exactly what is missing.
  "The retrieved context does not cover X" is a correct and useful answer.
- If the question assumes something that the context contradicts, say so
  plainly instead of going along with it.
- Be specific. Name functions, types and files rather than describing them.
- Do not pad the answer. Two accurate sentences beat two vague paragraphs.`

// Generator produces an answer from a question and retrieved chunks.
type Generator interface {
	Generate(ctx context.Context, question string, hits []store.Hit) (string, error)
	Model() string
}

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
		return "No chunks were retrieved, so there is nothing to answer from. " +
			"Has the corpus been indexed?", nil
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

// buildPrompt lays out the context above the question.
//
// Numbering the chunks and labelling each with its path and line range gives
// the model something concrete to cite, and gives you something to check the
// citation against when grading.
func buildPrompt(question string, hits []store.Hit) string {
	var sb strings.Builder
	sb.WriteString("<context>\n")
	for i, h := range hits {
		fmt.Fprintf(&sb, "\n[%d] %s (lines %d-%d, similarity %.3f)\n",
			i+1, h.SourcePath, h.StartLine, h.EndLine, h.Score)
		sb.WriteString("```\n")
		sb.WriteString(h.Content)
		sb.WriteString("\n```\n")
	}
	sb.WriteString("\n</context>\n\n")
	fmt.Fprintf(&sb, "Question: %s", question)
	return sb.String()
}
