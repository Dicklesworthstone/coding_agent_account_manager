package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ExtractFromCodexAuth reads a Codex auth.json file and extracts identity from the JWT.
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
		// Identity claims (email, plan, account) come from the id_token, but
		// its lifetime does not describe the account: Codex authenticates API
		// calls with the access token, and the id_token expires an hour after
		// login while the session keeps working. Report the access token's
		// expiry so a healthy account is not shown as expired (#22).
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

type tokenCandidate struct {
	value  string
	source string
}

// codexTokenCandidates lists the tokens to mine for identity claims, id_tokens
// first: they carry the account's email, plan, and account id.
func codexTokenCandidates(auth map[string]interface{}) []tokenCandidate {
	return append(codexIDTokenCandidates(auth), codexAccessTokenCandidates(auth)...)
}

func codexIDTokenCandidates(auth map[string]interface{}) []tokenCandidate {
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

	return candidates
}

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

func codexNestedTokens(auth map[string]interface{}) map[string]interface{} {
	rawTokens, ok := auth["tokens"]
	if !ok {
		return nil
	}
	tokenMap, ok := rawTokens.(map[string]interface{})
	if !ok {
		return nil
	}
	return tokenMap
}

// codexAccessTokenExpiry returns the exp claim of the first parseable access
// token, which is the credential the API actually checks.
func codexAccessTokenExpiry(auth map[string]interface{}) (time.Time, bool) {
	for _, candidate := range codexAccessTokenCandidates(auth) {
		if candidate.value == "" {
			continue
		}
		identity, err := ExtractFromJWT(candidate.value)
		if err != nil || identity.ExpiresAt.IsZero() {
			continue
		}
		return identity.ExpiresAt, true
	}
	return time.Time{}, false
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
