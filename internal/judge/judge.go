// Package judge scores generated answers with a model.
//
// A judge is itself a model and can be wrong, so everything here is designed
// to be checkable: the prompt asks for a structured verdict rather than
// prose, the judge sees the retrieved context so faithfulness is decidable
// rather than guessed, and the caller is expected to validate the judge
// against hand-scored answers before trusting it. See eval/phase3 for the
// agreement check.
package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Verdict is one graded answer.
type Verdict struct {
	// Correctness against ground truth: 2 correct and complete, 1
	// directionally right but incomplete, 0 wrong.
	Correctness int `json:"correctness"`

	// Faithful means every claim is supported by the retrieved context.
	// An answer can be faithful and still score 0 - that is the case where
	// retrieval failed and the model behaved correctly given bad input.
	Faithful bool `json:"faithful"`

	Reason string `json:"reason"`
}

// Judge grades an answer.
type Judge interface {
	Grade(ctx context.Context, question, groundTruth, retrievedContext, answer string) (Verdict, error)
	Model() string
}

const systemPrompt = `You grade answers produced by a code question-answering system. Reply with JSON only.

You are given a question, the correct answer, the context the system retrieved, and the system's answer.

Return exactly this shape:
{"correctness": 0|1|2, "faithful": true|false, "reason": "one sentence"}

correctness, judged against the CORRECT ANSWER:
  2 - matches the correct answer on every substantive point
  1 - directionally right but missing a substantive point, or hedged into uselessness
  0 - wrong, contradicts the correct answer, or states something absent from the retrieved context

faithful, judged against the RETRIEVED CONTEXT only:
  true  - every claim in the system's answer is supported by the retrieved context
  false - the answer asserts something the retrieved context does not support

These two are independent and often disagree. An answer that correctly says
"the retrieved context does not cover this" is faithful; score its correctness
against whether saying so was the right response. An answer that happens to be
correct by guessing something not in the context is unfaithful, and that is a
real failure even though the words are right.

Be strict. Do not award 2 for an answer that is merely plausible.
Output JSON only, no prose before or after.`

// Ollama is a Judge backed by a local model.
type Ollama struct {
	BaseURL string
	ModelID string
	HTTP    *http.Client
}

// NewOllama returns a local judge.
//
// Use a different, stronger model than the one that produced the answers.
// A model grading its own output is a known bias - it recognises its own
// phrasing as correct - and the whole point of the judge is independence.
func NewOllama(baseURL, model string) *Ollama {
	return &Ollama{
		BaseURL: baseURL,
		ModelID: model,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
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
	Format   string        `json:"format,omitempty"`
	Options  chatOptions   `json:"options"`
}

type chatOptions struct {
	Temperature float64 `json:"temperature"`
	Seed        int     `json:"seed"`
	NumCtx      int     `json:"num_ctx"`
}

// evalSeed matches the generator's. Temperature 0 alone does not make a
// local model reproducible - ties are still broken by a per-request seed.
const evalSeed = 42

type chatResponse struct {
	Message chatMessage `json:"message"`
	Error   string      `json:"error,omitempty"`
}

// Grade asks the judge for a verdict.
func (o *Ollama) Grade(ctx context.Context, question, groundTruth, retrievedContext, answer string) (Verdict, error) {
	prompt := buildPrompt(question, groundTruth, retrievedContext, answer)

	body, err := json.Marshal(chatRequest{
		Model: o.ModelID,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		},
		Stream: false,
		// Ollama constrains decoding to valid JSON. Cheaper and more
		// reliable than asking politely and parsing whatever comes back.
		Format: "json",
		// Temperature 0: a judge that changes its mind between runs makes
		// every before/after comparison meaningless.
		Options: chatOptions{Temperature: 0, Seed: evalSeed, NumCtx: 16384},
	})
	if err != nil {
		return Verdict{}, fmt.Errorf("judge: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Verdict{}, fmt.Errorf("judge: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.HTTP.Do(req)
	if err != nil {
		return Verdict{}, fmt.Errorf("judge: post: %w (is ollama running?)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		if resp.StatusCode == http.StatusNotFound {
			return Verdict{}, fmt.Errorf("judge: model %q not pulled - run `ollama pull %s`", o.ModelID, o.ModelID)
		}
		return Verdict{}, fmt.Errorf("judge: status %d: %s", resp.StatusCode, buf.String())
	}

	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Verdict{}, fmt.Errorf("judge: decode: %w", err)
	}
	if out.Error != "" {
		return Verdict{}, fmt.Errorf("judge: ollama: %s", out.Error)
	}

	return parseVerdict(out.Message.Content)
}

var jsonObject = regexp.MustCompile(`(?s)\{.*\}`)

// parseVerdict tolerates a model that wraps its JSON in prose or a code
// fence despite being told not to. Small models do this often enough that
// failing the whole run over it would be the wrong trade.
func parseVerdict(raw string) (Verdict, error) {
	s := strings.TrimSpace(raw)
	if m := jsonObject.FindString(s); m != "" {
		s = m
	}

	var v Verdict
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return Verdict{}, fmt.Errorf("judge: verdict was not JSON: %w (got %.200q)", err, raw)
	}
	if v.Correctness < 0 || v.Correctness > 2 {
		return Verdict{}, fmt.Errorf("judge: correctness %d out of range 0-2", v.Correctness)
	}
	return v, nil
}

func buildPrompt(question, groundTruth, retrievedContext, answer string) string {
	var sb strings.Builder
	sb.WriteString("<question>\n")
	sb.WriteString(question)
	sb.WriteString("\n</question>\n\n<correct_answer>\n")
	sb.WriteString(groundTruth)
	sb.WriteString("\n</correct_answer>\n\n<retrieved_context>\n")
	sb.WriteString(retrievedContext)
	sb.WriteString("\n</retrieved_context>\n\n<system_answer>\n")
	sb.WriteString(answer)
	sb.WriteString("\n</system_answer>\n")
	return sb.String()
}
