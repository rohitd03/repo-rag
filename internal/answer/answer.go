// Package answer turns retrieved chunks into a grounded answer.
//
// Two backends implement Generator: a local model via Ollama, and Claude via
// the Anthropic API. Both are driven from the same systemPrompt and the same
// buildPrompt below - if each backend shaped its own prompt, a Phase 4
// comparison between them would be measuring prompt differences rather than
// model differences.
package answer

import (
	"context"
	"fmt"
	"strings"

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

// noContext is returned by every backend when retrieval came back empty, so
// the behaviour does not depend on which model is configured.
const noContext = "No chunks were retrieved, so there is nothing to answer from. " +
	"Has the corpus been indexed?"
