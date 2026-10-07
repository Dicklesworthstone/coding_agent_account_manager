// Package health provides token expiry parsing for all supported providers.
//
// Each provider stores OAuth tokens differently. This file contains parsers
// that extract token expiration times from auth files.
package health

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

// ErrNoExpiry indicates that expiry information could not be determined.
var ErrNoExpiry = errors.New("expiry not found in auth file")

// ErrNoAuthFile indicates that the auth file does not exist.
var ErrNoAuthFile = errors.New("auth file not found")

// CursorReloginLead gives operators time to replace a non-renewable Cursor
// session login before its hard deadline interrupts unattended work.
const CursorReloginLead = 7 * 24 * time.Hour

// ExpiryInfo contains parsed token expiry information.
type ExpiryInfo struct {
	// ExpiresAt is when the token expires.
	ExpiresAt time.Time

	// HasRefreshToken indicates that a usable refresh token is available.
	// Cursor leaves it false: its refreshToken field cannot renew the login.
	HasRefreshToken bool

	// SelfRefreshing reports that the provider's own CLI renews this access
	// token in place from the refresh token stored beside it, and that caam
	// neither can nor should refresh it. Claude Code works this way (see
	// refresh.ClaudeRefreshDisabled): its access tokens live only a few
	// hours and are renewed on next use, so a short TTL is routine lifecycle
	// rather than a fault to warn about (PR #84).
	//
	// It answers only "must caam stay out of the way?". Cursor API-key logins
	// also qualify: cursor-agent re-mints their tokens from the stored API
	// key. Whether a lapsed token needs a human is the separate Renewable
	// question below.
	SelfRefreshing bool

	// Renewable reports that an expired or expiring access token here can be
	// renewed WITHOUT a human re-authenticating: a refresh token is stored
	// beside it, or the provider's CLI renews the credential in place.
	//
	// This is deliberately distinct from SelfRefreshing (issue #102). The two
	// answer different questions and Codex answers them differently:
	//
	//   - "Does this credential need a refresh soon?" — SelfRefreshing says
	//     caam must not act. For Codex the answer is yes: caam
	//     has a Codex refresher and a pool refresher that both run off the
	//     expiry signal, so the warning must survive.
	//   - "Is this account unusable until someone logs in again?" — Renewable
	//     says no. A Codex profile whose access token lapsed but whose refresh
	//     token is present is live; the CLI renews it on next use. Reporting
	//     it as expired made healthy profiles look dead in `caam ls` and had
	//     controllers route around working accounts.
	//
	// OAuth providers set it from HasRefreshToken. Cursor sets it only from
	// a stored apiKey, never from its misleading refreshToken field. A
	// self-refreshing credential is renewable by construction.
	Renewable bool

	// ReloginWarningLead widens the warning window for credentials that need a human
	// login to replace them. It is derived from the credential at report time,
	// never persisted, and ignored for renewable credentials.
	ReloginWarningLead time.Duration `json:"-"`

	// Fingerprint identifies the credential that was parsed (see
	// CodexCredentialFingerprint). Cursor uses it to warn once per login.
	// It is derived from credential material, never unrelated configuration.
	Fingerprint string

	// Source describes where the expiry was parsed from.
	Source string
}

