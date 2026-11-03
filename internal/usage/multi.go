package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
)

// ProfileUsage combines usage info with profile metadata.
type ProfileUsage struct {
	Provider    string     `json:"provider"`
	ProfileName string     `json:"profile_name"`
	Usage       *UsageInfo `json:"usage"`
	AccessToken string     `json:"-"` // Not serialized

	// CredentialSource names which of caam's credential namespaces this row
	// was actually read from, and what else holds the same profile name.
	// One name can exist in the vault, in an isolated profile and in a
	// shallow profile at once, holding different (and differently fresh)
	// credentials; without this a caller cannot tell which copy produced the
	// verdict (issue #100). Populated for single-profile lookups.
	CredentialSource *CredentialSource `json:"credential_source,omitempty"`
}

// CredentialSource describes the credential namespace a row was read from.
type CredentialSource struct {
	// Namespace is "vault", "isolated" or "shallow".
	Namespace string `json:"namespace"`
	// Path is the credential file (or, for an offline read, the .claude.json)
	// that was actually opened.
	Path string `json:"path"`
	// State is caam's offline read of that credential: "healthy", "expired",
	// "unknown", or "missing".
	State string `json:"state"`
	// Explicit reports whether the caller named the namespace (--source)
	// rather than taking the default.
	Explicit bool `json:"explicit"`
	// Alternatives lists the other namespaces holding the same profile name.
	// Credentials are never copied between them implicitly.
	Alternatives []CredentialAlternative `json:"alternatives,omitempty"`
}

// CredentialAlternative is one namespace that also holds the profile name.
type CredentialAlternative struct {
	Namespace string `json:"namespace"`
	Path      string `json:"path"`
	State     string `json:"state"`
	// Healthier reports that this copy is in a strictly better state than the
	// one that was read.
	Healthier bool `json:"healthier"`
}

// MultiProfileFetcher fetches usage data for multiple profiles concurrently.
type MultiProfileFetcher struct {
	claudeFetcher *ClaudeFetcher
	codexFetcher  *CodexFetcher
	grokFetcher   *GrokFetcher
	cursorFetcher *CursorFetcher
	logScanner    logs.Scanner // Optional scanner for burn rate calculation
}

// burnRateWindow is how much recent log history feeds a profile's burn rate.
const burnRateWindow = 24 * time.Hour

// FetcherOption configures the MultiProfileFetcher.
type FetcherOption func(*MultiProfileFetcher)

// WithLogScanner sets the log scanner for burn rate calculation.
func WithLogScanner(scanner logs.Scanner) FetcherOption {
	return func(m *MultiProfileFetcher) {
		m.logScanner = scanner
	}
}

