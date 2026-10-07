// Package installscript tests the release-metadata helpers in the repository's
// install.sh by sourcing it in bash. The installer can parse GitHub release
// JSON with either python3 or jq; these tests hold both backends to the same
// results so a machine with only jq installs the same asset as one with python3.
package installscript

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const releaseJSON = `{
  "tag_name": "v9.9.9",
  "assets": [
    {"name": "caam_9.9.9_linux_amd64.tar.gz", "browser_download_url": ""},
    {"name": "caam_9.9.9_linux_amd64.tar.gz.sbom.json", "browser_download_url": "https://example.test/sbom"},
    {"name": "caam_9.9.9_linux_amd64.tar.gz", "browser_download_url": "https://example.test/linux_amd64.tar.gz"},
    {"name": "caam_9.9.9_windows_amd64.zip", "browser_download_url": "https://example.test/windows_amd64.zip"},
    {"name": "caam_9.9.9_darwinarm64.tar.gz", "browser_download_url": "https://example.test/darwinarm64.tar.gz"},
    {"name": "SHA256SUMS", "browser_download_url": "https://example.test/SHA256SUMS"},
    {"name": "SHA256SUMS.minisig", "browser_download_url": ""}
  ]
}`

func repoInstallScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	path, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("install.sh not found: %v", err)
	}
	return path
}

func bashPath(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	return bash
}

// backends returns the JSON backends usable on this machine, as shell
// snippets that pin install.sh to that backend after sourcing it.
func backends(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	if py, err := exec.LookPath("python3"); err == nil {
		out["python"] = `JSON_TOOL=python; PYTHON_CMD='` + py + `'`
	}
	if _, err := exec.LookPath("jq"); err == nil {
		out["jq"] = `JSON_TOOL=jq`
	}
	if len(out) == 0 {
		t.Skip("neither python3 nor jq available")
	}
	return out
}

// runInstallFunc sources install.sh (which does not run main when sourced),
// applies setup, then runs body. It returns stdout, stderr and the exit code.
func runInstallFunc(t *testing.T, env []string, setup, body, stdin string) (string, string, int) {
	t.Helper()
	script := `set -uo pipefail
source "$INSTALL_SH"
set +e
` + setup + `
` + body
	cmd := exec.Command(bashPath(t), "-c", script)
	// The child has a deliberately minimal environment, so give mktemp its own
	// test directory instead of falling back to the machine's shared /tmp.
	cmd.Env = append([]string{"INSTALL_SH=" + repoInstallScript(t), "NO_COLOR=1", "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}, env...)
	if !hasPath(env) {
		cmd.Env = append(cmd.Env, "PATH="+os.Getenv("PATH"))
	}
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run bash: %v", err)
	}
	return stdout.String(), stderr.String(), code
}

func hasPath(env []string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			return true
		}
	}
	return false
}

func TestSelectReleaseAssetBackendsAgree(t *testing.T) {
	cases := []struct {
		platform string
		want     string
		code     int
	}{
		// Skips the same-named asset with an empty URL and the .sbom.json.
		{"linux_amd64", "v9.9.9\nhttps://example.test/linux_amd64.tar.gz\ncaam_9.9.9_linux_amd64.tar.gz\n", 0},
		{"windows_amd64", "v9.9.9\nhttps://example.test/windows_amd64.zip\ncaam_9.9.9_windows_amd64.zip\n", 0},
		// Underscore-insensitive fallback.
		{"darwin_arm64", "v9.9.9\nhttps://example.test/darwinarm64.tar.gz\ncaam_9.9.9_darwinarm64.tar.gz\n", 0},
		{"freebsd_amd64", "v9.9.9\n\n\n", 1},
	}
	for name, setup := range backends(t) {
		for _, tc := range cases {
			out, stderr, code := runInstallFunc(t, nil, setup, `select_release_asset "`+tc.platform+`"`, releaseJSON)
			if out != tc.want || code != tc.code {
				t.Errorf("%s/%s: got (%q, exit %d), want (%q, exit %d); stderr=%s", name, tc.platform, out, code, tc.want, tc.code, stderr)
			}
		}
	}
}

func TestSelectNamedAssetBackendsAgree(t *testing.T) {
	cases := []struct {
		asset string
		want  string
		code  int
	}{
		{"SHA256SUMS", "https://example.test/SHA256SUMS\n", 0},
		{"SHA256SUMS.minisig", "\n", 1}, // present but without a URL
		{"SHA256SUMS.sig", "\n", 1},
	}
	for name, setup := range backends(t) {
		for _, tc := range cases {
			out, stderr, code := runInstallFunc(t, nil, setup, `select_named_asset "`+tc.asset+`"`, releaseJSON)
			if out != tc.want || code != tc.code {
				t.Errorf("%s/%s: got (%q, exit %d), want (%q, exit %d); stderr=%s", name, tc.asset, out, code, tc.want, tc.code, stderr)
			}
		}
	}
}

