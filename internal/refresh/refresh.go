package refresh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

// maxErrorBodySize limits how much of an error response body we read.
// Prevents memory exhaustion from malicious/buggy servers.
const maxErrorBodySize = 64 * 1024 // 64KB

// DefaultRefreshThreshold is the time before expiry to trigger a refresh.
const DefaultRefreshThreshold = 10 * time.Minute

// ShouldRefresh determines if a profile needs refreshing.
func ShouldRefresh(h *health.ProfileHealth, threshold time.Duration) bool {
	if h == nil || h.TokenExpiresAt.IsZero() {
		return false // Unknown expiry, do not assume refresh needed (avoid loops)
	}

	if threshold == 0 {
		threshold = DefaultRefreshThreshold
	}

	ttl := time.Until(h.TokenExpiresAt)
	return ttl > 0 && ttl < threshold
}

// RefreshProfile orchestrates the refresh for a specific provider/profile.
func RefreshProfile(ctx context.Context, provider, profile string, vault *authfile.Vault, store *health.Storage) error {
	// Check if this profile is currently active before we modify the vault
	// (which would change the hash and break ActiveProfile detection).
	//
	// IMPORTANT: Capture a snapshot first, then verify it matches the target profile.
	// This avoids a race where the active profile changes between ActiveProfile()
	// and the snapshot read, which could otherwise overwrite a newly-activated profile.
	isActive := false
	var preRefreshState map[string][]byte

	fileSet, ok := authfile.GetAuthFileSet(provider)
	if ok {
		preRefreshState, _ = readAuthFiles(fileSet)
		if len(preRefreshState) > 0 && snapshotMatchesProfile(fileSet, vault, profile, preRefreshState) {
			isActive = true
		}
	}

	vaultPath := vault.ProfilePath(provider, profile)

	var err error
	switch provider {
	case "claude":
		err = refreshClaude(ctx, vaultPath)
	case "codex":
		err = refreshCodex(ctx, vaultPath)
	case "gemini":
		err = refreshGemini(ctx, provider, profile, store, vaultPath)
	default:
		return &UnsupportedError{Provider: provider, Reason: "provider not supported"}
	}

	if err != nil {
		return err
	}

	// If the profile was active, restore the updated files to the active location
	if isActive && len(preRefreshState) > 0 {
		// Re-verify that the live files haven't changed since we started.
		// We cannot use ActiveProfile here because the vault has been updated (new token),
		// so it would no longer match the live files (old token).
		// Instead, we verify that the live files are exactly as they were before the refresh.
		currentState, _ := readAuthFiles(fileSet)
		if filesEqual(preRefreshState, currentState) {
			if restoreErr := vault.Restore(fileSet, profile); restoreErr != nil {
				return fmt.Errorf("refresh successful but failed to update active files: %w", restoreErr)
			}
		}
	}

	return nil
}

func refreshClaude(ctx context.Context, vaultPath string) error {
	info, err := health.ParseClaudeExpiry(vaultPath)
	if err != nil {
		return fmt.Errorf("parse auth: %w", err)
	}

	if info.Source == "" {
		return fmt.Errorf("auth file source unknown")
	}

	refreshToken, err := getRefreshTokenFromJSON(info.Source)
	if err != nil {
		return fmt.Errorf("read refresh token: %w", err)
	}

	resp, err := RefreshClaudeToken(ctx, refreshToken)
	if err != nil {
		return fmt.Errorf("refresh api: %w", err)
	}

	if err := UpdateClaudeAuth(info.Source, resp); err != nil {
		return fmt.Errorf("update auth: %w", err)
	}

	return nil
}

func refreshCodex(ctx context.Context, vaultPath string) error {
	authPath := filepath.Join(vaultPath, "auth.json")

	refreshToken, err := getRefreshTokenFromJSON(authPath)
	if err != nil {
		return fmt.Errorf("read refresh token: %w", err)
	}

	resp, err := RefreshCodexToken(ctx, refreshToken)
	if err != nil {
		return fmt.Errorf("refresh api: %w", err)
	}

	if err := UpdateCodexAuth(authPath, resp); err != nil {
		return fmt.Errorf("update auth: %w", err)
	}

	return nil
}

