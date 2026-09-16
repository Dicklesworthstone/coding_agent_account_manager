//go:build caam_scratch

package shallow

import (
	"os"
	"testing"
)

func TestScratchRoundTripRealConfig(t *testing.T) {
	path := os.Getenv("SCRATCH_TOML")
	if path == "" {
		t.Skip("no SCRATCH_TOML")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip(err)
	}
	doc, err := parseTOMLBlocks(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := doc.render(); got != string(data) {
		t.Fatalf("round trip differs (len %d vs %d)", len(got), len(data))
	}
	t.Logf("sections=%d", len(doc.sections))
	for _, s := range doc.sections {
		t.Logf("  %v", s.path)
	}
	entries := parseRootEntries(doc.root.text)
	for _, e := range entries {
		t.Logf("root key=%q", e.key)
	}
	var b string
	for _, e := range entries {
		b += e.text
	}
	if b != doc.root.text {
		t.Fatal("root entries round trip differs")
	}
}