func TestFirstReleaseJSONBackendsAgree(t *testing.T) {
	for name, setup := range backends(t) {
		out, stderr, code := runInstallFunc(t, nil, setup,
			`first_release_json | select_named_asset SHA256SUMS`,
			`[{"tag_name":"v2","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/v2"}]},{"tag_name":"v1"}]`)
		if code != 0 || out != "https://example.test/v2\n" {
			t.Errorf("%s: got (%q, exit %d); stderr=%s", name, out, code, stderr)
		}
		for _, input := range []string{`[]`, `{"message":"Not Found"}`} {
			out, _, code = runInstallFunc(t, nil, setup, `first_release_json`, input)
			if code == 0 || strings.TrimSpace(out) != "" {
				t.Errorf("%s: input %s: got (%q, exit %d), want failure with no output", name, input, out, code)
			}
		}
	}
}

// A machine with jq but no python must use jq; one with neither must say that
// either tool would do.
func TestEnsureJSONToolFallsBackToJQ(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not available")
	}
	onlyJQ := t.TempDir()
	if err := os.Symlink(jq, filepath.Join(onlyJQ, "jq")); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runInstallFunc(t, []string{"PATH=" + onlyJQ}, "", `ensure_json_tool && printf '%s\n' "$JSON_TOOL"`, "")
	if code != 0 || out != "jq\n" {
		t.Fatalf("got (%q, exit %d), want jq; stderr=%s", out, code, stderr)
	}

	empty := t.TempDir()
	out, stderr, code = runInstallFunc(t, []string{"PATH=" + empty}, "", `ensure_json_tool`, "")
	if code == 0 {
		t.Fatalf("ensure_json_tool succeeded with no python3 or jq on PATH: %q", out)
	}
	if !strings.Contains(stderr, "python3 or jq is required") {
		t.Errorf("error should say python3 or jq is required, got stderr=%q", stderr)
	}
}

func TestVersionGE(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.21", "1.21", 0},
		{"1.22.3", "1.21", 0},
		{"1.21.0", "1.21", 0},
		{"1.20.14", "1.21", 1},
		{"2", "1.21", 0},
		{"1.9", "1.21", 1},
	}
	for _, tc := range cases {
		_, stderr, code := runInstallFunc(t, nil, "", `version_ge "`+tc.a+`" "`+tc.b+`"`, "")
		if code != tc.want {
			t.Errorf("version_ge %s %s: exit %d, want %d; stderr=%s", tc.a, tc.b, code, tc.want, stderr)
		}
	}
}

// goDevReleasesJSON has the real shape of https://go.dev/dl/?mode=json (taken
// from the live feed for go1.27.1, trimmed, with an unstable release first):
// file entries have filename/os/arch/version/sha256/size/kind and NO url field.
const goDevReleasesJSON = `[
 {"version": "go1.28rc1", "stable": false, "files": [
  {"filename": "go1.28rc1.darwin-arm64.pkg", "os": "darwin", "arch": "arm64", "version": "go1.28rc1", "sha256": "1111111111111111111111111111111111111111111111111111111111111111", "size": 69000000, "kind": "installer"}]},
 {"version": "go1.27.1", "stable": true, "files": [
  {"filename": "go1.27.1.src.tar.gz", "os": "", "arch": "", "version": "go1.27.1", "sha256": "4e408abae126d916b6164627193f2c54f0e3ca1312d693b86db45f862ab238b1", "size": 35109201, "kind": "source"},
  {"filename": "go1.27.1.darwin-amd64.tar.gz", "os": "darwin", "arch": "amd64", "version": "go1.27.1", "sha256": "8f8f52c6649542cf027bbc9b9c68d1ec042f9f34808a40413f0b8b3f66f3caa4", "size": 71621873, "kind": "archive"},
  {"filename": "go1.27.1.darwin-amd64.pkg", "os": "darwin", "arch": "amd64", "version": "go1.27.1", "sha256": "c0b5baf81da6ebc851c6e0bef9df1a13b5507cb7c560ee34ba637a88e216cced", "size": 73082677, "kind": "installer"},
  {"filename": "go1.27.1.darwin-arm64.tar.gz", "os": "darwin", "arch": "arm64", "version": "go1.27.1", "sha256": "ee215d57e0ec269c60cc9ceca68e6bda321ba9ee5afe24f4b0988703c2d87d12", "size": 68100347, "kind": "archive"},
  {"filename": "go1.27.1.darwin-arm64.pkg", "os": "darwin", "arch": "arm64", "version": "go1.27.1", "sha256": "81aee79ea3bd85f00624c216e9105674fa72626dd0479fc85a632939d6c5986b", "size": 69436229, "kind": "installer"}]},
 {"version": "go1.26.8", "stable": true, "files": [
  {"filename": "go1.26.8.darwin-arm64.pkg", "os": "darwin", "arch": "arm64", "version": "go1.26.8", "sha256": "2222222222222222222222222222222222222222222222222222222222222222", "size": 68000000, "kind": "installer"}]}
]`

