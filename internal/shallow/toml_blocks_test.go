package shallow

import (
	"strings"
	"testing"
)

// The splice is only safe if the block scanner never mistakes a `[` inside a
// comment, a string or a multi-line value for a table header, and if an
// untouched document round-trips byte-for-byte.

const trickyTOML = `# leading comment
model = "gpt-5.2-codex"
tricky = "a [not.a.header] b"
literal = 'also [not.a.header]'
prose = """
[definitely.not.a.header]
still inside the string
"""
raw = '''
[nor.this]
'''
list = [
  # [not.a.header] in a comment inside an array
  "one",
  "two",
]
inline = { a = 1, b = "[x]" }
trailing = "value"  # [comment.header]

# a comment attached to the next table
[skills]
config = [
  { path = "/a/b" },
  { path = "/c/d" },
]

[mcp_servers.kernel]
url = "https://example/mcp"

[hooks.state."/Users/x/.codex/hooks.json:pre_tool_use:0:0"]
trusted_hash = "abc"

[[arrayed]]
n = 1

[[arrayed]]
n = 2
`

func TestParseTOMLBlocksRoundTrip(t *testing.T) {
	doc, err := parseTOMLBlocks([]byte(trickyTOML))
	if err != nil {
		t.Fatalf("parseTOMLBlocks: %v", err)
	}
	if got := doc.render(); got != trickyTOML {
		t.Fatalf("round trip is not byte-identical:\n--- got ---\n%s\n--- want ---\n%s", got, trickyTOML)
	}

	var paths []string
	for _, s := range doc.sections {
		paths = append(paths, strings.Join(s.path, "\x1f"))
	}
	want := []string{
		"skills",
		"mcp_servers\x1fkernel",
		"hooks\x1fstate\x1f/Users/x/.codex/hooks.json:pre_tool_use:0:0",
		"arrayed",
		"arrayed",
	}
	if len(paths) != len(want) {
		t.Fatalf("sections = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("section %d = %q, want %q", i, paths[i], want[i])
		}
	}

	// The root block must stop before the comment that introduces [skills]:
	// a header's leading comment travels with the header, so replacing the
	// section takes its comment with it instead of orphaning it.
	if strings.Contains(doc.root.text, "a comment attached to the next table") {
		t.Errorf("leading comment stayed with the root block:\n%s", doc.root.text)
	}
	if !strings.Contains(doc.sections[0].text, "a comment attached to the next table") {
		t.Errorf("leading comment did not travel with [skills]:\n%s", doc.sections[0].text)
	}
	if !strings.Contains(doc.root.text, `trailing = "value"`) {
		t.Errorf("root block lost its last key:\n%s", doc.root.text)
	}
}

func TestParseRootEntriesRoundTripAndKeys(t *testing.T) {
	doc, err := parseTOMLBlocks([]byte(trickyTOML))
	if err != nil {
		t.Fatal(err)
	}
	entries := parseRootEntries(doc.root.text)

	var rebuilt strings.Builder
	var keys []string
	for _, e := range entries {
		rebuilt.WriteString(e.text)
		if e.key != "" {
			keys = append(keys, e.key)
		}
	}
	if rebuilt.String() != doc.root.text {
		t.Fatalf("root entries do not round-trip:\n%s", rebuilt.String())
	}
	want := []string{"model", "tricky", "literal", "prose", "raw", "list", "inline", "trailing"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("key %d = %q, want %q", i, keys[i], want[i])
		}
	}
}

func TestParseTableHeader(t *testing.T) {
	tests := map[string][]string{
		`[a]`:                       {"a"},
		`[a.b.c]`:                   {"a", "b", "c"},
		`[[a.b]]`:                   {"a", "b"},
		`[ a . b ]`:                 {"a", "b"},
		`[hooks.state."x.y:0:0"]`:   {"hooks", "state", "x.y:0:0"},
		`[projects."/a/b"]  # note`: {"projects", "/a/b"},
		`[servers.'lit.eral']`:      {"servers", "lit.eral"},
	}
	for input, want := range tests {
		got, err := parseTableHeader(input)
		if err != nil {
			t.Errorf("parseTableHeader(%q): %v", input, err)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("parseTableHeader(%q) = %v, want %v", input, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("parseTableHeader(%q) = %v, want %v", input, got, want)
				break
			}
		}
	}

	for _, bad := range []string{`[`, `[]`, `[a`, `[a."unterminated]`} {
		if _, err := parseTableHeader(bad); err == nil {
			t.Errorf("parseTableHeader(%q) accepted a malformed header", bad)
		}
	}
}

func TestParseTOMLBlocksEmptyAndHeaderOnly(t *testing.T) {
	doc, err := parseTOMLBlocks(nil)
	if err != nil {
		t.Fatal(err)
	}
	if doc.render() != "" || len(doc.sections) != 0 {
		t.Errorf("empty document = %q with %d sections", doc.render(), len(doc.sections))
	}

	doc, err = parseTOMLBlocks([]byte("[a]\nx = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.root.text != "" {
		t.Errorf("root = %q, want empty", doc.root.text)
	}
	if doc.render() != "[a]\nx = 1\n" {
		t.Errorf("render = %q", doc.render())
	}

	// A file with no trailing newline must survive intact.
	doc, err = parseTOMLBlocks([]byte("model = 1"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.render() != "model = 1" {
		t.Errorf("render = %q, want %q", doc.render(), "model = 1")
	}
}

// TestCodexUnitPathAndLocality pins the classification the merge keys on.
func TestCodexUnitPathAndLocality(t *testing.T) {
	unit := func(path ...string) string { return strings.Join(codexUnitPath(path), ".") }

	if got := unit("mcp_servers", "kernel", "env"); got != "mcp_servers.kernel" {
		t.Errorf("unit(mcp_servers.kernel.env) = %q, want mcp_servers.kernel", got)
	}
	if got := unit("mcp_servers"); got != "mcp_servers" {
		t.Errorf("unit(mcp_servers) = %q", got)
	}
	if got := unit("features"); got != "features" {
		t.Errorf("unit(features) = %q", got)
	}
	if got := unit("hooks", "state", "x"); got != "hooks" {
		t.Errorf("unit(hooks.state.x) = %q, want hooks", got)
	}

	local := map[string]bool{
		"hooks.state":             true,
		"hooks.state.x":           true,
		"projects":                true,
		"projects./a":             true,
		"notice":                  true,
		"notice.model_migrations": true,
		"hooks":                   false,
		"skills":                  false,
		"mcp_servers.projects":    false,
	}
	for path, want := range local {
		if got := isCodexProfileLocal(strings.Split(path, ".")); got != want {
			t.Errorf("isCodexProfileLocal(%s) = %v, want %v", path, got, want)
		}
	}
}
