package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GrokFetcher reads Grok Build billing through the authenticated native ACP
// process (`grok agent stdio`, method `_x.ai/billing`). It does not send a
// prompt and does not call a model.
type GrokFetcher struct {
	// Bin overrides the grok executable. Empty looks it up on PATH.
	Bin string
}

// NewGrokFetcher creates a fetcher that uses the grok binary on PATH.
func NewGrokFetcher() *GrokFetcher {
	return &GrokFetcher{}
}

// Fetch loads billing for the profile whose GROK_HOME is grokHome (the
// directory that contains auth.json, or that file's path). The home may be
// prefixed with "grok-home:" so callers can tell a path from an access token.
func (f *GrokFetcher) Fetch(ctx context.Context, grokHome string) (*UsageInfo, error) {
	info := &UsageInfo{
		Provider:    "grok",
		FetchedAt:   time.Now(),
		Source:      SourceAPI,
		QuotaStatus: QuotaUnavailable,
	}
	if f == nil {
		f = NewGrokFetcher()
	}
	home := strings.TrimPrefix(strings.TrimSpace(grokHome), "grok-home:")
	if home == "" || strings.ContainsAny(home, "\r\n") {
		info.Error = "no grok profile home"
		info.QuotaNote = info.Error
		return info, nil
	}
	if st, err := os.Stat(home); err == nil && !st.IsDir() {
		home = filepath.Dir(home)
	}
	authPath := filepath.Join(home, "auth.json")
	if _, err := os.Stat(authPath); err != nil {
		info.Error = "grok auth.json not found for this profile"
		info.QuotaNote = info.Error
		return info, nil
	}

	raw, err := f.queryBilling(ctx, home)
	if err != nil {
		info.Error = err.Error()
		info.QuotaNote = info.Error
		return info, nil
	}
	parsed := parseGrokBilling(raw, info.FetchedAt)
	if email := grokAccountEmail(authPath); email != "" {
		parsed.AccountID = email
	}
	return parsed, nil
}

// queryBilling copies the profile's auth into a private GROK_HOME and asks
// the CLI for `_x.ai/billing`. The copy is removed before returning. Nothing
// from the credential file is written to the error text.
func (f *GrokFetcher) queryBilling(ctx context.Context, srcHome string) (json.RawMessage, error) {
	stage, err := stageGrokHome(srcHome)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	bin := f.Bin
	if bin == "" {
		p, lookErr := exec.LookPath("grok")
		if lookErr != nil {
			return nil, fmt.Errorf("grok is not installed")
		}
		bin = p
	}

	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	sock := filepath.Join(stage, "leader.sock")
	cmd := exec.CommandContext(cctx, bin, "agent", "--no-leader", "stdio", "--leader-socket", sock)
	cmd.Env = grokChildEnv(stage)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("grok billing: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("grok billing: %w", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start grok agent: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	nextID := 1
	call := func(method string, params any) (json.RawMessage, error) {
		want := nextID
		nextID++
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      want,
			"method":  method,
			"params":  params,
		})
		if err != nil {
			return nil, err
		}
		if _, err := stdin.Write(append(body, '\n')); err != nil {
			return nil, fmt.Errorf("grok billing request failed")
		}
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var msg struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(line, &msg); err != nil || msg.ID == nil || *msg.ID != want {
				continue
			}
			if msg.Error != nil {
				return nil, fmt.Errorf("grok %s: %s", method, sanitizeProviderText(msg.Error.Message))
			}
			return msg.Result, nil
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("grok billing response was not readable")
		}
		return nil, fmt.Errorf("grok closed before %s completed", method)
	}

	if _, err := call("initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]string{"name": "caam", "version": "0"},
	}); err != nil {
		return nil, err
	}
	if _, err := call("authenticate", map[string]any{"methodId": "cached_token"}); err != nil {
		return nil, err
	}
	// session/new establishes the ACP session the extension is served on.
	// It is not a model prompt; no session/prompt is sent.
	if _, err := call("session/new", map[string]any{"cwd": stage, "mcpServers": []any{}}); err != nil {
		return nil, err
	}
	return call("_x.ai/billing", map[string]any{})
}

func stageGrokHome(src string) (string, error) {
	stage, err := os.MkdirTemp("", "caam-grok-billing-")
	if err != nil {
		return "", err
	}
	if err := copyCredentialFile(filepath.Join(src, "auth.json"), filepath.Join(stage, "auth.json")); err != nil {
		os.RemoveAll(stage)
		return "", fmt.Errorf("stage grok auth: %w", err)
	}
	cfg := filepath.Join(src, "config.toml")
	if _, err := os.Stat(cfg); err == nil {
		_ = copyCredentialFile(cfg, filepath.Join(stage, "config.toml"))
	}
	return stage, nil
}

func copyCredentialFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0600)
}

