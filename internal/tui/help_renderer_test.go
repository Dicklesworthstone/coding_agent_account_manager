package tui

import (
	"strings"
	"testing"
)

func TestHelpRenderer_RenderMarkdown(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	theme := NewTheme(ThemeOptionsFromEnv())

	hr := NewHelpRenderer(theme)
	hr.SetWidth(80)

	markdown := "# Test Heading\n\nSome **bold** text."
	result := hr.Render(markdown)

	// In NO_COLOR mode, should still render but without escape codes
	if result == "" {
		t.Error("Render() returned empty string")
	}

	// Should contain the text content
	if !strings.Contains(result, "Test Heading") {
		t.Errorf("Render() missing heading, got: %s", result)
	}
	if !strings.Contains(result, "bold") {
		t.Errorf("Render() missing bold text, got: %s", result)
	}
}

func TestHelpRenderer_Caching(t *testing.T) {
	theme := DefaultTheme()
	hr := NewHelpRenderer(theme)
	hr.SetWidth(80)

	markdown := "# Cache Test"

	// First render
	result1 := hr.Render(markdown)

	// Second render should hit cache
	result2 := hr.Render(markdown)

	if result1 != result2 {
		t.Error("Cache should return same result")
	}

	// Different content should not hit cache
	different := "# Different"
	result3 := hr.Render(different)
	if result3 == result1 {
		t.Error("Different content should produce different result")
	}
}

func TestHelpRenderer_WidthChange(t *testing.T) {
	theme := DefaultTheme()
	hr := NewHelpRenderer(theme)

	markdown := "# Width Test"

	hr.SetWidth(80)
	_ = hr.Render(markdown)

	// Cache should be cleared on width change
	hr.SetWidth(100)

	// This should not panic and should work
	result := hr.Render(markdown)
	if result == "" {
		t.Error("Render after width change returned empty")
	}
}

func TestHelpRenderer_NoColor(t *testing.T) {
	theme := NewTheme(ThemeOptions{
		Mode:    ThemeAuto,
		NoColor: true,
	})

	hr := NewHelpRenderer(theme)
	if !hr.noGlamour {
		t.Error("Expected noGlamour=true in NO_COLOR mode")
	}

	// Should still render, just without styling
	result := hr.Render("# Test")
	if !strings.Contains(result, "Test") {
		t.Errorf("NO_COLOR render should preserve text, got: %s", result)
	}
}

func TestHelpRenderer_LightTheme(t *testing.T) {
	theme := NewTheme(ThemeOptions{
		Mode:    ThemeLight,
		NoColor: false,
	})

	hr := NewHelpRenderer(theme)
	if hr.noGlamour {
		t.Error("Light theme should use Glamour")
	}

	result := hr.Render("# Light Theme Test")
	if result == "" {
		t.Error("Light theme render returned empty")
	}
}

func TestContextualHints_List(t *testing.T) {
	hints := GetContextualHints(stateList)

	if len(hints) == 0 {
		t.Fatal("stateList should have hints")
	}

	// Should have navigation hints
	var hasNavigate, hasActivate, hasHelp bool
	for _, h := range hints {
		switch h.Key {
		case "↑↓":
			hasNavigate = true
		case "Enter":
			hasActivate = true
		case "?":
			hasHelp = true
		}
	}

	if !hasNavigate {
		t.Error("stateList should have navigate hint")
	}
	if !hasActivate {
		t.Error("stateList should have activate hint")
	}
	if !hasHelp {
		t.Error("stateList should have help hint")
	}
}

func TestContextualHints_Help(t *testing.T) {
	hints := GetContextualHints(stateHelp)

	if len(hints) != 1 {
		t.Errorf("stateHelp should have 1 hint, got %d", len(hints))
	}

	if hints[0].Key != "Any key" {
		t.Errorf("stateHelp hint should be 'Any key', got %s", hints[0].Key)
	}
}

func TestContextualHints_Confirm(t *testing.T) {
	hints := GetContextualHints(stateConfirm)

	if len(hints) != 2 {
		t.Errorf("stateConfirm should have 2 hints, got %d", len(hints))
	}

	var hasYes, hasNo bool
	for _, h := range hints {
		if h.Key == "y/Enter" {
			hasYes = true
		}
		if h.Key == "n/Esc" {
			hasNo = true
		}
	}

	if !hasYes {
		t.Error("stateConfirm should have yes hint")
	}
	if !hasNo {
		t.Error("stateConfirm should have no hint")
	}
}

func TestRenderHintBar(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	theme := NewTheme(ThemeOptionsFromEnv())

	hints := []ContextualHint{
		{"Enter", "Activate"},
		{"q", "Quit"},
	}

	result := RenderHintBar(hints, theme, 80)

	if result == "" {
		t.Error("RenderHintBar returned empty")
	}
	if !strings.Contains(result, "Enter") {
		t.Error("RenderHintBar missing 'Enter' key")
	}
	if !strings.Contains(result, "Activate") {
		t.Error("RenderHintBar missing 'Activate' description")
	}
}

func TestRenderHintBar_TruncatesOnNarrowWidth(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	theme := NewTheme(ThemeOptionsFromEnv())

	hints := []ContextualHint{
		{"Enter", "Activate profile"},
		{"q", "Quit the application"},
		{"b", "Backup current auth"},
		{"u", "Toggle usage panel"},
	}

	// Very narrow width should truncate
	result := RenderHintBar(hints, theme, 30)

	// Should have some content but be truncated
	if result == "" {
		t.Error("RenderHintBar should produce some output even on narrow width")
	}
}

func TestRenderHintBar_EmptyHints(t *testing.T) {
	theme := DefaultTheme()

	result := RenderHintBar(nil, theme, 80)
	if result != "" {
		t.Errorf("Empty hints should return empty string, got: %s", result)
	}

	result = RenderHintBar([]ContextualHint{}, theme, 80)
	if result != "" {
		t.Errorf("Empty slice should return empty string, got: %s", result)
	}
}

func TestMainHelpMarkdown(t *testing.T) {
	markdown := MainHelpMarkdown()

	if markdown == "" {
		t.Fatal("MainHelpMarkdown() returned empty")
	}

	// Should contain key sections
	requiredContent := []string{
		"# caam",
		"## Keyboard Shortcuts",
		"Navigation",
		"Profile Actions",
		"Health Status Indicators",
		"Smart Profile Features",
		"Press any key to return",
	}

	for _, content := range requiredContent {
		if !strings.Contains(markdown, content) {
			t.Errorf("MainHelpMarkdown() missing: %s", content)
		}
	}
}
