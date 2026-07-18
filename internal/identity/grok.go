package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// ExtractFromGrokAuth reads Grok Build's auth.json (written by `grok login`)
// and extracts identity information.
//
// Observed on a real Grok CLI login (v0.1.210), the file is a JSON object
// keyed by a dynamic credential key of the form "<oidc-issuer>::<client-id>"
// (e.g. "https://auth.x.ai::<uuid>"); the entry object carries the account
// fields, including "email" and "user_id" alongside the token material. The
// CLI's own bundled docs show a different top-level key in their example
// ("https://accounts.x.ai/sign-in"), so top-level keys must be treated as
// opaque: this extractor scans every top-level object value for identity
// fields rather than hard-coding any credential key.
//
// Flat top-level identity fields are checked first so the extractor keeps
// working should a future CLI version flatten the file.
func ExtractFromGrokAuth(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read grok auth file: %w", err)
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, fmt.Errorf("parse grok auth file: %w", err)
	}

	// Flat layout: identity fields directly at the top level.
	if id := grokIdentityFromEntry(auth); id != nil {
		return id, nil
	}

	// Observed layout: dynamic credential key -> entry object with identity
	// fields. Keys are scanned in sorted order for deterministic results when
	// multiple credential entries are present.
	keys := make([]string, 0, len(auth))
	for k := range auth {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Prefer an entry with an email over one with only a user_id.
	var fallback *Identity
	for _, k := range keys {
		entry, ok := auth[k].(map[string]interface{})
		if !ok {
			continue
		}
		id := grokIdentityFromEntry(entry)
		if id == nil {
			continue
		}
		if id.Email != "" && id.Email != id.AccountID {
			return id, nil
		}
		if fallback == nil {
			fallback = id
		}
	}
	if fallback != nil {
		return fallback, nil
	}

	return nil, fmt.Errorf("no identity found in grok auth file")
}

// grokIdentityFromEntry extracts identity fields from a single credential
// entry (or a flat top-level object). Returns nil when no identity-bearing
// field is present.
func grokIdentityFromEntry(entry map[string]interface{}) *Identity {
	id := &Identity{}
	if email := stringFromMap(entry, "email"); email != "" {
		id.Email = email
		if userID := stringFromMap(entry, "user_id"); userID != "" {
			id.AccountID = userID
		}
		return id
	}
	if userID := stringFromMap(entry, "user_id"); userID != "" {
		id.AccountID = userID
		id.Email = userID // display fallback, mirroring ExtractFromGenericAuth
		return id
	}
	return nil
}