func grokChildEnv(home string) []string {
	drop := map[string]struct{}{
		"HOME": {}, "GROK_HOME": {}, "GROK_DEPLOYMENT_KEY": {}, "XAI_API_KEY": {},
	}
	out := make([]string, 0, 32)
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, skip := drop[key]; skip {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "HOME="+home, "GROK_HOME="+home)
	return out
}

// sanitizeProviderText keeps a short CLI error and drops anything that looks
// like a credential.
func sanitizeProviderText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "billing request failed"
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "bearer ") || strings.Contains(s, "eyJ") || len(s) > 240 {
		return "billing request failed"
	}
	return s
}

// parseGrokBilling turns an `_x.ai/billing` result into UsageInfo.
// Missing or non-numeric usage fields leave the percentage unknown.
func parseGrokBilling(raw []byte, now time.Time) *UsageInfo {
	info := &UsageInfo{
		Provider:    "grok",
		FetchedAt:   now,
		Source:      SourceAPI,
		QuotaStatus: QuotaDegraded,
		QuotaNote:   "grok billing response did not include a usage percentage",
	}
	if now.IsZero() {
		info.FetchedAt = time.Now()
	}
	if len(bytes.TrimSpace(raw)) == 0 || !json.Valid(raw) {
		info.QuotaStatus = QuotaUnavailable
		info.Error = "grok billing response was not valid JSON"
		info.QuotaNote = info.Error
		return info
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		info.QuotaStatus = QuotaUnavailable
		info.Error = "grok billing response was not a JSON object"
		info.QuotaNote = info.Error
		return info
	}

	info.PlanType = firstString(root, "subscription_tier", "subscriptionTier")
	bill := &BillingSnapshot{}
	if v, ok := firstRaw(root, "on_demand_enabled", "onDemandEnabled"); ok {
		if b, ok := jsonBool(v); ok {
			bill.OnDemandEnabled = &b
		}
	}

	cfgRaw, hasCfg := firstRaw(root, "config")
	if !hasCfg || string(bytes.TrimSpace(cfgRaw)) == "null" {
		info.Billing = emptyBilling(bill)
		if info.PlanType != "" {
			info.QuotaNote = "grok billing response had no config object, so no usage percentage is available"
		}
		return info
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil || cfg == nil {
		info.QuotaStatus = QuotaUnavailable
		info.Error = "grok billing config was not a JSON object"
		info.QuotaNote = info.Error
		return info
	}

	if period, ok := firstRaw(cfg, "currentPeriod", "current_period"); ok {
		var p map[string]json.RawMessage
		if json.Unmarshal(period, &p) == nil {
			bill.PeriodType = firstString(p, "type", "periodType", "period_type")
			bill.PeriodStart = firstString(p, "start")
			bill.PeriodEnd = firstString(p, "end")
		}
	}
	if bill.PeriodStart == "" {
		bill.PeriodStart = firstString(cfg, "billingPeriodStart", "billing_period_start")
	}
	if bill.PeriodEnd == "" {
		bill.PeriodEnd = firstString(cfg, "billingPeriodEnd", "billing_period_end")
	}
	bill.OnDemandCapCents = centPtr(cfg, "onDemandCap", "on_demand_cap")
	bill.OnDemandUsedCents = centPtr(cfg, "onDemandUsed", "on_demand_used")
	bill.PrepaidBalanceCents = centPtr(cfg, "prepaidBalance", "prepaid_balance")
	if v, ok := firstRaw(cfg, "isUnifiedBillingUser", "is_unified_billing_user"); ok {
		if b, ok := jsonBool(v); ok {
			bill.Unified = &b
		}
	}
	info.Billing = emptyBilling(bill)

	if bill.PrepaidBalanceCents != nil {
		has := *bill.PrepaidBalanceCents > 0
		info.Credits = &CreditInfo{HasCredits: has}
	}

	reset := parseProviderTime(bill.PeriodEnd)
	var measured *UsageWindow
	if pct, ok := jsonFloat(firstPresent(cfg, "creditUsagePercent", "credit_usage_percent")); ok {
		if pct < 0 || pct > 100 {
			info.QuotaNote = "grok creditUsagePercent was outside 0-100, so it was not used"
		} else {
			measured = percentWindow(pct, reset, bill.PeriodType, bill.PeriodStart, bill.PeriodEnd)
		}
	}
	if measured == nil {
		limit, limitOK := centValue(cfg, "monthlyLimit", "monthly_limit")
		used, usedOK := centValue(cfg, "used")
		if limitOK && usedOK && limit > 0 {
			pct := float64(used) / float64(limit) * 100
			if pct < 0 || pct > 100 {
				info.QuotaNote = "grok used/limit ratio was outside 0-100, so it was not used"
			} else {
				measured = percentWindow(pct, reset, bill.PeriodType, bill.PeriodStart, bill.PeriodEnd)
			}
		}
	}

	if measured != nil {
		info.PrimaryWindow = measured
		info.QuotaStatus = QuotaOK
		info.QuotaNote = ""
		info.Error = ""
		return info
	}

	// Period bounds are real even when the percentage is not. Keep them on an
	// unmeasured window so the reset is visible, and leave the percent unknown.
	if !reset.IsZero() || bill.PeriodStart != "" {
		info.PrimaryWindow = &UsageWindow{
			ResetsAt:       reset,
			WindowDuration: periodDuration(bill.PeriodType, bill.PeriodStart, bill.PeriodEnd),
			Kind:           "billing",
			Label:          "included",
			Unmeasured:     true,
		}
	}
	return info
}

func percentWindow(pct float64, reset time.Time, periodType, start, end string) *UsageWindow {
	return &UsageWindow{
		Utilization:    pct / 100,
		UsedPercent:    int(pct),
		ResetsAt:       reset,
		WindowDuration: periodDuration(periodType, start, end),
		Kind:           "billing",
		Label:          "included",
	}
}

func periodDuration(periodType, start, end string) time.Duration {
	if s, e := parseProviderTime(start), parseProviderTime(end); !s.IsZero() && e.After(s) {
		return e.Sub(s)
	}
	switch {
	case strings.Contains(strings.ToUpper(periodType), "WEEK"):
		return 7 * 24 * time.Hour
	case strings.Contains(strings.ToUpper(periodType), "MONTH"):
		return 30 * 24 * time.Hour
	default:
		return 0
	}
}

func emptyBilling(b *BillingSnapshot) *BillingSnapshot {
	if b == nil {
		return nil
	}
	if b.PeriodType == "" && b.PeriodStart == "" && b.PeriodEnd == "" &&
		b.OnDemandCapCents == nil && b.OnDemandUsedCents == nil &&
		b.PrepaidBalanceCents == nil && b.Unified == nil && b.OnDemandEnabled == nil {
		return nil
	}
	return b
}

func parseProviderTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func firstString(obj map[string]json.RawMessage, keys ...string) string {
	raw, ok := firstRaw(obj, keys...)
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func firstRaw(obj map[string]json.RawMessage, keys ...string) (json.RawMessage, bool) {
	for _, k := range keys {
		if raw, ok := obj[k]; ok && len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
			return raw, true
		}
	}
	return nil, false
}

func firstPresent(obj map[string]json.RawMessage, keys ...string) json.RawMessage {
	raw, _ := firstRaw(obj, keys...)
	return raw
}

func jsonFloat(raw json.RawMessage) (float64, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, false
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, false
	}
	var parsed float64
	if _, err := fmt.Sscan(strings.TrimSpace(s), &parsed); err != nil {
		return 0, false
	}
	return parsed, true
}

