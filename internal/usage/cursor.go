package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cursorService         = "/aiserver.v1.DashboardService/"
	cursorUsagePath       = cursorService + "GetUsageLimitStatusAndActiveGrants"
	cursorPeriodUsagePath = cursorService + "GetCurrentPeriodUsage"
	cursorPlanInfoPath    = cursorService + "GetPlanInfo"
)

// CursorFetcher calls Cursor's authenticated DashboardService RPC for
// GetUsageLimitStatusAndActiveGrants. It reads the access token from the
// profile's own auth file and sends it as a header. The token is never
// placed on a command line or written into an error.
type CursorFetcher struct {
	// BaseURL overrides the API host. Empty uses https://api2.cursor.sh,
	// or CURSOR_API_ENDPOINT when that is set.
	BaseURL string
	// ClientVersion is sent as x-cursor-client-version. Empty detects
	// `cursor-agent --version` once, then falls back to "cli-caam".
	ClientVersion string
	HTTP          *http.Client
}

// NewCursorFetcher creates a fetcher for the live Cursor API.
func NewCursorFetcher() *CursorFetcher {
	return &CursorFetcher{}
}

// Fetch loads limit status for a Cursor credential root. locator is a
// directory (or auth.json path) that contains only that profile's files,
// optionally prefixed with "cursor-root:". The machine-global Cursor config
// is never consulted.
func (f *CursorFetcher) Fetch(ctx context.Context, locator string) (*UsageInfo, error) {
	now := time.Now()
	info := &UsageInfo{
		Provider:    "cursor",
		FetchedAt:   now,
		Source:      SourceAPI,
		QuotaStatus: QuotaUnavailable,
	}
	if f == nil {
		f = NewCursorFetcher()
	}
	root := strings.TrimPrefix(strings.TrimSpace(locator), "cursor-root:")
	authPath, err := findCursorAuth(root)
	if err != nil {
		info.Error = err.Error()
		info.QuotaNote = info.Error
		return info, nil
	}
	token, err := readCursorAccessToken(authPath)
	if err != nil {
		info.Error = "cursor access token is missing or unreadable"
		info.QuotaNote = info.Error
		return info, nil
	}

	// Included remaining/limit lives on GetCurrentPeriodUsage. The limit-status
	// call carries stage and grants, which are empty on an account that has
	// not hit a limit, so it cannot be the only read.
	periodBody, periodCode, periodErr := f.post(ctx, token, cursorPeriodUsagePath)
	statusBody, statusCode, statusErr := f.post(ctx, token, cursorUsagePath)
	planBody, planCode, _ := f.post(ctx, token, cursorPlanInfoPath)

	var period, status *UsageInfo
	if periodErr == nil && periodCode >= 200 && periodCode < 300 {
		period = parseCursorPeriod(periodBody, now)
	}
	if statusErr == nil && statusCode >= 200 && statusCode < 300 {
		status = parseCursorUsage(statusBody, now)
	}

	switch {
	case period != nil && period.QuotaStatus == QuotaOK:
		info = period
		if status != nil {
			if status.LimitStage != "" {
				info.LimitStage = status.LimitStage
			}
			info.Grants = append(info.Grants, status.Grants...)
		}
	case status != nil && status.QuotaStatus == QuotaOK:
		info = status
	case period != nil && period.QuotaStatus == QuotaDegraded:
		info = period
		if status != nil && status.LimitStage != "" {
			info.LimitStage = status.LimitStage
		}
	case status != nil && status.Error == "":
		info = status
	default:
		msg := "cursor usage request failed"
		switch {
		case periodErr != nil:
			msg = periodErr.Error()
		case periodCode >= 300:
			msg = fmt.Sprintf("cursor usage HTTP %d", periodCode)
		case statusErr != nil:
			msg = statusErr.Error()
		case statusCode >= 300:
			msg = fmt.Sprintf("cursor usage HTTP %d", statusCode)
		}
		info.Error = msg
		info.QuotaNote = msg
		info.QuotaStatus = QuotaUnavailable
	}
	if planCode >= 200 && planCode < 300 {
		if name := parseCursorPlanName(planBody); name != "" {
			info.PlanType = name
		}
	}
	if email := cursorAccountEmail(root, authPath); email != "" {
		info.AccountID = email
	}
	return info, nil
}

