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
	"strings"
	"testing"
)

// wrongMinisignKey is a valid minisign public key that is NOT the release
// key (it is the retired epoch-1 dsr key). Signatures by the release key
// must not verify against it and vice versa.
const wrongMinisignKey = "RWSoYi6NXJWzaRs1mJmOwwXrZfPWcq6MXnQlNMLBYKzlIQTLwuVQG6uO"

// The testdata fixtures were produced by signing testdata/SHA256SUMS with the
// real release secret key:
//
//	minisign -S -s minisign.key -m SHA256SUMS -x SHA256SUMS.minisig
//
// so TestVerifyMinisignFixture proves the embedded MinisignPublicKey and the
// pure-Go verification agree with the actual signing setup, not just with
// signatures this test generated for itself.
func TestVerifyMinisignFixture(t *testing.T) {
	err := VerifyMinisign(
		filepath.Join("testdata", "SHA256SUMS"),
		filepath.Join("testdata", "SHA256SUMS.minisig"),
	)
	if err != nil {
		t.Fatalf("VerifyMinisign() on real signed fixture: %v", err)
	}
}

func TestVerifyMinisignTamperedSums(t *testing.T) {
	sums, err := os.ReadFile(filepath.Join("testdata", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join("testdata", "SHA256SUMS.minisig"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("flipped byte", func(t *testing.T) {
		tampered := append([]byte{}, sums...)
		tampered[0] ^= 0x01
		if err := verifyMinisignBytes(MinisignPublicKey, tampered, sig); err == nil {
			t.Error("verification succeeded on tampered SHA256SUMS")
		}
	})

	t.Run("swapped hash", func(t *testing.T) {
		tampered := strings.Replace(string(sums),
			"a46aa56378a6dc012f7499697b9eab9d69a51ef36b763e02925a3195de791f22",
			"deadbeef78a6dc012f7499697b9eab9d69a51ef36b763e02925a3195de791f22", 1)
		if err := verifyMinisignBytes(MinisignPublicKey, []byte(tampered), sig); err == nil {
			t.Error("verification succeeded on SHA256SUMS with a substituted hash")
		}
	})

	t.Run("appended entry", func(t *testing.T) {
		tampered := append(append([]byte{}, sums...),
			[]byte("a46aa56378a6dc012f7499697b9eab9d69a51ef36b763e02925a3195de791f22  caam_9.9.9_windows_amd64.zip\n")...)
		if err := verifyMinisignBytes(MinisignPublicKey, tampered, sig); err == nil {
			t.Error("verification succeeded on SHA256SUMS with an appended entry")
		}
	})
}

func TestVerifyMinisignWrongKey(t *testing.T) {
	sums, err := os.ReadFile(filepath.Join("testdata", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join("testdata", "SHA256SUMS.minisig"))
	if err != nil {
		t.Fatal(err)
	}
	err = verifyMinisignBytes(wrongMinisignKey, sums, sig)
	if err == nil {
		t.Fatal("verification succeeded against the wrong public key")
	}
	// Should be rejected on key ID, before any cryptographic comparison.
	if !strings.Contains(err.Error(), "key") {
		t.Errorf("expected key mismatch error, got: %v", err)
	}
}

func TestVerifyMinisignGarbageSignature(t *testing.T) {
	sums, err := os.ReadFile(filepath.Join("testdata", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMinisignBytes(MinisignPublicKey, sums, []byte("not a minisig")); err == nil {
		t.Error("verification succeeded on garbage signature")
	}
}

func TestVerifyMinisignMissingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	missing := filepath.Join(tmpDir, "does-not-exist")

	if err := VerifyMinisign(missing, filepath.Join("testdata", "SHA256SUMS.minisig")); err == nil {
		t.Error("VerifyMinisign() succeeded with missing checksums file")
	}
	if err := VerifyMinisign(filepath.Join("testdata", "SHA256SUMS"), missing); err == nil {
		t.Error("VerifyMinisign() succeeded with missing signature file")
	}
}

func TestMinisignRequired(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"0.1.17", false},
		{"0.1.16", false},
		{"0.0.1", false},
		{"dev", false},
		{"0.1.18", true},
		{"v0.1.18", true},
		{"0.1.19", true},
		{"0.2.0", true},
		{"1.0.0", true},
		{"9.9.9", true},
	}
	for _, tt := range tests {
		if got := MinisignRequired(tt.version); got != tt.want {
			t.Errorf("MinisignRequired(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

// fakeRelease serves a GitHub-releases-shaped API plus asset downloads from a
// single httptest server. assets maps asset name to content.
func fakeReleaseServer(t *testing.T, tag string, assets map[string][]byte) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	var server *httptest.Server

	buildRelease := func() Release {
		rel := Release{TagName: tag, HTMLURL: server.URL + "/release/" + tag}
		for name := range assets {
			rel.Assets = append(rel.Assets, Asset{
				Name:               name,
				BrowserDownloadURL: server.URL + "/download/" + name,
				Size:               int64(len(assets[name])),
			})
		}
		return rel
	}

	mux.HandleFunc("/repos/test/test/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]Release{buildRelease()}); err != nil {
			t.Errorf("encode releases: %v", err)
		}
	})
	mux.HandleFunc("/repos/test/test/releases/tags/"+tag, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(buildRelease()); err != nil {
			t.Errorf("encode release: %v", err)
		}
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/download/")
		content, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(content)
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func fixtureAssets(t *testing.T) (sums, sig, archive []byte) {
	t.Helper()
	var err error
	if sums, err = os.ReadFile(filepath.Join("testdata", "SHA256SUMS")); err != nil {
		t.Fatal(err)
	}
	if sig, err = os.ReadFile(filepath.Join("testdata", "SHA256SUMS.minisig")); err != nil {
		t.Fatal(err)
	}
	if archive, err = os.ReadFile(filepath.Join("testdata", "archive.tar.gz")); err != nil {
		t.Fatal(err)
	}
	return sums, sig, archive
}

func newFixtureUpdater(t *testing.T, server *httptest.Server) *Updater {
	t.Helper()
	tmpDir := t.TempDir()
	exePath := filepath.Join(tmpDir, "caam")
	if err := os.WriteFile(exePath, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(Config{
		Owner:      "test",
		Repo:       "test",
		Channel:    ChannelStable,
		HTTPClient: server.Client(),
		APIBase:    server.URL,
		ExePath:    exePath,
		BackupDir:  tmpDir,
	})
}

// TestUpdateEndToEndMinisign runs the full Update() flow against a fake
// release whose SHA256SUMS carries a REAL minisign signature from the release
// key: fetch release -> match asset (the #75 wildcard path) -> verify
// minisign -> verify sha256 -> extract -> atomic replace.
func TestUpdateEndToEndMinisign(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture archive is tar.gz; windows expects zip")
	}
	sums, sig, archive := fixtureAssets(t)

	assetName := fmt.Sprintf("caam_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	if _, err := readExpectedChecksum(filepath.Join("testdata", "SHA256SUMS"), assetName); err != nil {
		t.Skipf("no fixture checksum entry for %s: %v", assetName, err)
	}

	server := fakeReleaseServer(t, "v9.9.9", map[string][]byte{
		assetName:            archive,
		"SHA256SUMS":         sums,
		"SHA256SUMS.minisig": sig,
	})
	u := newFixtureUpdater(t, server)

	result, err := u.Update(context.Background())
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	if !result.Updated {
		t.Fatal("Update() did not update")
	}

	replaced, err := os.ReadFile(u.config.ExePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(replaced), "caam-fixture-9.9.9") {
		t.Errorf("binary was not replaced with the archive contents")
	}
}

// TestUpdateMinisignMissingIsFatal pins the fail-closed contract: a release
// >= 0.1.18 without SHA256SUMS.minisig must not install, even though a legacy
// SHA256SUMS.sig is present (cosign-absent hosts used to silently skip it).
func TestUpdateMinisignMissingIsFatal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture archive is tar.gz; windows expects zip")
	}
	sums, _, archive := fixtureAssets(t)

	assetName := fmt.Sprintf("caam_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	server := fakeReleaseServer(t, "v9.9.9", map[string][]byte{
		assetName:        archive,
		"SHA256SUMS":     sums,
		"SHA256SUMS.sig": []byte("legacy cosign bundle"),
	})
	u := newFixtureUpdater(t, server)

	_, err := u.Update(context.Background())
	if err == nil {
		t.Fatal("Update() succeeded without SHA256SUMS.minisig on a >= 0.1.18 release")
	}
	if !strings.Contains(err.Error(), "SHA256SUMS.minisig") {
		t.Errorf("error should name the missing minisig asset, got: %v", err)
	}
}

// TestUpdateMinisignTamperedSumsIsFatal serves a tampered SHA256SUMS with the
// genuine signature; the update must refuse before downloading the binary.
func TestUpdateMinisignTamperedSumsIsFatal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture archive is tar.gz; windows expects zip")
	}
	sums, sig, archive := fixtureAssets(t)
	tampered := append([]byte{}, sums...)
	tampered[0] ^= 0x01

	assetName := fmt.Sprintf("caam_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	server := fakeReleaseServer(t, "v9.9.9", map[string][]byte{
		assetName:            archive,
		"SHA256SUMS":         tampered,
		"SHA256SUMS.minisig": sig,
	})
	u := newFixtureUpdater(t, server)

	_, err := u.Update(context.Background())
	if err == nil {
		t.Fatal("Update() succeeded with tampered SHA256SUMS")
	}
	if !strings.Contains(err.Error(), "verify signature") {
		t.Errorf("expected signature verification failure, got: %v", err)
	}
}

