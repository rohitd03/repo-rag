// Package chunk splits a repository into retrievable pieces.
//
// Phase 1 uses fixed-size line windows on purpose. It is the naive baseline,
// and its failure modes are the point: it cuts functions in half, separates a
// doc comment from the function it documents, and shifts every chunk boundary
// in a file when you insert a line near the top. Phase 2 replaces this with
// AST-aware chunking and measures the difference.
package chunk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Roughly 800 tokens at ~4 bytes per token.
	targetBytes = 3200
	// Roughly 100 tokens of overlap, so a fact split across a boundary
	// still appears whole in one of the two chunks.
	overlapBytes = 400
)

// Chunk is one indexable piece of one file.
type Chunk struct {
	SourcePath string // repo-relative, forward slashes
	Index      int    // 0-based position within the file
	StartLine  int    // 1-based, inclusive
	EndLine    int    // 1-based, inclusive
	Content    string
	Hash       string // sha256 of Content
}

// includeExts and includeNames must stay in sync with scripts/pack-corpus.sh.
// If the indexer and the baseline packer disagree about what counts as the
// corpus, every comparison between them is meaningless.
var includeExts = map[string]bool{
	".go": true, ".md": true, ".sql": true,
	".yml": true, ".yaml": true, ".sh": true,
}

var includeNames = map[string]bool{
	"Makefile": true, "Dockerfile": true, "go.mod": true,
}

var skipDirs = map[string]bool{
	".git": true, ".claude": true, "bin": true,
	"node_modules": true, "vendor": true,

	// eval/ holds the golden question set, which contains the answers.
	// Indexing it would let retrieval find the answer key and score near
	// perfect while telling us nothing. Test set contamination - the single
	// easiest way to fool yourself in this project.
	"eval": true,
}

// Walk collects every file in root that belongs in the corpus.
func Walk(root string) ([]string, error) {
	var out []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		name := d.Name()
		if !includeExts[filepath.Ext(name)] && !includeNames[name] {
			return nil
		}
		// Matches the packer's -not -name '*copy*' rule.
		if strings.Contains(name, "copy") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})

	return out, err
}

// File reads one file and splits it into fixed-size line windows.
func File(root, relPath string) ([]Chunk, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relPath)))
	if err != nil {
		return nil, fmt.Errorf("chunk: read %s: %w", relPath, err)
	}

	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")

	var (
		chunks   []Chunk
		start    int // 0-based index of the first line in the current window
		idx      int
		size     int
		curLines []string
	)

	flush := func(endLine int) {
		body := strings.Join(curLines, "\n")
		if strings.TrimSpace(body) == "" {
			return
		}
		chunks = append(chunks, Chunk{
			SourcePath: relPath,
			Index:      idx,
			StartLine:  start + 1,
			EndLine:    endLine,
			Content:    body,
			Hash:       hash(body),
		})
		idx++
	}

	for i, line := range lines {
		curLines = append(curLines, line)
		size += len(line) + 1

		if size < targetBytes {
			continue
		}

		flush(i + 1)

		// Walk backwards from the end of the window to build the overlap.
		back, keep := 0, 0
		for j := len(curLines) - 1; j >= 0 && back < overlapBytes; j-- {
			back += len(curLines[j]) + 1
			keep++
		}
		curLines = append([]string(nil), curLines[len(curLines)-keep:]...)
		start = i + 1 - keep
		size = back
	}

	if len(curLines) > 0 {
		flush(len(lines))
	}

	return chunks, nil
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
