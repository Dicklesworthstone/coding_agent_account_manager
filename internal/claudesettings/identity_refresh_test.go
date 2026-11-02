package claudesettings

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

func TestIdentityOnlyBatchRollsBackWhenCredentialChanges(t *testing.T) {
	dir := t.TempDir()
	credentialPath := filepath.Join(dir, ".credentials.json")
	metaPath := filepath.Join(dir, "meta.json")
	statePath := filepath.Join(dir, ".claude.json")
	credential := []byte(`{"claudeAiOauth":{"accessToken":"captured-access","refreshToken":"captured-refresh"}}`)
	replacement := []byte(`{"claudeAiOauth":{"accessToken":"concurrent-access","refreshToken":"concurrent-refresh"}}`)
	metadata := []byte(`{"description":"operator label"}`)
	state := []byte(`{"oauthAccount":{"accountUuid":"stale"},"numStartups":9}`)
	for path, data := range map[string][]byte{credentialPath: credential, metaPath: metadata, statePath: state} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := identity.ClaudeCredentialFingerprint(credential)
	if err != nil {
		t.Fatal(err)
	}
	binding := &identity.ClaudeIdentityBinding{
		Identity:              identity.Identity{Provider: "claude", AccountID: "account", OrganizationID: "team"},
		CredentialFingerprint: fingerprint, VerifiedAt: time.Now().UTC(),
	}
	changed := false
	verify := func() error {
		current, err := os.ReadFile(metaPath)
		if err != nil {
			return err
		}
		if !changed && !bytes.Equal(current, metadata) {
			// An independent native writer replaces the source after the
			// first metadata destination was installed, before the next one.
			changed = true
			if err := os.WriteFile(credentialPath, replacement, 0600); err != nil {
				return err
			}
		}
		current, err = os.ReadFile(credentialPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, credential) {
			return errors.New("captured credential changed")
		}
		return nil
	}
	metaUpdate, err := PrepareIdentityMetadata(metaPath, credentialPath, metadata, credential, binding, verify)
	if err != nil {
		t.Fatal(err)
	}
	stateUpdate, err := PrepareOAuthAccount(statePath, state, []byte(`{"accountUuid":"account","organizationUuid":"team"}`), verify)
	if err != nil {
		t.Fatal(err)
	}
	err = ApplyUpdatesWithFinalizer([]*Update{metaUpdate, stateUpdate}, nil, verify)
	if err == nil || !changed {
		t.Fatal("identity publication accepted a changed credential generation")
	}
	for path, expected := range map[string][]byte{metaPath: metadata, statePath: state, credentialPath: replacement} {
		actual, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("failed identity publication changed %s or overwrote the concurrent credential", filepath.Base(path))
		}
	}
}
