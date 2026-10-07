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
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	profilepkg "github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

// maxErrorBodySize limits how much of an error response body we read.
// Prevents memory exhaustion from malicious/buggy servers.
const maxErrorBodySize = 64 * 1024 // 64KB

// DefaultRefreshThreshold is the time before expiry to trigger a refresh.
const DefaultRefreshThreshold = 10 * time.Minute

const claudeRefreshDisabledReason = "token refresh disabled; Claude Code handles refresh internally. Use /login to re-authenticate."

// ShouldRefresh determines if a profile needs refreshing.
func ShouldRefresh(h *health.ProfileHealth, threshold time.Duration) bool {
	if h == nil || h.TokenExpiresAt.IsZero() {
		return false // Unknown expiry, do not assume refresh needed (avoid loops)
	}
	if h.SelfRefreshing || (!h.CredentialRenewable() && h.ReloginWarningLead > 0) {
		// Cursor API keys and Claude renew through their own CLI. A Cursor
		// session has a hard login deadline and cannot be refreshed at all.
		// Activation must not announce or attempt a refresh for either kind.
		return false
	}

	if threshold == 0 {
		threshold = DefaultRefreshThreshold
	}

	ttl := time.Until(h.TokenExpiresAt)
	return ttl < threshold && (ttl > 0 || h.TokenRenewable) && !h.ProviderRejected()
}

// Preflight checks whether CAAM can safely attempt a profile refresh using the
// current credentials. It only reads files; it does not migrate, synchronize,
// or refresh them, and does not update health metadata. Callers may use it
// before announcing an attempt, but RefreshProfile always rechecks because a
// native CLI can rotate credentials between daemon ticks or UI actions.
func Preflight(provider, profile string, vault *authfile.Vault) error {
	switch provider {
	case "claude":
		return &UnsupportedError{Provider: provider, Reason: claudeRefreshDisabledReason}
	case "grok":
		return &UnsupportedError{Provider: provider, Reason: "Grok Build handles token renewal; CAAM does not refresh Grok credentials"}
	case "opencode":
		return &UnsupportedError{Provider: provider, Reason: "CAAM does not refresh OpenCode credentials; use OpenCode to authenticate"}
	case "codex", "gemini", "cursor":
		if vault == nil {
			return fmt.Errorf("profile vault is required for %s refresh", provider)
		}
	default:
		return &UnsupportedError{Provider: provider, Reason: "provider not supported"}
	}

	vaultPath := vault.ProfilePath(provider, profile)
	switch provider {
	case "codex":
		if _, err := refreshSourcePath(provider, vaultPath); err != nil {
			return err
		}
		for _, spec := range authfile.CodexAuthFiles().Files {
			if authfile.CodexLiveIsNewer(spec.Path, filepath.Join(vaultPath, filepath.Base(spec.Path))) {
				return &StaleCredentialError{Provider: provider, Profile: profile}
			}
		}
	case "gemini":
		_, _, err := readGeminiADC(vaultPath)
		return err
	case "cursor":
		info, err := health.ParseCursorExpiry(filepath.Join(vaultPath, "auth.json"))
		if err == nil && info.SelfRefreshing {
			return &UnsupportedError{Provider: provider, Reason: "cursor-agent renews tokens from the stored API key automatically"}
		}
		return &UnsupportedError{Provider: provider, Reason: "Cursor session logins cannot refresh. " + health.CursorReloginInstructions(profile)}
	}
	return nil
}

