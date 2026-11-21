package keychain

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"unicode/utf8"
)

const (
	// AgyService and AgyAccount identify the macOS login keychain item where
	// the Antigravity CLI (agy) stores its OAuth credential via go-keyring.
	AgyService = "gemini"
	AgyAccount = "antigravity"

	goKeyringBase64Prefix = "go-keyring-base64:"
)

// DecodeGoKeyringSecret decodes a secret read via macOS `security -w` that may
// have been stored by zalando/go-keyring. `security -w` emits hex when a
// keychain item holds newlines or non-ASCII bytes, and go-keyring stores
// payloads prefixed with "go-keyring-base64:". Plain UTF-8 payloads pass
// through unchanged.
func DecodeGoKeyringSecret(raw []byte) ([]byte, error) {
	data := bytes.TrimRight(raw, "\r\n")
	if len(data) == 0 {
		return nil, errors.New("keychain: empty secret")
	}

	if !bytes.HasPrefix(data, []byte(goKeyringBase64Prefix)) && isEvenHex(data) {
		if decoded, err := hex.DecodeString(string(data)); err == nil && len(decoded) > 0 && utf8.Valid(decoded) {
			trimmed := bytes.TrimSpace(decoded)
			if bytes.HasPrefix(decoded, []byte(goKeyringBase64Prefix)) ||
				(len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')) {
				data = decoded
			}
		}
	}

	if bytes.HasPrefix(data, []byte(goKeyringBase64Prefix)) {
		encoded := bytes.TrimSpace(data[len(goKeyringBase64Prefix):])
		decoded, err := base64.StdEncoding.DecodeString(string(encoded))
		if err != nil {
			return nil, fmt.Errorf("keychain: decode go-keyring base64 payload: %w", err)
		}
		data = decoded
	}

	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("keychain: decoded secret is empty")
	}
	return data, nil
}

// EncodeGoKeyringSecret encodes blob in zalando/go-keyring's base64-prefixed
// format so ASCII-safe single-line writes round-trip cleanly through `security`
// and are decoded natively by agy on macOS.
func EncodeGoKeyringSecret(blob []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(blob)
	out := make([]byte, len(goKeyringBase64Prefix)+len(encoded))
	copy(out, goKeyringBase64Prefix)
	copy(out[len(goKeyringBase64Prefix):], encoded)
	return out
}

func isEvenHex(data []byte) bool {
	if len(data) == 0 || len(data)%2 != 0 {
		return false
	}
	for _, b := range data {
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')) {
			return false
		}
	}
	return true
}

func validAgyPayload(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		var probe any
		return json.Unmarshal(trimmed, &probe) == nil
	}
	return true
}

// ReadAgy returns the decoded Antigravity CLI (agy) OAuth token payload from
// the macOS login keychain (service "gemini", account "antigravity").
func ReadAgy() ([]byte, error) {
	raw, err := Get(AgyService, AgyAccount)
	if err != nil {
		return nil, err
	}
	secret, err := DecodeGoKeyringSecret(raw)
	if err != nil {
		return nil, fmt.Errorf("keychain: item %q/%q: %w", AgyService, AgyAccount, err)
	}
	if !validAgyPayload(secret) {
		return nil, fmt.Errorf("keychain: item %q/%q does not hold a valid credential", AgyService, AgyAccount)
	}
	return secret, nil
}

// WriteAgy stores blob in the macOS login keychain under agy's service/account
// in go-keyring's base64 format, replacing any existing item in place.
func WriteAgy(blob []byte) error {
	if !validAgyPayload(blob) {
		return errors.New("keychain: refusing to store an empty or malformed Antigravity credential")
	}
	return Set(AgyService, AgyAccount, EncodeGoKeyringSecret(blob))
}

// DeleteAgy removes the Antigravity CLI item from the login keychain. A missing
// item, or no keychain at all, is not an error.
func DeleteAgy() error {
	ForgetMirrors()
	return Delete(AgyService, AgyAccount)
}

// EnsureAgyMirror refreshes tokenPath from the macOS login keychain so
// file-shaped authfile operations see the active agy login. Written atomically
// with mode 0600 only when contents differ.
func EnsureAgyMirror(tokenPath string) (bool, error) {
	if !Enabled() {
		return false, ErrNoKeychain
	}
	if err, ok := cachedMirror(tokenPath); ok {
		return false, err
	}
	wrote, err := ensureAgyMirror(tokenPath)
	rememberMirror(tokenPath, err)
	return wrote, err
}

func ensureAgyMirror(tokenPath string) (bool, error) {
	blob, err := ReadAgy()
	if err != nil {
		return false, err
	}
	if existing, readErr := os.ReadFile(tokenPath); readErr == nil && bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(blob)) {
		return false, nil
	}
	if err := writeSecretFile(tokenPath, blob); err != nil {
		return false, err
	}
	return true, nil
}

// PushAgyMirror writes tokenPath's contents back into the macOS login keychain
// so a restored agy vault profile takes effect in desktop agy sessions.
func PushAgyMirror(tokenPath string) error {
	if !Enabled() {
		return ErrNoKeychain
	}
	blob, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("keychain: read %s: %w", tokenPath, err)
	}
	forgetMirror(tokenPath)
	return WriteAgy(bytes.TrimSpace(blob))
}
