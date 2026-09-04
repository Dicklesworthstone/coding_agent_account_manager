package shallow

// A round-trip-preserving structural view of a TOML document.
//
// Reconciling a shallow codex profile's config.toml with the real one (issue
// #103) has to replace whole [tables] without disturbing anything else — and
// "anything else" includes comments, key order and formatting, which a
// parse-and-reserialize round trip through any Go TOML library destroys. Go has
// no comment-preserving TOML editor to depend on, so this file provides the
// narrow thing the merge actually needs: split a document into its root block
// and its table blocks, carry each block's text VERBATIM, and reassemble by
// concatenation. Regions nothing touches come out byte-identical.
//
// Only structure is parsed, never values. That keeps the scanner small, but it
// still has to know where a table header genuinely starts, so it tracks the
// three things that can put a `[` somewhere it does not mean a header:
// comments, strings (basic, literal and both multi-line forms), and multi-line
// arrays or inline tables.

import (
	"fmt"
	"strings"
)

// tomlBlock is one region of a document, copied verbatim.
type tomlBlock struct {
	// path is the table path, e.g. ["mcp_servers", "kernel"]. Nil for the
	// document's root block (everything before the first table header).
	path []string
	// text is the region's exact source, including its header line, any
	// comment/blank lines that immediately precede that header, and its
	// trailing newline.
	text string
}

// tomlDoc is a document as a root block plus an ordered list of table blocks.
type tomlDoc struct {
	root     tomlBlock
	sections []tomlBlock
}

// render reassembles the document.
func (d *tomlDoc) render() string {
	var b strings.Builder
	b.WriteString(d.root.text)
	for _, s := range d.sections {
		b.WriteString(s.text)
	}
	return b.String()
}

// tomlScan tracks whether the next line starts at top level, i.e. outside any
// multi-line string and outside any unclosed [ or {.
type tomlScan struct {
	multi string // "", `"""` or `'''`
	depth int
}

func (s *tomlScan) atTopLevel() bool { return s.multi == "" && s.depth == 0 }

// feed advances the scanner over one line of source (without its newline).
func (s *tomlScan) feed(line string) {
	i := 0
	for i < len(line) {
		if s.multi != "" {
			idx := strings.Index(line[i:], s.multi)
			if idx < 0 {
				return
			}
			i += idx + len(s.multi)
			s.multi = ""
			continue
		}
		switch {
		case line[i] == '#':
			return // comment runs to end of line
		case strings.HasPrefix(line[i:], `"""`):
			s.multi = `"""`
			i += 3
		case strings.HasPrefix(line[i:], `'''`):
			s.multi = `'''`
			i += 3
		case line[i] == '"':
			i++
			for i < len(line) {
				if line[i] == '\\' {
					i += 2
					continue
				}
				if line[i] == '"' {
					i++
					break
				}
				i++
			}
		case line[i] == '\'':
			i++
			for i < len(line) && line[i] != '\'' {
				i++
			}
			if i < len(line) {
				i++
			}
		case line[i] == '[' || line[i] == '{':
			s.depth++
			i++
		case line[i] == ']' || line[i] == '}':
			if s.depth > 0 {
				s.depth--
			}
			i++
		default:
			i++
		}
	}
}

// splitLines splits source into lines, each keeping its trailing newline. A
// final fragment without a newline is kept as its own line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			return out
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
		if s == "" {
			return out
		}
	}
}

