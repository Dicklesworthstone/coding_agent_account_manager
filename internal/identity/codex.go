package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ExtractFromCodexAuth reads a Codex auth.json file and extracts identity from the JWT.
//
// Identity claims (email, plan, account id) come from the first token that
// parses, id_token first, since that is where OpenAI puts them. ExpiresAt is
// a different matter: Codex authenticates requests with the access token and
// refreshes it from the refresh token, while the id_token expires shortly
// after login and stays expired for the rest of a working session. So when an
// access token parses, its exp is the identity's expiry, even if the id_token
// supplied the other claims.
func ExtractFromCodexAuth(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read codex auth.json: %w", err)
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, fmt.Errorf("parse codex auth.json: %w", err)
	}

	candidates := codexTokenCandidates(auth)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no token found in auth.json")
	}

	var lastErr error
	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		identity, err := ExtractFromJWT(candidate.value)
		if err != nil {
			lastErr = fmt.Errorf("parse jwt from %s: %w", candidate.source, err)
			continue
		}
		identity.Provider = "codex"
		if exp, ok := codexAccessTokenExpiry(auth); ok {
			identity.ExpiresAt = exp
		}
		return identity, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no token found in auth.json")
}

// codexAccessTokenExpiry returns the exp claim of the first access token in
// auth that parses as a JWT with an expiry.
func codexAccessTokenExpiry(auth map[string]interface{}) (time.Time, bool) {
	for _, candidate := range codexAccessTokenCandidates(auth) {
		if candidate.value == "" {
			continue
		}
		id, err := ExtractFromJWT(candidate.value)
		if err != nil || id.ExpiresAt.IsZero() {
			continue
		}
		return id.ExpiresAt, true
	}
	return time.Time{}, false
}

type tokenCandidate struct {
	value  string
	source string
}

// codexTokenCandidates lists every token that may carry identity claims, in
// preference order: id tokens (top-level, then nested) before access tokens.
func codexTokenCandidates(auth map[string]interface{}) []tokenCandidate {
	candidates := []tokenCandidate{
		{value: stringFromMap(auth, "id_token"), source: "id_token"},
		{value: stringFromMap(auth, "idToken"), source: "idToken"},
	}

	if tokenMap := codexNestedTokens(auth); tokenMap != nil {
		candidates = append(candidates,
			tokenCandidate{value: stringFromMap(tokenMap, "id_token"), source: "tokens.id_token"},
			tokenCandidate{value: stringFromMap(tokenMap, "idToken"), source: "tokens.idToken"},
		)
	}

	return append(candidates, codexAccessTokenCandidates(auth)...)
}

// codexAccessTokenCandidates lists the access-token fields of both layouts,
// top-level first.
func codexAccessTokenCandidates(auth map[string]interface{}) []tokenCandidate {
	candidates := []tokenCandidate{
		{value: stringFromMap(auth, "access_token"), source: "access_token"},
		{value: stringFromMap(auth, "accessToken"), source: "accessToken"},
		{value: stringFromMap(auth, "token"), source: "token"},
	}

	if tokenMap := codexNestedTokens(auth); tokenMap != nil {
		candidates = append(candidates,
			tokenCandidate{value: stringFromMap(tokenMap, "access_token"), source: "tokens.access_token"},
			tokenCandidate{value: stringFromMap(tokenMap, "accessToken"), source: "tokens.accessToken"},
			tokenCandidate{value: stringFromMap(tokenMap, "token"), source: "tokens.token"},
		)
	}

	return candidates
}

// codexNestedTokens returns the "tokens" object of a ChatGPT-mode auth.json,
// or nil when absent.
func codexNestedTokens(auth map[string]interface{}) map[string]interface{} {
	tokenMap, _ := auth["tokens"].(map[string]interface{})
	return tokenMap
}

func stringFromMap(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok {
		return ""
	}
	if str, ok := value.(string); ok {
		return str
	}
	return ""
}
