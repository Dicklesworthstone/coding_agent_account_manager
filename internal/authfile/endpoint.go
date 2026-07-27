package authfile

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Endpoint profiles are the second member of the env-injection profile family
// introduced with token profiles (see token.go). Where a token profile stores
// only a credential, an endpoint profile stores a service endpoint URL plus an
// OPTIONAL bearer token, and run/exec inject provider-specific environment
// variables pointing the wrapped tool at that endpoint:
//
//	ollama   OLLAMA_HOST=<endpoint>                        (no auth)
//	quick    VITE_AGENT_WS_URL=<endpoint>                  (Amazon Quick local
//	         VITE_INSTANCE_TOKEN=<token>                    desktop agent)
//	claude   ANTHROPIC_BASE_URL=<endpoint>                 (anthropic-compatible
//	         ANTHROPIC_AUTH_TOKEN=<token>                   endpoints: GLM,
//	         CLAUDE_CONFIG_DIR=$HOME/.claude-<name>         Moonshot/Kimi, ...)
//
// One representation serves all three: TokenMeta with Type "endpoint" and the
// Endpoint field set, stored in the same vault layout as token profiles:
//
//	vault/<tool>/<name>/token      the bearer token, mode 0600 (absent when the
//	                               provider needs no auth, e.g. ollama)
//	vault/<tool>/<name>/meta.json  TokenMeta with Type: "endpoint" + Endpoint
//
// Because IsTokenProfile treats both family members alike, endpoint profiles
// are first-class everywhere token profiles are: ls, status, activate,
// rotation, cooldowns, run/exec env injection.

// EndpointSpec describes how a provider uses endpoint profiles.
type EndpointSpec struct {
	// TokenRequired indicates the endpoint needs a bearer token/API key
	// alongside the URL (quick, anthropic-compatible claude). When false the
	// profile stores no token at all (ollama).
	TokenRequired bool

	// DefaultEndpoint is used when the user does not supply an endpoint URL
	// explicitly. Empty means an endpoint must always be given (claude:
	// the whole point of the profile is a non-default base URL).
	DefaultEndpoint string

	// Schemes are the allowed URL schemes for this provider's endpoint.
	Schemes []string
}

// endpointSpecs is the single source of truth for which providers support
// endpoint profiles and how.
var endpointSpecs = map[string]EndpointSpec{
	// Local/remote Ollama server; the ollama CLI honors OLLAMA_HOST.
	"ollama": {
		TokenRequired:   false,
		DefaultEndpoint: "http://127.0.0.1:11434",
		Schemes:         []string{"http", "https"},
	},
	// Amazon Quick ships no public API; its desktop app runs a local agent
	// server driven over an authenticated WebSocket. The bearer token is the
	// per-launch VITE_INSTANCE_TOKEN from the agent process environment.
	"quick": {
		TokenRequired:   true,
		DefaultEndpoint: "ws://localhost:8771",
		Schemes:         []string{"ws", "wss", "http", "https"},
	},
	// Anthropic-compatible endpoints (GLM, Moonshot/Kimi, ...): the claude
	// binary is pointed at a different base URL with a provider-issued token.
	"claude": {
		TokenRequired: true,
		Schemes:       []string{"http", "https"},
	},
}

// EndpointSpecFor returns the endpoint-profile spec for a tool, if the tool
// supports endpoint profiles.
func EndpointSpecFor(tool string) (EndpointSpec, bool) {
	spec, ok := endpointSpecs[strings.ToLower(strings.TrimSpace(tool))]
	return spec, ok
}

// ValidateEndpointURL checks that endpoint is an absolute URL with a host and
// a scheme the tool accepts.
func ValidateEndpointURL(tool, endpoint string) error {
	spec, ok := EndpointSpecFor(tool)
	if !ok {
		return fmt.Errorf("endpoint profiles are not supported for %s", tool)
	}
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("endpoint URL must include a host (got %q)", endpoint)
	}
	for _, s := range spec.Schemes {
		if u.Scheme == s {
			return nil
		}
	}
	return fmt.Errorf("endpoint scheme %q not supported for %s (allowed: %s)",
		u.Scheme, tool, strings.Join(spec.Schemes, ", "))
}

// SaveEndpointProfile creates (or overwrites) an endpoint profile in the
// vault. token may be empty only when the provider's spec does not require
// one; the endpoint URL is always required and validated.
func (v *Vault) SaveEndpointProfile(tool, name, endpoint, token, source string) error {
	tool = strings.ToLower(strings.TrimSpace(tool))
	spec, ok := EndpointSpecFor(tool)
	if !ok {
		return fmt.Errorf("endpoint profiles are not supported for %s", tool)
	}

	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return fmt.Errorf("endpoint URL is empty")
	}
	if err := ValidateEndpointURL(tool, endpoint); err != nil {
		return err
	}

	token = strings.TrimSpace(token)
	if spec.TokenRequired && token == "" {
		return fmt.Errorf("%s endpoint profiles require a token", tool)
	}
	if !spec.TokenRequired && token != "" {
		return fmt.Errorf("%s endpoint profiles do not take a token", tool)
	}

	dir, err := v.safeProfileDir(tool, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create endpoint profile dir: %w", err)
	}

	meta := TokenMeta{
		Type:      ProfileTypeEndpoint,
		Provider:  tool,
		Name:      name,
		Endpoint:  endpoint,
		Source:    source,
		CreatedAt: time.Now().UTC(),
	}
	if err := writeTokenProfileFiles(dir, token, meta); err != nil {
		return err
	}
	return nil
}
