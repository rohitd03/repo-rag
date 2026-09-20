package chunk

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// goFile splits a Go source file at declaration boundaries.
//
// Fixed-size windows cut functions in half and separate a doc comment from
// the function it documents. That matters more than it sounds: several
// golden questions (q10, q19) have their answers in doc comments, and a
// comment orphaned from its code embeds as vague prose.
//
// Falls back to fixed-size windows when the file does not parse, so a
// syntax error in one file degrades that file rather than the whole index.
func goFile(relPath, src string) ([]Chunk, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, relPath, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	lines := strings.Split(src, "\n")
	pkg := f.Name.Name

	lineOf := func(p token.Pos) int { return fset.Position(p).Line }

	// Header chunk: package clause and imports. Small, but it is what
	// carries "this file imports segmentio/kafka-go", which is exactly the
	// kind of fact q01 asks about.
	var chunks []Chunk
	idx := 0
	add := func(startLine, endLine int, kind, symbol string) {
		if startLine < 1 || endLine > len(lines) || endLine < startLine {
			return
		}
		body := strings.Join(lines[startLine-1:endLine], "\n")
		if strings.TrimSpace(body) == "" {
			return
		}
		c := Chunk{
			SourcePath: relPath,
			Index:      idx,
			StartLine:  startLine,
			EndLine:    endLine,
			Package:    pkg,
			Kind:       kind,
			Symbol:     symbol,
			Content:    body,
		}
		c.Hash = hash(c.EmbedText())
		chunks = append(chunks, c)
		idx++
	}

	headerEnd := lineOf(f.Name.End())
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			if e := lineOf(gd.End()); e > headerEnd {
				headerEnd = e
			}
		}
	}
	add(1, headerEnd, "header", pkg)

	for _, d := range f.Decls {
		start := lineOf(d.Pos())
		end := lineOf(d.End())

		var kind, symbol string
		switch decl := d.(type) {
		case *ast.FuncDecl:
			kind, symbol = "func", funcSignature(decl)
			// Pull the doc comment into the chunk. Without this, "why does
			// it poll before the loop?" is unanswerable - the reason is in
			// the comment and the comment would be in a different chunk.
			if decl.Doc != nil {
				start = lineOf(decl.Doc.Pos())
			}
		case *ast.GenDecl:
			if decl.Tok == token.IMPORT {
				continue // already in the header chunk
			}
			kind, symbol = decl.Tok.String(), genDeclName(decl)
			if decl.Doc != nil {
				start = lineOf(decl.Doc.Pos())
			}
		default:
			continue
		}

		// A very long declaration still needs splitting, or one 400-line
		// function becomes a single unretrievable blob.
		if declBytes(lines, start, end) > maxDeclBytes {
			for _, w := range splitLines(lines, start, end, targetBytes, overlapBytes) {
				add(w[0], w[1], kind, symbol)
			}
			continue
		}
		add(start, end, kind, symbol)
	}

	return chunks, nil
}

// maxDeclBytes is the point at which a declaration is split. Higher than
// targetBytes because keeping a whole function together is worth more than
// hitting an exact size.
const maxDeclBytes = 6000

func declBytes(lines []string, start, end int) int {
	n := 0
	for i := start - 1; i < end && i < len(lines); i++ {
		n += len(lines[i]) + 1
	}
	return n
}

// funcSignature renders a readable name, including the receiver so that
// methods on different types stay distinguishable.
func funcSignature(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return fmt.Sprintf("(%s) %s", typeName(fn.Recv.List[0].Type), fn.Name.Name)
	}
	return fn.Name.Name
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return "*" + typeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver
		return typeName(t.X)
	default:
		return "?"
	}
}

func genDeclName(gd *ast.GenDecl) string {
	var names []string
	for _, spec := range gd.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			names = append(names, s.Name.Name)
		case *ast.ValueSpec:
			for _, n := range s.Names {
				names = append(names, n.Name)
			}
		}
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) > 4 {
		names = append(names[:4], "...")
	}
	return strings.Join(names, ", ")
}

// splitLines returns [start,end] line windows over an oversized range.
func splitLines(lines []string, start, end, target, overlap int) [][2]int {
	var out [][2]int
	i := start
	for i <= end {
		size, j := 0, i
		for j <= end && size < target {
			size += len(lines[j-1]) + 1
			j++
		}
		out = append(out, [2]int{i, min(j-1, end)})
		if j > end {
			break
		}
		back, keep := 0, 0
		for b := j - 1; b >= i && back < overlap; b-- {
			back += len(lines[b-1]) + 1
			keep++
		}
		i = j - keep
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
