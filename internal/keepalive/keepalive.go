package keepalive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
)

const (
	DefaultTTL     = 2 * time.Hour
	DefaultMinGap  = 25 * time.Minute
	DefaultTimeout = 2 * time.Minute
)

// Options controls a bounded native renewal of discovered live grants. The
// native CLI always runs against its owning home; no credential is staged.
type Options struct {
	TTL           time.Duration
	MinGap        time.Duration
	DisableMinGap bool
	Timeout       time.Duration
	DryRun        bool
	StateDir      string
	Now           func() time.Time
	ClaudeBin     string
	GrokBin       string

	// AfterRenew runs under the keepalive lock, after the native CLI exits and
	// the live credential has been checked. It may acquire the native lock to
	// copy a stable, newer credential to matching vault snapshots.
	AfterRenew func(context.Context, Grant, CredentialSnapshot) ([]SyncResult, error)
}

// Result deliberately excludes provider output, tokens, credential hashes, and
// the child environment. Ref identifies the actual live owner for automation.
type Result struct {
	Provider       string       `json:"provider"`
	Profile        string       `json:"profile"`
	Kind           string       `json:"kind"`
	Ref            string       `json:"ref"`
	AuthPath       string       `json:"auth_path"`
	Status         string       `json:"status"`
	Reason         string       `json:"reason,omitempty"`
	Success        bool         `json:"success"`
	Attempted      bool         `json:"attempted"`
	ExpiresBefore  time.Time    `json:"expires_before,omitempty"`
	ExpiresAfter   time.Time    `json:"expires_after,omitempty"`
	NextEligibleAt time.Time    `json:"next_eligible_at,omitempty"`
	SyncedProfiles []string     `json:"synced_profiles,omitempty"`
	Sync           []SyncResult `json:"sync,omitempty"`
}

var errLockBusy = errors.New("keepalive already running for this live grant")

type attemptState struct {
	Version     int       `json:"version"`
	LastAttempt time.Time `json:"last_attempt"`
}

