// Package eval scores retrieval against a hand-written golden question set.
//
// Retrieval metrics only. They are pure arithmetic over data you already
// have, so they need no model, cost nothing, and are deterministic - which
// makes them safe to run after every change. Answer-quality scoring needs a
// judge model and lands in Phase 3.
package eval

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Golden is a parsed question set.
type Golden struct {
	Corpus struct {
		Name     string `yaml:"name"`
		Root     string `yaml:"root"`
		Commit   string `yaml:"commit"`
		Captured string `yaml:"captured"`
	} `yaml:"corpus"`
	Questions []Question `yaml:"questions"`
}

// Question is one golden question with its verified answer.
type Question struct {
	ID              string   `yaml:"id"`
	Question        string   `yaml:"question"`
	Category        string   `yaml:"category"`
	Difficulty      string   `yaml:"difficulty"`
	GroundTruth     string   `yaml:"ground_truth"`
	ExpectedSources []string `yaml:"expected_sources"`
	MustContain     []string `yaml:"must_contain"`
	MustNotContain  []string `yaml:"must_not_contain"`
	WhyItIsHard     string   `yaml:"why_it_is_hard"`
}

// Negative reports whether this question has no answer in the corpus.
//
// These are excluded from recall averages: a question with no expected
// sources has no recall to measure, and averaging a defined-as-1.0 or
// defined-as-0.0 value into the mean would distort it either way.
func (q Question) Negative() bool { return len(q.ExpectedSources) == 0 }

// Load reads a golden set from disk.
func Load(path string) (*Golden, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("eval: read %s: %w", path, err)
	}
	var g Golden
	if err := yaml.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("eval: parse %s: %w", path, err)
	}
	if len(g.Questions) == 0 {
		return nil, fmt.Errorf("eval: %s has no questions", path)
	}
	return &g, nil
}