// TestUpdateLegacyReleaseStillWantsCosignSig pins the version gate's other
// side: a release < 0.1.18 keeps the legacy contract and still hard-errors on
// a missing SHA256SUMS.sig (and must NOT demand a minisig).
func TestUpdateLegacyReleaseStillWantsCosignSig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture archive is tar.gz; windows expects zip")
	}
	sums, _, archive := fixtureAssets(t)

	assetName := fmt.Sprintf("caam_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	server := fakeReleaseServer(t, "v0.1.17", map[string][]byte{
		assetName:    archive,
		"SHA256SUMS": sums,
	})
	u := newFixtureUpdater(t, server)

	_, err := u.Update(context.Background())
	if err == nil {
		t.Fatal("Update() succeeded without SHA256SUMS.sig on a legacy release")
	}
	if !strings.Contains(err.Error(), "SHA256SUMS.sig") || strings.Contains(err.Error(), "minisig") {
		t.Errorf("legacy release should require SHA256SUMS.sig (not minisig), got: %v", err)
	}
}

// TestSelectBinaryAssetSkipsMinisig extends the #75 matcher contract to the
// new signature asset.
func TestSelectBinaryAssetSkipsMinisig(t *testing.T) {
	u := New(DefaultConfig())
	if got := selectBinaryAsset([]Asset{{Name: "SHA256SUMS.minisig"}}, u.binaryAssetName()); got != nil {
		t.Errorf("selectBinaryAsset wrongly selected %q", got.Name)
	}
}