func (f *CursorFetcher) post(ctx context.Context, token, path string) ([]byte, int, error) {
	base := strings.TrimRight(f.BaseURL, "/")
	if base == "" {
		base = strings.TrimRight(os.Getenv("CURSOR_API_ENDPOINT"), "/")
	}
	if base == "" {
		base = "https://api2.cursor.sh"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader("{}"))
	if err != nil {
		return nil, 0, fmt.Errorf("cursor usage request could not be built")
	}
	version := f.ClientVersion
	if version == "" {
		version = detectedCursorVersion()
	}
	if !strings.HasPrefix(version, "cli-") {
		version = "cli-" + version
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-cursor-client-type", "cli")
	req.Header.Set("x-cursor-client-version", version)
	req.Header.Set("User-Agent", "OpenAI File Downloader, XaiImageApiFetch/1.0")

	client := f.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cursor usage request failed")
	}
	defer resp.Body.Close()
	// Cap the body. A usage payload is small; a huge body is not trusted.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("cursor usage response was not readable")
	}
	return body, resp.StatusCode, nil
}

// cursorVaultLocator is the Fetch key for a vault profile directory, which
// stores the XDG credential as xdg-auth.json so it does not collide with the
// legacy auth.json copy.
func cursorVaultLocator(profileDir string) (string, error) {
	for _, name := range []string{"xdg-auth.json", "auth.json"} {
		path := filepath.Join(profileDir, name)
		if fileHasCursorAccessToken(path) {
			return "cursor-root:" + path, nil
		}
	}
	return "", fmt.Errorf("cursor access token not found")
}

