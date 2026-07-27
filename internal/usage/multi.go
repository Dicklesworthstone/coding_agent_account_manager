package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
)

// ProfileUsage combines usage info with profile metadata.
type ProfileUsage struct {
	Provider    string     `json:"provider"`
	ProfileName string     `json:"profile_name"`
	Usage       *UsageInfo `json:"usage"`
	AccessToken string     `json:"-"` // Not serialized
}

// MultiProfileFetcher fetches usage data for multiple profiles concurrently.
type MultiProfileFetcher struct {
	claudeFetcher *ClaudeFetcher
	codexFetcher  *CodexFetcher
	logScanner    logs.Scanner // Optional scanner for burn rate calculation
}

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

			if info != nil {
				info.ProfileName = name

				// Calculate burn rate if scanner is available
				if m.logScanner != nil {
					// Scan logs for this provider.
					// Note: Currently logs.Scanner interface takes logDir.
					// We might need to know the specific log directory for the profile if isolated,
					// or use the provider's default log dir and filter by user?
					// For now, we'll scan the provider's default logs and we might need to filter by profile?
					//
					// CAAM usually runs one profile at a time in Vault mode, so the logs in ~/.local/share/claude/logs
					// belong to the *active* profile at that time. But historical logs might be mixed.
					//
					// However, typical CLI usage is sequential.
					// We'll scan the last 24 hours of logs.

					// Use a 24-hour window for burn rate calculation
					window := 24 * time.Hour
					since := time.Now().Add(-window)

					// We need to find the correct log directory.
					// For Vault mode, it's the standard provider log dir.
					// But MultiProfileFetcher doesn't know about file system paths easily.
					// We'll rely on the scanner's default behavior if logDir is empty.
					//
					// If using MultiScanner, we need to cast or select the right scanner.
					var scanner logs.Scanner
					if ms, ok := m.logScanner.(*logs.MultiScanner); ok {
						scanner = ms.Scanner(provider)
					} else {
						scanner = m.logScanner
					}

					if scanner != nil {
						scanRes, err := scanner.Scan(ctx, "", since)
						if err == nil && scanRes != nil {
							// Filter logs?
							// If we have profile-specific logs, great.
							// For now, assume all logs for the provider are relevant usage
							// (as we are usually checking *our* usage).
							//
							// TODO: If we want to be precise per-profile, we'd need logs to contain
							// some identity info, which they often don't.
							// But for "burn rate", recent usage is what matters.

							burnRate := CalculateBurnRate(scanRes.Entries, window, DefaultBurnRateOptions())
							if burnRate != nil {
								info.BurnRate = burnRate
								info.UpdateDepletion()
							}
						}
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

// GetBestProfile returns the profile with the highest availability score.
func (m *MultiProfileFetcher) GetBestProfile(ctx context.Context, provider string, profiles map[string]string) *ProfileUsage {
	results := m.FetchAllProfiles(ctx, provider, profiles)
	if len(results) == 0 {
		return nil
	}
	return &results[0]
}

// GetProfilesAboveThreshold returns profiles with usage below the threshold.
// threshold is the maximum utilization (e.g., 0.8 for 80%).
func (m *MultiProfileFetcher) GetProfilesAboveThreshold(ctx context.Context, provider string, profiles map[string]string, threshold float64) []ProfileUsage {
	results := m.FetchAllProfiles(ctx, provider, profiles)
	available := make([]ProfileUsage, 0)

	for _, p := range results {
		if p.Usage != nil && !p.Usage.IsNearLimit(threshold) {
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

// ReadCodexCredentials reads the access token from Codex auth file.
func ReadCodexCredentials(path string) (accessToken string, accountID string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	var creds struct {
		// Direct API key format
		OpenAIAPIKey string `json:"OPENAI_API_KEY"`

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

	// Fall back to OAuth tokens
	if creds.Tokens != nil && creds.Tokens.AccessToken != "" {
		return creds.Tokens.AccessToken, creds.Tokens.AccountID, nil
	}

	if creds.AccessToken != "" {
		return creds.AccessToken, creds.AccountID, nil
	}

	// API key auth does not support ChatGPT usage endpoints.
	if creds.OpenAIAPIKey != "" {
		return "", "", fmt.Errorf("API key auth does not support usage fetch")
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
			// Token profiles store the raw token directly.
			if tok, err := readTokenProfileFile(profileDir); err == nil {
				token, readErr = tok, nil
				break
			}
			// Try new location first
			credPath := filepath.Join(profileDir, ".credentials.json")
			token, _, readErr = ReadClaudeCredentials(credPath)
			if readErr != nil {
				// Fall back to old location
				oldPath := filepath.Join(profileDir, ".claude.json")
				token, _, readErr = ReadClaudeCredentials(oldPath)
				if readErr != nil {
					// Fall back to claude-code auth.json (optional file)
					authPath := filepath.Join(profileDir, "auth.json")
					token, _, readErr = ReadClaudeCredentials(authPath)
				}
			}
		case "codex":
			authPath := filepath.Join(profileDir, "auth.json")
			token, _, readErr = ReadCodexCredentials(authPath)
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

// readTokenProfileFile reads the raw token from a token profile directory in
// the vault (marked by a meta.json with type "token"). Returns an error for
// non-token profile directories so callers fall back to auth-file parsing.
func readTokenProfileFile(profileDir string) (string, error) {
	metaData, err := os.ReadFile(filepath.Join(profileDir, authfile.TokenMetaFileName))
	if err != nil {
		return "", err
	}
	var meta struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return "", fmt.Errorf("parse token meta: %w", err)
	}
	if meta.Type != authfile.ProfileTypeToken {
		return "", fmt.Errorf("not a token profile")
	}

	data, err := os.ReadFile(filepath.Join(profileDir, authfile.TokenFileName))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file is empty")
	}
	return token, nil
}