// NewMultiProfileFetcher creates a new multi-profile fetcher.
func NewMultiProfileFetcher(opts ...FetcherOption) *MultiProfileFetcher {
	m := &MultiProfileFetcher{
		claudeFetcher: NewClaudeFetcher(),
		codexFetcher:  NewCodexFetcher(),
		grokFetcher:   NewGrokFetcher(),
		cursorFetcher: NewCursorFetcher(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// FetchAllProfiles fetches usage for all profiles of a given provider.
// profiles is a map of profile name to access token.
func (m *MultiProfileFetcher) FetchAllProfiles(ctx context.Context, provider string, profiles map[string]string) []ProfileUsage {
	if m == nil {
		m = &MultiProfileFetcher{}
	}

	var wg sync.WaitGroup
	results := make([]ProfileUsage, 0, len(profiles))
	var mu sync.Mutex

	// The logs are the same for every profile, and a busy machine has many
	// megabytes of recent transcripts, so read them once per call rather than
	// once per profile.
	var burnOnce sync.Once
	var burnScan []*logs.LogEntry
	burnEntries := func() []*logs.LogEntry {
		burnOnce.Do(func() {
			scanner := m.logScanner
			if ms, ok := scanner.(*logs.MultiScanner); ok {
				scanner = ms.Scanner(provider)
			}
			if scanner == nil {
				return
			}
			if res, err := scanner.Scan(ctx, "", time.Now().Add(-burnRateWindow)); err == nil && res != nil {
				burnScan = res.Entries
			}
		})
		return burnScan
	}

	for name, token := range profiles {
		wg.Add(1)
		go func(name, token string) {
			defer wg.Done()

			var info *UsageInfo
			var err error

			switch provider {
			case "claude":
				if m.claudeFetcher == nil {
					info = &UsageInfo{
						Provider:  provider,
						FetchedAt: time.Now(),
						Error:     "claude fetcher unavailable",
					}
				} else {
					info, err = m.claudeFetcher.Fetch(ctx, token)
				}
			case "codex":
				if m.codexFetcher == nil {
					info = &UsageInfo{
						Provider:  provider,
						FetchedAt: time.Now(),
						Error:     "codex fetcher unavailable",
					}
				} else {
					info, err = m.codexFetcher.Fetch(ctx, token)
				}
			case "grok":
				if m.grokFetcher == nil {
					info = &UsageInfo{
						Provider:    provider,
						FetchedAt:   time.Now(),
						QuotaStatus: QuotaUnavailable,
						Error:       "grok fetcher unavailable",
					}
				} else {
					info, err = m.grokFetcher.Fetch(ctx, token)
				}
			case "cursor":
				if m.cursorFetcher == nil {
					info = &UsageInfo{
						Provider:    provider,
						FetchedAt:   time.Now(),
						QuotaStatus: QuotaUnavailable,
						Error:       "cursor fetcher unavailable",
					}
				} else {
					info, err = m.cursorFetcher.Fetch(ctx, token)
				}
			default:
				info = &UsageInfo{
					Provider:  provider,
					FetchedAt: time.Now(),
					Error:     fmt.Sprintf("unsupported provider: %s", provider),
				}
			}

			if info == nil {
				errMsg := "usage fetcher returned no data"
				if err != nil {
					errMsg = err.Error()
				}
				info = &UsageInfo{
					Provider:  provider,
					FetchedAt: time.Now(),
					Error:     errMsg,
				}
			} else if err != nil && info.Error == "" {
				info.Error = err.Error()
			}
			// Older consumers (including recommendations and forecasts) use
			// Error as their eligibility check. Keep the precise native status
			// and display metadata, but do not let an incomplete row appear
			// healthy to those consumers.
			if (provider == "grok" || provider == "cursor") && !info.NumericQuotaKnown() && info.Error == "" {
				info.Error = info.QuotaNote
				if info.Error == "" {
					info.Error = "numeric quota unavailable"
				}
			}

			if info != nil {
				info.ProfileName = name

				// Burn rate comes from the provider's local session logs. They
				// carry no account identity, so every profile of the provider
				// gets the machine's recent rate; see burnEntries.
				if entries := burnEntries(); len(entries) > 0 {
					if burnRate := CalculateBurnRate(entries, burnRateWindow, DefaultBurnRateOptions()); burnRate != nil {
						info.BurnRate = burnRate
						info.UpdateDepletion()
					}
				}
			}

			mu.Lock()
			results = append(results, ProfileUsage{
				Provider:    provider,
				ProfileName: name,
				Usage:       info,
				AccessToken: token,
			})
			mu.Unlock()
		}(name, token)
	}

	wg.Wait()

	// Sort by availability score (highest first)
	sort.Slice(results, func(i, j int) bool {
		scoreI := 0
		scoreJ := 0
		if results[i].Usage != nil {
			scoreI = results[i].Usage.AvailabilityScore()
		}
		if results[j].Usage != nil {
			scoreJ = results[j].Usage.AvailabilityScore()
		}
		if scoreI == scoreJ {
			return results[i].ProfileName < results[j].ProfileName
		}
		return scoreI > scoreJ
	})

	return results
}

// GetBestProfile returns the highest-scoring profile with usable quota below
// its limits, or nil when every fetch failed or every account is exhausted.
func (m *MultiProfileFetcher) GetBestProfile(ctx context.Context, provider string, profiles map[string]string) *ProfileUsage {
	results := m.GetProfilesAboveThreshold(ctx, provider, profiles, 1)
	if len(results) > 0 {
		return &results[0]
	}
	return nil
}

// GetProfilesAboveThreshold returns profiles with usage below the threshold.
// threshold is the maximum utilization (e.g., 0.8 for 80%).
func (m *MultiProfileFetcher) GetProfilesAboveThreshold(ctx context.Context, provider string, profiles map[string]string, threshold float64) []ProfileUsage {
	return profilesBelowThreshold(m.FetchAllProfiles(ctx, provider, profiles), threshold)
}

// A failed fetch is not an unused account. Apply the existing quota-validity
// contract to every provider, not only native quota sources. Preserve the
// fetcher's ranked order and diagnostic rows; selection must not rewrite them.
func profilesBelowThreshold(results []ProfileUsage, threshold float64) []ProfileUsage {
	available := make([]ProfileUsage, 0, len(results))
	for _, p := range results {
		if !p.Usage.NumericQuotaKnown() {
			continue
		}
		if !p.Usage.IsNearLimit(threshold) {
			available = append(available, p)
		}
	}

	return available
}

// ReadClaudeCredentials reads the access token from Claude credentials file.
func ReadClaudeCredentials(path string) (accessToken string, accountID string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	return parseClaudeAccessToken(data)
}

func parseClaudeAccessToken(data []byte) (accessToken string, accountID string, err error) {
	var creds struct {
		ClaudeAiOauth *struct {
			AccessToken string `json:"accessToken"`
			AccountID   string `json:"accountId"`
		} `json:"claudeAiOauth"`
		OAuthToken  json.RawMessage `json:"oauthToken"`
		AccessToken string          `json:"access_token"`
		AccessCamel string          `json:"accessToken"`
	}

	if err := json.Unmarshal(data, &creds); err != nil {
		return "", "", err
	}

	if creds.ClaudeAiOauth != nil {
		return creds.ClaudeAiOauth.AccessToken, creds.ClaudeAiOauth.AccountID, nil
	}

	if len(creds.OAuthToken) > 0 {
		var token string
		if err := json.Unmarshal(creds.OAuthToken, &token); err == nil && token != "" {
			return token, "", nil
		}

		var oauth struct {
			AccessToken string `json:"access_token"`
			AccessCamel string `json:"accessToken"`
		}
		if err := json.Unmarshal(creds.OAuthToken, &oauth); err == nil {
			if oauth.AccessToken != "" {
				return oauth.AccessToken, "", nil
			}
			if oauth.AccessCamel != "" {
				return oauth.AccessCamel, "", nil
			}
		}
	}
	if creds.AccessToken != "" {
		return creds.AccessToken, "", nil
	}
	if creds.AccessCamel != "" {
		return creds.AccessCamel, "", nil
	}

	return "", "", fmt.Errorf("no access token found in credentials")
}

// ClaudeProfileCredential describes the single saved authority selected for
// a quota read. Expiry belongs to the same captured bytes as the access token;
// descriptive account fields are not proof of credential ownership.
type ClaudeProfileCredential struct {
	Path        string
	AccessToken string `json:"-"`
	AccountID   string
	ExpiresAt   time.Time
	Renewable   bool
}

// ReadClaudeProfileCredentials follows the saved Claude authority used by
// activation: modern credentials, auth.json, then legacy state. Only a genuinely
// absent primary permits another source. Settings must also permit subscription
// OAuth, even when a primary exists: activation preserves their auth overrides.
// Policy-only settings do not select a login. No live files, keychain, or
// credential helpers are consulted or changed.
//
// A present invalid source retains its path in the result. os.ErrNotExist with
// an empty result means that no supported credential authority was present.
func ReadClaudeProfileCredentials(profileDir string) (ClaudeProfileCredential, error) {
	settingsPath := filepath.Join(profileDir, "settings.json")
	if data, present, err := readClaudeProfileFile(settingsPath); present {
		selected := ClaudeProfileCredential{Path: settingsPath}
		if err != nil {
			return selected, fmt.Errorf("read Claude settings: %w", err)
		}
		if err := authfile.ValidateClaudeOAuthSettings(data); err != nil {
			return selected, fmt.Errorf("Claude settings do not select an unambiguous subscription OAuth login: %w", err)
		}
	}
	// Legacy state also carries settings that activation preserves. Capture it
	// once so its selectors are checked even when a modern credential wins.
	legacyPath := filepath.Join(profileDir, ".claude.json")
	legacyData, legacyPresent, err := readClaudeProfileFile(legacyPath)
	if legacyPresent {
		selected := ClaudeProfileCredential{Path: legacyPath}
		if err != nil {
			return selected, fmt.Errorf("read Claude legacy state: %w", err)
		}
		if err := authfile.ValidateClaudeOAuthState(legacyData); err != nil {
			return selected, fmt.Errorf("Claude legacy state does not select an unambiguous subscription OAuth login: %w", err)
		}
	}
	for _, name := range []string{".credentials.json", "auth.json", ".claude.json"} {
		path := filepath.Join(profileDir, name)
		data, present, err := legacyData, legacyPresent, error(nil)
		if name != ".claude.json" {
			data, present, err = readClaudeProfileFile(path)
		}
		if !present {
			continue
		}
		selected := ClaudeProfileCredential{Path: path}
		if err != nil {
			return selected, fmt.Errorf("read selected Claude %s: %w", name, err)
		}
		if name != ".claude.json" {
			material, err := authfile.ClaudeCredentialMaterial(data, name)
			if err != nil {
				return selected, fmt.Errorf("selected Claude %s has invalid authentication data", name)
			}
			if !material {
				return selected, fmt.Errorf("selected Claude %s has no supported access credential", name)
			}
		}
		token, accountID, err := parseSelectedClaudeAccessToken(data, name)
		if name == ".claude.json" && errors.Is(err, authfile.ErrNoCredentials) {
			continue // Account labels and cached usage alone do not select a login.
		}
		if err != nil || strings.TrimSpace(token) == "" {
			return selected, fmt.Errorf("selected Claude %s has no valid subscription access token", name)
		}
		selected.AccessToken, selected.AccountID = token, accountID
		if expiry, err := health.ParseClaudeCredentialExpiry(path, data); err == nil && expiry != nil {
			selected.ExpiresAt, selected.Renewable = expiry.ExpiresAt, expiry.Renewable
		}
		return selected, nil
	}
	return ClaudeProfileCredential{}, os.ErrNotExist
}

// Select within the document by its supported native layout, not by whichever
// token-shaped field a generic decoder happens to prefer. Expiry parsing uses
// the same grant selection in health.ParseClaudeCredentialExpiry.
func parseSelectedClaudeAccessToken(data []byte, name string) (string, string, error) {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		return "", "", authfile.ErrInvalidCredentials
	}
	// API keys and session keys do not prove that an accompanying OAuth cache
	// is the selected subscription login. Refuse the ambiguous combination.
	if name != ".credentials.json" {
		for _, key := range []string{"apiKey", "api_key", "primaryApiKey", "sessionKey", "apiKeyHelper"} {
			if raw, present := root[key]; present {
				var value string
				if json.Unmarshal(raw, &value) != nil || string(raw) == "null" || strings.TrimSpace(value) != "" {
					return "", "", authfile.ErrInvalidCredentials
				}
			}
		}
	}
	grant := data
	switch name {
	case ".credentials.json":
		var oauth struct {
			AccessToken string `json:"accessToken"`
			AccountID   string `json:"accountId"`
		}
		if json.Unmarshal(root["claudeAiOauth"], &oauth) != nil {
			return "", "", authfile.ErrInvalidCredentials
		}
		return oauth.AccessToken, oauth.AccountID, nil
	case ".claude.json":
		var present bool
		grant, present = root["oauthToken"]
		if !present {
			material, err := authfile.ClaudeCredentialMaterial(data, name)
			if err != nil || material {
				return "", "", authfile.ErrInvalidCredentials
			}
			return "", "", authfile.ErrNoCredentials
		}
		var token string
		if json.Unmarshal(grant, &token) == nil {
			return token, "", nil
		}
	}
	var oauth struct {
		AccessToken string `json:"access_token"`
		AccessCamel string `json:"accessToken"`
	}
	if json.Unmarshal(grant, &oauth) != nil {
		return "", "", authfile.ErrInvalidCredentials
	}
	if strings.TrimSpace(oauth.AccessToken) != "" {
		return oauth.AccessToken, "", nil
	}
	return oauth.AccessCamel, "", nil
}

// Probe existence separately from reading so dangling links, nonregular files,
// denied reads, and a disappearing selected file never enable a legacy login.
func readClaudeProfileFile(path string) ([]byte, bool, error) {
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if !before.Mode().IsRegular() {
		return nil, true, fmt.Errorf("credential must be a regular file")
	}
	limit := authfile.CredentialFileSizeLimit("claude", filepath.Base(path))
	if before.Size() > limit {
		return nil, true, fmt.Errorf("credential exceeds its size limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, true, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, true, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, true, fmt.Errorf("credential changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, true, err
	}
	if int64(len(data)) > limit {
		return nil, true, fmt.Errorf("credential exceeds its size limit")
	}
	return data, true, nil
}

// ReadCodexCredentials reads the selected OAuth access token from Codex auth.
// API-key logins cannot supply subscription quota, even if an older OAuth
// cache remains in the file. Share the selector used by restore and health.
func ReadCodexCredentials(path string) (accessToken string, accountID string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	if _, selectedKey, err := authfile.CodexSelectedAPIKey(data); err != nil {
		return "", "", fmt.Errorf("invalid Codex credential selection")
	} else if selectedKey {
		return "", "", fmt.Errorf("API key auth does not support usage fetch")
	}

	var creds struct {
		// OAuth token format
		Tokens *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`

		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	}

	if err := json.Unmarshal(data, &creds); err != nil {
		return "", "", err
	}

	// Preserve the native OAuth formats and their nested-token precedence.
	if creds.Tokens != nil && creds.Tokens.AccessToken != "" {
		return creds.Tokens.AccessToken, creds.Tokens.AccountID, nil
	}

	if creds.AccessToken != "" {
		return creds.AccessToken, creds.AccountID, nil
	}

	return "", "", fmt.Errorf("no access token found in credentials")
}

// LoadProfileCredentials loads credentials for all profiles of a provider from the vault.
func LoadProfileCredentials(vaultDir, provider string) (map[string]string, error) {
	providerDir := filepath.Join(vaultDir, provider)
	entries, err := os.ReadDir(providerDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No profiles
		}
		return nil, err
	}

	credentials := make(map[string]string)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		profileName := entry.Name()
		profileDir := filepath.Join(providerDir, profileName)

		var token string
		var readErr error

		switch provider {
		case "claude":
			selected, err := ReadClaudeProfileCredentials(profileDir)
			token, readErr = selected.AccessToken, err
		case "codex":
			authPath := filepath.Join(profileDir, "auth.json")
			token, _, readErr = ReadCodexCredentials(authPath)
		case "grok", "cursor":
			authPath := filepath.Join(profileDir, "auth.json")
			token, readErr = NativeCredentialLocator(provider, authPath)
		}

		if readErr != nil {
			continue // Skip profiles with invalid credentials
		}

		if token != "" {
			credentials[profileName] = token
		}
	}

	return credentials, nil
}