// findCursorAuth locates auth.json under root and nowhere else.
func findCursorAuth(root string) (string, error) {
	if root == "" || strings.ContainsAny(root, "\r\n") {
		return "", fmt.Errorf("no cursor profile root")
	}
	if st, err := os.Stat(root); err == nil && !st.IsDir() {
		if fileHasCursorAccessToken(root) {
			return root, nil
		}
		return "", fmt.Errorf("cursor auth file has no access token")
	}
	candidates := []string{
		filepath.Join(root, "xdg-auth.json"),
		filepath.Join(root, "auth.json"),
		filepath.Join(root, "xdg_config", "cursor", "auth.json"),
		filepath.Join(root, "home", ".cursor", "auth.json"),
		filepath.Join(root, "cursor", "auth.json"),
		filepath.Join(root, ".config", "cursor", "auth.json"),
		filepath.Join(root, ".cursor", "auth.json"),
	}
	for _, path := range candidates {
		if fileHasCursorAccessToken(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("cursor access token not found in this profile")
}

func fileHasCursorAccessToken(path string) bool {
	tok, err := readCursorAccessToken(path)
	return err == nil && tok != ""
}

func readCursorAccessToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	for _, key := range []string{"accessToken", "access_token"} {
		raw, ok := parsed[key]
		if !ok {
			continue
		}
		var token string
		if err := json.Unmarshal(raw, &token); err != nil {
			continue
		}
		token = strings.TrimSpace(token)
		if token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("missing")
}

// cursorAccountEmail reads authInfo.email from a cli-config next to the
// credential. It is a label only.
func cursorAccountEmail(root, authPath string) string {
	dirs := []string{filepath.Dir(authPath)}
	if root != "" {
		dirs = append(dirs,
			root,
			filepath.Join(root, "xdg_config", "cursor"),
			filepath.Join(root, "cursor"),
			filepath.Join(root, ".cursor"),
			filepath.Join(root, "home", ".cursor"),
		)
	}
	names := []string{"cli-config.json", "xdg-cli-config.json"}
	for _, dir := range dirs {
		for _, name := range names {
			if email := emailFromCursorConfig(filepath.Join(dir, name)); email != "" {
				return email
			}
		}
	}
	return ""
}

func emailFromCursorConfig(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return ""
	}
	raw, ok := root["authInfo"]
	if !ok {
		return ""
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return ""
	}
	email := strings.TrimSpace(info.Email)
	if !strings.Contains(email, "@") || len(email) > 200 {
		return ""
	}
	return email
}

// parseCursorPeriod reads GetCurrentPeriodUsage. included_spend / limit is
// the included-usage percentage the CLI prints ("You've used N%"). A reset
// alone, or a percent field with no spend and no limit, is not a measurement.
func parseCursorPeriod(raw []byte, now time.Time) *UsageInfo {
	info := &UsageInfo{
		Provider:    "cursor",
		FetchedAt:   now,
		Source:      SourceAPI,
		QuotaStatus: QuotaDegraded,
		QuotaNote:   "cursor period usage did not include an included spend and limit",
	}
	if now.IsZero() {
		info.FetchedAt = time.Now()
	}
	if !json.Valid(raw) {
		info.QuotaStatus = QuotaUnavailable
		info.Error = "cursor period usage was not valid JSON"
		info.QuotaNote = info.Error
		return info
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		info.QuotaStatus = QuotaUnavailable
		info.Error = "cursor period usage was not a JSON object"
		info.QuotaNote = info.Error
		return info
	}
	var reset time.Time
	if ms, ok := jsonInt(firstPresent(root, "billingCycleEnd", "billing_cycle_end")); ok && ms > 0 {
		reset = time.UnixMilli(ms).UTC()
	}
	usageRaw, ok := firstRaw(root, "planUsage", "plan_usage")
	if !ok {
		if !reset.IsZero() {
			info.PrimaryWindow = &UsageWindow{ResetsAt: reset, Kind: "included", Label: "included", Unmeasured: true}
		}
		return info
	}
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(usageRaw, &plan); err != nil || plan == nil {
		info.QuotaStatus = QuotaUnavailable
		info.Error = "cursor plan usage was not a JSON object"
		info.QuotaNote = info.Error
		return info
	}
	limit, limitOK := jsonInt(firstPresent(plan, "limit"))
	included, includedOK := jsonInt(firstPresent(plan, "includedSpend", "included_spend"))
	remaining, remainingOK := jsonInt(firstPresent(plan, "remaining"))
	var used int64
	switch {
	case limitOK && limit > 0 && includedOK && included >= 0 && included <= limit:
		used = included
	case limitOK && limit > 0 && remainingOK && remaining >= 0 && remaining <= limit:
		used = limit - remaining
	default:
		if !reset.IsZero() {
			info.PrimaryWindow = &UsageWindow{ResetsAt: reset, Kind: "included", Label: "included", Unmeasured: true}
		}
		return info
	}
	pct := float64(used) / float64(limit) * 100
	info.PrimaryWindow = &UsageWindow{
		Utilization: pct / 100,
		UsedPercent: int(math.Round(pct)),
		ResetsAt:    reset,
		Kind:        "included",
		Label:       "included",
	}
	if start, ok := jsonInt(firstPresent(root, "billingCycleStart", "billing_cycle_start")); ok && start > 0 && !reset.IsZero() {
		info.PrimaryWindow.WindowDuration = reset.Sub(time.UnixMilli(start))
	}
	info.QuotaStatus = QuotaOK
	info.QuotaNote = ""
	info.Error = ""
	return info
}

// parseCursorPlanName reads GetPlanInfo's plan name. Empty when the field is
// missing. The name is a short label such as "Ultra", not a secret.
func parseCursorPlanName(raw []byte) string {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return ""
	}
	infoRaw, ok := firstRaw(root, "planInfo", "plan_info")
	if !ok {
		return ""
	}
	var info map[string]json.RawMessage
	if err := json.Unmarshal(infoRaw, &info); err != nil {
		return ""
	}
	name := firstString(info, "planName", "plan_name")
	if name == "" || len(name) > 40 {
		return ""
	}
	return name
}

