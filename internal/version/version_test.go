// Package version provides version information for the application.
package version

import (
	"runtime"
	"strings"
	"testing"
)

func TestInfo(t *testing.T) {
	info := Info()

	// Info should contain all components
	if info == "" {
		t.Error("Info() returned empty string")
	}

	// Should contain version
	if !strings.Contains(info, Version) {
		t.Errorf("Info() should contain Version %q, got %q", Version, info)
	}

	// Should contain commit
	if !strings.Contains(info, Commit) {
		t.Errorf("Info() should contain Commit %q, got %q", Commit, info)
	}

	// Should contain date
	if !strings.Contains(info, Date) {
		t.Errorf("Info() should contain Date %q, got %q", Date, info)
	}

	// Should contain Go runtime version
	goVersion := runtime.Version()
	if !strings.Contains(info, goVersion) {
		t.Errorf("Info() should contain Go version %q, got %q", goVersion, info)
	}

	// Should contain "caam" prefix
	if !strings.HasPrefix(info, "caam ") {
		t.Errorf("Info() should start with 'caam ', got %q", info)
	}
}

func TestShort(t *testing.T) {
	short := Short()

	// Short should return the version
	if short != Version {
		t.Errorf("Short() = %q, want %q", short, Version)
	}

	// Short should not be empty
	if short == "" {
		t.Error("Short() returned empty string")
	}
}

func TestDefaultValues(t *testing.T) {
	// Test that default values are set (when not overridden by ldflags)
	// In test environment, these should be their default values

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"Version", Version, "dev"},
		{"Commit", Commit, "unknown"},
		{"Date", Date, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value != tt.want {
				t.Errorf("%s = %q, want %q", tt.name, tt.value, tt.want)
			}
		})
	}
}

func TestInfoFormat(t *testing.T) {
	info := Info()

	// Info should follow the format: "caam VERSION (COMMIT) built on DATE with GO_VERSION"
	// Check for expected structural elements

	if !strings.Contains(info, "(") || !strings.Contains(info, ")") {
		t.Errorf("Info() should contain parentheses for commit, got %q", info)
	}

	if !strings.Contains(info, "built on") {
		t.Errorf("Info() should contain 'built on', got %q", info)
	}

	if !strings.Contains(info, "with") {
		t.Errorf("Info() should contain 'with', got %q", info)
	}
}

func TestShortIsSubstringOfInfo(t *testing.T) {
	short := Short()
	info := Info()

	if !strings.Contains(info, short) {
		t.Errorf("Info() should contain Short() value; Info=%q, Short=%q", info, short)
	}
}

func TestInfoContainsGoVersion(t *testing.T) {
	info := Info()
	goVer := runtime.Version()

	if !strings.Contains(info, goVer) {
		t.Errorf("Info() should contain runtime.Version() %q, got %q", goVer, info)
	}
}

// TestInfoDeterministic verifies that Info() returns consistent results
func TestInfoDeterministic(t *testing.T) {
	info1 := Info()
	info2 := Info()

	if info1 != info2 {
		t.Errorf("Info() should be deterministic; got %q and %q", info1, info2)
	}
}

// TestShortDeterministic verifies that Short() returns consistent results
func TestShortDeterministic(t *testing.T) {
	short1 := Short()
	short2 := Short()

	if short1 != short2 {
		t.Errorf("Short() should be deterministic; got %q and %q", short1, short2)
	}
}
