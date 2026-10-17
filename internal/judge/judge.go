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
	// Grade scores correctness and faithfulness together. Convenient for a
	// single-configuration run; not used by the benchmark, see below.
	Grade(ctx context.Context, question, groundTruth, retrievedContext, answer string) (Verdict, error)

	// GradeBlind scores correctness only, against ground truth, with the
	// retrieved context deliberately withheld.
	//
	// This is what the benchmark uses for its headline metric. Showing the
	// judge the context leaks which arm produced the answer - an answer
	// built from 60 chunks of repository, or from none at all, is
	// identifiable at a glance - and a judge that can tell the arms apart
	// is not blind. Correctness is defined against ground truth, so the
	// context is not needed to decide it.
	GradeBlind(ctx context.Context, question, groundTruth, answer string) (Verdict, error)

	// GradeFaithfulness checks whether every claim is supported by the
	// context. This one needs the context by definition, so it cannot be
	// blind - which is why it is kept separate from correctness rather
	// than decided in the same call.
	GradeFaithfulness(ctx context.Context, question, retrievedContext, answer string) (Verdict, error)

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

// Grade scores correctness and faithfulness in one call.
func (o *Ollama) Grade(ctx context.Context, question, groundTruth, retrievedContext, answer string) (Verdict, error) {
	return o.chat(ctx, systemPrompt, buildPrompt(question, groundTruth, retrievedContext, answer))
}

// chat is the one HTTP path all three grading methods share, so they cannot
// drift apart on decoding settings - which would make their scores
// incomparable.
func (o *Ollama) chat(ctx context.Context, system, user string) (Verdict, error) {
	body, err := json.Marshal(chatRequest{
		Model: o.ModelID,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Stream: false,
		// Ollama constrains decoding to valid JSON. Cheaper and more
		// reliable than asking politely and parsing whatever comes back.
		Format: "json",
		// Temperature 0 with a fixed seed: a judge that changes its mind
		// between runs makes every comparison meaningless.
		Options: chatOptions{Temperature: 0, Seed: evalSeed, NumCtx: 32768},
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

const blindSystemPrompt = `You grade answers produced by a code question-answering system. Reply with JSON only.

You are given a question, the correct answer, and the system's answer. You are NOT given the context the system worked from, by design - judge only whether the answer is right.

Return exactly this shape:
{"correctness": 0|1|2, "faithful": true, "reason": "one sentence"}

correctness, judged against the CORRECT ANSWER:
  2 - matches the correct answer on every substantive point
  1 - directionally right but missing a substantive point, or hedged into uselessness
  0 - wrong, contradicts the correct answer, or declines to answer a question the correct answer does answer

Be strict about specifics. If the correct answer states a number, a name or a
mechanism, an answer that omits it or gets it wrong is not a 2. An answer that
says the information was unavailable scores 0 when the correct answer provides
it - declining is not partial credit.

Always set "faithful" to true; it is judged separately and ignored here.
Output JSON only, no prose before or after.`

// GradeBlind scores correctness without showing the judge the context.
func (o *Ollama) GradeBlind(ctx context.Context, question, groundTruth, answer string) (Verdict, error) {
	prompt := fmt.Sprintf(
		"<question>\n%s\n</question>\n\n"+
			"<correct_answer>\n%s\n</correct_answer>\n\n"+
			"<system_answer>\n%s\n</system_answer>\n",
		question, groundTruth, answer)

	return o.chat(ctx, blindSystemPrompt, prompt)
}

const faithfulnessSystemPrompt = `You check whether an answer is grounded in the context it was given. Reply with JSON only.

Return exactly this shape:
{"correctness": 0, "faithful": true|false, "reason": "one sentence"}

faithful, judged against the CONTEXT only:
  true  - every claim in the answer is supported by the context
  false - the answer asserts something the context does not support, including
          naming a file, function or component that does not appear in it

An answer that correctly reports the context does not cover the question is
faithful. Whether that was the right response is judged elsewhere, so do not
consider it here.

Always set "correctness" to 0; it is judged separately and ignored here.
Output JSON only, no prose before or after.`

// GradeFaithfulness checks grounding. Needs the context, so it is not blind.
func (o *Ollama) GradeFaithfulness(ctx context.Context, question, retrievedContext, answer string) (Verdict, error) {
	prompt := fmt.Sprintf(
		"<question>\n%s\n</question>\n\n"+
			"<context>\n%s\n</context>\n\n"+
			"<answer>\n%s\n</answer>\n",
		question, retrievedContext, answer)

	return o.chat(ctx, faithfulnessSystemPrompt, prompt)
}
