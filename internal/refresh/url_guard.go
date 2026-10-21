package refresh

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Token exchanges and authenticated probes must reach only the endpoint that
// the caller validated. In particular, a 307/308 would replay a refresh token
// (and sometimes a client secret) in the POST body even when net/http strips
// Authorization on a cross-host redirect. A redirected 200 is not evidence
// that the selected endpoint accepted the credential, either.
//
// Refuse ALL redirects, including same-origin ones. The original status stays
// available to the caller's existing error classifier; no extra token exchange,
// login attempt or provider-rejection verdict is authorized by a redirect.
func newTokenHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: tokenEndpointTransport{base: http.DefaultTransport},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type tokenEndpointTransport struct{ base http.RoundTripper }

func (t tokenEndpointTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err != nil || response == nil || response.StatusCode < 300 || response.StatusCode >= 400 {
		return response, err
	}
	// Client parses Location BEFORE CheckRedirect, including an invalid target
	// verbatim in its error. A provider/proxy may echo secrets into that URL.
	// Remove only this unused header before Client sees it, retaining the body,
	// status and retry metadata for normal bounded handling by the caller.
	// Clone rather than changing a response owned by an injected transport.
	blocked := *response
	blocked.Header = response.Header.Clone()
	blocked.Header.Del("Location")
	return &blocked, nil
}

func validateTokenEndpoint(raw string, allowHosts []string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("token endpoint is empty")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid token endpoint: %w", err)
	}

	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return fmt.Errorf("token endpoint missing host")
	}

	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "https" && !(scheme == "http" && isLoopbackHost(host)) {
		return fmt.Errorf("refusing token endpoint scheme %q (host=%q)", scheme, host)
	}

	// Always allow loopback endpoints (used in tests).
	if isLoopbackHost(host) {
		return nil
	}

	for _, allowed := range allowHosts {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return nil
		}
	}

	return fmt.Errorf("refusing token endpoint host %q (not allowlisted)", host)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
