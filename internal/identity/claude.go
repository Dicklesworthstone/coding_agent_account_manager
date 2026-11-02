package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// claudeSettingsFile is the Claude Code state file that carries the account
// identity block (oauthAccount) next to the credentials.
const claudeSettingsFile = ".claude.json"

// ClaudeIdentityCacheFile belongs to CAAM, not Claude Code. Its provider-verified
// identity is bound to the access and refresh tokens, so a subsequent login or
// refresh cannot accidentally inherit a previous account's cached identity.
const ClaudeIdentityCacheFile = ".caam-identity.json"

// MaxClaudeIdentityBytes bounds saved identity metadata and provider responses.
// Transfer readers enforce the same limit before allocating or decoding JSON.
const MaxClaudeIdentityBytes = 1 << 20

const (
	claudeProfileURL      = "https://api.anthropic.com/api/oauth/profile"
	claudeIdentityTimeout = 3 * time.Second
)

var (
	ErrClaudeIdentityUnavailable = errors.New("claude token identity could not be verified")
	ErrClaudeTokenExpired        = errors.New("claude access token is expired; identity verification was skipped")
	ErrClaudeTokenRejected       = errors.New("claude access token was rejected by the identity endpoint")
)

// ClaudeIdentityBinding persists provider-confirmed ownership without storing a
// second copy of the credentials. The fingerprint includes BOTH token values:
// a substituted access token must not inherit trust from an unchanged refresh
// token. Expiry, scopes and unrelated telemetry do not change token ownership.
type ClaudeIdentityBinding struct {
	Identity              Identity  `json:"identity"`
	CredentialFingerprint string    `json:"credential_fingerprint"`
	VerifiedAt            time.Time `json:"verified_at"`
}

const (
	ClaudeIdentityUnknown           = "unknown"
	ClaudeIdentityVerified          = "verified"
	ClaudeIdentityCredentialChanged = "credential_changed"
	ClaudeIdentityInvalidMetadata   = "invalid_metadata"
)

// ClaudeIdentityAssessment distinguishes absent evidence from a broken prior
// binding. RecordedIdentity describes the expected owner of the saved profile;
// it must not be presented as verified ownership of a replacement credential.
// Binding is returned only when both captured token values still match.
type ClaudeIdentityAssessment struct {
	State            string                 `json:"state"`
	Binding          *ClaudeIdentityBinding `json:"-"`
	RecordedIdentity *Identity              `json:"recorded_identity,omitempty"`
}

// InspectClaudeIdentityBinding assesses captured metadata without file or
// network access. It never treats email, names, or .claude.json as proof, and
// errors never contain credential bytes or fragments of invalid JSON.
func InspectClaudeIdentityBinding(metadata, credentials []byte) (ClaudeIdentityAssessment, error) {
	result := ClaudeIdentityAssessment{State: ClaudeIdentityUnknown}
	if metadata == nil {
		return result, nil
	}
	var envelope map[string]json.RawMessage
	if len(metadata) > MaxClaudeIdentityBytes || json.Unmarshal(metadata, &envelope) != nil || envelope == nil {
		result.State = ClaudeIdentityInvalidMetadata
		return result, errors.New("invalid Claude identity metadata")
	}
	raw, present := envelope["claude_identity"]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return result, nil
	}
	var binding ClaudeIdentityBinding
	if json.Unmarshal(raw, &binding) != nil || binding.Identity.Provider != "claude" || binding.VerifiedAt.IsZero() ||
		!ClaudeSameAccount(&binding.Identity, &binding.Identity) || len(binding.CredentialFingerprint) != sha256.Size*2 {
		result.State = ClaudeIdentityInvalidMetadata
		return result, errors.New("incomplete Claude identity binding")
	}
	for _, digit := range binding.CredentialFingerprint {
		if digit < '0' || digit > '9' && digit < 'a' || digit > 'f' {
			result.State = ClaudeIdentityInvalidMetadata
			return result, errors.New("invalid Claude identity fingerprint")
		}
	}
	recorded := binding.Identity
	recorded.Verified = false
	result.RecordedIdentity = &recorded
	if !binding.MatchesCredentials(credentials) {
		result.State = ClaudeIdentityCredentialChanged
		return result, nil
	}
	binding.Identity.Verified = true
	result.State = ClaudeIdentityVerified
	result.Binding = &binding
	return result, nil
}