// ParseLiveExpiry reads only the credential captured from the supplied native
// file set. It never creates a keychain mirror or borrows a saved profile,
// isolated profile, or ambient API key. Unknown expiry remains unknown; a
// readable grant still carries its fingerprint for provider-verdict matching.
func ParseLiveExpiry(fileSet authfile.AuthFileSet) (*ExpiryInfo, error) {
	name, data, err := authfile.ReadLiveCredential(fileSet)
	if err != nil {
		if errors.Is(err, authfile.ErrNoCredentials) {
			return nil, ErrNoAuthFile
		}
		return nil, err
	}
	if fileSet.Tool == "claude" || fileSet.Tool == "codex" || fileSet.Tool == "cursor" || fileSet.Tool == "grok" || (fileSet.Tool == "gemini" && name != ".env") {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil || object == nil {
			return nil, fmt.Errorf("%w: live %s credential must be a JSON object", authfile.ErrInvalidCredentials, fileSet.Tool)
		}
	}
	var info *ExpiryInfo
	switch fileSet.Tool {
	case "claude":
		if name == ".credentials.json" {
			info, err = parseClaudeCredentialsJSON(data)
		} else {
			info, err = parseLiveOAuthJSON(data)
		}
	case "codex":
		if key, selected, modeErr := codexSelectedAPIKey(data); modeErr != nil {
			return nil, modeErr
		} else if selected {
			info = &ExpiryInfo{Renewable: true, SelfRefreshing: true, Fingerprint: credentialFingerprint("codex-api-key\x00" + key)}
			break
		}
		info, err = parseCodexAuthJSON(data)
		fingerprint := CodexCredentialFingerprint(data)
		if errors.Is(err, ErrNoExpiry) && fingerprint != "" {
			info, err = &ExpiryInfo{}, nil
		}
		if info != nil {
			info.Fingerprint = fingerprint
		}
	case "cursor":
		if name != "auth.json" {
			// Metadata-only native-keychain logins have no readable grant.
			if _, err := authfile.CursorConfigAuthInfo(data); err != nil {
				return nil, err
			}
			return nil, ErrNoExpiry
		}
		if ok, shapeErr := authfile.CursorCredentialMaterial(data); shapeErr != nil || !ok {
			return nil, fmt.Errorf("%w: Cursor auth has no valid accessToken or apiKey", authfile.ErrInvalidCredentials)
		}
		info, err = parseCursorAuthJSON(data)
	case "gemini":
		if name == "settings.json" {
			var selected struct {
				Legacy   string `json:"selectedAuthType"`
				Security struct {
					Auth struct {
						Type string `json:"selectedType"`
					} `json:"auth"`
				} `json:"security"`
			}
			if err := json.Unmarshal(data, &selected); err != nil {
				return nil, err
			}
			if selected.Legacy == "vertex-ai" || selected.Security.Auth.Type == "vertex-ai" {
				return nil, ErrNoExpiry
			}
		}
		if name == ".env" {
			info, err = parseLiveGeminiAPIKey(data)
		} else {
			info, err = parseLiveOAuthJSON(data)
		}
	case "grok":
		info, err = parseLiveGrokJSON(data)
	default:
		return nil, ErrNoExpiry
	}
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, ErrNoExpiry
	}
	if fileSet.Tool != "cursor" {
		info.Renewable = info.Renewable || info.HasRefreshToken
		info.SelfRefreshing = info.SelfRefreshing || (fileSet.Tool == "claude" && info.HasRefreshToken)
	}
	info.Source = name
	for _, spec := range fileSet.Files {
		if filepath.Base(spec.Path) == name {
			info.Source = spec.Path
			break
		}
	}
	return info, nil
}

// Parse only the captured .env, following Gemini's stored-key syntax and mode
// conflicts. Ambient keys and unrelated dotenv entries cannot identify a grant.
func parseLiveGeminiAPIKey(data []byte) (*ExpiryInfo, error) {
	values := make(map[string]string)
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || strings.ContainsAny(key, " \t\r\x00") {
			return nil, fmt.Errorf("%w: expected a Gemini dotenv assignment", authfile.ErrInvalidCredentials)
		}
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			quote := value[0]
			end := strings.IndexByte(value[1:], quote)
			if end < 0 {
				return nil, fmt.Errorf("%w: unterminated Gemini dotenv value", authfile.ErrInvalidCredentials)
			}
			end++
			tail := strings.TrimSpace(value[end+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return nil, fmt.Errorf("%w: unexpected text after Gemini dotenv value", authfile.ErrInvalidCredentials)
			}
			value = value[1:end]
		} else if comment := strings.IndexByte(value, '#'); comment >= 0 {
			value = strings.TrimSpace(value[:comment])
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("%w: invalid Gemini dotenv value", authfile.ErrInvalidCredentials)
		}
		if key == "GEMINI_API_KEY" && (strings.TrimSpace(value) == "" || strings.ContainsAny(value, " \t")) {
			return nil, fmt.Errorf("%w: Gemini API key is empty or contains whitespace", authfile.ErrInvalidCredentials)
		}
		values[key] = value
	}
	key := values["GEMINI_API_KEY"]
	if key == "" || strings.ContainsAny(key, " \t") || values["GOOGLE_API_KEY"] != "" || values["GOOGLE_GENAI_USE_GCA"] == "true" || values["GOOGLE_GENAI_USE_VERTEXAI"] == "true" {
		return nil, fmt.Errorf("%w: selected Gemini API key is absent or conflicts with another auth method", authfile.ErrInvalidCredentials)
	}
	return &ExpiryInfo{Renewable: true, SelfRefreshing: true, Fingerprint: credentialFingerprint(key)}, nil
}

// Preserve opaque access-token fingerprints without changing the saved-file
// parsers' existing ErrNoExpiry contract.
func parseLiveOAuthJSON(data []byte) (*ExpiryInfo, error) {
	info, err := parseOAuthJSON(data)
	if !errors.Is(err, ErrNoExpiry) {
		return info, err
	}
	var oauth oauthJSON
	if err := json.Unmarshal(data, &oauth); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	fingerprint := credentialFingerprint(oauth.RefreshToken, oauth.RefreshTokenCamel, oauth.AccessToken, oauth.AccessTokenCamel, oauth.Key)
	if fingerprint == "" {
		return nil, ErrNoExpiry
	}
	return &ExpiryInfo{Fingerprint: fingerprint}, nil
}

func parseLiveGrokJSON(data []byte) (*ExpiryInfo, error) {
	if info, err := parseGrokAuthJSON(data); !errors.Is(err, ErrNoExpiry) {
		return info, err
	}
	if info, err := parseLiveOAuthJSON(data); err == nil {
		return info, nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		info, err := parseLiveOAuthJSON(entries[key])
		if err == nil {
			return info, nil
		}
		if !errors.Is(err, ErrNoExpiry) {
			return nil, err
		}
	}
	return nil, ErrNoExpiry
}

