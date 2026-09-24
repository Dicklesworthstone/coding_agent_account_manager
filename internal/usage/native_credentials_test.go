package usage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeCredentialLocatorsStayInSelectedFile(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "auth.json")
	stale := filepath.Join(dir, "xdg-auth.json")
	if err := os.WriteFile(stale, []byte(`{"accessToken":"SYNTHETIC-STALE"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NativeCredentialLocator("cursor", canonical); err == nil {
		t.Fatal("missing canonical credential fell back to another file")
	}
	if err := os.WriteFile(canonical, []byte(`{"accessToken":"SYNTHETIC-CURRENT"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := NativeCredentialLocator("cursor", canonical); err != nil || got != "cursor-root:"+canonical {
		t.Fatalf("cursor locator=%q err=%v", got, err)
	}
	if got, err := NativeCredentialLocator("grok", canonical); err != nil || got != "grok-home:"+dir {
		t.Fatalf("grok locator=%q err=%v", got, err)
	}
	if _, err := NativeCredentialLocator("grok", stale); err == nil {
		t.Fatal("Grok locator silently substituted a sibling auth.json")
	}
	if _, err := NativeCredentialLocator("grok", dir); err == nil {
		t.Fatal("directory accepted as a credential file")
	}
}

func TestLoadNativeCredentialsUsesCanonicalVaultLayout(t *testing.T) {
	root := t.TempDir()
	for _, provider := range []string{"cursor", "grok"} {
		for _, name := range []string{"selected", "stale-only"} {
			dir := filepath.Join(root, provider, name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file := "auth.json"
			if name == "stale-only" {
				file = "xdg-auth.json"
			}
			if err := os.WriteFile(filepath.Join(dir, file), []byte(`{"accessToken":"SYNTHETIC","key":"SYNTHETIC"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		got, err := LoadProfileCredentials(root, provider)
		if err != nil || len(got) != 1 || got["selected"] == "" {
			t.Errorf("%s credential lookup = %v, %v", provider, got, err)
		}
	}
}
