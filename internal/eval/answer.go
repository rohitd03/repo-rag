package eval

import (
	"regexp"
	"sort"
	"strings"
)

// AnswerResult scores one generated answer.
//
// Deterministic checks and judge scores are kept in separate fields on
// purpose. The deterministic ones are arithmetic over strings and are always
// true; the judge is a model and can be wrong. Merging them into a single
// number would hide which part of a score you can actually rely on.
type AnswerResult struct {
	Question Question
	Answer   string

	// Retrieval context, carried through so a wrong answer can be blamed
	// on the right stage.
	Recall           float64
	RetrievedSources []string

	// --- deterministic ---

	MissingRequired []string // must_contain phrases absent from the answer
	Violations      []string // must_not_contain phrases present: hallucinations

	Citations      []string // file paths the answer cited
	BadCitations   []string // cited paths that were never retrieved
	CitedCorrectly bool     // cited at least one expected source
	Refused        bool     // answer says the context does not cover it

	// --- judge ---

	JudgeCorrectness int  // 0, 1 or 2 against ground_truth
	JudgeFaithful    bool // every claim supported by retrieved context
	JudgeReason      string
	Judged           bool
}

// Hallucinated reports a must_not_contain violation or a cited file that was
// never retrieved. Either is an automatic zero regardless of how good the
// rest of the answer reads.
func (a AnswerResult) Hallucinated() bool {
	return len(a.Violations) > 0 || len(a.BadCitations) > 0
}

// Attribution says which stage limited this answer.
//
// This is the diagnostic that Phase 1 showed matters most and that a single
// score destroys. An incomplete answer from incomplete context is a
// retrieval problem; a wrong answer from complete context is a generation
// problem. They need opposite fixes, and telling them apart by hand does not
// scale past a handful of questions.
func (a AnswerResult) Attribution() string {
	switch {
	case a.Hallucinated():
		return "hallucination"
	case a.Question.Negative():
		if a.Refused {
			return "ok"
		}
		return "generation-limited"
	case a.JudgeCorrectness == 2:
		return "ok"
	case a.Recall < 1:
		return "retrieval-limited"
	default:
		return "generation-limited"
	}
}

// pathPattern matches repo-relative source paths in prose: at least one
// slash-separated segment and a known extension, or a bare filename with
// one. Deliberately strict - a loose pattern turns ordinary prose into
// phantom citations and inflates the citation rate.
var pathPattern = regexp.MustCompile(`[\w./-]*\w+\.(?:go|md|sql|ya?ml|sh|mod)\b`)

// refusalPattern matches the grounding contract's escape hatch.
var refusalPattern = regexp.MustCompile(`(?i)(does not (?:contain|cover|include|mention)|no (?:mention|reference|evidence) of|not (?:present|found|covered) in the (?:context|repository|codebase)|context does not)`)

// ScoreAnswer runs every deterministic check. The judge fills in the rest.
func ScoreAnswer(q Question, answer string, recall float64, retrieved []string) AnswerResult {
	a := AnswerResult{
		Question:         q,
		Answer:           answer,
		Recall:           recall,
		RetrievedSources: retrieved,
		Refused:          refusalPattern.MatchString(answer),
	}

	lower := strings.ToLower(answer)
	for _, want := range q.MustContain {
		if !containsAny(lower, want) {
			a.MissingRequired = append(a.MissingRequired, want)
		}
	}
	for _, bad := range q.MustNotContain {
		if strings.Contains(lower, strings.ToLower(bad)) {
			a.Violations = append(a.Violations, bad)
		}
	}

	retrievedSet := make(map[string]bool, len(retrieved))
	for _, p := range retrieved {
		retrievedSet[normalize(p)] = true
	}
	expectedSet := make(map[string]bool, len(q.ExpectedSources))
	for _, p := range q.ExpectedSources {
		expectedSet[normalize(p)] = true
	}

	seen := map[string]bool{}
	for _, m := range pathPattern.FindAllString(answer, -1) {
		p := normalize(strings.Trim(m, ".,;:()[]`"))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		a.Citations = append(a.Citations, p)

		// A cited path that was never retrieved is a fabricated citation -
		// the most dangerous failure mode here, because it looks like
		// evidence. Matched on basename too, since answers often shorten
		// a path to just the filename.
		if !retrievedSet[p] && !matchesAnyBasename(p, retrievedSet) {
			a.BadCitations = append(a.BadCitations, p)
		}
		if expectedSet[p] || matchesAnyBasename(p, expectedSet) {
			a.CitedCorrectly = true
		}
	}
	sort.Strings(a.Citations)

	return a
}

// containsAny treats "a|b" in a must_contain entry as alternatives.
//
// Added after the first Phase 3 run flagged q06 as missing "sync.Once" when
// the answer said "once.Do" - the same fact in different words. An exact
// string check turns a correct answer into a failure and overstates the
// error count, so the golden set can now name the ways a fact may be
// phrased. It stays a substring check, not a judge, so it remains
// deterministic.
func containsAny(lowerAnswer, want string) bool {
	for _, alt := range strings.Split(want, "|") {
		alt = strings.ToLower(strings.TrimSpace(alt))
		if alt != "" && strings.Contains(lowerAnswer, alt) {
			return true
		}
	}
	return false
}

func matchesAnyBasename(p string, set map[string]bool) bool {
	base := p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		base = p[i+1:]
	}
	for candidate := range set {
		cb := candidate
		if i := strings.LastIndex(candidate, "/"); i >= 0 {
			cb = candidate[i+1:]
		}
		if cb == base {
			return true
		}
	}
	return false
}

// AnswerSummary aggregates answer results across a run.
type AnswerSummary struct {
	Total     int
	Scored    int // answerable questions
	Negatives int

	// Deterministic.
	Hallucinations   int
	BadCitations     int
	AnswersWithCite  int
	CitedCorrectly   int
	MissingRequired  int
	NegativesRefused int

	// Judge.
	Judged          int
	MeanCorrectness float64
	Perfect         int // correctness == 2
	Faithful        int

	// Attribution.
	ByAttribution map[string]int
}

// SummarizeAnswers aggregates per-answer results.
func SummarizeAnswers(results []AnswerResult) AnswerSummary {
	s := AnswerSummary{ByAttribution: map[string]int{}}

	var correctnessSum float64
	for _, r := range results {
		s.Total++
		if r.Question.Negative() {
			s.Negatives++
			if r.Refused {
				s.NegativesRefused++
			}
		} else {
			s.Scored++
		}

		if r.Hallucinated() {
			s.Hallucinations++
		}
		s.BadCitations += len(r.BadCitations)
		if len(r.Citations) > 0 {
			s.AnswersWithCite++
		}
		if r.CitedCorrectly {
			s.CitedCorrectly++
		}
		s.MissingRequired += len(r.MissingRequired)

		if r.Judged {
			s.Judged++
			correctnessSum += float64(r.JudgeCorrectness)
			if r.JudgeCorrectness == 2 {
				s.Perfect++
			}
			if r.JudgeFaithful {
				s.Faithful++
			}
		}

		s.ByAttribution[r.Attribution()]++
	}

	if s.Judged > 0 {
		s.MeanCorrectness = correctnessSum / float64(s.Judged)
	}
	return s
}

// AttributionOrder is a stable print order, worst first.
var AttributionOrder = []string{"ok", "retrieval-limited", "generation-limited", "hallucination"}