// ParseClaudeExpiry extracts token expiry from Claude Code auth files.
//
// Claude Code stores OAuth credentials in:
//   - ~/.claude/.credentials.json (primary - contains claudeAiOauth object)
//
// The credentials file structure:
//
//	{
//	  "claudeAiOauth": {
//	    "accessToken": "...",
//	    "refreshToken": "...",
//	    "expiresAt": 1768042451877,  // Unix milliseconds
//	    "rateLimitTier": "default_claude_max_20x",
//	    "subscriptionType": "max",
//	    "scopes": [...]
//	  }
//	}
func ParseClaudeExpiry(authDir string) (*ExpiryInfo, error) {
	info, err := parseClaudeExpiryFiles(authDir)
	if err != nil {
		return nil, err
	}
	// Claude Code renews its own access token from the refresh token, and
	// caam's Claude refresh is disabled, so a credential that carries a
	// refresh token is self-refreshing — and therefore also renewable.
	info.SelfRefreshing = info.HasRefreshToken
	info.Renewable = info.HasRefreshToken
	return info, nil
}

// parseClaudeExpiryFiles locates and parses the Claude credential file for
// authDir ("" means the live locations under HOME / CLAUDE_CONFIG_DIR).
func parseClaudeExpiryFiles(authDir string) (*ExpiryInfo, error) {
	homeDir, _ := os.UserHomeDir()

	if authDir == "" {
		var (
			info *ExpiryInfo
			err  error
		)

		var authCandidates []string
		if cfg := os.Getenv("CLAUDE_CONFIG_DIR"); cfg != "" {
			authCandidates = append(authCandidates, filepath.Join(cfg, "auth.json"))
		}

		xdgConfig := os.Getenv("XDG_CONFIG_HOME")
		if xdgConfig == "" {
			xdgConfig = filepath.Join(homeDir, ".config")
		}
		authCandidates = append(authCandidates, filepath.Join(xdgConfig, "claude-code", "auth.json"))

		for _, candidate := range authCandidates {
			info, err = parseOAuthFile(candidate)
			if err == nil {
				info.Source = candidate
				return info, nil
			}
		}

		// System state probing - check the actual credentials file location.
		// On macOS the live token is in the login keychain and this file is
		// its mirror, so refresh it first or expiry reads as unknown (#98).
		credentialsPath := filepath.Join(homeDir, ".claude", ".credentials.json")
		_, _ = keychain.EnsureMirror(credentialsPath)
		info, err = parseClaudeCredentialsFile(credentialsPath)
		if err == nil {
			info.Source = credentialsPath
			return info, nil
		}

		// Fallback to legacy locations for backwards compatibility
		claudeJsonPath := filepath.Join(homeDir, ".claude.json")
		info, err = parseOAuthFile(claudeJsonPath)
		if err == nil {
			info.Source = claudeJsonPath
			return info, nil
		}

		if _, statErr := os.Stat(credentialsPath); os.IsNotExist(statErr) {
			if _, statErr2 := os.Stat(claudeJsonPath); os.IsNotExist(statErr2) {
				missing := true
				for _, candidate := range authCandidates {
					if _, statErr3 := os.Stat(candidate); !os.IsNotExist(statErr3) {
						missing = false
						break
					}
				}
				if missing {
					return nil, ErrNoAuthFile
				}
			}
		}

		return nil, ErrNoExpiry
	}

	// Vault/profile probing - check for credentials file in vault
	credentialsPath := filepath.Join(authDir, ".credentials.json")
	info, err := parseClaudeCredentialsFile(credentialsPath)
	if err == nil {
		info.Source = credentialsPath
		return info, nil
	}

	// Fallback to legacy vault structure
	claudeJsonPath := filepath.Join(authDir, ".claude.json")
	info, err = parseOAuthFile(claudeJsonPath)
	if err == nil {
		info.Source = claudeJsonPath
		return info, nil
	}

	flatAuthPath := filepath.Join(authDir, "auth.json")
	info, err = parseOAuthFile(flatAuthPath)
	if err == nil {
		info.Source = flatAuthPath
		return info, nil
	}

	nestedAuthPath := filepath.Join(authDir, "claude-code", "auth.json")
	info, err = parseOAuthFile(nestedAuthPath)
	if err == nil {
		info.Source = nestedAuthPath
		return info, nil
	}

	if _, statErr := os.Stat(credentialsPath); os.IsNotExist(statErr) {
		if _, statErr2 := os.Stat(claudeJsonPath); os.IsNotExist(statErr2) {
			if _, statErr3 := os.Stat(flatAuthPath); os.IsNotExist(statErr3) {
				if _, statErr4 := os.Stat(nestedAuthPath); os.IsNotExist(statErr4) {
					return nil, ErrNoAuthFile
				}
			}
		}
	}

	return nil, ErrNoExpiry
}

