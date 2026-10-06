package rotation

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

func TestSelectionHydratesActualVaultCredentials(t *testing.T) {
	root := t.TempDir()
	actualVault := filepath.Join(root, "custom", "vault")
	store := health.NewStorage(filepath.Join(root, "metadata", "health.json"))
	if err := os.MkdirAll(filepath.Join(root, "metadata"), 0700); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Hour)
	claims := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expired.Unix())))
	for name, body := range map[string]string{
		"expired": `{"accessToken":"e30.` + claims + `.synthetic"}`,
		"api":     `{"apiKey":"synthetic-api-key"}`,
	} {
		dir := filepath.Join(actualVault, "cursor", name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		// The API key replaced an older session with a persisted expiry.
		if err := store.SetTokenExpiry("cursor", name, expired.Add(-24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for _, algorithm := range []Algorithm{AlgorithmSmart, AlgorithmRoundRobin, AlgorithmRandom} {
		for _, policy := range []Policy{PolicyAvailability, PolicyDrain} {
			s := NewSelector(algorithm, store, nil)
			s.SetVaultPath(actualVault)
			s.SetPolicy(policy)
			s.SetIgnoreCooldown(true)
			if result, err := s.Select("cursor", []string{"expired"}, ""); err == nil || !strings.Contains(err.Error(), "login required") {
				t.Fatalf("sole expired session was selected: %+v, %v", result, err)
			}
			if result, err := s.Select("cursor", []string{"expired", "api"}, ""); err != nil || result.Selected != "api" {
				t.Fatalf("stored session expiry blocked current API key: %+v, %v", result, err)
			}
		}
	}
	// Callers that construct selectors without metadata still get fresh
	// credential evidence after binding their actual vault.
	s := NewSelector(AlgorithmRoundRobin, nil, nil)
	s.SetVaultPath(actualVault)
	if _, err := s.Select("cursor", []string{"expired"}, ""); err == nil {
		t.Fatal("nil health storage bypassed credential eligibility")
	}

	apiPath := filepath.Join(actualVault, "cursor", "api", "auth.json")
	info, err := health.ParseCursorExpiry(apiPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordProviderVerification("cursor", "api", health.ProviderVerification{Reason: "access_token_rejected", Fingerprint: info.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	s = NewSelector(AlgorithmSmart, store, nil)
	s.SetVaultPath(actualVault)
	if _, err := s.Select("cursor", []string{"api"}, ""); err == nil {
		t.Fatal("API key renewal bypassed the provider's rejection of that credential")
	}
	if err := os.WriteFile(apiPath, []byte(`{"apiKey":"synthetic-replacement-key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := s.Select("cursor", []string{"api"}, ""); err != nil || result.Selected != "api" {
		t.Fatalf("new login retained rejection of a different credential: %+v, %v", result, err)
	}
}

func TestNativeQuotaCandidatesFailClosed(t *testing.T) {
	profiles := []string{"missing", "failed", "spent", "invalid", "known"}
	data := map[string]*UsageInfo{
		"failed":  {Error: "quota unavailable"},
		"spent":   {SecondaryPercent: 100},
		"invalid": {PrimaryPercent: -1},
		"known":   {PrimaryPercent: 20, SecondaryPercent: 40},
	}
	for _, provider := range []string{"grok", "cursor"} {
		got, err := NativeQuotaCandidates(provider, profiles, data)
		if err != nil || !reflect.DeepEqual(got, []string{"known"}) {
			t.Fatalf("%s candidates=%v err=%v", provider, got, err)
		}
		for _, unknown := range []map[string]*UsageInfo{{}, {"missing": nil}, {"missing": {Error: "unknown"}}} {
			if got, err := NativeQuotaCandidates(provider, []string{"missing"}, unknown); err == nil {
				t.Errorf("%s accepted sole unknown profile: %v", provider, got)
			}
		}
	}
	if profiles[0] != "missing" || len(profiles) != 5 {
		t.Fatal("candidate filtering mutated the caller's slice")
	}
}

func TestNativeQuotaCandidatesPreserveOptOutAndLegacyBehavior(t *testing.T) {
	profiles := []string{"one", "two"}
	for _, provider := range []string{"grok", "cursor", "claude", "codex"} {
		got, err := NativeQuotaCandidates(provider, profiles, nil)
		if err != nil || !reflect.DeepEqual(got, profiles) {
			t.Errorf("non-usage-aware %s changed: %v, %v", provider, got, err)
		}
	}
	for _, provider := range []string{"claude", "codex"} {
		got, err := NativeQuotaCandidates(provider, profiles, map[string]*UsageInfo{})
		if err != nil || !reflect.DeepEqual(got, profiles) {
			t.Errorf("legacy %s changed: %v, %v", provider, got, err)
		}
	}
}
