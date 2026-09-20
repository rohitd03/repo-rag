package chunk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalkFiltersCorpus(t *testing.T) {
	root := t.TempDir()

	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("main.go", "package main\n")
	write("README.md", "# hi\n")
	write("internal/db/schema.sql", "SELECT 1;\n")
	write("Makefile", "all:\n")
	write("notes copy.md", "dupe\n")
	write("app.exe", "binary")
	write(".git/config", "[core]\n")
	write("bin/tool.go", "package main\n")
	write("eval/goldens/set.yaml", "answers: here\n")

	got, err := Walk(root)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	want := map[string]bool{
		"main.go":                true,
		"README.md":              true,
		"internal/db/schema.sql": true,
		"Makefile":               true,
	}

	if len(got) != len(want) {
		t.Fatalf("got %d files %v, want %d", len(got), got, len(want))
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected file in corpus: %s", g)
		}
	}
}

// The golden question set contains the answers. If Walk ever picks it up, every
// eval score becomes meaningless, so this deserves its own named test.
func TestWalkExcludesEvalDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "eval", "goldens"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "eval", "goldens", "q.yaml"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Walk(root)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("eval/ must never be indexed, got %v", got)
	}
}

func TestFileChunking(t *testing.T) {
	root := t.TempDir()

	var sb strings.Builder
	for i := 0; i < 400; i++ {
		sb.WriteString("this line is about forty characters long\n")
	}
	if err := os.WriteFile(filepath.Join(root, "big.go"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	chunks, err := File(root, "big.go")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected the file to split, got %d chunk(s)", len(chunks))
	}

	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk %d has Index %d", i, c.Index)
		}
		if c.StartLine < 1 || c.EndLine < c.StartLine {
			t.Errorf("chunk %d has bad line range %d-%d", i, c.StartLine, c.EndLine)
		}
		if c.Hash == "" {
			t.Errorf("chunk %d has no hash", i)
		}
	}

	// Overlap is what keeps a fact that straddles a boundary intact in at
	// least one chunk.
	for i := 1; i < len(chunks); i++ {
		if chunks[i].StartLine > chunks[i-1].EndLine {
			t.Errorf("chunks %d and %d do not overlap: %d-%d then %d-%d",
				i-1, i, chunks[i-1].StartLine, chunks[i-1].EndLine,
				chunks[i].StartLine, chunks[i].EndLine)
		}
	}
}

func TestFileHashIsStable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := File(root, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	second, err := File(root, "a.go")
	if err != nil {
		t.Fatal(err)
	}

	if len(first) != len(second) {
		t.Fatalf("chunk count changed between runs: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Hash != second[i].Hash {
			t.Errorf("chunk %d hash is not stable - incremental indexing would re-embed everything", i)
		}
	}
}
