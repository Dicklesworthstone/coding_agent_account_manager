package keychain_test

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

const sampleAgyBlob = `{"auth_method":"oauth","token":"SYNTHETIC-AGY-TOKEN"}`

func TestDecodeGoKeyringSecret(t *testing.T) {
	b64 := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(sampleAgyBlob))
	hexB64 := hex.EncodeToString([]byte(b64))
	hexJSON := hex.EncodeToString([]byte(sampleAgyBlob))

	for _, tc := range []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "go-keyring base64 JSON", raw: b64, want: sampleAgyBlob},
		{name: "hex-wrapped go-keyring base64 JSON", raw: hexB64, want: sampleAgyBlob},
		{name: "hex-wrapped raw JSON", raw: hexJSON, want: sampleAgyBlob},
		{name: "plain JSON", raw: sampleAgyBlob, want: sampleAgyBlob},
		{name: "opaque string", raw: "synthetic-opaque-token", want: "synthetic-opaque-token"},
		{name: "empty", raw: "", wantErr: true},
		{name: "invalid base64", raw: "go-keyring-base64:!!!not-base64!!!", wantErr: true},
		{name: "empty decoded base64", raw: "go-keyring-base64:", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := keychain.DecodeGoKeyringSecret([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("DecodeGoKeyringSecret(%q) = %q, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeGoKeyringSecret(%q): %v", tc.raw, err)
			}
			if string(got) != tc.want {
				t.Fatalf("DecodeGoKeyringSecret() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgyKeychainRoundTrip(t *testing.T) {
	items := testutil.FakeKeychain(t)

	if _, err := keychain.ReadAgy(); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("ReadAgy() on empty keychain = %v, want ErrNotFound", err)
	}
	if err := keychain.WriteAgy([]byte(sampleAgyBlob)); err != nil {
		t.Fatalf("WriteAgy(): %v", err)
	}
	stored, ok := testutil.FakeKeychainRead(t, items, keychain.AgyService)
	if !ok || !strings.HasPrefix(stored, "go-keyring-base64:") {
		t.Fatalf("stored keychain item = %q (present=%v), want go-keyring-base64: prefix", stored, ok)
	}
	got, err := keychain.ReadAgy()
	if err != nil {
		t.Fatalf("ReadAgy(): %v", err)
	}
	if string(got) != sampleAgyBlob {
		t.Fatalf("ReadAgy() = %q, want %q", got, sampleAgyBlob)
	}

	tokenPath := filepath.Join(t.TempDir(), ".gemini", "antigravity-cli", "antigravity-oauth-token")
	wrote, err := keychain.EnsureAgyMirror(tokenPath)
	if err != nil || !wrote {
		t.Fatalf("EnsureAgyMirror() = (%v, %v), want (true, nil)", wrote, err)
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil || string(data) != sampleAgyBlob {
		t.Fatalf("mirror = %q (err=%v), want %q", data, err, sampleAgyBlob)
	}

	updated := `{"auth_method":"oauth","token":"SYNTHETIC-AGY-TOKEN-2"}`
	if err := os.WriteFile(tokenPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := keychain.PushAgyMirror(tokenPath); err != nil {
		t.Fatalf("PushAgyMirror(): %v", err)
	}
	got, err = keychain.ReadAgy()
	if err != nil || string(got) != updated {
		t.Fatalf("ReadAgy() after PushAgyMirror = (%q, %v), want %q", got, err, updated)
	}

	if err := keychain.DeleteAgy(); err != nil {
		t.Fatalf("DeleteAgy(): %v", err)
	}
	if _, err := keychain.ReadAgy(); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("ReadAgy() after DeleteAgy = %v, want ErrNotFound", err)
	}
}

func TestAgyKeychainRejectsInvalidAndLocked(t *testing.T) {
	items := testutil.FakeKeychain(t)

	if err := keychain.WriteAgy([]byte(`{"broken":`)); err == nil {
		t.Fatal("WriteAgy accepted malformed JSON")
	}
	if err := keychain.WriteAgy([]byte("   ")); err == nil {
		t.Fatal("WriteAgy accepted blank payload")
	}

	testutil.FakeKeychainStore(t, items, keychain.AgyService, keychain.AgyAccount, "go-keyring-base64:"+base64.StdEncoding.EncodeToString([]byte(`{"broken":`)))
	if _, err := keychain.ReadAgy(); err == nil {
		t.Fatal("ReadAgy accepted malformed JSON payload")
	}

	t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "1")
	if _, err := keychain.ReadAgy(); !errors.Is(err, keychain.ErrDenied) {
		t.Fatalf("ReadAgy against locked keychain = %v, want ErrDenied", err)
	}
}