// isCommentOrBlank reports whether a line contributes nothing but whitespace or
// a comment. Such a run immediately before a table header is treated as that
// header's leading comment and moves with it.
func isCommentOrBlank(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// parseTOMLBlocks splits a document into its root block and table blocks.
func parseTOMLBlocks(data []byte) (*tomlDoc, error) {
	doc := &tomlDoc{}
	var (
		scan    tomlScan
		cur     []string
		curPath []string
		trail   int // trailing comment/blank lines in cur, candidates to move
	)

	emit := func(path []string, lines []string) {
		text := strings.Join(lines, "")
		if path == nil {
			doc.root = tomlBlock{text: text}
			return
		}
		doc.sections = append(doc.sections, tomlBlock{path: path, text: text})
	}

	for _, raw := range splitLines(string(data)) {
		line := strings.TrimRight(raw, "\r\n")
		topLevel := scan.atTopLevel()

		if topLevel && strings.HasPrefix(strings.TrimSpace(line), "[") {
			path, err := parseTableHeader(strings.TrimSpace(line))
			if err != nil {
				return nil, err
			}
			lead := cur[len(cur)-trail:]
			emit(curPath, cur[:len(cur)-trail])
			cur = append(append([]string{}, lead...), raw)
			curPath = path
			trail = 0
			scan.feed(line)
			continue
		}

		cur = append(cur, raw)
		if topLevel && isCommentOrBlank(line) {
			trail++
		} else {
			trail = 0
		}
		scan.feed(line)
	}
	emit(curPath, cur)

	if curPath == nil && doc.root.text == "" && len(doc.sections) == 0 {
		// Empty document: keep an empty root so callers can append to it.
		doc.root = tomlBlock{}
	}
	return doc, nil
}

// parseTableHeader parses "[a.b]" or "[[a.b]]" into its path segments,
// honoring quoted keys such as [hooks.state."/path:pre:0:0"].
func parseTableHeader(line string) ([]string, error) {
	body := line
	// Strip a trailing comment that sits outside any quoted key.
	if idx := headerEnd(body); idx >= 0 {
		body = body[:idx+1]
	}
	body = strings.TrimSpace(body)
	array := strings.HasPrefix(body, "[[")
	switch {
	case array && strings.HasSuffix(body, "]]"):
		body = body[2 : len(body)-2]
	case !array && strings.HasPrefix(body, "[") && strings.HasSuffix(body, "]"):
		body = body[1 : len(body)-1]
	default:
		return nil, fmt.Errorf("malformed table header: %s", line)
	}
	path, err := parseKeyPath(body)
	if err != nil {
		return nil, fmt.Errorf("malformed table header %s: %w", line, err)
	}
	if len(path) == 0 {
		return nil, fmt.Errorf("empty table header: %s", line)
	}
	return path, nil
}

// headerEnd returns the index of the header's final ']', ignoring brackets
// inside quoted key segments. It returns -1 when no closing bracket is found.
func headerEnd(s string) int {
	depth := 0
	last := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			i++
			for i < len(s) {
				if s[i] == '\\' {
					i++
				} else if s[i] == '"' {
					break
				}
				i++
			}
		case '\'':
			i++
			for i < len(s) && s[i] != '\'' {
				i++
			}
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				last = i
			}
		case '#':
			if depth == 0 {
				return last
			}
		}
	}
	return last
}

// parseKeyPath splits a dotted key into segments, unquoting quoted ones.
func parseKeyPath(s string) ([]string, error) {
	var (
		out []string
		cur strings.Builder
		any bool
	)
	i := 0
	for i < len(s) {
		switch c := s[i]; {
		case c == ' ' || c == '\t':
			i++
		case c == '.':
			if !any {
				return nil, fmt.Errorf("empty key segment")
			}
			out = append(out, cur.String())
			cur.Reset()
			any = false
			i++
		case c == '"':
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated quoted key")
			}
			i++
			any = true
		case c == '\'':
			i++
			for i < len(s) && s[i] != '\'' {
				cur.WriteByte(s[i])
				i++
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated quoted key")
			}
			i++
			any = true
		default:
			cur.WriteByte(c)
			any = true
			i++
		}
	}
	if !any && len(out) > 0 {
		return nil, fmt.Errorf("trailing dot in key")
	}
	if any {
		out = append(out, cur.String())
	}
	return out, nil
}

// rootEntry is one item of a document's root block: either a key assignment
// (with any comment lines that precede it) or a standalone comment/blank run.
type rootEntry struct {
	key  string // "" for a comment/blank run
	text string
}

// parseRootEntries splits the root block into ordered entries so a single key
// can be replaced in place without touching the rest.
func parseRootEntries(text string) []rootEntry {
	var (
		scan    tomlScan
		out     []rootEntry
		cur     []string
		curKey  string
		trail   int
		inEntry bool
	)
	flush := func(keep []string) {
		if len(keep) == 0 {
			return
		}
		out = append(out, rootEntry{key: curKey, text: strings.Join(keep, "")})
	}

	for _, raw := range splitLines(text) {
		line := strings.TrimRight(raw, "\r\n")
		topLevel := scan.atTopLevel()

		if topLevel && !isCommentOrBlank(line) {
			// A new key assignment starts here. Anything buffered before it,
			// minus its own leading comment run, closes out first.
			if inEntry || len(cur) > 0 {
				lead := cur[len(cur)-trail:]
				flush(cur[:len(cur)-trail])
				cur = append([]string{}, lead...)
			}
			curKey = rootKeyOf(line)
			inEntry = true
			trail = 0
			cur = append(cur, raw)
			scan.feed(line)
			continue
		}

		cur = append(cur, raw)
		if topLevel && isCommentOrBlank(line) {
			trail++
		} else {
			trail = 0
		}
		scan.feed(line)
	}
	if len(cur) > 0 {
		flush(cur)
	}
	return out
}

// rootKeyOf extracts the (possibly dotted) key a root assignment defines,
// canonicalized by removing insignificant whitespace. It returns "" when the
// line is not an assignment.
func rootKeyOf(line string) string {
	depth := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			i++
			for i < len(line) {
				if line[i] == '\\' {
					i++
				} else if line[i] == '"' {
					break
				}
				i++
			}
		case '\'':
			i++
			for i < len(line) && line[i] != '\'' {
				i++
			}
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		case '=':
			if depth == 0 {
				key := strings.TrimSpace(line[:i])
				if key == "" {
					return ""
				}
				return strings.Join(strings.Fields(strings.ReplaceAll(key, ".", " . ")), "")
			}
		}
	}
	return ""
}
