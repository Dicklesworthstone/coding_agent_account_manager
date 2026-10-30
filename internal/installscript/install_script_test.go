// Package installscript tests the release-metadata helpers in the repository's
// install.sh by sourcing it in bash. The installer can parse GitHub release
// JSON with either python3 or jq; these tests hold both backends to the same
// results so a machine with only jq installs the same asset as one with python3.
package installscript

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestSourceTarballRefHonorsRequestedVersion(t *testing.T) {
	out, stderr, code := runInstallFunc(t, nil, "", `INSTALL_VERSION=v9.9.9; source_tarball_ref; INSTALL_VERSION=; source_tarball_ref`, "")
	if code != 0 || out != "refs/tags/v9.9.9\nrefs/heads/main\n" {
		t.Fatalf("source tarball refs = %q, exit %d; stderr=%s", out, code, stderr)
	}
}

func TestTryGoInstallRefusesRequestedTagCheckoutFailure(t *testing.T) {
	tmp := t.TempDir()
	setup := `
ensure_go() { printf '1.26.10'; }
make_tmp_dir() { printf '%s\n' "$TEST_TMP"; }
git() {
    if [ "$1" = "clone" ]; then
        mkdir -p "${@: -1}"
        return 0
    fi
    return 1
}
`
	stdout, stderr, code := runInstallFunc(t, []string{"TEST_TMP=" + tmp}, setup, `INSTALL_VERSION=v9.9.9; try_go_install`, "")
	if code == 0 {
		t.Fatalf("requested tag checkout failure built another revision; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "refusing to build a different source version") {
		t.Fatalf("checkout failure did not explain fail-closed behavior; stdout=%s stderr=%s", stdout, stderr)
	}
}

func TestTryGoInstallTarballFallbackUsesRequestedTag(t *testing.T) {
	tmp := t.TempDir()
	capture := filepath.Join(t.TempDir(), "url")
	setup := `
ensure_go() { printf '1.26.10'; }
make_tmp_dir() { printf '%s\n' "$TEST_TMP"; }
git() { return 1; }
download_file() {
    printf '%s\n' "$1" > "$CAPTURE"
    return 1
}
`
	_, _, code := runInstallFunc(t,
		[]string{"TEST_TMP=" + tmp, "CAPTURE=" + capture, "CAAM_NO_CACHE_BUST=1"},
		setup, `INSTALL_VERSION=v9.9.9; try_go_install`, "")
	if code == 0 {
		t.Fatal("stubbed tarball download unexpectedly succeeded")
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://codeload.github.com/Dicklesworthstone/coding_agent_account_manager/tar.gz/refs/tags/v9.9.9"
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("requested source fallback URL = %q, want %q", strings.TrimSpace(string(data)), want)
	}
}

func TestEnsureGoRequiresPinnedSourceBuildFloor(t *testing.T) {
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{"1.26.8", false},
		{"1.26.9", false},
		{"1.26.10", true},
		{"1.27.0", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			setup := `
go() { printf 'go version go%s linux/amd64\n' "$FAKE_GO_VERSION"; }
brew() { return 1; }
uname() { printf 'Linux\n'; }
`
			out, stderr, code := runInstallFunc(t, []string{"FAKE_GO_VERSION=" + tc.version}, setup, `ensure_go`, "")
			if (code == 0) != tc.ok {
				t.Fatalf("ensure_go(go%s) exit=%d stdout=%q stderr=%q", tc.version, code, out, stderr)
			}
			if !tc.ok && !strings.Contains(out+stderr, "1.26.10") {
				t.Fatalf("old toolchain diagnostic did not name the required floor: stdout=%s stderr=%s", out, stderr)
			}
		})
	}
}

func TestPinnedGoToolchainStaysAligned(t *testing.T) {
	root := filepath.Dir(repoInstallScript(t))
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}

	const want = "1.26.10"
	checks := []struct {
		path, needle string
	}{
		{"go.mod", "toolchain go" + want},
		{filepath.Join(".github", "workflows", "ci.yml"), "go-version: '" + want + "'"},
		{"install.sh", `local min_version="` + want + `"`},
	}
	for _, tc := range checks {
		if !strings.Contains(read(tc.path), tc.needle) {
			t.Errorf("%s is not pinned to Go %s (missing %q)", tc.path, want, tc.needle)
		}
	}
}

