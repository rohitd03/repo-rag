package eval

import "testing"

func TestScoreAnswerDeterministicChecks(t *testing.T) {
	q := Question{
		ID:              "q15",
		Category:        "negative",
		MustContain:     []string{"Redis"},
		MustNotContain:  []string{"go-redis", "cache layer"},
		ExpectedSources: nil,
	}

	tests := []struct {
		name            string
		answer          string
		wantViolations  int
		wantMissing     int
		wantRefused     bool
		wantHallucinate bool
	}{
		{
			name:        "clean refusal",
			answer:      "The retrieved context does not cover Redis.",
			wantRefused: true,
		},
		{
			name:            "fabricates a cache layer",
			answer:          "Redis is used as a cache layer in front of Postgres.",
			wantViolations:  1,
			wantHallucinate: true,
		},
		{
			name:        "misses the required term entirely",
			answer:      "The context does not mention any caching.",
			wantMissing: 1,
			wantRefused: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScoreAnswer(q, tt.answer, 0, []string{"a.go"})
			if len(got.Violations) != tt.wantViolations {
				t.Errorf("Violations = %v, want %d", got.Violations, tt.wantViolations)
			}
			if len(got.MissingRequired) != tt.wantMissing {
				t.Errorf("MissingRequired = %v, want %d", got.MissingRequired, tt.wantMissing)
			}
			if got.Refused != tt.wantRefused {
				t.Errorf("Refused = %v, want %v", got.Refused, tt.wantRefused)
			}
			if got.Hallucinated() != tt.wantHallucinate {
				t.Errorf("Hallucinated = %v, want %v", got.Hallucinated(), tt.wantHallucinate)
			}
		})
	}
}

// A citation to a file that was never retrieved is the most dangerous
// failure here, because it reads as evidence.
func TestFabricatedCitationsAreDetected(t *testing.T) {
	q := Question{
		ID:              "q04",
		ExpectedSources: []string{"internal/consumer/lag_monitor.go"},
	}
	retrieved := []string{"internal/consumer/lag_monitor.go", "alerts.yml"}

	honest := ScoreAnswer(q,
		"Lag is computed in internal/consumer/lag_monitor.go.", 1, retrieved)
	if len(honest.BadCitations) != 0 {
		t.Errorf("citing a retrieved file must be clean, got %v", honest.BadCitations)
	}
	if !honest.CitedCorrectly {
		t.Error("expected CitedCorrectly for a cite of an expected source")
	}
	if honest.Hallucinated() {
		t.Error("an honest citation must not count as a hallucination")
	}

	invented := ScoreAnswer(q,
		"See internal/consumer/lag_monitor.go and internal/cache/redis_store.go.", 1, retrieved)
	if len(invented.BadCitations) != 1 {
		t.Fatalf("BadCitations = %v, want exactly the invented path", invented.BadCitations)
	}
	if !invented.Hallucinated() {
		t.Error("a fabricated citation must count as a hallucination")
	}
}

// Answers routinely shorten a path to its basename; that is not a
// fabrication and must not be scored as one.
func TestBasenameCitationsCount(t *testing.T) {
	q := Question{ID: "q04", ExpectedSources: []string{"internal/consumer/lag_monitor.go"}}
	got := ScoreAnswer(q, "The logic lives in lag_monitor.go.", 1,
		[]string{"internal/consumer/lag_monitor.go"})

	if len(got.BadCitations) != 0 {
		t.Errorf("BadCitations = %v, want none", got.BadCitations)
	}
	if !got.CitedCorrectly {
		t.Error("a basename cite of an expected source should count")
	}
}

// Attribution is the diagnostic a single score destroys. These cases are the
// whole reason the field exists.
func TestAttribution(t *testing.T) {
	answerable := Question{ID: "q07", ExpectedSources: []string{"a.go", "b.md"}}

	tests := []struct {
		name string
		res  AnswerResult
		want string
	}{
		{
			name: "complete context, correct answer",
			res:  AnswerResult{Question: answerable, Recall: 1, JudgeCorrectness: 2},
			want: "ok",
		},
		{
			name: "incomplete context, incomplete answer: blame retrieval",
			res:  AnswerResult{Question: answerable, Recall: 0.5, JudgeCorrectness: 1},
			want: "retrieval-limited",
		},
		{
			name: "complete context, wrong answer: blame generation",
			res:  AnswerResult{Question: answerable, Recall: 1, JudgeCorrectness: 0},
			want: "generation-limited",
		},
		{
			name: "a forbidden phrase outranks everything else",
			res: AnswerResult{
				Question: answerable, Recall: 1, JudgeCorrectness: 2,
				Violations: []string{"go-redis"},
			},
			want: "hallucination",
		},
		{
			name: "negative question answered by refusing",
			res:  AnswerResult{Question: Question{ID: "q15"}, Refused: true},
			want: "ok",
		},
		{
			name: "negative question answered anyway",
			res:  AnswerResult{Question: Question{ID: "q15"}, Refused: false},
			want: "generation-limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.res.Attribution(); got != tt.want {
				t.Errorf("Attribution() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Ordinary prose must not be mined for phantom citations - a loose pattern
// would inflate the citation rate into meaninglessness.
func TestCitationPatternDoesNotMatchProse(t *testing.T) {
	q := Question{ID: "x", ExpectedSources: []string{"a.go"}}
	got := ScoreAnswer(q,
		"The consumer retries three times, then gives up. That is all.", 1, []string{"a.go"})
	if len(got.Citations) != 0 {
		t.Errorf("Citations = %v, want none from plain prose", got.Citations)
	}
}

// must_contain entries may name alternative phrasings of the same fact.
// Without this, an answer saying "once.Do" fails a check written as
// "sync.Once" and the error count overstates real failures.
func TestMustContainAlternatives(t *testing.T) {
	q := Question{ID: "q06", MustContain: []string{"sync.Once|once.Do"}}

	for _, answer := range []string{
		"Guarded by sync.Once.",
		"The once.Do function ensures it runs once.",
	} {
		if got := ScoreAnswer(q, answer, 1, nil); len(got.MissingRequired) != 0 {
			t.Errorf("%q should satisfy the alternatives, got missing %v", answer, got.MissingRequired)
		}
	}

	if got := ScoreAnswer(q, "It uses a mutex.", 1, nil); len(got.MissingRequired) != 1 {
		t.Error("an answer matching neither alternative must still fail")
	}
}
