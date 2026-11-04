// Package installscript tests the release-metadata helpers in the repository's
// install.sh by sourcing it in bash. The installer can parse GitHub release
// JSON with either python3 or jq; these tests hold both backends to the same
// results so a machine with only jq installs the same asset as one with python3.
package installscript

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
ensure_go() { SOURCE_GO_VERSION=1.26.10; }
make_tmp_dir() { printf -v "$1" '%s' "$TEST_TMP"; }
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
ensure_go() { SOURCE_GO_VERSION=1.26.10; }
make_tmp_dir() { printf -v "$1" '%s' "$TEST_TMP"; }
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

const minisignBootstrapSHA256 = "9a599b48ba6eb7b1e80f12f36b94ceca7c00b7a5173c95c3efc88d9822957e73"

// minisignBootstrapArchive builds a deliberately synthetic verifier, not a
// cryptographic fixture. The real SHA-256 and tar programs must authenticate
// and extract it before it can run. The existing release-verification tests
// and maintainer-signed updater fixtures cover CAAM's signature policy.
func minisignBootstrapArchive(t *testing.T, kind string) ([]byte, string) {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for _, arch := range []string{"x86_64", "aarch64"} {
		if kind == "missing member" && arch == "x86_64" {
			continue
		}
		program := []byte(`#!/bin/sh
printf 'exec ` + arch + ` %s\n' "${1:-}" >> "$BOOTSTRAP_TRACE"
if [ "${1:-}" = -v ]; then printf 'minisign 0.12\n'; exit 0; fi
[ "$#" -eq 7 ] && [ "$1" = -V ] && [ "$2" = -p ] && [ "$4" = -m ] && [ "$6" = -x ] || exit 1
grep -Fx 'RWTQGPeLsnm9G7VFdFWkkcRi3wJK/PqsYxWC+oLNN74W9IjBxRU1Xu70' "$3" >/dev/null || exit 1
[ -f "$5" ] && [ "$(cat "$7")" = 'synthetic minisign signature' ] || exit 1
[ "${BOOTSTRAP_BAD_SIGNATURE:-}" != 1 ]
`)
		hdr := &tar.Header{Name: "minisign-linux/" + arch + "/minisign", Mode: 0755, Size: int64(len(program))}
		if arch == "x86_64" {
			switch kind {
			case "empty executable":
				hdr.Size = 0
				program = nil
			case "directory":
				hdr.Typeflag = tar.TypeDir
				hdr.Size = 0
				program = nil
			case "symlink":
				hdr.Typeflag = tar.TypeSymlink
				hdr.Linkname = "../aarch64/minisign"
				hdr.Size = 0
				program = nil
			}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(program); err != nil {
			t.Fatal(err)
		}
	}
	// Selecting one executable must not extract unrelated archive members.
	if err := tw.WriteHeader(&tar.Header{Name: "minisign-linux/unrelated", Mode: 0644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	if kind == "invalid tar" {
		data = []byte("this is an authenticated fixture, but not a gzip archive\n")
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

// runMinisignBootstrap uses an isolated copy of install.sh with ONLY its literal
// archive digest changed to the generated fixture's digest. Production has no
// environment override for its trust anchor. Tests that reject tampering and
// environment overrides can retain the production pin by setting fixturePin=false.
// Every invocation also checks that EXIT cleans up its own directory while
// preserving unrelated contents of the same TMPDIR.
func runMinisignBootstrap(t *testing.T, kind string, fixturePin bool, extraEnv []string, body string) (string, string, int, string) {
	t.Helper()
	for _, tool := range []string{"tar", "gzip", "awk", "cp", "tr", "grep", "cat", "mkdir", "mktemp", "rm", "chmod", "stat"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("Minisign bootstrap test requires %s", tool)
		}
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("Minisign bootstrap test requires sha256sum")
	}
	dir := t.TempDir()
	tmpRoot := filepath.Join(dir, "temporary directories")
	if err := os.Mkdir(tmpRoot, 0700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(tmpRoot, "preexisting")
	if err := os.WriteFile(unrelated, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	data, digest := minisignBootstrapArchive(t, kind)
	archive := filepath.Join(dir, "bootstrap.tar.gz")
	if err := os.WriteFile(archive, data, 0600); err != nil {
		t.Fatal(err)
	}
	script := repoInstallScript(t)
	if fixturePin {
		source, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		pin := `readonly MINISIGN_BOOTSTRAP_SHA256="` + minisignBootstrapSHA256 + `"`
		if strings.Count(string(source), pin) != 1 {
			t.Fatal("bootstrap must have exactly one literal readonly SHA-256 pin; update fixture only after authenticating a new upstream release")
		}
		script = filepath.Join(dir, "install-fixture.sh")
		fixture := strings.Replace(string(source), pin, `readonly MINISIGN_BOOTSTRAP_SHA256="`+digest+`"`, 1)
		if err := os.WriteFile(script, []byte(fixture), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tracePath := filepath.Join(dir, "trace")
	env := []string{
		"INSTALL_SH=" + script, "TMPDIR=" + tmpRoot,
		"BOOTSTRAP_ARCHIVE=" + archive, "BOOTSTRAP_TRACE=" + tracePath,
		"BOOTSTRAP_UNAME_S=Linux", "BOOTSTRAP_UNAME_M=x86_64", "BOOTSTRAP_HASH=sha256sum",
	}
	env = append(env, extraEnv...)
	setup := `
uname() {
    case "$1" in -s) printf '%s\n' "$BOOTSTRAP_UNAME_S" ;; -m) printf '%s\n' "$BOOTSTRAP_UNAME_M" ;; *) return 1 ;; esac
}
command() {
    if [ "${1:-}" = -v ]; then
        case "${2:-}" in
            minisign) [ "${BOOTSTRAP_INSTALLED:-}" = 1 ] || return 1 ;;
            sha256sum) [ "$BOOTSTRAP_HASH" != none ] && [ "$BOOTSTRAP_HASH" != shasum ] && [ "$BOOTSTRAP_HASH" != shasum-error ] || return 1 ;;
            shasum) [ "$BOOTSTRAP_HASH" != none ] || return 1 ;;
        esac
    fi
    builtin command "$@"
}
download_file() {
    printf 'download %s\n' "$1" >> "$BOOTSTRAP_TRACE"
    case "$1" in
        https://github.com/jedisct1/minisign/releases/download/0.12/minisign-0.12-linux.tar.gz)
            if [ "${BOOTSTRAP_DOWNLOAD_FAIL:-}" = 1 ]; then printf partial > "$2"; return 22; fi
            cp "$BOOTSTRAP_ARCHIVE" "$2" || return
            if [ "${BOOTSTRAP_TAMPER:-}" = 1 ]; then printf tampered >> "$2"; fi ;;
        https://example.test/SHA256SUMS)
            printf '%s  caam_0.1.25_linux_amd64.tar.gz\n' "$BOOTSTRAP_RELEASE_SUM" > "$2" ;;
        https://example.test/SHA256SUMS.minisig)
            printf 'synthetic minisign signature\n' > "$2" ;;
        *) return 99 ;;
    esac
}
sha256sum() {
    printf 'sha256sum\n' >> "$BOOTSTRAP_TRACE"
    if [ "$BOOTSTRAP_HASH" = malformed ]; then printf 'not-a-digest\n'; return 0; fi
    builtin command sha256sum "$@" || return
    [ "$BOOTSTRAP_HASH" != sha256sum-error ]
}
shasum() {
    printf 'shasum\n' >> "$BOOTSTRAP_TRACE"
    builtin command shasum "$@" || return
    [ "$BOOTSTRAP_HASH" != shasum-error ]
}
tar() { printf 'extract\n' >> "$BOOTSTRAP_TRACE"; builtin command tar "$@"; }
chmod() { [ "${BOOTSTRAP_CHMOD_FAIL:-}" != 1 ] || return 1; builtin command chmod "$@"; }
minisign() { printf 'installed verifier\n' >> "$BOOTSTRAP_TRACE"; printf 'minisign 0.11\n'; }
sudo() { printf 'PACKAGE sudo\n' >> "$BOOTSTRAP_TRACE"; return 99; }
apt() { printf 'PACKAGE apt\n' >> "$BOOTSTRAP_TRACE"; return 99; }
apt-get() { printf 'PACKAGE apt-get\n' >> "$BOOTSTRAP_TRACE"; return 99; }
brew() { printf 'PACKAGE brew\n' >> "$BOOTSTRAP_TRACE"; return 99; }
`
	if body == "" {
		body = `
make_tmp_dir bootstrap_tmp || exit "$?"
ensure_minisign "$bootstrap_tmp" || exit "$?"
printf 'selected %s\n' "$MINISIGN_BIN"
[ ! -e "$bootstrap_tmp/minisign-bootstrap/minisign-linux/unrelated" ] || exit 91
"$MINISIGN_BIN" -v
`
	}
	stdout, stderr, code := runInstallFunc(t, env, setup, body, "")
	entries, err := os.ReadDir(tmpRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "preexisting" {
		t.Errorf("bootstrap left owned temporary files or deleted unrelated data: %v; stdout=%s stderr=%s", entries, stdout, stderr)
	}
	if preserved, err := os.ReadFile(unrelated); err != nil || string(preserved) != "must survive" {
		t.Errorf("bootstrap changed unrelated TMPDIR data: %q, %v", preserved, err)
	}
	trace, err := os.ReadFile(tracePath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(trace), "PACKAGE ") {
		t.Errorf("bootstrap attempted system package changes: %s", trace)
	}
	return stdout, stderr, code, string(trace)
}

// The fallback is distribution-independent Linux code. These are the missing-
// package/tool scenarios on Ubuntu 22.04 and on minimal Ubuntu 24.04+/Debian 12+
// systems, not simulated executions of those distributions' package managers.
func TestMinisignBootstrapArchitecture(t *testing.T) {
	for _, tc := range []struct{ uname, member string }{
		{"x86_64", "x86_64"}, {"amd64", "x86_64"}, {"aarch64", "aarch64"}, {"arm64", "aarch64"},
	} {
		t.Run(tc.uname, func(t *testing.T) {
			stdout, stderr, code, trace := runMinisignBootstrap(t, "valid", true, []string{"BOOTSTRAP_UNAME_M=" + tc.uname}, "")
			if code != 0 || !strings.Contains(stdout, "/minisign-bootstrap/minisign") || !strings.Contains(stdout, "minisign 0.12") {
				t.Fatalf("bootstrap did not acquire the correct executable: exit=%d stdout=%s stderr=%s trace=%s", code, stdout, stderr, trace)
			}
			want := "download https://github.com/jedisct1/minisign/releases/download/0.12/minisign-0.12-linux.tar.gz\nsha256sum\nextract\nexec " + tc.member + " -v\n"
			if trace != want {
				t.Errorf("bootstrap authentication/execution order: got %q, want %q", trace, want)
			}
		})
	}
}

// Ubuntu 24.04+ and Debian 12+ can provide Minisign 0.11 via their authenticated
// package repositories. An installed verifier must be reused without bootstrap
// downloads, extraction, privilege escalation, or package-manager invocations.
func TestMinisignAcquisitionUsesInstalledVerifier(t *testing.T) {
	for _, arch := range []string{"x86_64", "aarch64"} {
		t.Run(arch, func(t *testing.T) {
			stdout, stderr, code, trace := runMinisignBootstrap(t, "valid", false,
				[]string{"BOOTSTRAP_INSTALLED=1", "BOOTSTRAP_UNAME_M=" + arch, "BOOTSTRAP_HASH=none"}, "")
			if code != 0 || !strings.Contains(stdout, "selected minisign\n") || trace != "installed verifier\n" {
				t.Fatalf("installed verifier not reused: exit=%d stdout=%s stderr=%s trace=%q", code, stdout, stderr, trace)
			}
		})
	}
}

func TestMinisignBootstrapFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, kind, setting, wantError string
		extracted                      bool
	}{
		{"changed archive", "valid", "BOOTSTRAP_TAMPER=1", "Minisign archive checksum verification failed", false},
		{"missing hash utilities", "valid", "BOOTSTRAP_HASH=none", "sha256sum or shasum is required", false},
		{"hash process fails despite correct output", "valid", "BOOTSTRAP_HASH=sha256sum-error", "Could not authenticate the Minisign download", false},
		{"malformed hash output", "valid", "BOOTSTRAP_HASH=malformed", "Minisign archive checksum verification failed", false},
		{"download failure", "valid", "BOOTSTRAP_DOWNLOAD_FAIL=1", "Failed to download Minisign", false},
		{"wrong archive member", "missing member", "", "Failed to extract Minisign", true},
		{"invalid archive", "invalid tar", "", "Failed to extract Minisign", true},
		{"empty executable member", "empty executable", "", "Minisign archive does not contain the expected executable", true},
		{"cannot set executable mode", "valid", "BOOTSTRAP_CHMOD_FAIL=1", "", true},
		{"directory instead of executable", "directory", "", "Minisign archive does not contain the expected executable", true},
		{"symlink instead of executable", "symlink", "", "Minisign archive does not contain the expected executable", true},
		{"unsupported operating system", "valid", "BOOTSTRAP_UNAME_S=Darwin", "Automatic Minisign acquisition is only supported", false},
		{"unsupported CPU", "valid", "BOOTSTRAP_UNAME_M=riscv64", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var env []string
			if tc.setting != "" {
				env = append(env, tc.setting)
			}
			stdout, stderr, code, trace := runMinisignBootstrap(t, tc.kind, true, env, "")
			if code == 0 || (tc.wantError != "" && !strings.Contains(stdout+stderr, tc.wantError)) {
				t.Fatalf("bootstrap did not fail closed: exit=%d stdout=%s stderr=%s trace=%s", code, stdout, stderr, trace)
			}
			if strings.Contains(trace, "exec ") || strings.Contains(trace, "installed verifier") || strings.Contains(stdout, "selected ") {
				t.Errorf("failed bootstrap selected/executed a verifier: stdout=%s trace=%s", stdout, trace)
			}
			if strings.Contains(trace, "extract\n") != tc.extracted {
				t.Errorf("unexpected extraction (want %t): %s", tc.extracted, trace)
			}
			if strings.Contains(stdout+stderr, "CAAM_SKIP_VERIFY=1") {
				t.Errorf("failure recommended bypassing authentication: stdout=%s stderr=%s", stdout, stderr)
			}
		})
	}
}

func TestMinisignBootstrapShasumFallback(t *testing.T) {
	if _, err := exec.LookPath("shasum"); err != nil {
		t.Skip("shasum not available")
	}
	for _, mode := range []string{"shasum", "shasum-error"} {
		t.Run(mode, func(t *testing.T) {
			stdout, stderr, code, trace := runMinisignBootstrap(t, "valid", true, []string{"BOOTSTRAP_HASH=" + mode}, "")
			if (code == 0) != (mode == "shasum") || !strings.Contains(trace, "shasum\n") || strings.Contains(trace, "sha256sum\n") {
				t.Fatalf("shasum fallback: exit=%d stdout=%s stderr=%s trace=%s", code, stdout, stderr, trace)
			}
			if mode == "shasum-error" && (strings.Contains(trace, "extract\n") || strings.Contains(trace, "exec ")) {
				t.Errorf("failed shasum authenticated an executable: %s", trace)
			}
		})
	}
}

func TestMinisignBootstrapTrustAnchorsCannotBeOverriddenByEnvironment(t *testing.T) {
	_, digest := minisignBootstrapArchive(t, "valid")
	stdout, stderr, code, trace := runMinisignBootstrap(t, "valid", false, []string{
		"MINISIGN_BOOTSTRAP_SHA256=" + digest,
		"MINISIGN_BOOTSTRAP_VERSION=attacker-controlled",
		"MINISIGN_BOOTSTRAP_URL=https://example.test/untrusted",
		"MINISIGN_BIN=minisign",
	}, "")
	if code == 0 || !strings.Contains(stdout+stderr, "Minisign archive checksum verification failed") {
		t.Fatalf("environment replaced a production trust anchor: exit=%d stdout=%s stderr=%s trace=%s", code, stdout, stderr, trace)
	}
	if strings.Contains(trace, "extract\n") || strings.Contains(trace, "exec ") || strings.Contains(trace, "installed verifier") || strings.Contains(trace, "example.test") {
		t.Errorf("untrusted environment steered acquisition or execution: %s", trace)
	}
}

func TestBootstrappedMinisignVerifiesCAAMRelease(t *testing.T) {
	for _, badSignature := range []bool{false, true} {
		name := "valid signature"
		if badSignature {
			name = "invalid signature"
		}
		t.Run(name, func(t *testing.T) {
			sum := sha256.Sum256([]byte("release payload\n"))
			env := []string{"BOOTSTRAP_RELEASE_SUM=" + hex.EncodeToString(sum[:])}
			if badSignature {
				env = append(env, "BOOTSTRAP_BAD_SIGNATURE=1")
			}
			body := `
make_tmp_dir bootstrap_tmp || exit "$?"
printf 'release payload\n' > "$bootstrap_tmp/release.tar.gz"
release_json='{"tag_name":"v0.1.25","assets":[{"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"},{"name":"SHA256SUMS.minisig","browser_download_url":"https://example.test/SHA256SUMS.minisig"}]}'
verify_release_assets "$release_json" v0.1.25 caam_0.1.25_linux_amd64.tar.gz "$bootstrap_tmp/release.tar.gz" "$bootstrap_tmp"
`
			stdout, stderr, code, trace := runMinisignBootstrap(t, "valid", true, env, body)
			wantCode := 0
			if badSignature {
				wantCode = 1
			}
			if code != wantCode || !strings.Contains(trace, "exec x86_64 -V\n") {
				t.Fatalf("bootstrapped verifier was not used for CAAM authentication: exit=%d stdout=%s stderr=%s trace=%s", code, stdout, stderr, trace)
			}
			if badSignature && (!strings.Contains(stdout+stderr, "Signature verification failed") || strings.Contains(stdout+stderr, "Verified release signature and checksum")) {
				t.Errorf("bootstrapped verifier failure bypassed authentication: stdout=%s stderr=%s", stdout, stderr)
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
			hashFailure      bool
			missingHash      bool
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
			{name: "verification bypass refused", missingVerifier: "both", mode: "CI=true", skipVerification: true, wantError: "Release verification cannot be disabled"},
			{name: "bypass cannot ignore invalid signature", signatures: "minisign", skipVerification: true, badSignature: true, wantError: "Release verification cannot be disabled"},
			{name: "bypass cannot ignore requested version", requested: "v0.1.25", skipVerification: true, wantError: "does not match the requested version"},
			{name: "bypass cannot ignore archive version", archiveVersion: "0.1.17", skipVerification: true, wantError: "archive name does not match its release version"},
			{name: "missing archive hash tool", signatures: "minisign", missingHash: true, wantError: "sha256sum or shasum is required", wantVerifier: "minisign"},
			{name: "failed hash command with correct output", signatures: "minisign", hashFailure: true, wantError: "Could not compute the release archive checksum", wantVerifier: "minisign"},
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
				if tc.missingHash {
					env = append(env, "MISSING_HASH=1")
				}
				if tc.hashFailure {
					env = append(env, "HASH_FAILURE=1")
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
            sha256sum|shasum)
                if [ "${MISSING_HASH:-}" = 1 ]; then return 1; fi ;;
        esac
    fi
    builtin command "$@"
}
sha256sum() {
    if [ "${HASH_FAILURE:-}" = 1 ]; then printf '%s  %s\n' "$FIXTURE_SUM" "$1"; return 1; fi
    command sha256sum "$@"
}
shasum() {
    if [ "${HASH_FAILURE:-}" = 1 ]; then printf '%s  archive\n' "$FIXTURE_SUM"; return 1; fi
    command shasum "$@"
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
				if strings.Contains(stdout+stderr, "CAAM_SKIP_VERIFY=1") || strings.Contains(stdout+stderr, "Skipping release verification") {
					t.Errorf("installer recommended or accepted a verification bypass: stdout=%s stderr=%s", stdout, stderr)
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

// Exercise the shell verifier with the same real maintainer-signed fixtures as
// the Go updater. Network access is replaced with local copies; Minisign,
// SHA-256, extraction, installation, and cleanup all run normally. This is an
// additional cryptographic gate when Minisign is installed, not a substitute
// for the always-running hermetic acquisition and failure tests above.
func TestInstallerRealMinisignFixture(t *testing.T) {
	if _, err := exec.LookPath("minisign"); err != nil {
		t.Skip("real Minisign executable required for cryptographic fixture tests")
	}
	fixtureDir := filepath.Join(filepath.Dir(repoInstallScript(t)), "internal", "update", "testdata")
	for _, platform := range []string{"linux_amd64", "linux_arm64"} {
		for _, scenario := range []string{"ordinary install", "install with self-test", "tampered manifest", "tampered signature", "tampered archive", "verification bypass"} {
			t.Run(platform+"/"+scenario, func(t *testing.T) {
				dir := t.TempDir()
				tmp := filepath.Join(dir, "tmp")
				installed := filepath.Join(dir, "installed")
				for _, path := range []string{tmp, installed} {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				previous := []byte("previous installed binary\n")
				if err := os.WriteFile(filepath.Join(installed, "caam"), previous, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(tmp, "unrelated"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"archive.tar.gz", "SHA256SUMS", "SHA256SUMS.minisig"} {
					data, err := os.ReadFile(filepath.Join(fixtureDir, name))
					if err != nil {
						t.Fatal(err)
					}
					switch {
					case scenario == "tampered manifest" && name == "SHA256SUMS":
						data = append(data, []byte("unsigned extra entry\n")...)
					case scenario == "tampered signature" && name == "SHA256SUMS.minisig":
						// Change signature bytes, not the deliberately untrusted comment.
						lines := strings.Split(string(data), "\n")
						lines[1] = lines[1][:20] + "AAAA" + lines[1][24:]
						data = []byte(strings.Join(lines, "\n"))
					case scenario == "tampered archive" && name == "archive.tar.gz":
						data[0] ^= 1
					}
					if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				asset := "caam_9.9.9_" + platform + ".tar.gz"
				release, err := json.Marshal(map[string]any{
					"tag_name": "v9.9.9",
					"assets": []map[string]string{
						{"name": asset, "browser_download_url": "https://example.test/" + asset},
						{"name": "SHA256SUMS", "browser_download_url": "https://example.test/SHA256SUMS"},
						{"name": "SHA256SUMS.minisig", "browser_download_url": "https://example.test/SHA256SUMS.minisig"},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				env := []string{
					"INSTALL_DIR=" + installed, "TMPDIR=" + tmp, "FIXTURE_DIR=" + dir,
					"RELEASE_JSON=" + string(release), "FIXTURE_PLATFORM=" + platform,
					"CI=true", "NONINTERACTIVE=1",
				}
				if scenario == "verification bypass" {
					env = append(env, "CAAM_SKIP_VERIFY=1")
				}
				setup := `
INSTALL_VERSION=v9.9.9
detect_terminal_features() { USE_COLOR=false; HAS_GUM=false; IS_TTY=false; }
detect_platform() { printf '%s\n' "$FIXTURE_PLATFORM"; }
get_installed_version() { :; }
get_release() { printf '%s' "$RELEASE_JSON"; }
download_file() {
    case "$1" in
        https://example.test/caam_*) cp "$FIXTURE_DIR/archive.tar.gz" "$2" ;;
        https://example.test/SHA256SUMS) cp "$FIXTURE_DIR/SHA256SUMS" "$2" ;;
        https://example.test/SHA256SUMS.minisig) cp "$FIXTURE_DIR/SHA256SUMS.minisig" "$2" ;;
        *) return 1 ;;
    esac
}
try_go_install() { printf 'unexpected fallback' > "$FIXTURE_DIR/fallback"; return 0; }
`
				body := "main"
				if scenario == "install with self-test" {
					body += " --verify"
				}
				stdout, stderr, code := runInstallFunc(t, env, setup, body, "")
				wantOK := scenario == "ordinary install" || scenario == "install with self-test"
				wantCode := 3
				if wantOK {
					wantCode = 0
				}
				if code != wantCode {
					t.Fatalf("exit=%d want %d; stdout=%s stderr=%s", code, wantCode, stdout, stderr)
				}
				reportedVerification := strings.Contains(stdout+stderr, "Verified release signature and checksum")
				if reportedVerification != wantOK {
					t.Errorf("incorrect verification claim: stdout=%s stderr=%s", stdout, stderr)
				}
				got, err := os.ReadFile(filepath.Join(installed, "caam"))
				if err != nil {
					t.Fatal(err)
				}
				if wantOK {
					if !bytes.Contains(got, []byte("caam-fixture-9.9.9")) {
						t.Errorf("signed fixture was not installed: %q", got)
					}
				} else if !bytes.Equal(got, previous) {
					t.Error("failed verification replaced the installed binary")
				}
				if _, err := os.Stat(filepath.Join(dir, "fallback")); !os.IsNotExist(err) {
					t.Errorf("verification path fell back to unsigned source: %v", err)
				}
				if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 1 || entries[0].Name() != "unrelated" {
					t.Errorf("installer temporary files remain: %v; err=%v", entries, err)
				}
				if data, err := os.ReadFile(filepath.Join(tmp, "unrelated")); err != nil || string(data) != "preserve" {
					t.Errorf("unrelated temporary content changed: %q; err=%v", data, err)
				}
			})
		}
	}
}

// Opt in through a DSR check with CAAM_INSTALLER_DISTRIBUTION_TESTS=1. These
// are actual userspaces and release downloads, not os-release fixtures. The
// engine must support both platforms (native workers or configured emulation).
// Keep the ordinary suite offline; explicitly requested matrix runs fail if
// their engine, architecture support, network, or package setup is unavailable.
func TestInstallerLinuxDistributionMatrix(t *testing.T) {
	if os.Getenv("CAAM_INSTALLER_DISTRIBUTION_TESTS") != "1" {
		t.Skip("set CAAM_INSTALLER_DISTRIBUTION_TESTS=1 for the live Ubuntu/Debian matrix")
	}
	engine := ""
	for _, candidate := range []string{"docker", "podman"} {
		if path, err := exec.LookPath(candidate); err == nil {
			engine = path
			break
		}
	}
	if engine == "" {
		t.Fatal("live distribution tests require Docker or Podman with amd64 and arm64 support")
	}
	const script = `
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl jq
if command -v minisign >/dev/null; then
    echo 'base image unexpectedly includes Minisign; missing-tool coverage would be lost' >&2
    exit 1
fi
export INSTALL_DIR=/caam-test/bin TMPDIR=/caam-test/tmp CI=true NO_COLOR=1
mkdir -p "$INSTALL_DIR" "$TMPDIR" /caam-test/transport
printf 'foreign\n' > "$TMPDIR/keep"
cat > /caam-test/transport/curl <<'CURL'
#!/bin/sh
exec /usr/bin/curl -A 'coding_agent_account_manager-agent (+https://github.com/Dicklesworthstone/coding_agent_account_manager)' "$@"
CURL
chmod 700 /caam-test/transport/curl
export PATH="/caam-test/transport:$PATH"
check_cleanup() {
    [ "$(cat "$TMPDIR/keep")" = foreign ]
    [ "$(find "$TMPDIR" -mindepth 1 -maxdepth 1 | wc -l)" -eq 1 ]
}
bash /install.sh --version=v0.1.25 --verify > /caam-test/bootstrap.log 2>&1 || {
    cat /caam-test/bootstrap.log; exit 1;
}
cat /caam-test/bootstrap.log
grep -F 'Acquiring authenticated temporary Minisign 0.12' /caam-test/bootstrap.log
grep -F 'Verified release signature and checksum.' /caam-test/bootstrap.log
"$INSTALL_DIR/caam" --version
check_cleanup

# Package availability is deliberately different on Jammy. Newer Ubuntu and
# Debian must also exercise the installed verifier path, with no bootstrap.
if [ "$TEST_SYSTEM_PACKAGE" = yes ]; then
    apt-get install -y --no-install-recommends minisign
    bash /install.sh --version=v0.1.25 --force --verify > /caam-test/package.log 2>&1 || {
        cat /caam-test/package.log; exit 1;
    }
    cat /caam-test/package.log
    grep -F 'Verified release signature and checksum.' /caam-test/package.log
    if grep -F 'Acquiring authenticated temporary Minisign' /caam-test/package.log; then exit 1; fi
    check_cleanup
fi

# Even in CI, a former bypass request must fail and preserve the installation.
before=$(sha256sum "$INSTALL_DIR/caam")
status=0
CAAM_SKIP_VERIFY=1 bash /install.sh --version=v0.1.25 --force > /caam-test/refusal.log 2>&1 || status=$?
cat /caam-test/refusal.log
[ "$status" -eq 3 ]
grep -F 'Release verification cannot be disabled' /caam-test/refusal.log
[ "$(sha256sum "$INSTALL_DIR/caam")" = "$before" ]
check_cleanup
`
	for _, image := range []string{"ubuntu:22.04", "ubuntu:24.04", "debian:12-slim", "debian:13-slim"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(image+"/"+arch, func(t *testing.T) {
				packageAvailable := "yes"
				if image == "ubuntu:22.04" {
					packageAvailable = "no"
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				cidFile := filepath.Join(t.TempDir(), "container-id")
				t.Cleanup(func() {
					data, err := os.ReadFile(cidFile)
					if os.IsNotExist(err) {
						return // The engine never created a container.
					}
					id := strings.TrimSpace(string(data))
					decoded, decodeErr := hex.DecodeString(id)
					if err != nil || decodeErr != nil || len(decoded) != 32 {
						t.Errorf("cannot identify this test's container for cleanup: read=%v decode=%v", err, decodeErr)
						return
					}
					// Killing the attached CLI on a timeout does not necessarily stop
					// its container. Remove only the ID written by this invocation.
					cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cleanupCancel()
					output, cleanupErr := exec.CommandContext(cleanupCtx, engine, "rm", "--force", id).CombinedOutput()
					if cleanupErr != nil && !strings.Contains(string(output), "No such container") && !strings.Contains(string(output), "no container with") {
						t.Errorf("container cleanup: %v: %s", cleanupErr, output)
					}
				})
				cmd := exec.CommandContext(ctx, engine, "run", "--rm", "--platform", "linux/"+arch,
					"--cidfile", cidFile,
					"--mount", "type=bind,source="+repoInstallScript(t)+",target=/install.sh,readonly",
					"-e", "TEST_SYSTEM_PACKAGE="+packageAvailable, image, "bash", "-c", script)
				output, err := cmd.CombinedOutput()
				t.Logf("%s/%s:\n%s", image, arch, output)
				if err != nil {
					t.Fatalf("distribution installation failed: %v (context: %v)", err, ctx.Err())
				}
			})
		}
	}
}

// Exercise the real installation paths, including their EXIT traps, with
// local archives and synthetic build/network tools. The unrelated sibling
// deliberately shares TMPDIR with the installer and must survive every exit.
func TestInstallerTemporaryDirectoryLifecycle(t *testing.T) {
	for _, tool := range []string{"tar", "find", "chmod", "mv", "cp", "stat", "mktemp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("installer test requires %s", tool)
		}
	}
	for _, tc := range []struct {
		name, method, source, failure, signal, stage string
		wantCode                                     int
	}{
		{name: "binary success", method: "binary"},
		{name: "binary verification failure", method: "binary", failure: "verify", wantCode: 3},
		{name: "binary self-test failure", method: "binary", failure: "self-test", wantCode: 3},
		{name: "binary download and source failure", method: "binary", source: "tar", failure: "download", wantCode: 4},
		{name: "git source success", source: "git"},
		{name: "git source build failure", source: "git", failure: "build", wantCode: 5},
		{name: "git source self-test failure", source: "git", failure: "self-test", wantCode: 3},
		{name: "git missing tag", source: "git", failure: "checkout", wantCode: 5},
		{name: "tar source success", source: "tar"},
		{name: "tar source build failure", source: "tar", failure: "build", wantCode: 5},
		{name: "tar source self-test failure", source: "tar", failure: "self-test", wantCode: 3},
		{name: "tar source download failure", source: "tar", failure: "download", wantCode: 5},
		{name: "binary HUP", method: "binary", signal: "HUP", stage: "download", wantCode: 129},
		{name: "binary INT", method: "binary", signal: "INT", stage: "download", wantCode: 130},
		{name: "binary TERM", method: "binary", signal: "TERM", stage: "download", wantCode: 143},
		{name: "source download TERM", source: "tar", signal: "TERM", stage: "download", wantCode: 143},
		{name: "source build INT", source: "git", signal: "INT", stage: "build", wantCode: 130},
		{name: "mktemp failure", method: "binary", failure: "mktemp", wantCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tmp := filepath.Join(dir, "temporary files")
			installed := filepath.Join(dir, "installed")
			for _, path := range []string{tmp, installed, filepath.Join(tmp, "unrelated")} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			foreign := filepath.Join(tmp, "unrelated", "keep")
			if err := os.WriteFile(foreign, []byte("must survive"), 0600); err != nil {
				t.Fatal(err)
			}
			old := []byte("previous installation")
			if err := os.WriteFile(filepath.Join(installed, "caam"), old, 0600); err != nil {
				t.Fatal(err)
			}
			program := []byte("#!/bin/sh\n[ \"$TEST_FAILURE\" != self-test ] || exit 1\nprintf 'caam v0.1.24\\n'\n")
			programPath := filepath.Join(dir, "program")
			if err := os.WriteFile(programPath, program, 0700); err != nil {
				t.Fatal(err)
			}
			writeArchive := func(path, name string) {
				t.Helper()
				var buf bytes.Buffer
				gz := gzip.NewWriter(&buf)
				tarWriter := tar.NewWriter(gz)
				if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(program))}); err != nil {
					t.Fatal(err)
				}
				if _, err := tarWriter.Write(program); err != nil {
					t.Fatal(err)
				}
				if err := tarWriter.Close(); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			binaryArchive := filepath.Join(dir, "binary.tar.gz")
			sourceArchive := filepath.Join(dir, "source.tar.gz")
			writeArchive(binaryArchive, "caam")
			writeArchive(sourceArchive, "coding_agent_account_manager-0.1.24/source.txt")
			setup := `
INSTALL_VERSION=v0.1.24
VERIFY_AFTER_INSTALL=true
detect_terminal_features() { USE_COLOR=false; HAS_GUM=false; IS_TTY=false; }
detect_platform() { printf 'linux_amd64\n'; }
get_installed_version() { :; }
ensure_json_tool() { :; }
get_release() { printf '{}'; }
select_release_asset() {
    [ "$TEST_METHOD" = binary ] || return 1
    printf 'v0.1.24\nhttps://example.test/binary.tar.gz\ncaam_0.1.24_linux_amd64.tar.gz\n'
}
verify_release_assets() { [ "$TEST_FAILURE" != verify ]; }
ensure_go() { SOURCE_GO_VERSION=1.26.10; }
mktemp() {
    [ "$TEST_FAILURE" != mktemp ] || return 1
    command mktemp "$@"
}
download_file() {
    if [ "$TEST_STAGE" = download ]; then kill -s "$TEST_SIGNAL" "$$"; return 1; fi
    [ "$TEST_FAILURE" != download ] || return 1
    case "$1" in
        https://example.test/binary.tar.gz) cp "$BINARY_ARCHIVE" "$2" ;;
        https://codeload.github.com/*) cp "$SOURCE_ARCHIVE" "$2" ;;
        *) return 1 ;;
    esac
}
git() {
    [ "$TEST_SOURCE" != tar ] || return 1
    if [ "$1" = clone ]; then mkdir -p "${@: -1}"; return; fi
    [ "$TEST_FAILURE" != checkout ] || return 1
    return 0
}
go() {
    if [ "$TEST_STAGE" = build ]; then kill -s "$TEST_SIGNAL" "$$"; return 1; fi
    [ "$TEST_FAILURE" != build ] || return 1
    local output=""
    while [ "$#" -gt 0 ]; do
        if [ "$1" = -o ]; then output=$2; break; fi
        shift
    done
    [ -n "$output" ] || return 1
    cp "$PROGRAM" "$output"
}
`
			stdout, stderr, code := runInstallFunc(t, []string{
				"TMPDIR=" + tmp, "INSTALL_DIR=" + installed, "PROGRAM=" + programPath,
				"BINARY_ARCHIVE=" + binaryArchive, "SOURCE_ARCHIVE=" + sourceArchive,
				"TEST_METHOD=" + tc.method, "TEST_SOURCE=" + tc.source, "TEST_FAILURE=" + tc.failure,
				"TEST_SIGNAL=" + tc.signal, "TEST_STAGE=" + tc.stage,
			}, setup, `main`, "")
			if code != tc.wantCode {
				t.Fatalf("installer exit=%d want %d; stdout=%s stderr=%s", code, tc.wantCode, stdout, stderr)
			}
			entries, err := os.ReadDir(tmp)
			if err != nil || len(entries) != 1 || entries[0].Name() != "unrelated" {
				t.Errorf("temporary root was not cleaned: entries=%v err=%v; stdout=%s stderr=%s", entries, err, stdout, stderr)
			}
			if content, err := os.ReadFile(foreign); err != nil || string(content) != "must survive" {
				t.Errorf("unrelated directory changed: content=%q err=%v", content, err)
			}
			want := old
			if tc.wantCode == 0 {
				want = program
			}
			if content, err := os.ReadFile(filepath.Join(installed, "caam")); err != nil || !bytes.Equal(content, want) {
				t.Errorf("installed binary changed incorrectly: content=%q err=%v", content, err)
			}
		})
	}
}

func TestTemporaryDirectoryRegistrationAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, setup, body string
		wantCode          int
	}{
		{name: "subshell rejected", body: `captured=$(make_tmp_dir candidate); exit $?`, wantCode: 1},
		{name: "signal during registration", setup: `mktemp() { command mktemp "$@"; kill -TERM "$$"; }`, body: `make_tmp_dir candidate`, wantCode: 143},
		{name: "subshell cannot remove parent directory", body: `make_tmp_dir candidate; (cleanup_tmp_dirs); test -d "$candidate"`},
		{name: "another invocation preserves active directory", body: `make_tmp_dir candidate; bash -c 'source "$INSTALL_SH"; make_tmp_dir other'; test -d "$candidate"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			stdout, stderr, code := runInstallFunc(t, []string{"TMPDIR=" + tmp}, tc.setup, tc.body, "")
			if code != tc.wantCode {
				t.Fatalf("exit=%d want %d; stdout=%s stderr=%s", code, tc.wantCode, stdout, stderr)
			}
			if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
				t.Errorf("temporary root was not cleaned: entries=%v err=%v; stdout=%s stderr=%s", entries, err, stdout, stderr)
			}
		})
	}
	for _, replacement := range []string{"directory", "symlink"} {
		t.Run("preserve replaced "+replacement, func(t *testing.T) {
			tmp := t.TempDir()
			body := `make_tmp_dir candidate
mv "$candidate" "$candidate.original"
if [ "$REPLACEMENT" = directory ]; then mkdir "$candidate"; else ln -s "$candidate.original" "$candidate"; fi
printf 'foreign\n' > "$candidate/keep"
printf '%s' "$candidate"`
			stdout, stderr, code := runInstallFunc(t, []string{"TMPDIR=" + tmp, "REPLACEMENT=" + replacement}, "", body, "")
			if code != 1 || !strings.Contains(stderr, "Preserving replaced temporary path") {
				t.Fatalf("replacement not reported: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			if content, err := os.ReadFile(filepath.Join(stdout, "keep")); err != nil || string(content) != "foreign\n" {
				t.Errorf("replaced path was removed: content=%q err=%v", content, err)
			}
		})
	}
}

func TestGoBootstrapTemporaryDirectoryCleanup(t *testing.T) {
	for _, failure := range []string{"", "download", "checksum", "install", "interrupt"} {
		t.Run(failure, func(t *testing.T) {
			tmp := t.TempDir()
			setup := `
GO_READY=false
IS_TTY=false
USE_COLOR=false
go() { if [ "$GO_READY" = true ]; then printf 'go version go1.26.10 darwin/arm64\n'; else printf 'go version go1.26.9 darwin/arm64\n'; fi; }
brew() { return 1; }
uname() { printf 'Darwin\n'; }
fetch_latest_go_pkg() { printf 'go1.26.10\nhttps://example.test/go.pkg\n%s\n' "$PKG_SUM"; }
curl() {
    [ "$TEST_FAILURE" != download ] || return 1
    if [ "$TEST_FAILURE" = interrupt ]; then kill -TERM "$$"; return 1; fi
    local output=""
    while [ "$#" -gt 0 ]; do if [ "$1" = -o ]; then output=$2; break; fi; shift; done
    if [ "$TEST_FAILURE" = checksum ]; then printf 'tampered' > "$output"; else printf 'verified-pkg' > "$output"; fi
}
sudo() { [ "$TEST_FAILURE" != install ] || return 1; GO_READY=true; }
`
			sum := sha256.Sum256([]byte("verified-pkg"))
			stdout, stderr, code := runInstallFunc(t, []string{
				"TMPDIR=" + tmp, "TEST_FAILURE=" + failure, "PKG_SUM=" + hex.EncodeToString(sum[:]),
			}, setup, `if ensure_go; then [ "$SOURCE_GO_VERSION" = 1.26.10 ]; else exit 1; fi`, "")
			wantCode := 1
			if failure == "" {
				wantCode = 0
			} else if failure == "interrupt" {
				wantCode = 143
			}
			if code != wantCode {
				t.Fatalf("exit=%d want %d; stdout=%s stderr=%s", code, wantCode, stdout, stderr)
			}
			if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
				t.Errorf("Go package directory was not cleaned: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestTemporaryCleanupErrorsPreserveInstallStatus(t *testing.T) {
	for _, status := range []string{"0", "7"} {
		t.Run(status, func(t *testing.T) {
			tmp := t.TempDir()
			stdout, stderr, code := runInstallFunc(t, []string{"TMPDIR=" + tmp, "TEST_STATUS=" + status}, "",
				`make_tmp_dir candidate; rm() { return 1; }; exit "$TEST_STATUS"`, "")
			wantCode := 1
			if status == "7" {
				wantCode = 7
			}
			if code != wantCode || !strings.Contains(stderr, "Could not remove installer temporary directory") {
				t.Fatalf("cleanup error hidden or original status lost: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
		})
	}
}
