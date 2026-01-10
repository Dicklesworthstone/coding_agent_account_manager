package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
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
}

// NewMultiProfileFetcher creates a new multi-profile fetcher.
func NewMultiProfileFetcher() *MultiProfileFetcher {
	return &MultiProfileFetcher{
		claudeFetcher: NewClaudeFetcher(),
		codexFetcher:  NewCodexFetcher(),
	}
}

// FetchAllProfiles fetches usage for all profiles of a given provider.
// profiles is a map of profile name to access token.
func (m *MultiProfileFetcher) FetchAllProfiles(ctx context.Context, provider string, profiles map[string]string) []ProfileUsage {
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
				info, err = m.claudeFetcher.Fetch(ctx, token)
			case "codex":
				info, err = m.codexFetcher.Fetch(ctx, token)
			default:
				info = &UsageInfo{
					Provider:  provider,
					FetchedAt: time.Now(),
					Error:     fmt.Sprintf("unsupported provider: %s", provider),
				}
			}

			if err != nil && info == nil {
				info = &UsageInfo{
					Provider:  provider,
					FetchedAt: time.Now(),
					Error:     err.Error(),
				}
			}

			if info != nil {
				info.ProfileName = name
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
		scoreI := results[i].Usage.AvailabilityScore()
		scoreJ := results[j].Usage.AvailabilityScore()
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
	}

	if err := json.Unmarshal(data, &creds); err != nil {
		return "", "", err
	}

	if creds.ClaudeAiOauth == nil {
		return "", "", fmt.Errorf("claudeAiOauth not found in credentials")
	}

	return creds.ClaudeAiOauth.AccessToken, creds.ClaudeAiOauth.AccountID, nil
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
	}

	if err := json.Unmarshal(data, &creds); err != nil {
		return "", "", err
	}

	// Prefer API key if present
	if creds.OpenAIAPIKey != "" {
		return creds.OpenAIAPIKey, "", nil
	}

	// Fall back to OAuth tokens
	if creds.Tokens != nil && creds.Tokens.AccessToken != "" {
		return creds.Tokens.AccessToken, creds.Tokens.AccountID, nil
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
			// Try new location first
			credPath := filepath.Join(profileDir, ".credentials.json")
			token, _, readErr = ReadClaudeCredentials(credPath)
			if readErr != nil {
				// Fall back to old location
				oldPath := filepath.Join(profileDir, ".claude.json")
				token, _, readErr = ReadClaudeCredentials(oldPath)
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