// Run processes live grants independently, retaining a result for every grant.
// Operational failures appear in Results; the returned error is reserved for
// invalid options and cancellation of the overall run.
func Run(ctx context.Context, grants []Grant, opts Options) ([]Result, error) {
	if opts.TTL < 0 || opts.MinGap < 0 || opts.Timeout < 0 {
		return nil, errors.New("keepalive durations must not be negative")
	}
	if opts.TTL == 0 {
		opts.TTL = DefaultTTL
	}
	if opts.MinGap == 0 && !opts.DisableMinGap {
		opts.MinGap = DefaultMinGap
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.StateDir == "" {
		opts.StateDir = filepath.Join(config.DefaultDataPath(), "keepalive")
	}
	results := make([]Result, 0, len(grants))
	seen := make(map[string]bool, len(grants))
	for _, grant := range grants {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if grant.BlockedReason != "" {
			// A diagnostic reference can share the owner's exact path. It
			// must not consume the runnable owner's deduplication key.
			results = append(results, runGrant(ctx, grant, opts))
			continue
		}
		key := grantKey(grant)
		if seen[key] {
			result := grantResult(grant)
			result.Status, result.Reason = "blocked", "duplicate_live_grant"
			results = append(results, result)
			continue
		}
		seen[key] = true
		results = append(results, runGrant(ctx, grant, opts))
	}
	return results, ctx.Err()
}

func grantResult(grant Grant) Result {
	return Result{
		Provider: grant.Provider, Profile: grant.Name, Kind: grant.Kind,
		Ref: grant.Ref(), AuthPath: grant.AuthPath,
	}
}

func runGrant(ctx context.Context, grant Grant, opts Options) Result {
	result := grantResult(grant)
	if grant.BlockedReason != "" {
		result.Status, result.Reason = "blocked", grant.BlockedReason
		if grant.Owner != "" && grant.source != nil {
			result.Status, result.Success = "skipped", true
		}
		return result
	}
	before, err := ReadCredential(grant)
	if err != nil {
		result.Status, result.Reason = "blocked", credentialReadReason(err)
		return result
	}
	result.ExpiresBefore = before.ExpiresAt
	if !SameAccount(grant.Identity, before.Identity) {
		result.Status, result.Reason = "blocked", "account_changed_since_discovery"
		return result
	}
	if before.ExpiresAt.IsZero() {
		result.Status, result.Reason = "blocked", "expiry_unknown"
		return result
	}
	if before.ExpiresAt.Sub(opts.Now()) > opts.TTL {
		result.Status, result.Reason, result.Success = "skipped", "outside_ttl", true
		return result
	}
	if !before.HasRefreshToken {
		result.Status, result.Reason = "blocked", "refresh_credential_missing"
		return result
	}

	statePath := filepath.Join(opts.StateDir, grantKey(grant)+".json")
	// A dry run is read-only, including the state/lock directory. It describes
	// eligibility at this instant; a real run repeats these checks under lock.
	if opts.DryRun {
		if applyMinGap(&result, before, statePath, opts) {
			return result
		}
		result.Status, result.Reason, result.Success = "dry_run", "would_run", true
		return result
	}
	if err := os.MkdirAll(opts.StateDir, 0700); err != nil {
		result.Status, result.Reason = "failed", "state_directory_unavailable"
		return result
	}
	// Keep the lock with the grant, independently of CAAM_HOME/StateDir. A
	// manual call and a timer using different state roots still share it.
	lock, err := os.OpenFile(grant.AuthPath+".caam-keepalive.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		result.Status, result.Reason = "failed", "lock_unavailable"
		return result
	}
	defer lock.Close()
	if err := tryFileLock(lock); err != nil {
		result.Status, result.Reason = "failed", "lock_unavailable"
		if errors.Is(err, errLockBusy) {
			result.Status, result.Reason = "skipped", "already_running"
			result.Success = true
		}
		return result
	}
	defer unlockFile(lock)

	// A different keepalive may have rotated this grant while this caller was
	// discovering it. Re-read instead of executing from the old snapshot.
	before, err = ReadCredential(grant)
	if err != nil {
		result.Status, result.Reason = "blocked", credentialReadReason(err)
		return result
	}
	result.ExpiresBefore = before.ExpiresAt
	if !SameAccount(grant.Identity, before.Identity) {
		result.Status, result.Reason = "blocked", "account_changed_since_discovery"
		return result
	}
	if before.ExpiresAt.IsZero() {
		result.Status, result.Reason = "blocked", "expiry_unknown"
		return result
	}
	if before.ExpiresAt.Sub(opts.Now()) > opts.TTL {
		result.Status, result.Reason, result.Success = "skipped", "outside_ttl", true
		return result
	}
	if !before.HasRefreshToken {
		result.Status, result.Reason = "blocked", "refresh_credential_missing"
		return result
	}
	if applyMinGap(&result, before, statePath, opts) {
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Status, result.Reason = "failed", "cancelled"
		return result
	}

	bin, args, err := nativeCommand(grant.Provider, opts)
	if err != nil {
		result.Status, result.Reason = "failed", "native_cli_unavailable"
		return result
	}
	workDir, err := os.MkdirTemp("", "caam-keepalive-")
	if err != nil {
		result.Status, result.Reason = "failed", "working_directory_unavailable"
		return result
	}
	defer os.RemoveAll(workDir)
	if err := writeAttemptState(statePath, opts.Now()); err != nil {
		result.Status, result.Reason = "failed", "state_write_failed"
		return result
	}

	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	configureNativeProcess(cmd)
	cmd.Dir = workDir
	cmd.Env = nativeEnv(grant, os.Environ())
	// Provider errors can contain opaque credentials. Discard both streams
	// entirely instead of retaining a buffer that might leak through JSON or
	// logs. WaitDelay also bounds descendants retaining these stream pipes.
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	result.Attempted = true
	runErr := cmd.Run()
	// Always inspect the file, including nonzero exit and timeout. Grok can
	// delete a rejected auth.json yet return exit status zero.
	after, readErr := ReadCredential(grant)
	if readErr != nil {
		result.Status, result.Reason = "failed", credentialReadReason(readErr)
		return result
	}
	result.ExpiresAfter = after.ExpiresAt
	if !SameAccount(before.Identity, after.Identity) {
		result.Status, result.Reason = "failed", "account_changed"
		return result
	}
	if after.ExpiresAt.IsZero() {
		result.Status, result.Reason = "failed", "expiry_unknown"
		return result
	}
	if !after.ExpiresAt.After(opts.Now()) {
		result.Status, result.Reason = "failed", "still_expired"
		return result
	}
	rotated := after.Fingerprint != before.Fingerprint
	renewed := rotated && after.ExpiresAt.After(before.ExpiresAt)
	if runErr != nil && !renewed {
		result.Status, result.Reason = "failed", "native_cli_failed"
		if cctx.Err() != nil {
			result.Reason = "native_cli_timeout"
			if ctx.Err() != nil {
				result.Reason = "cancelled"
			}
		}
		return result
	}
	result.Status, result.Success = "still_valid", true
	if rotated {
		result.Status = "rotated"
	}
	if runErr != nil {
		// The native client can rotate successfully and then fail its model
		// request (for example, on quota). A new, extended, valid credential
		// proves that renewal succeeded independently of that request.
		result.Reason = "native_cli_failed_after_rotation"
	}
	if opts.AfterRenew != nil {
		result.Sync, err = opts.AfterRenew(ctx, grant, after)
		for _, syncResult := range result.Sync {
			if syncResult.Status == "synced" {
				result.SyncedProfiles = append(result.SyncedProfiles, syncResult.Profile)
			}
		}
		if err != nil {
			// Renewal succeeded even if a newer or unrecognizable vault copy
			// could not be updated. Do not tell automation the live grant is
			// still expired merely because snapshot synchronization failed.
			result.Reason = "vault_sync_failed"
			result.Sync = append(result.Sync, SyncResult{Status: "failed", Reason: "vault_sync_failed"})
		}
	}
	return result
}

func applyMinGap(result *Result, snapshot CredentialSnapshot, statePath string, opts Options) bool {
	// Expired access credentials must be retried even after a recent failed
	// keepalive. Applying the throttle here is what stranded idle accounts.
	if opts.DisableMinGap || !snapshot.ExpiresAt.After(opts.Now()) {
		return false
	}
	state, err := readAttemptState(statePath)
	if err != nil {
		result.Status, result.Reason = "failed", "state_invalid"
		return true
	}
	if state.LastAttempt.IsZero() {
		return false
	}
	next := state.LastAttempt.Add(opts.MinGap)
	if opts.Now().Before(next) {
		result.Status, result.Reason, result.Success = "skipped", "min_gap", true
		result.NextEligibleAt = next
		return true
	}
	return false
}

func credentialReadReason(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "credential_missing"
	}
	return "credential_invalid"
}