// ClaudeSameAccount requires the account AND organization UUIDs. Email and
// organization labels are presentation fields, not account-selection keys.
func ClaudeSameAccount(a, b *Identity) bool {
	return a != nil && b != nil &&
		strings.TrimSpace(a.AccountID) != "" && strings.TrimSpace(a.OrganizationID) != "" &&
		strings.TrimSpace(a.AccountID) == strings.TrimSpace(b.AccountID) &&
		strings.TrimSpace(a.OrganizationID) == strings.TrimSpace(b.OrganizationID)
}

// ClaudeIdentityConflicts reports a disagreement in known stable identifiers.
// Missing identifiers are uncertainty, not evidence of a different account.
func ClaudeIdentityConflicts(a, b *Identity) bool {
	if a == nil || b == nil {
		return false
	}
	aa, ba := strings.TrimSpace(a.AccountID), strings.TrimSpace(b.AccountID)
	ao, bo := strings.TrimSpace(a.OrganizationID), strings.TrimSpace(b.OrganizationID)
	return aa != "" && ba != "" && aa != ba || ao != "" && bo != "" && ao != bo
}

// ClaudeCredentialFingerprint identifies exactly the token pair in a captured
// credentials document. It never returns either secret, including in errors.
func ClaudeCredentialFingerprint(credentials []byte) (string, error) {
	var root struct {
		OAuth *struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(credentials, &root) != nil || root.OAuth == nil || strings.TrimSpace(root.OAuth.AccessToken) == "" {
		return "", errors.New("claude credentials have no usable OAuth access token")
	}
	// JSON's array/string framing prevents ambiguous concatenations.
	pair, err := json.Marshal([]string{root.OAuth.AccessToken, root.OAuth.RefreshToken})
	if err != nil {
		return "", errors.New("cannot fingerprint Claude credentials")
	}
	digest := sha256.Sum256(append([]byte("caam:claude-token-pair:v1:"), pair...))
	return fmt.Sprintf("%x", digest), nil
}

// MatchesCredentials rejects stale, incomplete and cross-provider bindings.
func (b *ClaudeIdentityBinding) MatchesCredentials(credentials []byte) bool {
	if b == nil || b.Identity.Provider != "claude" || b.VerifiedAt.IsZero() || !ClaudeSameAccount(&b.Identity, &b.Identity) {
		return false
	}
	fingerprint, err := ClaudeCredentialFingerprint(credentials)
	return err == nil && b.CredentialFingerprint == fingerprint
}

// DecodeClaudeIdentityBinding reads the small metadata envelope from captured
// bytes, making it usable by callers that already hold a credential snapshot.
// An absent or stale binding is not proof of ownership and returns nil.
func DecodeClaudeIdentityBinding(metadata, credentials []byte) (*ClaudeIdentityBinding, error) {
	var envelope struct {
		Binding *ClaudeIdentityBinding `json:"claude_identity"`
	}
	if len(metadata) > MaxClaudeIdentityBytes || json.Unmarshal(metadata, &envelope) != nil {
		return nil, errors.New("invalid Claude identity metadata")
	}
	if !envelope.Binding.MatchesCredentials(credentials) {
		return nil, nil
	}
	envelope.Binding.Identity.Verified = true
	return envelope.Binding, nil
}

// ReadClaudeIdentityBinding reads an exact-token binding without contacting the
// provider. Callers that have captured credentials should use the ForCredentials
// variant to avoid rereading a concurrently changing credential file.
func ReadClaudeIdentityBinding(credentialsPath string) (*ClaudeIdentityBinding, error) {
	credentials, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("read Claude credentials: %w", err)
	}
	return ReadClaudeIdentityBindingForCredentials(credentialsPath, credentials)
}

// ReadClaudeIdentityBindingForCredentials resolves the cache for an already
// captured token generation without rereading the native credential file.
func ReadClaudeIdentityBindingForCredentials(credentialsPath string, credentials []byte) (*ClaudeIdentityBinding, error) {
	binding, _, err := readClaudeIdentityBinding(credentialsPath, credentials)
	return binding, err
}