// claudeCredentialsJSON represents the Claude Code credentials file structure.
type claudeCredentialsJSON struct {
	ClaudeAiOauth *claudeOAuthJSON `json:"claudeAiOauth"`
}

// claudeOAuthJSON represents the OAuth data within the credentials file.
type claudeOAuthJSON struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        float64  `json:"expiresAt"` // Unix milliseconds
	RateLimitTier    string   `json:"rateLimitTier"`
	SubscriptionType string   `json:"subscriptionType"`
	Scopes           []string `json:"scopes"`
}

// parseClaudeCredentialsFile parses the Claude Code credentials file format.
func parseClaudeCredentialsFile(path string) (*ExpiryInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseClaudeCredentialsJSON(data)
}

func parseClaudeCredentialsJSON(data []byte) (*ExpiryInfo, error) {
	var creds claudeCredentialsJSON
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	if creds.ClaudeAiOauth == nil {
		return nil, ErrNoExpiry
	}

	oauth := creds.ClaudeAiOauth
	info := &ExpiryInfo{
		HasRefreshToken: strings.TrimSpace(oauth.RefreshToken) != "",
		Fingerprint:     credentialFingerprint(oauth.RefreshToken, oauth.AccessToken),
	}

	// Parse expiresAt (Unix milliseconds)
	if oauth.ExpiresAt > 0 {
		info.ExpiresAt = time.UnixMilli(int64(oauth.ExpiresAt))
	}

	// If we have expiry info, return successfully
	if !info.ExpiresAt.IsZero() || info.HasRefreshToken {
		return info, nil
	}

	// If we have an access token but no expiry, still return valid
	if oauth.AccessToken != "" {
		return info, nil
	}

	return nil, ErrNoExpiry
}

// ParseCodexExpiry extracts token expiry from Codex CLI auth file.
//
// Codex stores auth in $CODEX_HOME/auth.json (default ~/.codex/auth.json).
//
// Two layouts exist. The flat OAuth layout carries an explicit expiry:
//
//	{
//	  "access_token": "...",
//	  "refresh_token": "...",
//	  "expires_at": 1734451200,  // Unix timestamp (seconds)
//	  "token_type": "Bearer"
//	}
//
// ChatGPT-mode logins (the common case for Codex subscriptions) nest JWTs
// under "tokens" and record no expiry field at all; the lifetimes live in
// the JWT "exp" claims:
//
//	{
//	  "auth_mode": "chatgpt",
//	  "tokens": {"id_token": "<jwt>", "access_token": "<jwt>", "refresh_token": "..."},
//	  "last_refresh": "2026-08-28T03:25:03Z"
//	}
//
// For that layout the expiry is the access token's: it is what Codex sends
// with API requests and refreshes from the refresh token. The id_token only
// carries identity claims and routinely sits expired for days during a
// working session, so its exp must not drive health.
func ParseCodexExpiry(authPath string) (*ExpiryInfo, error) {
	if authPath == "" {
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			homeDir, _ := os.UserHomeDir()
			codexHome = filepath.Join(homeDir, ".codex")
		}
		authPath = filepath.Join(codexHome, "auth.json")
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoAuthFile
		}
		return nil, err
	}

	if key, selected, err := codexSelectedAPIKey(data); err != nil {
		return nil, fmt.Errorf("%w: %v", authfile.ErrInvalidCredentials, err)
	} else if selected {
		return &ExpiryInfo{
			Renewable: true, SelfRefreshing: true, Source: authPath,
			Fingerprint: credentialFingerprint("codex-api-key\x00" + key),
		}, nil
	}

	info, err := parseCodexAuthJSON(data)
	if err != nil {
		return nil, err
	}

	// SelfRefreshing stays unset: caam DOES refresh Codex (internal/refresh
	// /codex.go plus the pool refresher), so the expiry signal those
	// subsystems run on must keep flowing. Renewable is what tells `caam ls`
	// and rotation that a lapsed access token here does not mean the account
	// needs a human (issue #102).
	info.Renewable = info.HasRefreshToken
	info.Fingerprint = CodexCredentialFingerprint(data)

	info.Source = authPath
	return info, nil
}

// codexTokensJSON is the nested "tokens" block of a ChatGPT-mode Codex
// auth.json. CAAM also persists expires_at from the refresh response so opaque
// access tokens retain their known lifetime.
type codexTokensJSON struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    any    `json:"expires_at"`
}

// codexAuthJSON is the subset of a Codex auth.json needed to locate JWTs in
// either layout (nested under "tokens", or flat at the top level).
type codexAuthJSON struct {
	IDToken     string          `json:"id_token"`
	AccessToken string          `json:"access_token"`
	Tokens      codexTokensJSON `json:"tokens"`
}

