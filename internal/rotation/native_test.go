package rotation

import (
	"reflect"
	"testing"
)

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
