package identity

import (
	"encoding/json"
	"fmt"
	"os"
)

// ExtractFromGeminiConfig reads Gemini/Google auth config and extracts identity.
// Supports settings.json, oauth_creds.json (with JWT id_token decoding), and
// service account JSON files.
func ExtractFromGeminiConfig(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read gemini config: %w", err)
	}

	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse gemini config: %w", err)
	}

	identity := &Identity{Provider: "gemini"}

	identity.Email = pickString(root, "client_email", "user_email", "email")
	if identity.Email == "" {
		if account, ok := root["account"].(map[string]interface{}); ok {
			identity.Email = pickString(account, "email", "user_email")
		}
	}
	if identity.Email == "" {
		if user, ok := root["user"].(map[string]interface{}); ok {
			identity.Email = pickString(user, "email", "user_email")
		}
	}

	// If still no email, try decoding the id_token JWT (oauth_creds.json).
	if identity.Email == "" {
		if idToken, ok := root["id_token"].(string); ok && idToken != "" {
			if claims, err := parseJWTClaims(idToken); err == nil {
				identity.Email = pickString(claims, "email")
				if name := pickString(claims, "name"); name != "" && identity.Organization == "" {
					identity.Organization = name
				}
			}
		}
	}

	if identity.Organization == "" {
		identity.Organization = pickString(root, "project_id", "projectId", "quota_project_id")
	}

	// A refresh_token indicates persistent OAuth auth (Gemini CLI login),
	// which implies a subscription rather than an ad-hoc API key.
	if _, hasRefresh := root["refresh_token"]; hasRefresh {
		if identity.PlanType == "" {
			identity.PlanType = "ultra" // Gemini CLI OAuth = Ultra subscription
		}
	}

	return identity, nil
}