// parseCodexAuthJSON extracts expiry info from the contents of a Codex
// auth.json in either layout. An explicit expiry field (flat layout, and
// Grok's auth.json which reuses this parser) always wins; otherwise the
// expiry is the access token's exp claim, then its stored nested expires_at,
// falling back to the id_token only when neither describes the access token.
func parseCodexAuthJSON(data []byte) (*ExpiryInfo, error) {
	info, err := parseOAuthJSON(data)
	if err != nil {
		if !errors.Is(err, ErrNoExpiry) {
			return nil, err
		}
		// Flat fields yielded nothing usable; the JWTs below may still.
		info = &ExpiryInfo{}
	}

	var auth codexAuthJSON
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	if auth.Tokens.RefreshToken != "" {
		info.HasRefreshToken = true
	}

	if info.ExpiresAt.IsZero() {
		// A native rotation can retain old CAAM metadata. The current access
		// token's own expiry takes precedence over that cached timestamp.
		for _, token := range []string{auth.Tokens.AccessToken, auth.AccessToken} {
			if exp := jwtExpiry(token); !exp.IsZero() {
				info.ExpiresAt = exp
				break
			}
		}
	}
	if info.ExpiresAt.IsZero() {
		info.ExpiresAt = parseExpiryField(auth.Tokens.ExpiresAt)
	}
	if info.ExpiresAt.IsZero() {
		for _, token := range []string{auth.Tokens.IDToken, auth.IDToken} {
			if exp := jwtExpiry(token); !exp.IsZero() {
				info.ExpiresAt = exp
				break
			}
		}
	}

	if info.ExpiresAt.IsZero() && !info.HasRefreshToken {
		return nil, ErrNoExpiry
	}

	return info, nil
}

// jwtExpiry returns the exp claim of a JWT without validating it, or the
// zero time when the token is empty, malformed, or carries no exp.
func jwtExpiry(token string) time.Time {
	if token == "" {
		return time.Time{}
	}
	id, err := identity.ExtractFromJWT(token)
	if err != nil {
		return time.Time{}
	}
	return id.ExpiresAt
}

// ParseCursorExpiry reads the accessToken JWT expiry from Cursor's auth.json.
// An empty path resolves the live credential through ResolveCursorPaths, so
// Linux XDG, macOS and Windows use the same paths as backup and activation.
//
// A browser/session login cannot refresh, even when refreshToken is present
// (it is normally the same JWT as accessToken). Only a stored apiKey lets
// cursor-agent exchange the key for new tokens without another login. Ambient
// CURSOR_API_KEY is deliberately not applied to saved profiles: it belongs to
// the current process, not necessarily the account in the snapshot.
func ParseCursorExpiry(authPath string) (*ExpiryInfo, error) {
	if authPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		authPath = authfile.ResolveCursorPaths(homeDir, runtime.GOOS, os.Getenv).AuthFile
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoAuthFile
		}
		return nil, err
	}
	info, err := parseCursorAuthJSON(data)
	if err != nil {
		return nil, err
	}
	if info.ExpiresAt.IsZero() && !info.Renewable {
		return nil, ErrNoExpiry
	}
	info.Source = authPath
	return info, nil
}