func jsonBool(raw json.RawMessage) (bool, bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

// centPtr returns a cents value when the field is present. A JSON number,
// a string, or {"val": n} all count. Absence stays nil, including when the
// key is missing, so a reported zero is preserved.
func centPtr(obj map[string]json.RawMessage, keys ...string) *int64 {
	raw, ok := firstRaw(obj, keys...)
	if !ok {
		return nil
	}
	n, ok := parseCent(raw)
	if !ok {
		return nil
	}
	return &n
}

func centValue(obj map[string]json.RawMessage, keys ...string) (int64, bool) {
	p := centPtr(obj, keys...)
	if p == nil {
		return 0, false
	}
	return *p, true
}

func parseCent(raw json.RawMessage) (int64, bool) {
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
	if err := json.Unmarshal(raw, &s); err == nil {
		var parsed int64
		if _, err := fmt.Sscan(strings.TrimSpace(s), &parsed); err == nil {
			return parsed, true
		}
		return 0, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, false
	}
	// An empty object is proto3's zero Cent: the value was sent and it is 0.
	if len(obj) == 0 {
		zero := int64(0)
		return zero, true
	}
	if v, ok := obj["val"]; ok {
		return parseCent(v)
	}
	return 0, false
}

// grokAccountEmail returns the account email from auth.json when the file
// has one. It never returns token material.
func grokAccountEmail(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return ""
	}
	return findEmail(root)
}

func findEmail(v any) string {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["email"].(string); ok {
			s = strings.TrimSpace(s)
			if strings.Contains(s, "@") && len(s) < 200 && !strings.Contains(s, " ") {
				return s
			}
		}
		for k, child := range t {
			switch strings.ToLower(k) {
			case "key", "token", "access_token", "refresh_token", "id_token", "secret":
				continue
			}
			if email := findEmail(child); email != "" {
				return email
			}
		}
	case []any:
		for _, child := range t {
			if email := findEmail(child); email != "" {
				return email
			}
		}
	}
	return ""
}
