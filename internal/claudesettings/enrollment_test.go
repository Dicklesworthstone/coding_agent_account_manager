package claudesettings

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLegacyEnrollmentHonorsAccountScope(t *testing.T) {
	const host = `{"userID":"installation","hasCompletedOnboarding":true,"theme":"dark","model":"live","permissions":{"allow":["Read"]},"autoMode":{"allow":["tests"]},"mcpServers":{"private":{"headers":{"Authorization":"synthetic-host"}}},"hooks":{"Stop":["host-private"]},"projects":{"/repo":{"mcpServers":{"private":{}},"history":["host"]}},"customAuth":"synthetic-host","apiKeyHelper":"host-helper","oauthAccount":{"accountUuid":"host"},"primaryApiKey":"synthetic-host","awsAuthRefresh":"host-aws","cachedUsageUtilization":{"value":1},"env":{"ANTHROPIC_API_KEY":"synthetic-host","EDITOR":"vim","CUSTOM_GATEWAY":"host"}}`
	policy := Policy{ProfileKeys: []string{"mcpServers", "hooks", "projects", "customAuth"}, SharedEnvKeys: []string{"EDITOR"}}
	data := []byte(host)
	seed, err := SeedLegacy(data, policy)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := object(seed)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range append(LegacyAccountKeys(), policy.ProfileKeys...) {
		if _, ok := obj[key]; ok {
			t.Fatalf("host account field %s crossed the enrollment boundary", key)
		}
	}
	for _, key := range []string{"userID", "hasCompletedOnboarding", "theme", "model", "permissions", "autoMode"} {
		if _, ok := obj[key]; !ok {
			t.Fatalf("shared enrollment field %s was lost", key)
		}
	}
	env := envOf(obj)
	if len(env) != 1 || string(env["EDITOR"]) != `"vim"` {
		t.Fatalf("enrollment did not conservatively scope env: %s", obj["env"])
	}
	if !bytes.Equal(data, []byte(host)) {
		t.Fatal("seeding mutated its source")
	}
	policy.Mode = "per-profile"
	seed, err = SeedLegacy(data, policy)
	if err != nil {
		t.Fatal(err)
	}
	obj, err = object(seed)
	if err != nil || len(obj) != 2 || string(obj["userID"]) != `"installation"` || string(obj["hasCompletedOnboarding"]) != "true" {
		t.Fatalf("private-policy enrollment inherited host workflow/auth: %s, %v", seed, err)
	}
}

func TestLegacyEnrollmentScopesEveryKnownHelperAndEnvironmentByDefault(t *testing.T) {
	data := []byte(`{"apiKey":"synthetic","api_key":"synthetic","apiKeyHelper":"helper","awsAuthRefresh":"refresh","awsCredentialExport":"export","otelHeadersHelper":"headers","forceLoginMethod":"console","forceLoginOrgUUID":"host-org","oauthToken":"synthetic","sessionKey":"synthetic","env":{"UNCLASSIFIED":"synthetic"},"permissions":{"allow":["Read"]}}`)
	seed, err := SeedLegacy(data, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	obj, err := object(seed)
	if err != nil || len(obj) != 1 || obj["permissions"] == nil {
		t.Fatalf("fresh profile inherited authentication-bearing host data: %s, %v", seed, err)
	}
	for _, bad := range []string{`null`, `[]`, `{"env":[]}`, `{broken`} {
		if _, err := SeedLegacy([]byte(bad), Policy{}); err == nil {
			t.Fatalf("malformed legacy seed was accepted: %s", bad)
		}
	}
	if _, err := SeedLegacy(data, Policy{SharedEnvKeys: []string{"ANTHROPIC_API_KEY"}}); err == nil {
		t.Fatal("enrollment bypassed unsafe environment policy validation")
	}
	if _, err := SeedLegacy(data, Policy{Mode: "typo"}); err == nil {
		t.Fatal("enrollment bypassed mode validation")
	}
}

func TestPrivatePreparationIsReadOnlyAndIgnoresCanonicalContents(t *testing.T) {
	dir := t.TempDir()
	host, private := filepath.Join(dir, "host"), filepath.Join(dir, "profile", ".claude.json")
	writeSource(t, host, `{broken-host-policy`)
	const own = "{\"apiKey\":\"synthetic-own\", \"model\":\"private\"}\n"
	writeSource(t, private, own)
	before, err := os.Stat(private)
	if err != nil {
		t.Fatal(err)
	}
	u, err := PreparePrivate(private, host)
	if err != nil {
		t.Fatal(err)
	}
	if u.Changed() {
		t.Fatal("validation should not schedule a rewrite")
	}
	if err := ApplyUpdates([]*Update{u}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(private)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("read-only launch validation rewrote a private document: %v", err)
	}
	requireSourceUnchanged(t, private, own)
	missing := filepath.Join(dir, "profile", "missing.json")
	u, err = PreparePrivate(missing, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("validation created a missing document: %v", err)
	}
	if _, err := PreparePrivate(missing, missing); err == nil {
		t.Fatal("an absent host path was accepted as a private destination")
	}
}

func TestPrivatePreparationRejectsMalformedDocuments(t *testing.T) {
	for _, bad := range []string{"", "null", "[]", `{broken`, `{"env":[]}`} {
		path := filepath.Join(t.TempDir(), ".claude.json")
		writeSource(t, path, bad)
		if _, err := PreparePrivate(path); err == nil {
			t.Fatalf("accepted malformed private state: %q", bad)
		}
		requireSourceUnchanged(t, path, bad)
	}
}

func TestPrivatePreparationRejectsAliasesAndRechecksAfterPrepare(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("link creation requires privileges on Windows")
	}
	for _, kind := range []string{"symlink", "hardlink"} {
		for _, timing := range []string{"before", "after"} {
			t.Run(kind+"/"+timing, func(t *testing.T) {
				dir := t.TempDir()
				host, private := filepath.Join(dir, "host"), filepath.Join(dir, "private")
				const same = `{"apiKey":"synthetic-identical","permissions":{"allow":["Read"]}}`
				writeSource(t, host, same)
				var u *Update
				var err error
				if timing == "after" {
					writeSource(t, private, same)
					u, err = PreparePrivate(private, host)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(private, private+".saved"); err != nil {
						t.Fatal(err)
					}
				}
				link := os.Symlink
				if kind == "hardlink" {
					link = os.Link
				}
				if err := link(host, private); err != nil {
					t.Fatal(err)
				}
				if timing == "before" {
					_, err = PreparePrivate(private, host)
				} else {
					err = ApplyUpdates([]*Update{u})
				}
				if err == nil {
					t.Fatal("same bytes hid a shared identity alias")
				}
				requireSourceUnchanged(t, host, same)
			})
		}
	}
}