func TestReleaseSignatureKind(t *testing.T) {
	for _, tc := range []struct {
		version string
		kind    string
	}{
		{"0.0.999999999999999999999999999999", "cosign"},
		{"v0.1.0", "cosign"},
		{"0.1.17", "cosign"},
		{"v0.1.17-beta.2+build.4", "cosign"},
		{"0.1.18", "minisign"},
		{"v0.1.18-rc.1", "minisign"},
		{"0.1.24+host.1", "minisign"},
		{"0.2.0", "minisign"},
		{"1.0.0", "minisign"},
		{"18446744073709551616.0.0", "minisign"},
		{"0.18446744073709551616.0", "minisign"},
		{"0.1.18446744073709551616", "minisign"},
		{"", ""},
		{"dev", ""},
		{"v0.1", ""},
		{"0.1.17.9", ""},
		{"01.1.17", ""},
		{"0.1.17\n2.3.4", ""},
		{"0.1.$(printf injected)", ""},
	} {
		t.Run(tc.version, func(t *testing.T) {
			stdout, stderr, code := runInstallFunc(t, []string{"RELEASE_VERSION=" + tc.version}, "",
				`release_signature_kind "$RELEASE_VERSION"`, "")
			if tc.kind == "" {
				if code == 0 || stdout != "" || !strings.Contains(stderr, "major.minor.patch") {
					t.Fatalf("ambiguous release version was not refused: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
				}
				return
			}
			if code != 0 || stdout != tc.kind+"\n" {
				t.Fatalf("signature kind: exit=%d stdout=%q stderr=%q; want %s", code, stdout, stderr, tc.kind)
			}
		})
	}
}

// These tests run the real installer through metadata selection, verification,
// extraction, replacement and its optional executable check. Network reads and
// external signature tools are synthetic; real hash checks and filesystem
// changes are confined to test directories. The Go updater separately tests
// the pinned key against an actual maintainer-signed minisign fixture.
func TestInstallerReleaseVerification(t *testing.T) {
	for _, name := range []string{"tar", "find", "chmod", "mv", "cp", "awk", "sed", "grep", "head", "date"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("installer test requires %s", name)
		}
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		if _, err := exec.LookPath("shasum"); err != nil {
			t.Skip("installer test requires sha256sum or shasum")
		}
	}
	for backend, backendSetup := range backends(t) {
		for _, tc := range []struct {
			name             string
			version          string
			requested        string
			archiveVersion   string
			signatures       string
			missingVerifier  string
			mode             string
			checksums        string
			badSignature     bool
			skipVerification bool
			wantOK           bool
			wantError        string
			wantVerifier     string
		}{
			{name: "current signed release", requested: "0.1.24", signatures: "minisign", wantOK: true, wantVerifier: "minisign"},
			{name: "legacy signed release", version: "v0.1.17", signatures: "cosign", wantOK: true, wantVerifier: "cosign"},
			{name: "legacy release with minisign", version: "v0.1.17", signatures: "minisign", wantOK: true, wantVerifier: "minisign"},
			{name: "missing minisign interactive", signatures: "minisign", missingVerifier: "minisign", wantError: "minisign is required"},
			{name: "missing minisign CI", signatures: "minisign", missingVerifier: "minisign", mode: "CI=true", wantError: "minisign is required"},
			{name: "missing minisign noninteractive", signatures: "minisign", missingVerifier: "minisign", mode: "NONINTERACTIVE=1", wantError: "minisign is required"},
			{name: "missing cosign interactive", version: "v0.1.17", signatures: "cosign", missingVerifier: "cosign", wantError: "cosign is required"},
			{name: "missing cosign CI", version: "v0.1.17", signatures: "cosign", missingVerifier: "cosign", mode: "CI=true", wantError: "cosign is required"},
			{name: "missing cosign noninteractive", version: "v0.1.17", signatures: "cosign", missingVerifier: "cosign", mode: "NONINTERACTIVE=1", wantError: "cosign is required"},
			{name: "current cannot downgrade to cosign", signatures: "cosign", wantError: "required for releases >= v0.1.18"},
			{name: "boundary prerelease cannot downgrade", version: "v0.1.18-rc.1", signatures: "cosign", wantError: "required for releases >= v0.1.18"},
			{name: "invalid release tag", version: "dev", signatures: "minisign", wantError: "major.minor.patch"},
			{name: "requested version mismatch", requested: "v0.1.25", signatures: "minisign", wantError: "does not match the requested version"},
			{name: "older signed archive under newer metadata", archiveVersion: "0.1.17", signatures: "minisign", wantError: "archive name does not match its release version"},
			{name: "checksum prefix is not the archive", signatures: "minisign", checksums: "prefix", wantError: "exactly one valid entry", wantVerifier: "minisign"},
			{name: "duplicate checksum entry", signatures: "minisign", checksums: "duplicate", wantError: "exactly one valid entry", wantVerifier: "minisign"},
			{name: "malformed checksum entry", signatures: "minisign", checksums: "malformed", wantError: "exactly one valid entry", wantVerifier: "minisign"},
			{name: "wrong archive checksum", signatures: "minisign", checksums: "wrong", wantError: "Checksum verification failed", wantVerifier: "minisign"},
			{name: "binary marker checksum", signatures: "minisign", checksums: "binary", wantOK: true, wantVerifier: "minisign"},
			{name: "invalid minisign signature", signatures: "minisign", badSignature: true, wantError: "Signature verification failed", wantVerifier: "minisign"},
			{name: "invalid minisign cannot fall back to cosign", signatures: "both", badSignature: true, wantError: "Signature verification failed", wantVerifier: "minisign"},
			{name: "invalid cosign signature", version: "v0.1.17", signatures: "cosign", badSignature: true, wantError: "Signature verification failed", wantVerifier: "cosign"},
			{name: "missing signature assets", wantError: "required for releases >= v0.1.18"},
			{name: "explicit verification opt out", missingVerifier: "both", mode: "CI=true", skipVerification: true, wantOK: true},
			{name: "opt out preserves requested version", requested: "v0.1.25", skipVerification: true, wantError: "does not match the requested version"},
			{name: "opt out preserves archive version", archiveVersion: "0.1.17", skipVerification: true, wantError: "archive name does not match its release version"},
		} {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				version := tc.version
				if version == "" {
					version = "v0.1.24"
				}
				archiveVersion := tc.archiveVersion
				if archiveVersion == "" {
					archiveVersion = strings.TrimPrefix(version, "v")
				}
				assetName := "caam_" + archiveVersion + "_linux_amd64.tar.gz"
				assetURL := "https://example.test/" + assetName
				assets := []map[string]string{
					{"name": assetName, "browser_download_url": assetURL},
					{"name": "SHA256SUMS", "browser_download_url": "https://example.test/SHA256SUMS"},
				}
				if tc.signatures == "minisign" || tc.signatures == "both" {
					assets = append(assets, map[string]string{"name": "SHA256SUMS.minisig", "browser_download_url": "https://example.test/SHA256SUMS.minisig"})
				}
				if tc.signatures == "cosign" || tc.signatures == "both" {
					assets = append(assets, map[string]string{"name": "SHA256SUMS.sig", "browser_download_url": "https://example.test/SHA256SUMS.sig"})
				}
				release, err := json.Marshal(map[string]any{"tag_name": version, "assets": assets})
				if err != nil {
					t.Fatal(err)
				}

				dir := t.TempDir()
				installed := filepath.Join(dir, "installed")
				if err := os.Mkdir(installed, 0700); err != nil {
					t.Fatal(err)
				}
				oldBinary := []byte("previous installed binary must survive verification failure\n")
				if err := os.WriteFile(filepath.Join(installed, "caam"), oldBinary, 0600); err != nil {
					t.Fatal(err)
				}
				program := []byte("#!/bin/sh\nprintf 'candidate-ran\\n' >> \"$RUN_MARKER\"\nprintf 'caam %s\\n' \"$TEST_RELEASE_VERSION\"\n")
				var archive bytes.Buffer
				gzipWriter := gzip.NewWriter(&archive)
				tarWriter := tar.NewWriter(gzipWriter)
				if err := tarWriter.WriteHeader(&tar.Header{Name: "caam", Mode: 0755, Size: int64(len(program))}); err != nil {
					t.Fatal(err)
				}
				if _, err := tarWriter.Write(program); err != nil {
					t.Fatal(err)
				}
				if err := tarWriter.Close(); err != nil {
					t.Fatal(err)
				}
				if err := gzipWriter.Close(); err != nil {
					t.Fatal(err)
				}
				archivePath := filepath.Join(dir, "fixture.tar.gz")
				if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(archive.Bytes())
				runMarker := filepath.Join(dir, "candidate-ran")
				fallbackMarker := filepath.Join(dir, "source-fallback")
				verifierTrace := filepath.Join(dir, "verifier-trace")
				env := []string{
					"INSTALL_DIR=" + installed, "RELEASE_JSON=" + string(release),
					"FIXTURE_ARCHIVE=" + archivePath, "FIXTURE_SUM=" + hex.EncodeToString(sum[:]),
					"FIXTURE_ASSET=" + assetName, "ASSET_URL=" + assetURL,
					"TEST_RELEASE_VERSION=" + version, "REQUESTED_VERSION=" + tc.requested,
					"RUN_MARKER=" + runMarker, "FALLBACK_MARKER=" + fallbackMarker,
					"VERIFIER_TRACE=" + verifierTrace, "MISSING_VERIFIER=" + tc.missingVerifier,
					"CHECKSUM_KIND=" + tc.checksums,
				}
				if tc.mode != "" {
					env = append(env, tc.mode)
				}
				if tc.badSignature {
					env = append(env, "BAD_SIGNATURE=1")
				}
				if tc.skipVerification {
					env = append(env, "CAAM_SKIP_VERIFY=1")
				}
				setup := backendSetup + `
INSTALL_VERSION="$REQUESTED_VERSION"
VERIFY_AFTER_INSTALL=true
detect_terminal_features() { USE_COLOR=false; HAS_GUM=false; IS_TTY=false; }
detect_platform() { printf 'linux_amd64\n'; }
get_release() { printf '%s' "$RELEASE_JSON"; }
get_installed_version() { :; }
try_go_install() { printf 'source-fallback\n' > "$FALLBACK_MARKER"; return 0; }
command() {
    if [ "$1" = '-v' ]; then
        case "${2:-}" in
            minisign|cosign)
                if [ "$MISSING_VERIFIER" = "$2" ] || [ "$MISSING_VERIFIER" = both ]; then return 1; fi ;;
        esac
    fi
    builtin command "$@"
}
download_file() {
    case "$1" in
        "$ASSET_URL") cp "$FIXTURE_ARCHIVE" "$2" ;;
        */SHA256SUMS)
            case "$CHECKSUM_KIND" in
                prefix) printf '%s  %s.extra\n' "$FIXTURE_SUM" "$FIXTURE_ASSET" > "$2" ;;
                duplicate) printf '%s  %s\n%s  %s\n' "$FIXTURE_SUM" "$FIXTURE_ASSET" "$FIXTURE_SUM" "$FIXTURE_ASSET" > "$2" ;;
                malformed) printf 'not-a-sha256  %s\n' "$FIXTURE_ASSET" > "$2" ;;
                wrong) printf '%064d  %s\n' 0 "$FIXTURE_ASSET" > "$2" ;;
                binary) printf '%s *%s\n' "$FIXTURE_SUM" "$FIXTURE_ASSET" > "$2" ;;
                *) printf '%s  %s\n' "$FIXTURE_SUM" "$FIXTURE_ASSET" > "$2" ;;
            esac ;;
        */SHA256SUMS.minisig) printf 'synthetic minisign signature\n' > "$2" ;;
        */SHA256SUMS.sig) printf 'synthetic cosign signature\n' > "$2" ;;
        *) return 1 ;;
    esac
}
minisign() {
    printf 'minisign\n' >> "$VERIFIER_TRACE"
    [ "$#" -eq 7 ] && [ "$1" = '-V' ] && [ "$2" = '-p' ] && [ "$4" = '-m' ] && [ "$6" = '-x' ] || return 1
    grep -Fx 'RWTQGPeLsnm9G7VFdFWkkcRi3wJK/PqsYxWC+oLNN74W9IjBxRU1Xu70' "$3" >/dev/null || return 1
    [ -f "$5" ] && [ "$(cat "$7")" = 'synthetic minisign signature' ] || return 1
    [ "${BAD_SIGNATURE:-}" != 1 ]
}
cosign() {
    printf 'cosign\n' >> "$VERIFIER_TRACE"
    [ "$#" -eq 8 ] && [ "$1" = verify-blob ] && [ "$2" = '--bundle' ] || return 1
    [ "$4" = '--certificate-oidc-issuer' ] && [ "$5" = 'https://token.actions.githubusercontent.com' ] || return 1
    [ "$6" = '--certificate-identity' ] && [ "$7" = "https://github.com/Dicklesworthstone/coding_agent_account_manager/.github/workflows/release.yml@refs/tags/$TEST_RELEASE_VERSION" ] || return 1
    [ -f "$8" ] && [ "$(cat "$3")" = 'synthetic cosign signature' ] || return 1
    [ "${BAD_SIGNATURE:-}" != 1 ]
}
`
				stdout, stderr, code := runInstallFunc(t, env, setup, `main`, "")
				wantCode := 3
				if tc.wantOK {
					wantCode = 0
				}
				if code != wantCode {
					t.Fatalf("installer exit=%d, want %d; stdout=%s stderr=%s", code, wantCode, stdout, stderr)
				}
				if tc.wantError != "" && !strings.Contains(stdout+stderr, tc.wantError) {
					t.Errorf("missing error %q; stdout=%s stderr=%s", tc.wantError, stdout, stderr)
				}
				if (!tc.wantOK || tc.skipVerification) && strings.Contains(stdout+stderr, "Verified release signature and checksum") {
					t.Errorf("installer falsely claimed verification: stdout=%s stderr=%s", stdout, stderr)
				}
				if tc.skipVerification && tc.wantOK && !strings.Contains(stderr, "Skipping release verification (CAAM_SKIP_VERIFY set)") {
					t.Errorf("explicit opt-out was not reported: stderr=%s", stderr)
				}
				gotBinary, err := os.ReadFile(filepath.Join(installed, "caam"))
				if err != nil {
					t.Fatal(err)
				}
				if tc.wantOK {
					if !bytes.Equal(gotBinary, program) {
						t.Error("successful install did not publish the verified candidate")
					}
					if ran, err := os.ReadFile(runMarker); err != nil || string(ran) != "candidate-ran\n" {
						t.Errorf("successful install did not execute the candidate exactly once: marker=%q err=%v", ran, err)
					}
				} else {
					if !bytes.Equal(gotBinary, oldBinary) {
						t.Error("verification failure changed the previous installation")
					}
					if _, err := os.Stat(runMarker); !os.IsNotExist(err) {
						t.Errorf("unverified candidate executed or marker lookup failed: %v", err)
					}
				}
				if _, err := os.Stat(fallbackMarker); !os.IsNotExist(err) {
					t.Errorf("verification path attempted an unsigned source fallback: %v", err)
				}
				trace, err := os.ReadFile(verifierTrace)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if tc.wantVerifier == "" {
					if len(trace) != 0 {
						t.Errorf("unexpected verifier invocation: %q", trace)
					}
				} else if !strings.Contains(string(trace), tc.wantVerifier+"\n") {
					t.Errorf("expected %s invocation, got %q", tc.wantVerifier, trace)
				}
				if tc.wantVerifier == "minisign" && strings.Contains(string(trace), "cosign\n") {
					t.Errorf("minisign verification fell back to cosign: %q", trace)
				}
			})
		}
	}
}