func grantKey(grant Grant) string {
	authPath := filepath.Clean(grant.AuthPath)
	if grant.source != nil {
		// Directory aliases share both a lock and their minimum-gap history.
		authPath = grant.source.canonicalAuth
	}
	sum := sha256.Sum256([]byte(grant.Provider + "\x00" + authPath))
	return hex.EncodeToString(sum[:])
}

func readAttemptState(path string) (attemptState, error) {
	var state attemptState
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return state, errors.New("invalid keepalive state")
	}
	dec := json.NewDecoder(io.LimitReader(f, 4097))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&state); err != nil {
		return state, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return state, errors.New("invalid keepalive state")
	}
	if state.Version != 1 || state.LastAttempt.IsZero() {
		return state, errors.New("invalid keepalive state")
	}
	return state, nil
}

func writeAttemptState(path string, now time.Time) error {
	data, err := json.Marshal(attemptState{Version: 1, LastAttempt: now.UTC()})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".keepalive-state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func nativeCommand(provider string, opts Options) (string, []string, error) {
	var bin string
	var args []string
	switch provider {
	case "claude":
		bin = opts.ClaudeBin
		args = []string{"-p", "ping", "--model", "haiku", "--effort", "low",
			"--no-session-persistence", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
			"--setting-sources", "project", "--settings", `{"disableAllHooks":true}`,
			"--tools", "", "--disable-slash-commands", "--system-prompt", "Reply with one word."}
	case "grok":
		bin = opts.GrokBin
		args = []string{"models"}
	default:
		return "", nil, errors.New("provider has no native keepalive")
	}
	if bin == "" {
		bin = provider
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return "", nil, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", nil, err
	}
	return resolved, args, nil
}

func nativeEnv(grant Grant, inherited []string) []string {
	values := make(map[string]string, len(inherited)+len(grant.Env))
	drop := map[string]bool{
		"GROK_AUTH": true, "GROK_AUTH_PATH": true, "GROK_API_KEY": true,
		"GROK_DEPLOYMENT_KEY": true, "XAI_API_KEY": true, "XAI_API_TOKEN": true,
		"CLAUDE_CODE_SESSION_ACCESS_TOKEN": true, "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR": true,
	}
	for _, key := range grant.Scrub {
		drop[key] = true
	}
	for _, entry := range inherited {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || drop[key] || strings.HasPrefix(key, "ANTHROPIC_") ||
			strings.HasPrefix(key, "CLAUDE_CODE_OAUTH_TOKEN") || strings.HasPrefix(key, "CLAUDE_CODE_USE_") {
			continue
		}
		values[key] = value
	}
	for key, value := range grant.Env {
		values[key] = value
	}
	if grant.Provider == "claude" {
		values["CLAUDE_CODE_DISABLE_AGENT_VIEW"] = "1"
		values["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, fmt.Sprintf("%s=%s", key, values[key]))
	}
	return env
}
