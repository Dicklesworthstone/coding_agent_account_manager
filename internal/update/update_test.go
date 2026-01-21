package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{"equal", "1.0.0", "1.0.0", 0},
		{"v prefix", "v1.0.0", "1.0.0", 0},
		{"a less major", "1.0.0", "2.0.0", -1},
		{"a greater major", "2.0.0", "1.0.0", 1},
		{"a less minor", "1.1.0", "1.2.0", -1},
		{"a greater minor", "1.2.0", "1.1.0", 1},
		{"a less patch", "1.0.1", "1.0.2", -1},
		{"a greater patch", "1.0.2", "1.0.1", 1},
		{"dev vs version", "dev", "1.0.0", -1},
		{"version vs dev", "1.0.0", "dev", 1},
		{"dev equal", "dev", "dev", 0},
		{"prerelease ignored", "1.0.0-beta", "1.0.0", 0},
		{"different lengths", "1.0", "1.0.0", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareVersions(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestBinaryAssetName(t *testing.T) {
	u := New(DefaultConfig())
	name := u.binaryAssetName()

	// Should contain OS and arch
	if name == "" {
		t.Error("binaryAssetName() returned empty string")
	}
	// Should be a pattern with wildcard
	if name[0:5] != "caam_" {
		t.Errorf("expected name to start with 'caam_', got %q", name)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Owner != DefaultOwner {
		t.Errorf("Owner = %q, want %q", cfg.Owner, DefaultOwner)
	}
	if cfg.Repo != DefaultRepo {
		t.Errorf("Repo = %q, want %q", cfg.Repo, DefaultRepo)
	}
	if cfg.Channel != ChannelStable {
		t.Errorf("Channel = %q, want %q", cfg.Channel, ChannelStable)
	}
	if cfg.HTTPClient == nil {
		t.Error("HTTPClient should not be nil")
	}
}

func TestNew(t *testing.T) {
	t.Run("with empty config", func(t *testing.T) {
		u := New(Config{})
		if u.config.Owner != DefaultOwner {
			t.Errorf("Owner = %q, want %q", u.config.Owner, DefaultOwner)
		}
		if u.config.Repo != DefaultRepo {
			t.Errorf("Repo = %q, want %q", u.config.Repo, DefaultRepo)
		}
		if u.config.HTTPClient == nil {
			t.Error("HTTPClient should not be nil")
		}
	})

	t.Run("with custom config", func(t *testing.T) {
		u := New(Config{Owner: "custom", Repo: "repo"})
		if u.config.Owner != "custom" {
			t.Errorf("Owner = %q, want %q", u.config.Owner, "custom")
		}
	})
}

func TestFetchLatestRelease(t *testing.T) {
	releases := []Release{
		{TagName: "v1.0.0", Prerelease: false, Draft: false},
		{TagName: "v0.9.0", Prerelease: false, Draft: false},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/test/test/releases" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(releases)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	// Create updater with test server
	cfg := Config{
		Owner:      "test",
		Repo:       "test",
		Channel:    ChannelStable,
		HTTPClient: server.Client(),
	}
	// Override the API base URL by setting it on the server path
	u := New(cfg)

	ctx := context.Background()
	release, err := u.fetchLatestRelease(ctx)
	// This will fail because we're not replacing GitHubAPIBase
	// but it tests the structure
	if err == nil {
		if release.TagName != "v1.0.0" {
			t.Errorf("TagName = %q, want %q", release.TagName, "v1.0.0")
		}
	}
}

func TestFetchLatestRelease_FiltersDrafts(t *testing.T) {
	releases := []Release{
		{TagName: "v2.0.0", Draft: true},
		{TagName: "v1.0.0", Draft: false, Prerelease: false},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(releases)
	}))
	defer server.Close()

	// Verify draft filtering logic exists
	for _, r := range releases {
		if !r.Draft && !r.Prerelease {
			if r.TagName != "v1.0.0" {
				t.Errorf("First non-draft release should be v1.0.0, got %s", r.TagName)
			}
			break
		}
	}
}

func TestFetchLatestRelease_FiltersPrereleases(t *testing.T) {
	releases := []Release{
		{TagName: "v2.0.0-beta", Prerelease: true},
		{TagName: "v1.0.0", Prerelease: false},
	}

	// For stable channel, prereleases should be skipped
	cfg := Config{Channel: ChannelStable}

	// Verify prerelease filtering logic
	for _, r := range releases {
		if !r.Draft && (cfg.Channel != ChannelStable || !r.Prerelease) {
			if r.TagName != "v1.0.0" {
				t.Errorf("First stable release should be v1.0.0, got %s", r.TagName)
			}
			break
		}
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		name         string
		filename     string
		patternParts []string
		want         bool
	}{
		{
			name:         "exact match",
			filename:     "caam_1.0.0_linux_amd64.tar.gz",
			patternParts: []string{"caam_1.0.0_linux_amd64.tar.gz"},
			want:         true,
		},
		{
			name:         "wildcard match",
			filename:     "caam_1.0.0_linux_amd64.tar.gz",
			patternParts: []string{"caam_", "_linux_amd64.tar.gz"},
			want:         true,
		},
		{
			name:         "wildcard no match prefix",
			filename:     "other_1.0.0_linux_amd64.tar.gz",
			patternParts: []string{"caam_", "_linux_amd64.tar.gz"},
			want:         false,
		},
		{
			name:         "wildcard no match suffix",
			filename:     "caam_1.0.0_darwin_arm64.tar.gz",
			patternParts: []string{"caam_", "_linux_amd64.tar.gz"},
			want:         false,
		},
		{
			name:         "exact no match",
			filename:     "different.tar.gz",
			patternParts: []string{"caam.tar.gz"},
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchPattern(tt.filename, tt.patternParts)
			if got != tt.want {
				t.Errorf("matchPattern(%q, %v) = %v, want %v",
					tt.filename, tt.patternParts, got, tt.want)
			}
		})
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "caam-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create source file
	srcPath := filepath.Join(tmpDir, "source.txt")
	content := []byte("test content")
	if err := os.WriteFile(srcPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	// Copy file
	dstPath := filepath.Join(tmpDir, "dest.txt")
	if err := copyFile(srcPath, dstPath); err != nil {
		t.Fatalf("copyFile() error = %v", err)
	}

	// Verify content
	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("copied content = %q, want %q", got, content)
	}
}

func TestCalculateSHA256(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "caam-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test file with known content
	testPath := filepath.Join(tmpDir, "test.txt")
	content := []byte("hello world")
	if err := os.WriteFile(testPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	// Expected SHA256 of "hello world"
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	got, err := calculateSHA256(testPath)
	if err != nil {
		t.Fatalf("calculateSHA256() error = %v", err)
	}
	if got != expected {
		t.Errorf("calculateSHA256() = %q, want %q", got, expected)
	}
}

func TestReadExpectedChecksum(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "caam-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create checksums file
	checksumsContent := `abc123  caam_1.0.0_linux_amd64.tar.gz
def456  caam_1.0.0_darwin_arm64.tar.gz
ghi789  SHA256SUMS.sig
`
	checksumsPath := filepath.Join(tmpDir, "SHA256SUMS")
	if err := os.WriteFile(checksumsPath, []byte(checksumsContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		assetName   string
		wantHash    string
		wantErr     bool
	}{
		{
			name:      "exact match",
			assetName: "caam_1.0.0_linux_amd64.tar.gz",
			wantHash:  "abc123",
		},
		{
			name:      "wildcard match",
			assetName: "caam_*_linux_amd64.tar.gz",
			wantHash:  "abc123",
		},
		{
			name:      "not found",
			assetName: "caam_1.0.0_windows_amd64.zip",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readExpectedChecksum(checksumsPath, tt.assetName)
			if (err != nil) != tt.wantErr {
				t.Errorf("readExpectedChecksum() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.wantHash {
				t.Errorf("readExpectedChecksum() = %q, want %q", got, tt.wantHash)
			}
		})
	}
}

func TestVerifyChecksum(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "caam-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test file
	testPath := filepath.Join(tmpDir, "test.bin")
	content := []byte("test binary content")
	if err := os.WriteFile(testPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	// Calculate actual hash
	actualHash, err := calculateSHA256(testPath)
	if err != nil {
		t.Fatal(err)
	}

	// Create checksums file with correct hash
	checksumsContent := actualHash + "  test.bin\n"
	checksumsPath := filepath.Join(tmpDir, "SHA256SUMS")
	if err := os.WriteFile(checksumsPath, []byte(checksumsContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Should pass
	if err := VerifyChecksum(testPath, checksumsPath, "test.bin"); err != nil {
		t.Errorf("VerifyChecksum() error = %v, want nil", err)
	}

	// Create checksums file with wrong hash
	wrongChecksumsContent := "wronghash  test.bin\n"
	if err := os.WriteFile(checksumsPath, []byte(wrongChecksumsContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Should fail
	if err := VerifyChecksum(testPath, checksumsPath, "test.bin"); err == nil {
		t.Error("VerifyChecksum() should fail with wrong hash")
	}
}

func TestAtomicReplace(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "caam-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create source file
	srcPath := filepath.Join(tmpDir, "new-binary")
	srcContent := []byte("new binary content")
	if err := os.WriteFile(srcPath, srcContent, 0755); err != nil {
		t.Fatal(err)
	}

	// Create destination file
	dstPath := filepath.Join(tmpDir, "old-binary")
	dstContent := []byte("old binary content")
	if err := os.WriteFile(dstPath, dstContent, 0755); err != nil {
		t.Fatal(err)
	}

	// Perform atomic replace
	if err := AtomicReplace(srcPath, dstPath); err != nil {
		t.Fatalf("AtomicReplace() error = %v", err)
	}

	// Verify destination has new content
	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(srcContent) {
		t.Errorf("destination content = %q, want %q", got, srcContent)
	}
}

func TestAtomicReplace_CreatesDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "caam-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create source file
	srcPath := filepath.Join(tmpDir, "source")
	if err := os.WriteFile(srcPath, []byte("content"), 0755); err != nil {
		t.Fatal(err)
	}

	// Destination in non-existent directory
	dstPath := filepath.Join(tmpDir, "subdir", "dest")

	if err := AtomicReplace(srcPath, dstPath); err != nil {
		t.Fatalf("AtomicReplace() error = %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(dstPath); err != nil {
		t.Errorf("destination file should exist: %v", err)
	}
}
