// Package chunk splits a repository into retrievable pieces.
//
// Chunking is structure-aware: Go files split at declaration boundaries via
// go/ast, Markdown at headings, and anything else falls back to fixed-size
// line windows. The Phase 1 fixed-size chunker averaged 82.7 lines per chunk
// and made 21 of 48 files a single indivisible blob, so retrieval could only
// discriminate between files rather than between functions.
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
	Content    string // the raw source text

	// Provenance, used to enrich the embedded text and to display results.
	Package string // Go package name, empty for non-Go
	Kind    string // func, type, const, var, header, section, window
	Symbol  string // "(*lagMonitor) updateLag", or a Markdown heading trail

	Hash string // sha256 of EmbedText
}

// EmbedText is what actually gets embedded: the chunk body prefixed with a
// short provenance header.
//
// This matters more than it looks. A bare function body says nothing about
// where it lives, so a chunk holding `fetchCommittedOffsets` embeds as
// generic offset-handling code. With the header it embeds as offset-handling
// code *in the consumer package's lag monitor*, which is far closer to how
// the question is phrased. Content stays raw so the stored text and line
// numbers still match the file on disk.
func (c Chunk) EmbedText() string {
	var sb strings.Builder
	sb.WriteString(c.SourcePath)
	if c.Package != "" {
		sb.WriteString(" · package ")
		sb.WriteString(c.Package)
	}
	if c.Kind != "" && c.Symbol != "" {
		sb.WriteString(" · ")
		sb.WriteString(c.Kind)
		sb.WriteString(" ")
		sb.WriteString(c.Symbol)
	}
	sb.WriteString("\n\n")
	sb.WriteString(c.Content)
	return sb.String()
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

// File reads one file and splits it according to its type.
func File(root, relPath string) ([]Chunk, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relPath)))
	if err != nil {
		return nil, fmt.Errorf("chunk: read %s: %w", relPath, err)
	}
	src := strings.ReplaceAll(string(raw), "\r\n", "\n")

	switch filepath.Ext(relPath) {
	case ".go":
		chunks, err := goFile(relPath, src)
		if err == nil {
			return chunks, nil
		}
		// A file that does not parse still belongs in the index; degrade
		// to fixed windows rather than dropping it.
		return fixedWindows(relPath, src), nil
	case ".md":
		return mdFile(relPath, src), nil
	default:
		return fixedWindows(relPath, src), nil
	}
}

// fixedWindows is the Phase 1 chunker, kept as the fallback for file types
// with no structure worth parsing (YAML, SQL, shell, Makefile).
func fixedWindows(relPath, src string) []Chunk {
	lines := strings.Split(src, "\n")

	var (
		chunks   []Chunk
		start    int
		idx      int
		size     int
		curLines []string
	)

	flush := func(endLine int) {
		body := strings.Join(curLines, "\n")
		if strings.TrimSpace(body) == "" {
			return
		}
		c := Chunk{
			SourcePath: relPath,
			Index:      idx,
			StartLine:  start + 1,
			EndLine:    endLine,
			Kind:       "window",
			Content:    body,
		}
		c.Hash = hash(c.EmbedText())
		chunks = append(chunks, c)
		idx++
	}

	for i, line := range lines {
		curLines = append(curLines, line)
		size += len(line) + 1

		if size < targetBytes {
			continue
		}

		flush(i + 1)

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

	return chunks
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
