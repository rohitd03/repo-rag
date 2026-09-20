package eval

import "testing"

func TestScore(t *testing.T) {
	tests := []struct {
		name       string
		expected   []string
		retrieved  []string
		wantRecall float64
		wantRR     float64
	}{
		{
			name:       "all found, best at rank 1",
			expected:   []string{"a.go"},
			retrieved:  []string{"a.go", "b.go"},
			wantRecall: 1,
			wantRR:     1,
		},
		{
			name:       "found at rank 3",
			expected:   []string{"c.go"},
			retrieved:  []string{"a.go", "b.go", "c.go"},
			wantRecall: 1,
			wantRR:     1.0 / 3,
		},
		{
			name:       "partial recall, rank taken from the best hit",
			expected:   []string{"a.go", "z.go"},
			retrieved:  []string{"a.go", "b.go"},
			wantRecall: 0.5,
			wantRR:     1,
		},
		{
			name:       "nothing found",
			expected:   []string{"z.go"},
			retrieved:  []string{"a.go", "b.go"},
			wantRecall: 0,
			wantRR:     0,
		},
		{
			name:       "duplicate chunks from one file collapse to its best rank",
			expected:   []string{"b.go"},
			retrieved:  []string{"a.go", "b.go", "b.go", "b.go"},
			wantRecall: 1,
			wantRR:     0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Score(Question{ID: "t", ExpectedSources: tt.expected}, tt.retrieved, 0.5)
			if got.Recall != tt.wantRecall {
				t.Errorf("Recall = %v, want %v", got.Recall, tt.wantRecall)
			}
			if got.RR != tt.wantRR {
				t.Errorf("RR = %v, want %v", got.RR, tt.wantRR)
			}
		})
	}
}

func TestNegativeQuestionsExcludedFromRecall(t *testing.T) {
	results := []Result{
		Score(Question{ID: "pos", Category: "factual", ExpectedSources: []string{"a.go"}},
			[]string{"a.go"}, 0.70),
		Score(Question{ID: "neg", Category: "negative"},
			[]string{"junk.yaml"}, 0.52),
	}

	s := Summarize(results, 5)
	if s.Scored != 1 {
		t.Errorf("Scored = %d, want 1 (negatives must not count)", s.Scored)
	}
	if s.Negatives != 1 {
		t.Errorf("Negatives = %d, want 1", s.Negatives)
	}
	if s.Recall != 1 {
		t.Errorf("Recall = %v, want 1 - the negative must not drag the mean down", s.Recall)
	}
}

func TestSeparationHolds(t *testing.T) {
	sep := Summarize([]Result{
		Score(Question{ID: "p", ExpectedSources: []string{"a.go"}}, []string{"a.go"}, 0.65),
		Score(Question{ID: "n"}, []string{"junk"}, 0.52),
	}, 5)
	if !sep.SeparationHolds() {
		t.Error("expected separation to hold when every positive outscores every negative")
	}

	overlap := Summarize([]Result{
		Score(Question{ID: "p", ExpectedSources: []string{"a.go"}}, []string{"a.go"}, 0.50),
		Score(Question{ID: "n"}, []string{"junk"}, 0.52),
	}, 5)
	if overlap.SeparationHolds() {
		t.Error("one overlapping pair must falsify separation")
	}
}