// parseCursorUsage normalizes a GetUsageLimitStatusAndActiveGrants JSON body.
// Proto JSON sends int64 as strings and omits empty fields. A stage without
// a used/remaining pair does not become a percentage.
func parseCursorUsage(raw []byte, now time.Time) *UsageInfo {
	info := &UsageInfo{
		Provider:    "cursor",
		FetchedAt:   now,
		Source:      SourceAPI,
		QuotaStatus: QuotaDegraded,
		QuotaNote:   "cursor usage response did not include a used/remaining amount",
	}
	if now.IsZero() {
		info.FetchedAt = time.Now()
	}
	if !json.Valid(raw) {
		info.QuotaStatus = QuotaUnavailable
		info.Error = "cursor usage response was not valid JSON"
		info.QuotaNote = info.Error
		return info
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		info.QuotaStatus = QuotaUnavailable
		info.Error = "cursor usage response was not a JSON object"
		info.QuotaNote = info.Error
		return info
	}

	var reset time.Time
	if policy, ok := firstRaw(root, "usageLimitPolicyStatus", "usage_limit_policy_status"); ok {
		var p map[string]json.RawMessage
		if json.Unmarshal(policy, &p) == nil && p != nil {
			if stage := cursorStage(firstPresent(p, "stage")); stage != "" {
				info.LimitStage = stage
			}
			if ms, ok := jsonInt(firstPresent(p, "resetAtMs", "reset_at_ms")); ok && ms > 0 {
				reset = time.UnixMilli(ms).UTC()
			}
			if ids := jsonStringList(firstPresent(p, "allowedModelIds", "allowed_model_ids")); len(ids) > 0 {
				info.Grants = append(info.Grants, GrantSnapshot{Type: "allowed_models", Models: ids})
			}
		}
	}

	grantsRaw, _ := firstRaw(root, "activeGrants", "active_grants")
	var grantTotal, grantRemain int64
	var grantN int
	var grantReset time.Time
	if len(grantsRaw) > 0 && string(bytes.TrimSpace(grantsRaw)) != "null" {
		var arr []map[string]json.RawMessage
		if json.Unmarshal(grantsRaw, &arr) == nil {
			for _, g := range arr {
				snap := GrantSnapshot{Type: firstString(g, "grantType", "grant_type")}
				if n, ok := jsonInt(firstPresent(g, "totalCents", "total_cents")); ok {
					snap.TotalCents = &n
				}
				if n, ok := jsonInt(firstPresent(g, "remainingCents", "remaining_cents")); ok {
					snap.RemainingCents = &n
				}
				if ms, ok := jsonInt(firstPresent(g, "expiresAtMs", "expires_at_ms")); ok && ms > 0 {
					t := time.UnixMilli(ms).UTC()
					snap.ExpiresAt = &t
					if grantReset.IsZero() || t.Before(grantReset) {
						grantReset = t
					}
				}
				snap.Models = jsonStringList(firstPresent(g, "allowedModelIds", "allowed_model_ids"))
				info.Grants = append(info.Grants, snap)
				if snap.TotalCents != nil && snap.RemainingCents != nil && *snap.TotalCents > 0 &&
					*snap.RemainingCents >= 0 && *snap.RemainingCents <= *snap.TotalCents {
					grantTotal += *snap.TotalCents
					grantRemain += *snap.RemainingCents
					grantN++
				}
			}
		}
	}

	if reset.IsZero() {
		reset = grantReset
	}
	if grantN > 0 && grantTotal > 0 {
		used := float64(grantTotal-grantRemain) / float64(grantTotal) * 100
		info.PrimaryWindow = &UsageWindow{
			Utilization: used / 100,
			UsedPercent: int(used),
			ResetsAt:    reset,
			Kind:        "grants",
			Label:       "grants",
		}
		info.QuotaStatus = QuotaOK
		info.QuotaNote = ""
		info.Error = ""
		return info
	}

	if !reset.IsZero() || info.LimitStage != "" {
		if !reset.IsZero() {
			info.PrimaryWindow = &UsageWindow{
				ResetsAt:   reset,
				Kind:       "cursor",
				Label:      "limit",
				Unmeasured: true,
			}
		}
		return info
	}
	return info
}

func cursorStage(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return ""
	}
	if n, ok := jsonInt(raw); ok {
		switch n {
		case 1:
			return "AUTO_SWITCH"
		case 2:
			return "SLOW_POOL"
		case 3:
			return "HARD_BLOCK"
		default:
			return ""
		}
	}
	s := firstString(map[string]json.RawMessage{"stage": raw}, "stage")
	s = strings.TrimPrefix(s, "LIMIT_HIT_STAGE_")
	switch s {
	case "", "UNSPECIFIED":
		return ""
	case "AUTO_SWITCH", "SLOW_POOL", "HARD_BLOCK":
		return s
	default:
		// An unknown stage name is surfaced as metadata, not dropped and not
		// turned into a percentage. Keep it only when it looks like a label.
		if len(s) > 0 && len(s) <= 64 && !strings.ContainsAny(s, " \t\r\n") {
			return s
		}
		return ""
	}
}

func jsonInt(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f), true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, false
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func jsonStringList(raw json.RawMessage) []string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

var (
	cursorVersionOnce sync.Once
	cursorVersion     string
)

func detectedCursorVersion() string {
	cursorVersionOnce.Do(func() {
		cursorVersion = "cli-caam"
		bin, err := exec.LookPath("cursor-agent")
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, "--version").Output()
		if err != nil {
			return
		}
		line := strings.TrimSpace(string(out))
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" && len(line) < 80 && !strings.Contains(line, " ") {
			cursorVersion = line
		}
	})
	return cursorVersion
}