// Both backends must build the .pkg URL from the filename (go.dev has no url
// field) and pass go.dev's SHA-256 through, for each macOS architecture.
func TestFetchLatestGoPkgBackendsAgree(t *testing.T) {
	cases := []struct {
		uname, want string
	}{
		{"arm64", "go1.27.1\nhttps://go.dev/dl/go1.27.1.darwin-arm64.pkg\n81aee79ea3bd85f00624c216e9105674fa72626dd0479fc85a632939d6c5986b\n"},
		{"x86_64", "go1.27.1\nhttps://go.dev/dl/go1.27.1.darwin-amd64.pkg\nc0b5baf81da6ebc851c6e0bef9df1a13b5507cb7c560ee34ba637a88e216cced\n"},
	}
	for name, backend := range backends(t) {
		for _, tc := range cases {
			setup := backend + `
uname() { echo ` + tc.uname + `; }
curl() { printf '%s' "$GO_JSON"; }`
			out, stderr, code := runInstallFunc(t, []string{"GO_JSON=" + goDevReleasesJSON, "PATH=" + os.Getenv("PATH")}, setup, `fetch_latest_go_pkg`, "")
			if code != 0 || out != tc.want {
				t.Errorf("%s/%s: got (%q, exit %d), want %q; stderr=%s", name, tc.uname, out, code, tc.want, stderr)
			}
		}
	}
}

// A release whose .pkg entry has no SHA-256 must not yield a download: the
// installer would otherwise have nothing to verify before running sudo.
func TestFetchLatestGoPkgRequiresSHA256(t *testing.T) {
	goJSON := `[{"version":"go1.27.1","stable":true,"files":[` +
		`{"filename":"go1.27.1.darwin-arm64.pkg","os":"darwin","arch":"arm64","version":"go1.27.1","size":69436229,"kind":"installer"}]}]`
	for name, backend := range backends(t) {
		setup := backend + `
uname() { echo arm64; }
curl() { printf '%s' "$GO_JSON"; }`
		out, stderr, code := runInstallFunc(t, []string{"GO_JSON=" + goJSON, "PATH=" + os.Getenv("PATH")}, setup, `fetch_latest_go_pkg`, "")
		if code == 0 || out != "" {
			t.Errorf("%s: got (%q, exit %d), want no output and a failure; stderr=%s", name, out, code, stderr)
		}
	}
}

// install_go_from_pkg must read every line fetch_latest_go_pkg prints; it used
// to read only the first and always gave up before downloading.
func TestInstallGoFromPkgReadsVersionAndURL(t *testing.T) {
	setup := `fetch_latest_go_pkg() { printf 'go1.98.2\nhttps://example.test/arm64.pkg\nbbbb\n'; }
curl() { echo "curl $*" >&2; return 22; }`
	_, stderr, code := runInstallFunc(t, nil, setup, `install_go_from_pkg`, "")
	if code == 0 {
		t.Fatal("expected failure from the stubbed download")
	}
	if !strings.Contains(stderr, "curl -fsSL https://example.test/arm64.pkg") {
		t.Errorf("install_go_from_pkg did not try to download the pkg URL; stderr=%s", stderr)
	}
}

// The .pkg runs under sudo, so a download that does not match go.dev's SHA-256
// must never reach the installer. sudo and curl are shell stubs; nothing is
// installed and HOME is a temp dir (runInstallFunc).
func TestInstallGoFromPkgRefusesChecksumMismatch(t *testing.T) {
	setup := `fetch_latest_go_pkg() { printf 'go1.98.2\nhttps://example.test/arm64.pkg\n%064d\n' 0; }
curl() { local out=""; while [ $# -gt 0 ]; do [ "$1" = "-o" ] && out="$2"; shift; done; printf 'tampered' > "$out"; }
sudo() { echo "SUDO $*" >&2; return 0; }`
	stdout, stderr, code := runInstallFunc(t, nil, setup, `install_go_from_pkg`, "")
	if code == 0 {
		t.Fatalf("install_go_from_pkg accepted a pkg with the wrong checksum; stdout=%s stderr=%s", stdout, stderr)
	}
	if strings.Contains(stderr, "SUDO") {
		t.Errorf("sudo installer ran on a pkg with the wrong checksum; stderr=%s", stderr)
	}
	if !strings.Contains(stdout+stderr, "checksum mismatch") {
		t.Errorf("expected a checksum mismatch error; stdout=%s stderr=%s", stdout, stderr)
	}
}

// A download that matches go.dev's SHA-256 is handed to the installer.
func TestInstallGoFromPkgRunsInstallerOnChecksumMatch(t *testing.T) {
	setup := `fetch_latest_go_pkg() { printf 'go1.98.2\nhttps://example.test/arm64.pkg\n%s\n' "$(printf 'verified-pkg' | { sha256sum 2>/dev/null || shasum -a 256; } | awk '{print $1}')"; }
curl() { local out=""; while [ $# -gt 0 ]; do [ "$1" = "-o" ] && out="$2"; shift; done; printf 'verified-pkg' > "$out"; }
sudo() { echo "SUDO $*" >&2; return 0; }`
	stdout, stderr, code := runInstallFunc(t, nil, setup, `install_go_from_pkg`, "")
	if code != 0 || !strings.Contains(stderr, "SUDO installer -pkg ") {
		t.Fatalf("install_go_from_pkg did not install a pkg with the right checksum (exit %d); stdout=%s stderr=%s", code, stdout, stderr)
	}
}
