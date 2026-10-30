package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	want := fmt.Sprintf("caam_0.1.24_%s_%s.%s", runtime.GOOS, runtime.GOARCH, ext)
	if name := u.binaryAssetName("0.1.24"); name != want {
		t.Errorf("binaryAssetName() = %q, want %q", name, want)
	}
}

// A published archive must match both the selected release and this platform.
// Matching any version would allow an older signed archive to stand in for it.
func TestBinaryAssetSelection(t *testing.T) {
	u := New(DefaultConfig())

	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	want := fmt.Sprintf("caam_0.1.17_%s_%s.%s", runtime.GOOS, runtime.GOARCH, ext)

	// The exact asset list published for v0.1.17, per issue #75.
	assets := []Asset{
		{Name: "caam_0.1.17_darwin_amd64.tar.gz"},
		{Name: "caam_0.1.17_darwin_arm64.tar.gz"},
		{Name: "caam_0.1.17_linux_amd64.tar.gz"},
		{Name: "caam_0.1.17_linux_arm64.tar.gz"},
		{Name: "caam_0.1.17_windows_amd64.zip"},
		{Name: "release-manifest.json"},
		{Name: "SHA256SUMS"},
		{Name: "SHA256SUMS.sig"},
	}

	got := selectBinaryAsset(assets, u.binaryAssetName("0.1.17"))
	if got == nil {
		t.Fatalf("selectBinaryAsset returned nil for published archive %q", want)
	}
	if got.Name != want {
		t.Errorf("selected %q, want %q", got.Name, want)
	}
}

func TestBinaryAssetSelectionAcrossVersions(t *testing.T) {
	u := New(DefaultConfig())

	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	for _, v := range []string{"0.1.17", "0.1.18", "1.0.0", "1.2.3-rc.1"} {
		name := fmt.Sprintf("caam_%s_%s_%s.%s", v, runtime.GOOS, runtime.GOARCH, ext)
		older := fmt.Sprintf("caam_0.0.1_%s_%s.%s", runtime.GOOS, runtime.GOARCH, ext)
		got := selectBinaryAsset([]Asset{{Name: older}, {Name: name}}, u.binaryAssetName(v))
		if got == nil || got.Name != name {
			t.Errorf("selectBinaryAsset failed to match published asset %q", name)
		}
		if got := selectBinaryAsset([]Asset{{Name: older}}, u.binaryAssetName(v)); got != nil {
			t.Errorf("selected older archive %q for release %s", got.Name, v)
		}
	}
	// SHA256SUMS must never be selected as the binary.
	if got := selectBinaryAsset([]Asset{{Name: "SHA256SUMS"}}, u.binaryAssetName("0.1.24")); got != nil {
		t.Errorf("selectBinaryAsset wrongly selected %q", got.Name)
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
		APIBase:    server.URL,
	}
	u := New(cfg)

	ctx := context.Background()
	release, err := u.fetchLatestRelease(ctx)
	if err != nil {
		t.Fatalf("fetchLatestRelease: %v", err)
	}
	if release.TagName != "v1.0.0" {
		t.Errorf("TagName = %q, want %q", release.TagName, "v1.0.0")
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

	u := New(Config{Owner: "test", Repo: "test", Channel: ChannelStable, HTTPClient: server.Client(), APIBase: server.URL})
	release, err := u.fetchLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("fetchLatestRelease: %v", err)
	}
	if release.TagName != "v1.0.0" {
		t.Errorf("selected draft release: %s", release.TagName)
	}
}

func TestFetchLatestRelease_FiltersPrereleases(t *testing.T) {
	releases := []Release{
		{TagName: "v2.0.0-beta", Prerelease: true},
		{TagName: "v1.0.0", Prerelease: false},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(releases)
	}))
	defer server.Close()
	u := New(Config{Owner: "test", Repo: "test", Channel: ChannelStable, HTTPClient: server.Client(), APIBase: server.URL})
	release, err := u.fetchLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("fetchLatestRelease: %v", err)
	}
	if release.TagName != "v1.0.0" {
		t.Errorf("selected prerelease for stable channel: %s", release.TagName)
	}
}

func TestNormalizedReleaseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"0.1.24", "0.1.24"},
		{"v0.1.24", "0.1.24"},
		{"v1.2.3-rc.1+build.7", "1.2.3-rc.1+build.7"},
		{"18446744073709551616.0.0", "18446744073709551616.0.0"},
		{"", ""},
		{"dev", ""},
		{"latest", ""},
		{"vv0.1.24", ""},
		{"v0.1", ""},
		{"v0.1.24.1", ""},
		{"v0.01.24", ""},
		{"v0.1.not-a-number", ""},
		{"v0.1.24-", ""},
		{"v0.1.24+", ""},
		{"v0.1.24-rc..1", ""},
		{"v0.1.24/../../v0.1.17", ""},
		{"v0.1.24?tag=v0.1.17", ""},
		{" v0.1.24", ""},
		{"v0.1.24\n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := normalizedReleaseVersion(tt.input)
			if tt.want == "" {
				if err == nil {
					t.Errorf("accepted malformed release version %q", tt.input)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("normalizedReleaseVersion(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
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
	checksumsContent := `older  caam_0.9.0_linux_amd64.tar.gz
abc123  caam_1.0.0_linux_amd64.tar.gz
def456  caam_1.0.0_darwin_arm64.tar.gz
ghi789  SHA256SUMS.sig
`
	checksumsPath := filepath.Join(tmpDir, "SHA256SUMS")
	if err := os.WriteFile(checksumsPath, []byte(checksumsContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		assetName string
		wantHash  string
		wantErr   bool
	}{
		{
			name:      "exact match",
			assetName: "caam_1.0.0_linux_amd64.tar.gz",
			wantHash:  "abc123",
		},
		{
			name:      "wildcard is not an archive name",
			assetName: "caam_*_linux_amd64.tar.gz",
			wantErr:   true,
		},
		{
			name:      "another version cannot supply the checksum",
			assetName: "caam_1.0.1_linux_amd64.tar.gz",
			wantErr:   true,
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
