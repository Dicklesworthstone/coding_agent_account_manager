package identity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var errInvalidJWT = errors.New("invalid jwt")

// nestedClaimNamespaces lists JWT claim keys whose values are nested objects
// that may contain identity fields. OpenAI/Codex tokens store plan_type and
// other metadata under "https://api.openai.com/auth".
var nestedClaimNamespaces = []string{
	"https://api.openai.com/auth",
	"https://api.openai.com/profile",
}

// ExtractFromJWT parses a JWT token and extracts identity claims without
// validating the signature.
func ExtractFromJWT(token string) (*Identity, error) {
	claims, err := parseJWTClaims(token)
	if err != nil {
		return nil, err
	}

	identity := &Identity{
		Email:        extractEmailClaim(claims),
		Organization: pickString(claims, "organization", "org", "org_name"),
		PlanType:     pickString(claims, "plan_type", "chatgpt_plan_type", "subscription_type", "planType", "subscriptionType", "plan"),
		AccountID:    pickString(claims, "account_id", "accountId", "user_id", "userId", "uid"),
	}

	if exp, ok := extractExpiry(claims); ok {
		identity.ExpiresAt = exp
	}

	return identity, nil
}

// ChatGPTAccountID returns the ChatGPT account (workspace) id that an OpenAI
// token was issued for, read from the chatgpt_account_id claim under
// "https://api.openai.com/auth". It returns "" when the token is not a JWT or
// carries no such claim.
//
// This is the value the Codex CLI stores as tokens.account_id and sends as
// the ChatGPT-Account-Id header. The same namespace also carries user_id and
// chatgpt_user_id; those identify the person, not the account, and are never
// substituted here.
func ChatGPTAccountID(token string) string {
	claims, err := parseJWTClaims(strings.TrimSpace(token))
	if err != nil {
		return ""
	}
	nested, ok := claims["https://api.openai.com/auth"].(map[string]interface{})
	if !ok {
		return ""
	}
	return valueAsString(nested["chatgpt_account_id"])
}

func parseJWTClaims(token string) (map[string]interface{}, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: expected 3 parts", errInvalidJWT)
	}

	payload := parts[1]
	if payload == "" {
		return nil, fmt.Errorf("%w: empty payload", errInvalidJWT)
	}

	decoded, err := decodeSegment(payload)
	if err != nil {
		return nil, fmt.Errorf("decode jwt payload: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(decoded))
	dec.UseNumber()

	var claims map[string]interface{}
	if err := dec.Decode(&claims); err != nil {
		return nil, fmt.Errorf("parse jwt claims: %w", err)
	}

	return claims, nil
}

func decodeSegment(segment string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(segment); err == nil {
		return decoded, nil
	}

	padded := addBase64Padding(segment)
	if decoded, err := base64.URLEncoding.DecodeString(padded); err == nil {
		return decoded, nil
	}

	return base64.StdEncoding.DecodeString(padded)
}

func addBase64Padding(s string) string {
	switch len(s) % 4 {
	case 2:
		return s + "=="
	case 3:
		return s + "="
	default:
		return s
	}
}

func extractEmailClaim(claims map[string]interface{}) string {
	emailFields := []string{"email", "preferred_username", "upn", "sub"}
	for _, claimMap := range collectClaimMaps(claims) {
		for _, field := range emailFields {
			if value := valueAsString(claimMap[field]); value != "" {
				if field == "sub" && !strings.Contains(value, "@") {
					continue
				}
				return value
			}
		}
	}
	return ""
}

func extractExpiry(claims map[string]interface{}) (time.Time, bool) {
	raw, ok := claims["exp"]
	if !ok {
		return time.Time{}, false
	}

	switch value := raw.(type) {
	case json.Number:
		secs, err := value.Int64()
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(secs, 0).UTC(), true
	case float64:
		return time.Unix(int64(value), 0).UTC(), true
	case float32:
		return time.Unix(int64(value), 0).UTC(), true
	case int64:
		return time.Unix(value, 0).UTC(), true
	case int:
		return time.Unix(int64(value), 0).UTC(), true
	case string:
		secs, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(secs, 0).UTC(), true
	default:
		return time.Time{}, false
	}
}

func pickString(claims map[string]interface{}, keys ...string) string {
	for _, claimMap := range collectClaimMaps(claims) {
		for _, key := range keys {
			if value := valueAsString(claimMap[key]); value != "" {
				return value
			}
		}
	}
	return ""
}

// collectClaimMaps returns the top-level claims map followed by any nested
// namespace maps found under known provider-specific keys. This allows
// pickString and extractEmailClaim to find values regardless of whether a
// provider places them at the top level or inside a namespace object.
func collectClaimMaps(claims map[string]interface{}) []map[string]interface{} {
	maps := []map[string]interface{}{claims}
	for _, ns := range nestedClaimNamespaces {
		if raw, ok := claims[ns]; ok {
			if nested, ok := raw.(map[string]interface{}); ok {
				maps = append(maps, nested)
			}
		}
	}
	return maps
}

func valueAsString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}
