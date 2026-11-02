// Package identity extracts account identity details from provider auth artifacts.
package identity

import "time"

// Identity captures account metadata extracted from auth files.
type Identity struct {
	Email          string    `json:"email,omitempty"`
	Organization   string    `json:"organization,omitempty"`
	OrganizationID string    `json:"organization_id,omitempty"`
	PlanType       string    `json:"plan_type,omitempty"`
	AccountID      string    `json:"account_id,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	Provider       string    `json:"provider"`
	// Verified means the provider confirmed ownership of these credentials.
	// It does not imply that the access token remains unexpired or unrevoked.
	Verified bool `json:"identity_verified,omitempty"`
}