func parseCursorAuthJSON(data []byte) (*ExpiryInfo, error) {
	var auth struct {
		AccessToken string `json:"accessToken"`
		APIKey      string `json:"apiKey"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, fmt.Errorf("parse Cursor auth JSON: %w", err)
	}

	renewable := strings.TrimSpace(auth.APIKey) != ""
	info := &ExpiryInfo{
		ExpiresAt:      jwtExpiry(auth.AccessToken),
		Renewable:      renewable,
		SelfRefreshing: renewable,
	}

	credential := "cursor-session\x00" + auth.AccessToken
	if renewable {
		credential = "cursor-api-key\x00" + auth.APIKey
	} else {
		info.ReloginWarningLead = CursorReloginLead
	}
	digest := sha256.Sum256([]byte(credential))
	info.Fingerprint = fmt.Sprintf("%x", digest)
	return info, nil
}

// ParseGrokExpiry extracts token expiry from Grok Build's auth.json.
//
// Grok does not use either Codex layout. Its file is a JSON object keyed by a
// dynamic credential key of the form "<oidc-issuer>::<client-id>", and the
// entry object holds the token material:
//
//	{
//	  "https://auth.x.ai::<uuid>": {
//	    "key": "<access token>",
//	    "auth_mode": "sso",
//	    "refresh_token": "...",
//	    "expires_at": "2026-12-31T00:00:00Z"
//	  }
//	}
//
// Reusing ParseCodexExpiry on this shape found neither an expiry nor a refresh
// token, so a live, working Grok profile scored as "unknown expiry" and every
// such profile was reported as a warning forever (issue #101). Top-level keys
// are treated as opaque and scanned, mirroring identity.ExtractFromGrokAuth;
// a flat layout is accepted first so this keeps working if a future CLI
// version flattens the file.
//
// The entry with the latest expiry wins, so a file carrying several credential
// entries reports the one that actually keeps the CLI working.
//
// SelfRefreshing is deliberately left unset, matching Codex: caam is free to
// refresh Grok, so the expiry signal must keep reaching the refresh paths.
// Renewable is set from the entry's refresh token, which is what keeps a
// live Grok profile out of the "needs re-login" bucket (issue #102).
func ParseGrokExpiry(authPath string) (*ExpiryInfo, error) {
	if authPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		authPath = filepath.Join(homeDir, ".grok", "auth.json")
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoAuthFile
		}
		return nil, err
	}

	info, err := parseGrokAuthJSON(data)
	if err != nil {
		return nil, err
	}
	info.Renewable = info.HasRefreshToken
	info.Source = authPath
	return info, nil
}

// parseGrokAuthJSON extracts expiry info from the contents of a Grok
// auth.json in either the flat or the dynamic-key layout.
func parseGrokAuthJSON(data []byte) (*ExpiryInfo, error) {
	// Flat layout: OAuth fields directly at the top level.
	if info, err := parseOAuthJSON(data); err == nil {
		return info, nil
	}

	var entries map[string]json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	// Keys are scanned in sorted order so a tie between two entries with the
	// same expiry resolves deterministically.
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var best *ExpiryInfo
	for _, key := range keys {
		entry, err := parseOAuthJSON(entries[key])
		if err != nil {
			continue
		}
		if best == nil {
			best = entry
			continue
		}
		// Prefer the entry that is usable for longest: a later expiry, or a
		// known expiry over an unknown one.
		if best.ExpiresAt.IsZero() || entry.ExpiresAt.After(best.ExpiresAt) {
			best = entry
		}
	}
	if best == nil {
		return nil, ErrNoExpiry
	}
	return best, nil
}

// ParseGeminiExpiry extracts token expiry from Gemini CLI auth files.
//
// Gemini CLI stores auth in:
//   - ~/.gemini/settings.json
//   - ~/.gemini/oauth_creds.json (optional)
//
// Note: Google OAuth tokens via ADC may not include expiry in the file itself.
// The expiry is typically short-lived and requires refresh.
//
// SelfRefreshing stays unset (caam may refresh Gemini); Renewable follows the
// stored refresh token, so an ADC credential — which carries a refresh token
// and no expiry at all — is never mistaken for one that needs a re-login
// (issue #102).
func ParseGeminiExpiry(authDir string) (*ExpiryInfo, error) {
	checkSystem := false
	if authDir == "" {
		checkSystem = true
		geminiHome := os.Getenv("GEMINI_HOME")
		if geminiHome == "" {
			homeDir, _ := os.UserHomeDir()
			geminiHome = filepath.Join(homeDir, ".gemini")
		}
		authDir = geminiHome
	}

	// The selected method owns health. Old OAuth caches commonly remain after
	// switching to an API key or Vertex and must not supply that method's TTL.
	settingsPath := filepath.Join(authDir, "settings.json")
	settings, settingsErr := readGeminiModeFile(settingsPath)
	if settingsErr != nil && !os.IsNotExist(settingsErr) {
		return nil, settingsErr
	}
	selected, err := authfile.GeminiSelectedAuthType(settings)
	if err != nil {
		return nil, fmt.Errorf("%w: Gemini settings: %v", authfile.ErrInvalidCredentials, err)
	}
	switch selected {
	case "gemini-api-key":
		path := filepath.Join(authDir, ".env")
		data, err := readGeminiModeFile(path)
		if err != nil {
			return nil, fmt.Errorf("%w: selected Gemini API key: %v", authfile.ErrInvalidCredentials, err)
		}
		info, err := parseLiveGeminiAPIKey(data)
		if info != nil {
			info.Source = path
		}
		return info, err
	case "vertex-ai":
		if checkSystem {
			path := getADCPath()
			if info, err := parseADCFile(path); err == nil {
				info.Renewable = info.HasRefreshToken
				info.Source = path
				return info, nil
			}
		}
		// The vault does not capture Vertex ADC. Do not borrow a different
		// account's OAuth credential or the invoking user's ambient ADC.
		return nil, ErrNoExpiry
	}

	// The current credential cache is authoritative. Settings can contain
	// unrelated policy or an older access token; never combine that token's
	// expiry with a different cache's refresh token. A present but invalid
	// cache must not resurrect a superseded login from a fallback file.
	oauthPath := filepath.Join(authDir, "oauth_creds.json")
	// Older vault snapshots used this filename. Passive readers must inspect
	// it in place rather than migrate the snapshot just to learn its expiry.
	legacyOAuthPath := filepath.Join(authDir, "oauth_credentials.json")
	for _, path := range []string{oauthPath, legacyOAuthPath} {
		fi, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			fi, err = os.Stat(path)
		}
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("gemini credential source is not a regular file")
		}
		info, err := parseOAuthFile(path)
		if err != nil {
			return nil, err
		}
		info.Renewable = info.HasRefreshToken
		info.Source = path
		return info, nil
	}

	// Settings-only OAuth profiles predate the separate credential cache.
	info, err := parseOAuthFile(settingsPath)
	if err == nil {
		info.Renewable = info.HasRefreshToken
		info.Source = settingsPath
		return info, nil
	}

	// Try gcloud ADC format (only if checking system state)
	adcPath := ""
	if checkSystem {
		adcPath = getADCPath()
		info, err = parseADCFile(adcPath)
		if err == nil {
			info.Renewable = info.HasRefreshToken
			info.Source = adcPath
			return info, nil
		}
	}

	// Report ErrNoAuthFile only when *none* of the supported auth files exist.
	_, settingsErr = os.Stat(settingsPath)
	_, oauthErr := os.Stat(oauthPath)
	_, legacyOAuthErr := os.Stat(legacyOAuthPath)
	adcExists := false
	if checkSystem && adcPath != "" {
		if _, err := os.Stat(adcPath); err == nil {
			adcExists = true
		}
	}
	if os.IsNotExist(settingsErr) && os.IsNotExist(oauthErr) && os.IsNotExist(legacyOAuthErr) && !adcExists {
		return nil, ErrNoAuthFile
	}

	return nil, ErrNoExpiry
}

// readGeminiModeFile bounds the small settings/key files required to choose a
// credential before opening any potentially unused OAuth cache.
func readGeminiModeFile(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > authfile.MaxDiscoveryFileBytes {
		return nil, fmt.Errorf("%w: Gemini settings/key source must be a bounded regular file", authfile.ErrInvalidCredentials)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, authfile.MaxDiscoveryFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > authfile.MaxDiscoveryFileBytes {
		return nil, fmt.Errorf("%w: Gemini settings/key source is too large", authfile.ErrInvalidCredentials)
	}
	return data, nil
}

func getADCPath() string {
	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		return path
	}

	if configDir := os.Getenv("CLOUDSDK_CONFIG"); configDir != "" {
		return filepath.Join(configDir, "application_default_credentials.json")
	}

	// Windows support
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "gcloud", "application_default_credentials.json")
		}
	}

	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
}

// oauthJSON represents common OAuth token response structures.
// Different providers use different field naming conventions.
type oauthJSON struct {
	// Snake case (common in OAuth specs)
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    any    `json:"expires_at"`
	ExpiresIn    any    `json:"expires_in"`

	// Camel case (some providers)
	AccessTokenCamel  string `json:"accessToken"`
	RefreshTokenCamel string `json:"refreshToken"`
	ExpiresAtCamel    any    `json:"expiresAt"`
	ExpiresInCamel    any    `json:"expiresIn"`

	// Other common fields
	Expiry     string `json:"expiry"`
	Key        string `json:"key"` // Grok dynamic credential entries
	TokenType  string `json:"token_type"`
	IssuedAt   any    `json:"issued_at"`
	IssuedTime any    `json:"issuedTime"`
}

// parseOAuthFile reads an OAuth token file and extracts expiry info.
func parseOAuthFile(path string) (*ExpiryInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseOAuthJSON(data)
}

// parseOAuthJSON extracts expiry info from the contents of an OAuth token
// file in the flat layout described by oauthJSON.
func parseOAuthJSON(data []byte) (*ExpiryInfo, error) {
	var oauth oauthJSON
	if err := json.Unmarshal(data, &oauth); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	info := &ExpiryInfo{
		HasRefreshToken: strings.TrimSpace(oauth.RefreshToken) != "" || strings.TrimSpace(oauth.RefreshTokenCamel) != "",
		Fingerprint:     credentialFingerprint(oauth.RefreshToken, oauth.RefreshTokenCamel, oauth.AccessToken, oauth.AccessTokenCamel, oauth.Key),
	}

	// Try to extract expiry from various fields
	if expiry := parseExpiryField(oauth.ExpiresAt); !expiry.IsZero() {
		info.ExpiresAt = expiry
		return info, nil
	}
	if expiry := parseExpiryField(oauth.ExpiresAtCamel); !expiry.IsZero() {
		info.ExpiresAt = expiry
		return info, nil
	}
	if expiry := parseExpiryField(oauth.Expiry); !expiry.IsZero() {
		info.ExpiresAt = expiry
		return info, nil
	}

	// Try expires_in with issued_at
	if expiresIn := parseExpiresIn(oauth.ExpiresIn); expiresIn > 0 {
		issuedAt := parseExpiryField(oauth.IssuedAt)
		if issuedAt.IsZero() {
			issuedAt = parseExpiryField(oauth.IssuedTime)
		}
		if issuedAt.IsZero() {
			// If issued_at is missing, assume now (common for OAuth tokens).
			issuedAt = time.Now()
		}
		info.ExpiresAt = issuedAt.Add(time.Duration(expiresIn) * time.Second)
		return info, nil
	}
	if expiresIn := parseExpiresIn(oauth.ExpiresInCamel); expiresIn > 0 {
		issuedAt := parseExpiryField(oauth.IssuedAt)
		if issuedAt.IsZero() {
			issuedAt = parseExpiryField(oauth.IssuedTime)
		}
		if issuedAt.IsZero() {
			// Without issued_at, assume now (less accurate but better than nothing)
			issuedAt = time.Now()
		}
		info.ExpiresAt = issuedAt.Add(time.Duration(expiresIn) * time.Second)
		return info, nil
	}

	// If we have a refresh token but no expiry, that's still useful info
	if info.HasRefreshToken {
		return info, nil
	}

	return nil, ErrNoExpiry
}

// adcJSON represents Google Application Default Credentials format.
type adcJSON struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	Type         string `json:"type"`
}

// parseADCFile reads Google ADC credentials.
// ADC files don't contain expiry - they contain refresh tokens.
func parseADCFile(path string) (*ExpiryInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var adc adcJSON
	if err := json.Unmarshal(data, &adc); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	if adc.RefreshToken == "" {
		return nil, ErrNoExpiry
	}

	// ADC with refresh token = valid but unknown expiry
	return &ExpiryInfo{
		HasRefreshToken: true,
	}, nil
}

// parseExpiryField attempts to parse an expiry value from various formats.
func parseExpiryField(v any) time.Time {
	if v == nil {
		return time.Time{}
	}

	switch val := v.(type) {
	case string:
		val = strings.TrimSpace(val)
		if val == "" {
			return time.Time{}
		}
		// Try ISO8601 / RFC3339
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t
		}
		// Try RFC3339Nano
		if t, err := time.Parse(time.RFC3339Nano, val); err == nil {
			return t
		}
		// Try common date formats
		formats := []string{
			"2006-01-02T15:04:05Z07:00",
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05",
		}
		for _, format := range formats {
			if t, err := time.Parse(format, val); err == nil {
				return t
			}
		}

		// Try numeric strings (unix seconds or milliseconds).
		if isNumericString(val) {
			if num, err := strconv.ParseFloat(val, 64); err == nil {
				if num > 1e12 {
					return time.UnixMilli(int64(num))
				}
				return time.Unix(int64(num), 0)
			}
		}

	case float64:
		// Unix timestamp (seconds or milliseconds)
		if val > 1e12 {
			// Likely milliseconds
			return time.UnixMilli(int64(val))
		}
		return time.Unix(int64(val), 0)

	case int64:
		if val > 1e12 {
			return time.UnixMilli(val)
		}
		return time.Unix(val, 0)

	case int:
		if val > 1e12 {
			return time.UnixMilli(int64(val))
		}
		return time.Unix(int64(val), 0)
	}

	return time.Time{}
}

func isNumericString(s string) bool {
	if s == "" {
		return false
	}
	hasDigit := false
	for i, r := range s {
		if r >= '0' && r <= '9' {
			hasDigit = true
			continue
		}
		if r == '.' {
			continue
		}
		if (r == '-' || r == '+') && i == 0 {
			continue
		}
		return false
	}
	return hasDigit
}

// parseExpiresIn extracts seconds from an expires_in field.
func parseExpiresIn(v any) int64 {
	if v == nil {
		return 0
	}

	switch val := v.(type) {
	case float64:
		return int64(val)
	case int64:
		return val
	case int:
		return int64(val)
	case string:
		// Sometimes expires_in is a string number
		var n int64
		fmt.Sscanf(val, "%d", &n)
		return n
	}

	return 0
}

// ParseAllExpiry attempts to parse expiry for all providers and returns combined results.
func ParseAllExpiry() map[string]*ExpiryInfo {
	results := make(map[string]*ExpiryInfo)

	if info, err := ParseClaudeExpiry(""); err == nil {
		results["claude"] = info
	}
	if info, err := ParseCodexExpiry(""); err == nil {
		results["codex"] = info
	}
	if info, err := ParseGeminiExpiry(""); err == nil {
		results["gemini"] = info
	}
	if info, err := ParseCursorExpiry(""); err == nil {
		results["cursor"] = info
	}

	return results
}

// TTL returns the time until expiry, or 0 if expired or unknown.
func (e *ExpiryInfo) TTL() time.Duration {
	if e == nil || e.ExpiresAt.IsZero() {
		return 0
	}
	ttl := time.Until(e.ExpiresAt)
	if ttl < 0 {
		return 0
	}
	return ttl
}

// IsExpired returns true if the token is expired.
func (e *ExpiryInfo) IsExpired() bool {
	if e == nil || e.ExpiresAt.IsZero() {
		return false // Unknown expiry is not treated as expired
	}
	return time.Now().After(e.ExpiresAt)
}

// NeedsRefresh returns true if the token should be refreshed.
// Default threshold is 10 minutes before expiry.
func (e *ExpiryInfo) NeedsRefresh(threshold time.Duration) bool {
	if threshold == 0 {
		threshold = 10 * time.Minute
	}
	if e == nil || e.ExpiresAt.IsZero() {
		return false // Unknown expiry - can't determine if refresh needed
	}
	if e.SelfRefreshing || (e.ReloginWarningLead > 0 && !e.Renewable) {
		return false // The provider renews it, or only another login can replace it.
	}
	return time.Until(e.ExpiresAt) < threshold
}