// RefreshProfile orchestrates the refresh for a specific provider/profile.
func RefreshProfile(ctx context.Context, provider, profile string, vault *authfile.Vault, store *health.Storage, opts ...RefreshOption) error {
	if err := Preflight(provider, profile, vault); err != nil {
		return err
	}
	if store != nil {
		store.SetVaultPath(vault.BasePath())
	}

	options := refreshOptions{profiles: profilepkg.NewStore(profilepkg.DefaultStorePath())}
	for _, option := range opts {
		option(&options)
	}
	vaultPath := vault.ProfilePath(provider, profile)
	path, err := refreshSourcePath(provider, vaultPath)
	if err != nil {
		return err
	}
	source, err := readCredentialSnapshot(path)
	if err != nil {
		return err
	}
	source.provider = provider
	deliveries := captureRefreshDeliveries(provider, profile, source, options)
	release, err := acquireRefreshLocks(ctx, source, deliveries)
	if err != nil {
		return err
	}
	defer release()
	// Another CAAM refresh may have completed while this invocation waited.
	// It must not exchange either the spent token or the newly rotated token.
	if err := source.unchanged(); err != nil {
		return err
	}
	if err := Preflight(provider, profile, vault); err != nil {
		return err
	}
	if err := checkRefreshSource(provider, vaultPath, source); err != nil {
		return err
	}
	for _, delivery := range deliveries {
		if err := delivery.source.unchanged(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Both provider verdicts and response merging use these exact request
	// bytes. A later login at the same pathname is never their subject.
	fingerprint := health.CodexCredentialFingerprint(source.data)
	build, err := exchangeRefresh(ctx, provider, source.data)
	if changed := source.unchanged(); changed != nil {
		return changed
	}
	if changed := checkRefreshSource(provider, vaultPath, source); changed != nil {
		return changed
	}
	if err != nil {
		// A refusal by the token endpoint is the provider saying this
		// credential is dead. Record it so ls/status stop calling the
		// profile healthy (issue #108).
		var rejected *RefreshRejectedError
		reason := "exchange_failed"
		switch {
		case errors.Is(err, ErrRefreshTokenReused):
			reason = "refresh_token_reused"
			recordProviderVerdict(store, provider, profile, false, reason, fingerprint)
		case errors.As(err, &rejected):
			reason = rejected.Reason()
			recordProviderVerdict(store, provider, profile, false, reason, fingerprint)
		}
		options.recordActivity(caamdb.Event{
			Type:        caamdb.EventError,
			Provider:    provider,
			ProfileName: profile,
			Details:     map[string]any{"operation": "refresh", "reason": reason},
		})
		// Wrap refresh_token_reused with profile context for actionable error messages.
		if errors.Is(err, ErrRefreshTokenReused) {
			return &RefreshTokenReusedError{Provider: provider, Profile: profile}
		}
		return err
	}

	updated, err := build(source.data)
	if err != nil {
		return err
	}
	if err := source.publish(updated); err != nil {
		return err
	}
	recordProviderVerdict(store, provider, profile, true, "", health.CodexCredentialFingerprint(updated))
	var skipped []string
	for _, delivery := range deliveries {
		// Keep destination-local metadata and settings rather than restoring
		// the vault's entire fileset. Only its captured token generation changes.
		data, buildErr := build(delivery.source.data)
		if buildErr != nil || delivery.source.publish(data) != nil {
			skipped = append(skipped, delivery.label)
		}
	}
	details := map[string]any{"operation": "refresh"}
	if len(skipped) > 0 {
		details["undelivered"] = skipped
	}
	options.recordActivity(caamdb.Event{
		Type:        caamdb.EventRefresh,
		Provider:    provider,
		ProfileName: profile,
		Details:     details,
	})
	if len(skipped) > 0 {
		return &DeliveryError{Destinations: skipped}
	}
	return nil
}

// recordProviderVerdict stores a provider's answer about a profile's
// credential. Best-effort: health metadata must never fail a refresh.
func recordProviderVerdict(store *health.Storage, provider, profile string, accepted bool, reason, fingerprint string) {
	if store == nil {
		return
	}
	_ = store.RecordProviderVerification(provider, profile, health.ProviderVerification{
		Accepted:    accepted,
		Reason:      reason,
		Fingerprint: fingerprint,
	})
}

func refreshClaude(ctx context.Context, vaultPath string) error {
	// DISABLED: Claude token refresh is not supported.
	//
	// The OAuth refresh endpoint (api.anthropic.com/oauth/token) is undocumented
	// and speculative. Claude Code handles token refresh internally, and attempting
	// to refresh tokens externally may cause auth corruption or undefined behavior.
	//
	// Users should re-authenticate via the /login command when tokens expire.
	//
	// See: docs/CLAUDE_AUTH_INVENTORY.md (CLAUDE-006)
	return &UnsupportedError{
		Provider: "claude",
		Reason:   claudeRefreshDisabledReason,
	}
}

func refreshSourcePath(provider, vaultPath string) (string, error) {
	if provider == "codex" {
		path := filepath.Join(vaultPath, "auth.json")
		source, err := readCredentialSnapshot(path)
		if err != nil {
			return "", err
		}
		selected, err := health.CodexUsesAPIKey(source.data)
		if err != nil {
			return "", err
		}
		if selected {
			return "", &UnsupportedError{Provider: provider, Reason: "the selected API key does not use OAuth token refresh"}
		}
		return path, nil
	}
	_, path, err := readGeminiADC(vaultPath)
	return path, err
}

func checkRefreshSource(provider, vaultPath string, source *credentialSnapshot) error {
	path, err := refreshSourcePath(provider, vaultPath)
	if err != nil {
		return ErrCredentialChanged
	}
	abs, err := filepath.Abs(path)
	if err != nil || abs != source.path {
		return ErrCredentialChanged
	}
	return nil
}

func exchangeRefresh(ctx context.Context, provider string, data []byte) (func([]byte) ([]byte, error), error) {
	if provider == "codex" {
		token, err := codexRefreshToken(data)
		if err != nil {
			return nil, err
		}
		response, err := RefreshCodexToken(ctx, token)
		return func(before []byte) ([]byte, error) { return updatedCodexAuth(before, response) }, err
	}
	adc, err := parseADC(data)
	if err != nil {
		return nil, err
	}
	response, err := RefreshGeminiToken(ctx, adc.ClientID, adc.ClientSecret, adc.RefreshToken)
	return func(before []byte) ([]byte, error) { return updatedGeminiAuth(before, response, true) }, err
}

// Codex's nested store is authoritative when present. A leftover flat token
// must not refresh one grant and then be merged into another nested grant.
func codexRefreshToken(data []byte) (string, error) {
	var auth map[string]json.RawMessage
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", fmt.Errorf("invalid Codex credential object")
	}
	if nested, exists := auth["tokens"]; exists {
		var tokens struct {
			Refresh string `json:"refresh_token"`
		}
		if json.Unmarshal(nested, &tokens) != nil || tokens.Refresh == "" {
			return "", fmt.Errorf("nested Codex credential has no refresh token")
		}
		return tokens.Refresh, nil
	}
	return refreshTokenFromJSON(data)
}

// readGeminiADC uses the same credential precedence before and after migration.
// Reading the legacy filename when the current one is absent keeps Preflight
// side-effect-free without rejecting a profile that refreshGemini can migrate.
func readGeminiADC(vaultPath string) (*ADC, string, error) {
	settings, err := readCredentialSnapshot(filepath.Join(vaultPath, "settings.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("read Gemini auth selection: %w", err)
	}
	if settings != nil {
		selected, err := authfile.GeminiSelectedAuthType(settings.data)
		if err != nil {
			return nil, "", fmt.Errorf("read Gemini auth selection: %w", err)
		}
		if selected == "gemini-api-key" || selected == "vertex-ai" {
			return nil, "", &UnsupportedError{Provider: "gemini", Reason: "the selected authentication method does not use the saved OAuth cache"}
		}
	}
	for _, name := range []string{"oauth_creds.json", "oauth_credentials.json", "settings.json"} {
		candidate := filepath.Join(vaultPath, name)
		fi, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			fi, err = os.Stat(candidate)
		}
		if err != nil {
			return nil, "", fmt.Errorf("stat oauth credentials: %w", err)
		}
		if !fi.Mode().IsRegular() {
			return nil, "", fmt.Errorf("oauth credential source is not a regular file")
		}
		adc, err := ReadADC(candidate)
		if err == nil {
			return adc, candidate, nil
		}
		if errors.Is(err, ErrADCIncomplete) {
			break
		}
		return nil, "", fmt.Errorf("read oauth credentials: %w", err)
	}
	return nil, "", &UnsupportedError{Provider: "gemini", Reason: "missing oauth client credentials (expected oauth_creds.json with client_id/client_secret/refresh_token)"}
}

// getRefreshTokenFromJSON reads a JSON file and extracts the refresh_token field.
// Supports snake_case and camelCase.
func getRefreshTokenFromJSON(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	return refreshTokenFromJSON(data)
}

func refreshTokenFromJSON(data []byte) (string, error) {
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

	return "", fmt.Errorf("refresh_token not found in credential")
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
