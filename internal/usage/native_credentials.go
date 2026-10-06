package usage

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// NativeCredentialLocator validates the selected credential file and returns
// an opaque locator for a read-only native quota fetch. It never searches a
// different file or namespace and never returns credential contents.
func NativeCredentialLocator(provider, authPath string) (string, error) {
	switch provider {
	case "cursor":
		if !fileHasCursorAccessToken(authPath) {
			return "", fmt.Errorf("cursor access token is missing or unreadable")
		}
		return "cursor-root:" + authPath, nil
	case "grok":
		// GROK_HOME is the directory containing this exact auth.json.
		// Reject arbitrary filenames rather than silently reading a sibling.
		if filepath.Base(authPath) != "auth.json" {
			return "", fmt.Errorf("grok credential must be auth.json")
		}
		st, err := os.Stat(authPath)
		if err != nil || !st.Mode().IsRegular() {
			return "", fmt.Errorf("grok auth.json is missing or unreadable")
		}
		return "grok-home:" + filepath.Dir(authPath), nil
	default:
		return "", fmt.Errorf("native quota credential unsupported for %s", provider)
	}
}

// nativeLiveCredential binds the selected live file to the saved account.
// Locators contain paths and identity metadata only, never access tokens.
type nativeLiveCredential struct {
	Provider string                `json:"provider"`
	AuthPath string                `json:"auth_path"`
	Identity nativeAccountIdentity `json:"identity"`
}

type nativeAccountIdentity struct {
	AccountID string `json:"account_id,omitempty"`
	Email     string `json:"email,omitempty"`
}

const nativeLivePrefix = "native-live:"

// NativeLiveCredentialLocator selects a live credential only if it belongs
// to the account in vaultAuthPath. The fetcher repeats the identity check on
// the exact bytes it uses, allowing token rotation but refusing account
// switches between source selection and the request. Neither file is changed.
func NativeLiveCredentialLocator(provider, authPath, vaultAuthPath string) (string, error) {
	if _, err := NativeCredentialLocator(provider, authPath); err != nil {
		return "", err
	}
	saved, err := os.ReadFile(vaultAuthPath)
	if err != nil {
		return "", fmt.Errorf("cannot verify live account: saved credential is missing or unreadable")
	}
	expected, err := nativeCredentialIdentity(provider, saved)
	if err != nil {
		return "", fmt.Errorf("cannot verify live account: saved credential identity is missing, malformed, or ambiguous")
	}
	live := nativeLiveCredential{Provider: provider, AuthPath: authPath, Identity: expected}
	if _, err := live.read(provider); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(live)
	if err != nil {
		return "", fmt.Errorf("encode live credential source")
	}
	return nativeLivePrefix + base64.RawURLEncoding.EncodeToString(encoded), nil
}

// readNativeLiveCredential distinguishes identity-bound live locators from
// ordinary saved-profile locators. Invalid live locators never fall back.
func readNativeLiveCredential(provider, locator string) (string, []byte, bool, error) {
	if !strings.HasPrefix(locator, nativeLivePrefix) {
		return "", nil, false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(locator, nativeLivePrefix))
	var live nativeLiveCredential
	if err != nil || json.Unmarshal(raw, &live) != nil {
		return "", nil, true, fmt.Errorf("invalid live credential source")
	}
	data, err := live.read(provider)
	return live.AuthPath, data, true, err
}

func (l nativeLiveCredential) read(provider string) ([]byte, error) {
	if l.Provider != provider || l.AuthPath == "" || strings.ContainsAny(l.AuthPath, "\r\n") {
		return nil, fmt.Errorf("invalid live credential source")
	}
	data, err := os.ReadFile(l.AuthPath)
	if err != nil {
		return nil, fmt.Errorf("live %s credential is missing or unreadable", provider)
	}
	id, err := nativeCredentialIdentity(provider, data)
	if err != nil {
		return nil, fmt.Errorf("cannot verify live account: live credential identity is missing, malformed, or ambiguous")
	}
	if !sameNativeAccount(l.Identity, id) {
		return nil, fmt.Errorf("live %s account does not match the requested saved profile", provider)
	}
	return data, nil
}

// A known account ID is authoritative. Email is a fallback only when one
// side lacks an ID, and only when it is carried by the credential itself.
// Cursor's separate cli-config.json can outlive a login and is a label only.
func sameNativeAccount(a, b nativeAccountIdentity) bool {
	if a.AccountID != "" && b.AccountID != "" {
		return a.AccountID == b.AccountID
	}
	return a.Email != "" && b.Email != "" && strings.EqualFold(a.Email, b.Email)
}

func nativeCredentialIdentity(provider string, data []byte) (nativeAccountIdentity, error) {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		return nativeAccountIdentity{}, fmt.Errorf("invalid credential")
	}
	switch provider {
	case "cursor":
		token, err := cursorAccessToken(data)
		if err != nil {
			return nativeAccountIdentity{}, fmt.Errorf("missing access token")
		}
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			return nativeAccountIdentity{}, fmt.Errorf("missing token identity")
		}
		payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		var claims map[string]json.RawMessage
		if err != nil || json.Unmarshal(payload, &claims) != nil || claims == nil {
			return nativeAccountIdentity{}, fmt.Errorf("invalid token identity")
		}
		return nativeIdentityFields(claims, "sub")
	case "grok":
		var entries []map[string]json.RawMessage
		if grokCredentialEntry(root) {
			entries = append(entries, root)
		}
		keys := make([]string, 0, len(root))
		for key := range root {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			var entry map[string]json.RawMessage
			if json.Unmarshal(root[key], &entry) == nil && grokCredentialEntry(entry) {
				entries = append(entries, entry)
			}
		}
		var selected nativeAccountIdentity
		for i, entry := range entries {
			id, err := nativeIdentityFields(entry, "user_id")
			if err != nil || (i > 0 && !sameNativeAccount(selected, id)) {
				return nativeAccountIdentity{}, fmt.Errorf("missing or conflicting account identity")
			}
			if selected.AccountID == "" {
				selected.AccountID = id.AccountID
			}
			if selected.Email == "" {
				selected.Email = id.Email
			}
		}
		if len(entries) > 0 {
			return selected, nil
		}
	}
	return nativeAccountIdentity{}, fmt.Errorf("missing account identity")
}

func grokCredentialEntry(entry map[string]json.RawMessage) bool {
	for _, key := range []string{"user_id", "email", "access_token", "accessToken", "refresh_token", "refreshToken", "token"} {
		if _, ok := entry[key]; ok {
			return true
		}
	}
	return false
}

func nativeIdentityFields(fields map[string]json.RawMessage, idKey string) (nativeAccountIdentity, error) {
	var id nativeAccountIdentity
	for key, target := range map[string]*string{idKey: &id.AccountID, "email": &id.Email} {
		if raw, ok := fields[key]; ok {
			if strings.TrimSpace(string(raw)) == "null" || json.Unmarshal(raw, target) != nil {
				return nativeAccountIdentity{}, fmt.Errorf("invalid identity field")
			}
			*target = strings.TrimSpace(*target)
		}
	}
	if !strings.Contains(id.Email, "@") || len(id.Email) > 200 {
		id.Email = ""
	}
	if id.AccountID == "" && id.Email == "" {
		return nativeAccountIdentity{}, fmt.Errorf("missing identity fields")
	}
	return id, nil
}