// The boolean also records rejected bindings. Once CAAM has established a
// credential binding, replacing the token must not resurrect an unverified
// .claude.json label from the previous login.
func readClaudeIdentityBinding(credentialsPath string, credentials []byte) (*ClaudeIdentityBinding, bool, error) {
	dir := filepath.Dir(credentialsPath)
	// A saved profile's metadata defines its expected owner. A copied native
	// cache must not relabel that profile after its token file was replaced.
	for _, name := range []string{"meta.json", ClaudeIdentityCacheFile} {
		f, err := os.Open(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, true, fmt.Errorf("read Claude identity metadata: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(f, MaxClaudeIdentityBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return nil, true, errors.New("cannot read Claude identity metadata")
		}
		binding, err := DecodeClaudeIdentityBinding(data, credentials)
		if err != nil {
			return nil, true, err
		}
		if binding != nil {
			return binding, true, nil
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(data, &envelope) == nil {
			if raw, ok := envelope["claude_identity"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return nil, true, nil
			}
		}
	}
	return nil, false, nil
}

// VerifyClaudeCredentials makes a single bounded request for the captured access
// token. It neither refreshes credentials nor reads shared .claude.json state.
// Only the fixed provider endpoint receives the token; redirects are refused,
// including redirects within the same host. Errors never contain a response
// body, request headers, or the transport's potentially sensitive diagnostics.
// A nil client selects the default transport. Tests can supply a transport
// without replacing the trusted endpoint or permitting HTTP in production.
func VerifyClaudeCredentials(ctx context.Context, credentials []byte, client *http.Client) (*ClaudeIdentityBinding, error) {
	fingerprint, err := ClaudeCredentialFingerprint(credentials)
	if err != nil {
		return nil, err
	}
	id, err := ParseClaudeIdentity(credentials, nil)
	if err != nil {
		return nil, err
	}
	if !id.ExpiresAt.IsZero() && !id.ExpiresAt.After(time.Now()) {
		return nil, ErrClaudeTokenExpired
	}
	var root struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(credentials, &root) != nil || strings.ContainsAny(root.OAuth.AccessToken, "\r\n") {
		return nil, errors.New("invalid Claude OAuth access token")
	}
	ctx, cancel := context.WithTimeout(ctx, claudeIdentityTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeProfileURL, nil)
	if err != nil {
		return nil, ErrClaudeIdentityUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+root.OAuth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "coding_agent_account_manager (+https://github.com/Dicklesworthstone/coding_agent_account_manager)")
	requestClient := http.Client{Timeout: claudeIdentityTimeout}
	if client != nil {
		requestClient.Transport = client.Transport
		if client.Timeout > 0 && client.Timeout < requestClient.Timeout {
			requestClient.Timeout = client.Timeout
		}
	}
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := requestClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", ErrClaudeIdentityUnavailable, ctx.Err())
		}
		return nil, ErrClaudeIdentityUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrClaudeTokenRejected
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (HTTP %d)", ErrClaudeIdentityUnavailable, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxClaudeIdentityBytes+1))
	if err != nil || len(data) > MaxClaudeIdentityBytes {
		return nil, ErrClaudeIdentityUnavailable
	}
	var profile struct {
		Account *struct {
			UUID  string `json:"uuid"`
			Email string `json:"email"`
		} `json:"account"`
		Organization *struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"organization"`
	}
	if json.Unmarshal(data, &profile) != nil || profile.Account == nil || profile.Organization == nil ||
		strings.TrimSpace(profile.Account.UUID) == "" || strings.TrimSpace(profile.Organization.UUID) == "" {
		return nil, ErrClaudeIdentityUnavailable
	}
	id.AccountID = strings.TrimSpace(profile.Account.UUID)
	id.OrganizationID = strings.TrimSpace(profile.Organization.UUID)
	id.Email = strings.TrimSpace(profile.Account.Email)
	id.Organization = strings.TrimSpace(profile.Organization.Name)
	id.Verified = true
	return &ClaudeIdentityBinding{
		Identity: *id, CredentialFingerprint: fingerprint, VerifiedAt: time.Now().UTC(),
	}, nil
}

// ExtractFromClaudeCredentials reads Claude .credentials.json and extracts identity.
//
// Current Claude auth files (early 2026 onward) carry only expiresAt and
// subscriptionType in claudeAiOauth; accountId and email are no longer written
// there. Claude Code records the account identity in the .claude.json that
// accompanies the credentials instead, under "oauthAccount" (emailAddress,
// accountUuid, organizationUuid, organizationName). A matching CAAM binding
// takes precedence over this mutable metadata. Without one, the fields remain
// unverified and must not authorize matching or overwriting a saved profile.
//
// See: docs/CLAUDE_AUTH_INVENTORY.md (CLAUDE-001)
func ExtractFromClaudeCredentials(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read claude credentials: %w", err)
	}
	id, err := ParseClaudeIdentity(data, nil)
	if err != nil {
		return nil, err
	}
	binding, known, bindingErr := readClaudeIdentityBinding(path, data)
	if binding != nil {
		verified := binding.Identity
		verified.ExpiresAt = id.ExpiresAt
		verified.PlanType = id.PlanType
		return &verified, nil
	}
	if !known && bindingErr == nil {
		fillFromClaudeSettings(id, claudeSettingsCandidates(path))
	} else {
		// A token replacement invalidates all previously associated labels,
		// including descriptive fields copied into the credential document.
		id.AccountID, id.OrganizationID, id.Email, id.Organization = "", "", "", ""
	}
	return id, nil
}

// ParseClaudeIdentity reads a captured credentials/settings pair, with no file
// or network access. Its metadata is descriptive, never provider verified.
// Conflicting settings are ignored as a unit rather than splicing fields from
// two accounts into one apparently complete identity.
func ParseClaudeIdentity(credentials, settings []byte) (*Identity, error) {
	dec := json.NewDecoder(bytes.NewReader(credentials))
	dec.UseNumber()

	var root map[string]interface{}
	if err := dec.Decode(&root); err != nil || root == nil {
		return nil, errors.New("invalid Claude credentials JSON")
	}
	if err := dec.Decode(new(interface{})); err != io.EOF {
		return nil, errors.New("invalid Claude credentials JSON")
	}

	identity := &Identity{Provider: "claude"}

	if raw, ok := root["claudeAiOauth"].(map[string]interface{}); ok {
		identity.AccountID = valueAsString(raw["accountId"])
		if identity.AccountID == "" {
			identity.AccountID = valueAsString(raw["accountUuid"])
		}
		identity.OrganizationID = valueAsString(raw["organizationUuid"])
		if identity.OrganizationID == "" {
			identity.OrganizationID = valueAsString(raw["organizationId"])
		}
		identity.Organization = valueAsString(raw["organizationName"])
		identity.PlanType = valueAsString(raw["subscriptionType"])
		identity.Email = valueAsString(raw["email"])
		if exp, ok := parseEpoch(raw["expiresAt"]); ok {
			identity.ExpiresAt = exp
		}
	}

	var state claudeSettingsIdentity
	if len(settings) > 0 && json.Unmarshal(settings, &state) == nil && state.OAuthAccount != nil {
		fillClaudeAccount(identity, state.OAuthAccount)
	}

	return identity, nil
}

// claudeOAuthAccount is the identity block Claude Code writes into
// .claude.json. The file also holds per-project session state and grows to
// hundreds of kilobytes, so it is decoded into this narrow struct rather than
// a generic map.
type claudeOAuthAccount struct {
	AccountUUID      string `json:"accountUuid"`
	OrganizationUUID string `json:"organizationUuid"`
	EmailAddress     string `json:"emailAddress"`
	OrganizationName string `json:"organizationName"`
}

type claudeSettingsIdentity struct {
	OAuthAccount *claudeOAuthAccount `json:"oauthAccount"`
}

// claudeSettingsCandidates lists, in precedence order, where the .claude.json
// paired with a credentials file lives. It mirrors how Claude Code itself
// resolves the file: the credentials always sit in the config directory, and
// the state file is <config dir>/.claude.json when CLAUDE_CONFIG_DIR is set
// but ~/.claude.json — one level ABOVE the default ~/.claude config dir — when
// it is not.
//
//   - Credentials in a directory not named ".claude" (a vault snapshot, or an
//     explicit CLAUDE_CONFIG_DIR): only the sibling .claude.json is paired.
//   - Credentials in a ".claude" directory (the live ~/.claude, or a shallow
//     profile's <home>/.claude): the parent's .claude.json is canonical and
//     is consulted first. A .claude.json inside the directory is only a
//     fallback — it is what an older tool that exported CLAUDE_CONFIG_DIR=
//     ~/.claude left behind, and the shallow symlink farm mirrors such a
//     stray file into every profile's .claude/, where it would otherwise
//     outrank the profile's own identity (issue #91). The one layout in which
//     the nested file IS canonical — CLAUDE_CONFIG_DIR currently pointing at
//     that very directory — puts it first again.
func claudeSettingsCandidates(credentialsPath string) []string {
	dir := filepath.Dir(credentialsPath)
	sibling := filepath.Join(dir, claudeSettingsFile)
	if filepath.Base(dir) != ".claude" {
		return []string{sibling}
	}
	parent := filepath.Join(filepath.Dir(dir), claudeSettingsFile)
	if cfg := os.Getenv("CLAUDE_CONFIG_DIR"); cfg != "" && samePath(cfg, dir) {
		return []string{sibling, parent}
	}
	return []string{parent, sibling}
}

// samePath reports whether a and b name the same directory, resolving
// symlinks when possible.
func samePath(a, b string) bool {
	return canonicalPath(a) == canonicalPath(b)
}

func canonicalPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return filepath.Clean(p)
}

// fillFromClaudeSettings copies the identity fields that are still empty out
// of the first candidate .claude.json that parses and carries an
// oauthAccount block.
func fillFromClaudeSettings(identity *Identity, candidates []string) {
	for _, path := range candidates {
		account, ok := readClaudeOAuthAccount(path)
		if !ok {
			continue
		}
		fillClaudeAccount(identity, account)
		return
	}
}

func fillClaudeAccount(identity *Identity, account *claudeOAuthAccount) {
	settingsIdentity := &Identity{AccountID: account.AccountUUID, OrganizationID: account.OrganizationUUID}
	if ClaudeIdentityConflicts(identity, settingsIdentity) ||
		identity.Email != "" && account.EmailAddress != "" && !strings.EqualFold(identity.Email, account.EmailAddress) {
		return
	}
	if identity.Email == "" {
		identity.Email = account.EmailAddress
	}
	if identity.AccountID == "" {
		identity.AccountID = account.AccountUUID
	}
	if identity.OrganizationID == "" {
		identity.OrganizationID = account.OrganizationUUID
	}
	if identity.Organization == "" {
		identity.Organization = account.OrganizationName
	}
}

// ClaudeSignedInIdentity performs an offline read of the native credential
// location. Only a result with Verified set has provider-confirmed ownership;
// raw .claude.json metadata must not authorize account-selection decisions.
// Native keychain-only credentials need the caller's keychain-aware snapshot.
func ClaudeSignedInIdentity() *Identity {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir = filepath.Join(home, ".claude")
	}
	id, err := ExtractFromClaudeCredentials(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return nil
	}
	return id
}

// readClaudeOAuthAccount returns the oauthAccount block of the .claude.json
// at path. ok is false when the file is missing, unparseable, or has no
// oauthAccount.
func readClaudeOAuthAccount(path string) (*claudeOAuthAccount, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var settings claudeSettingsIdentity
	if err := json.Unmarshal(data, &settings); err != nil || settings.OAuthAccount == nil {
		return nil, false
	}
	return settings.OAuthAccount, true
}

func parseEpoch(value interface{}) (time.Time, bool) {
	secs, ok := epochSeconds(value)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(secs, 0).UTC(), true
}

func epochSeconds(value interface{}) (int64, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return normalizeEpoch(n), true
	case float64:
		return normalizeEpoch(int64(v)), true
	case float32:
		return normalizeEpoch(int64(v)), true
	case int64:
		return normalizeEpoch(v), true
	case int:
		return normalizeEpoch(int64(v)), true
	case string:
		n, err := json.Number(v).Int64()
		if err != nil {
			return 0, false
		}
		return normalizeEpoch(n), true
	default:
		return 0, false
	}
}

func normalizeEpoch(value int64) int64 {
	// Treat values in milliseconds (13+ digits) as ms since epoch.
	if value > 1_000_000_000_000 {
		return value / 1000
	}
	return value
}