func refreshGemini(ctx context.Context, provider, profile string, store *health.Storage, vaultPath string) error {
	info, err := health.ParseGeminiExpiry(vaultPath)
	if err != nil {
		return fmt.Errorf("parse gemini auth: %w", err)
	}

	settingsPath := filepath.Join(vaultPath, "settings.json")
	oauthCredPath := filepath.Join(vaultPath, "oauth_credentials.json")

	var adc *ADC
	for _, candidate := range []string{oauthCredPath, settingsPath} {
		parsed, readErr := ReadADC(candidate)
		if readErr == nil {
			adc = parsed
			break
		}
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if errors.Is(readErr, ErrADCIncomplete) {
			continue
		}
		return fmt.Errorf("read oauth credentials: %w", readErr)
	}

	if adc == nil {
		return &UnsupportedError{Provider: provider, Reason: "missing oauth client credentials (expected oauth_credentials.json with client_id/client_secret/refresh_token)"}
	}

	resp, err := RefreshGeminiToken(ctx, adc.ClientID, adc.ClientSecret, adc.RefreshToken)
	if err != nil {
		return fmt.Errorf("refresh api: %w", err)
	}

	target := settingsPath
	if _, err := os.Stat(target); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat gemini settings: %w", err)
		}
		target = info.Source
	}

	if target != "" {
		if err := UpdateGeminiAuth(target, resp); err != nil {
			return fmt.Errorf("update auth: %w", err)
		}
	}

	if store != nil {
		if err := UpdateGeminiHealth(store, provider, profile, resp); err != nil {
			return fmt.Errorf("update health: %w", err)
		}
	}

	return nil
}

// getRefreshTokenFromJSON reads a JSON file and extracts the refresh_token field.
// Supports snake_case and camelCase.
func getRefreshTokenFromJSON(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", err
	}

	if val := readStringField(auth, "refresh_token", "refreshToken"); val != "" {
		return val, nil
	}

	if val := readNestedStringField(auth, "tokens", "refresh_token", "refreshToken"); val != "" {
		return val, nil
	}

	if val := readNestedStringField(auth, "claudeAiOauth", "refreshToken", "refresh_token"); val != "" {
		return val, nil
	}

	return "", fmt.Errorf("refresh_token not found in %s", path)
}

func readStringField(m map[string]interface{}, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, key := range keys {
		if val, ok := m[key].(string); ok && val != "" {
			return val
		}
	}
	return ""
}

func readNestedStringField(m map[string]interface{}, nestedKey string, keys ...string) string {
	if m == nil {
		return ""
	}
	raw, ok := m[nestedKey]
	if !ok {
		return ""
	}
	nested, ok := raw.(map[string]interface{})
	if !ok {
		return ""
	}
	return readStringField(nested, keys...)
}

func readAuthFiles(fileSet authfile.AuthFileSet) (map[string][]byte, error) {
	state := make(map[string][]byte)
	for _, spec := range fileSet.Files {
		// Only care about existing files
		if _, err := os.Stat(spec.Path); os.IsNotExist(err) {
			continue
		}
		data, err := os.ReadFile(spec.Path)
		if err != nil {
			return nil, err
		}
		state[spec.Path] = data
	}
	return state, nil
}

func filesEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !bytes.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

func snapshotMatchesProfile(fileSet authfile.AuthFileSet, vault *authfile.Vault, profile string, snapshot map[string][]byte) bool {
	if vault == nil || len(snapshot) == 0 {
		return false
	}

	for _, spec := range fileSet.Files {
		data, ok := snapshot[spec.Path]
		if !ok {
			continue
		}
		backupPath := vault.BackupPath(fileSet.Tool, profile, filepath.Base(spec.Path))
		backupData, err := os.ReadFile(backupPath)
		if err != nil {
			return false
		}
		if !bytes.Equal(data, backupData) {
			return false
		}
	}

	return true
}

// readLimitedBody reads up to maxErrorBodySize bytes from the reader.
// This prevents memory exhaustion from malicious or buggy servers that
// might return unexpectedly large error responses.
func readLimitedBody(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxErrorBodySize))
}
