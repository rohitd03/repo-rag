package chunk

import (
	"strings"
)

// mdFile splits Markdown at heading boundaries.
//
// A heading is an author-declared topic boundary, which is exactly what a
// retrievable unit should be. Fixed windows ignore them and produce chunks
// that start mid-paragraph under one heading and end under another.
//
// Each chunk carries its heading trail ("Monitoring > Consumer lag"), so a
// section titled "Configuration" is distinguishable from the six other
// sections titled "Configuration" elsewhere in the corpus.
func mdFile(relPath, src string) []Chunk {
	lines := strings.Split(src, "\n")

	type section struct {
		start, end int
		trail      []string
	}

	var (
		sections []section
		trail    []string
		cur      = section{start: 1}
		inFence  bool
	)

	for i, line := range lines {
		// A "###" inside a fenced code block is shell output or a comment,
		// not a heading. Splitting there would cut the code block in half.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		level, title := heading(line)
		if level == 0 {
			continue
		}

		if i+1 > cur.start {
			cur.end = i
			cur.trail = append([]string(nil), trail...)
			sections = append(sections, cur)
		}

		// Trim the trail to the parent level, then push this heading.
		if level-1 < len(trail) {
			trail = trail[:level-1]
		}
		for len(trail) < level-1 {
			trail = append(trail, "")
		}
		trail = append(trail, title)

		cur = section{start: i + 1}
	}
	cur.end = len(lines)
	cur.trail = append([]string(nil), trail...)
	sections = append(sections, cur)

	var chunks []Chunk
	idx := 0
	for _, s := range sections {
		if s.end < s.start {
			continue
		}
		body := strings.Join(lines[s.start-1:s.end], "\n")
		if strings.TrimSpace(body) == "" {
			continue
		}

		symbol := strings.Trim(strings.Join(s.trail, " > "), " >")

		// A section can still be longer than a chunk should be, so split
		// oversized ones while keeping the heading trail on every piece.
		if len(body) > maxDeclBytes {
			for _, w := range splitLines(lines, s.start, s.end, targetBytes, overlapBytes) {
				chunks = append(chunks, mdChunk(relPath, idx, w[0], w[1], symbol, lines))
				idx++
			}
			continue
		}
		chunks = append(chunks, mdChunk(relPath, idx, s.start, s.end, symbol, lines))
		idx++
	}

	return chunks
}

func mdChunk(relPath string, idx, start, end int, symbol string, lines []string) Chunk {
	c := Chunk{
		SourcePath: relPath,
		Index:      idx,
		StartLine:  start,
		EndLine:    end,
		Kind:       "section",
		Symbol:     symbol,
		Content:    strings.Join(lines[start-1:end], "\n"),
	}
	c.Hash = hash(c.EmbedText())
	return c
}

// heading returns the ATX heading level and title, or 0 if the line is not
// a heading.
func heading(line string) (int, string) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "#") {
		return 0, ""
	}
	level := 0
	for level < len(t) && t[level] == '#' {
		level++
	}
	if level > 6 || level >= len(t) || t[level] != ' ' {
		return 0, ""
	}
	return level, strings.TrimSpace(t[level:])
}
