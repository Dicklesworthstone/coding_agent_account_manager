package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/exec"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/wrap"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// getWd allows mocking os.Getwd in tests
var getWd = os.Getwd

// runCmd wraps AI CLI execution with automatic rate limit handling.
var runCmd = &cobra.Command{
	Use:   "run <tool> [-- args...]",
	Short: "Run AI CLI with automatic account switching",
	Long: `Wraps AI CLI execution with transparent rate limit detection and automatic
profile switching. Headless commands use bounded retries when they fail with
a rate limit. Interactive commands retain their native terminal handling.

When a headless command fails with a rate limit:
1. The current profile is put into cooldown
2. The next best profile is automatically selected
3. After the configured backoff, the command is re-executed with the same input

Retry settings come from config.json's wrap section, then per-provider overrides,
then explicit CLI flags. --max-retries 0 disables retries. An observable
Retry-After header sets a minimum wait. Successful commands and other failures
are not retried. A retry repeats the command; any completed work remains.

Use --precheck for proactive switching:
  When enabled, caam checks real-time usage levels BEFORE running and
  automatically switches to a healthier profile if current usage is near
  the limit. This prevents rate limit errors before they happen.
  Supported for claude, codex, grok and cursor. Grok and Cursor only switch
  to an account whose quota was actually measured; when the current
  account's quota cannot be measured, caam says so and does not switch.

Examples:
  caam run claude -- -p "explain this code"
  caam run codex -- exec "write tests"
  caam run gemini -- -p "summarize this file"

  # Proactive switching (checks usage before running)
  caam run claude --precheck -- -p "explain this code"

  # Interactive mode (no command replay)
  caam run claude

For shell integration, add an alias:
  alias claude='caam run claude --precheck --'

Then use native headless arguments when command retries are wanted:
  claude -p "explain this code"`,
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: false,
	RunE:               runWrap,
}

func init() {
	rootCmd.AddCommand(runCmd)
	addRunFlags(runCmd)
}

func addRunFlags(cmd *cobra.Command) {
	cmd.Flags().Int("max-retries", config.DefaultWrapConfig().MaxRetries, "maximum retries or interactive handoffs on rate limit (0 = none; overrides wrap config)")
	cmd.Flags().Duration("cooldown", 60*time.Minute, "cooldown duration after rate limit")
	cmd.Flags().Bool("quiet", false, "suppress profile switch notifications")
	cmd.Flags().String("algorithm", "smart", "rotation algorithm (smart, round_robin, random)")
	cmd.Flags().String("policy", "", "rotation policy: availability (default), drain (prefer soonest-resetting usable quota)")
	cmd.Flags().Bool("precheck", false, "check usage levels before running and switch if near limit")
	cmd.Flags().Float64("precheck-threshold", 0.8, "usage threshold for precheck switching (0-1)")
}

func runWrap(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("tool name required")
	}

	tool := strings.ToLower(args[0])

	// Validate tool
	if _, ok := tools[tool]; !ok {
		return fmt.Errorf("unknown tool: %s (supported: %s)", tool, supportedToolsList())
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := ctx.Err(); err != nil {
		return err
	}

	// Parse CLI args (everything after the tool name)
	var cliArgs []string
	if len(args) > 1 {
		cliArgs = args[1:]
	}

	// Get flags
	quiet, _ := cmd.Flags().GetBool("quiet")
	algorithmStr, _ := cmd.Flags().GetString("algorithm")
	retryConfig, err := loadRunRetryConfig(cmd, tool)
	if err != nil {
		return err
	}
	cooldownDur := retryConfig.CooldownDuration.Duration()

	// Parse algorithm
	var algorithm rotation.Algorithm
	switch strings.ToLower(algorithmStr) {
	case "smart":
		algorithm = rotation.AlgorithmSmart
	case "round_robin", "roundrobin":
		algorithm = rotation.AlgorithmRoundRobin
	case "random":
		algorithm = rotation.AlgorithmRandom
	default:
		return fmt.Errorf("unknown algorithm: %s (supported: smart, round_robin, random)", algorithmStr)
	}

	// Parse policy (issue #81: drain is opt-in; availability is the default)
	policyStr, _ := cmd.Flags().GetString("policy")
	policyStr = strings.ToLower(policyStr)
	switch policyStr {
	case "", "availability", "drain":
	default:
		return fmt.Errorf("unknown policy: %s (supported: availability, drain)", policyStr)
	}

	// Initialize vault
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	// Initialize database
	db, err := getDB()
	if err != nil {
		// Non-fatal: cooldowns won't be recorded but execution can continue
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: database unavailable, cooldowns will not be recorded\n")
		db = nil
	}

	// Load global config
	spmCfg, err := config.LoadSPMConfig()
	if err != nil {
		return fmt.Errorf("load run settings: %w", err)
	}

	// CLI --policy overrides the configured rotation policy
	if policyStr != "" {
		spmCfg.Stealth.Rotation.Policy = policyStr
	}

	// Get working directory
	cwd, err := getWd()
	if err != nil {
		cwd, _ = os.Getwd()
	}

	// Precheck: switch profile if near limit before running
	precheck, _ := cmd.Flags().GetBool("precheck")
	precheckThreshold, _ := cmd.Flags().GetFloat64("precheck-threshold")
	if precheck && isLimitsProvider(tool) {
		if switched := runPrecheck(ctx, tool, precheckThreshold, quiet, db, algorithm, spmCfg, modelFromArgs(cliArgs)); switched && !quiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "caam: switched profile before running (usage was near limit)\n")
		}
	} else if precheck {
		// Loud fallback (issue #79): usage prechecking needs real-time limit
		// support. Say so — on stderr, even in quiet mode — instead of
		// silently ignoring the flag.
		fmt.Fprintf(cmd.ErrOrStderr(), "caam: --precheck is not supported for %q (real-time limits are implemented for %s); running without a usage precheck\n", tool, strings.Join(limitsProviders, ", "))
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Initialize AuthPool (if enabled in config)
	var pool *authpool.AuthPool
	if spmCfg.Daemon.AuthPool.Enabled {
		pool = authpool.NewAuthPool(authpool.WithVault(vault))
		// Best effort load
		_ = pool.Load(authpool.PersistOptions{})
	}

	// Initialize Rotation Selector
	selector := rotation.NewSelector(algorithm, healthStore, db)
	bindRotationVault(selector)
	applyRotationPolicy(selector, spmCfg, "")

	// Initialize Runner
	if runner == nil {
		// Should be initialized in Root PersistentPreRunE, but defensive check
		runner = exec.NewRunner(registry)
	}

	// Initialize Notifier: account switches reach the terminal (unless
	// --quiet) and the configured desktop/webhook channels, which matter
	// most when the session is running in a window the user isn't watching.
	channels := notifierChannels{External: true}
	if !quiet {
		channels.Terminal = cmd.ErrOrStderr()
	}
	notifier := configuredNotifier(spmCfg, channels)

	// Create SmartRunner
	opts := exec.SmartRunnerOptions{
		HandoffConfig:    &spmCfg.Handoff,
		Notifier:         notifier,
		Vault:            vault,
		DB:               db,
		AuthPool:         pool,
		Rotation:         selector,
		CooldownDuration: cooldownDur,
		RetryConfig:      &retryConfig,
	}
	smartRunner := exec.NewSmartRunner(runner, opts)

	// Get provider
	prov, ok := registry.Get(tool)
	if !ok {
		return fmt.Errorf("provider %s not found in registry", tool)
	}

	// Get active profile
	fileSet := tools[tool]()
	activeProfileName, err := vault.CurrentProfile(fileSet)
	if err != nil {
		return fmt.Errorf("read current auth: %w", err)
	}
	stdin := cmd.InOrStdin()
	stdinTerminal := runInputIsTerminal(stdin)
	if runUsesHeadless(tool, cliArgs, stdinTerminal) {
		if stdinTerminal {
			// Native batch forms take their prompt from arguments or redirected
			// input. Do not wait for or attempt to replay terminal keystrokes.
			stdin = strings.NewReader("")
		}
		switchOptions := switchOptionsFromConfig(spmCfg)
		wrapConfig := wrap.Config{
			Provider:          tool,
			Args:              cliArgs,
			WorkDir:           cwd,
			MaxRetries:        retryConfig.MaxRetries,
			InitialDelay:      retryConfig.InitialDelay.Duration(),
			MaxDelay:          retryConfig.MaxDelay.Duration(),
			BackoffMultiplier: retryConfig.BackoffMultiplier,
			Jitter:            retryConfig.Jitter,
			CooldownDuration:  cooldownDur,
			NotifyOnSwitch:    !quiet,
			Algorithm:         algorithm,
			InitialProfile:    activeProfileName,
			Selector:          selector,
			SwitchOptions:     &switchOptions,
			Bin:               prov.DefaultBin(),
			Stdin:             stdin,
			ReplayStdin:       !stdinTerminal && runInputReplayable(stdin),
			Stdout:            cmd.OutOrStdout(),
			Stderr:            cmd.ErrOrStderr(),
		}
		result := wrap.NewWrapper(vault, db, healthStore, wrapConfig).Run(ctx)
		if result.Err != nil {
			return result.Err
		}
		if result.ExitCode != 0 {
			return &exec.ExitCodeError{Code: result.ExitCode}
		}
		return nil
	}
	if activeProfileName == "" {
		// If no active profile, try to select one
		profiles, err := vault.List(tool)
		if err != nil || len(profiles) == 0 {
			return fmt.Errorf("no profiles found for %s; create one with 'caam backup %s <name>'", tool, tool)
		}
		res, err := selector.Select(tool, profiles, "")
		if err != nil {
			return fmt.Errorf("select profile: %w", err)
		}
		if res == nil || res.Selected == "" {
			return fmt.Errorf("no profile selected for %s", tool)
		}
		activeProfileName = res.Selected
		switched, err := vault.Switch(fileSet, activeProfileName, switchOptionsFromConfig(spmCfg))
		if err != nil {
			return fmt.Errorf("activate profile: %w", err)
		}
		if !quiet {
			printSwitchPreservation(os.Stderr, tool, switched)
		}
	}
	if err := checkInteractiveRunCredential(ctx, fileSet, activeProfileName, selector); err != nil {
		return err
	}

	// Load profile object
	if profileStore == nil {
		profileStore = profile.NewStore(profile.DefaultStorePath())
	}
	prof, err := profileStore.Load(tool, activeProfileName)
	if err != nil {
		// If profile object doesn't exist (only in vault), create a transient one.
		// We need a proper BasePath for locking to work correctly - otherwise
		// the lock file ends up in the current directory which causes issues
		// when multiple runs use the same profile.
		var basePath string
		if profileStore != nil {
			basePath = profileStore.ProfilePath(tool, activeProfileName)
		} else {
			// Fallback: use default store path
			basePath = filepath.Join(profile.DefaultStorePath(), tool, activeProfileName)
		}
		prof = &profile.Profile{
			Name:     activeProfileName,
			Provider: tool,
			AuthMode: "oauth", // Assumption
			BasePath: basePath,
		}
	}

	// Set CLI overrides
	// Cooldown duration is now passed directly to SmartRunner via opts.CooldownDuration

	// Run
	runOptions := exec.RunOptions{
		Profile:      prof,
		Provider:     prov,
		Args:         cliArgs,
		WorkDir:      cwd,
		Env:          nil,  // Inherit
		UseGlobalEnv: true, // Force global environment for vault-based switching
	}

	return smartRunner.Run(ctx, runOptions)
}

// A recognized owner is not necessarily a usable credential. Global execution
// deliberately skips isolated-profile preflight, so check the actual live grant
// before loading/locking a profile or giving it to the native CLI. In particular,
// Cursor may discard a hard-expired browser session when it tries to use it.
func checkInteractiveRunCredential(ctx context.Context, fileSet authfile.AuthFileSet, name string, selector *rotation.Selector) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store := healthStore
	if store == nil {
		store = health.NewStorage(filepath.Join(filepath.Dir(vault.BasePath()), "health.json"))
		store.SetVaultPath(vault.BasePath())
	}
	ph, err := store.GetProfile(fileSet.Tool, name)
	if err != nil {
		return fmt.Errorf("read active credential health: %w", err)
	}
	if ph == nil {
		ph = &health.ProfileHealth{}
	}
	// Keep provider-verification evidence, but discard every property derived
	// from the saved grant. The CLI will use live auth, which may have rotated
	// since backup or belong to a newly authenticated grant of the same account.
	applyExpiryInfo(ph, &health.ExpiryInfo{})
	info, err := health.ParseLiveExpiry(fileSet)
	if err != nil && !errors.Is(err, health.ErrNoExpiry) && !errors.Is(err, health.ErrNoAuthFile) {
		return fmt.Errorf("read live %s credential: %w", fileSet.Tool, err)
	}
	if info != nil {
		applyExpiryInfo(ph, info)
	}
	selector.SetProfileHealth(map[string]*health.ProfileHealth{name: ph})
	// Later handoffs must hydrate their own saved credentials normally.
	defer selector.SetProfileHealth(nil)
	result, err := selector.Select(fileSet.Tool, []string{name}, "")
	if err != nil {
		return fmt.Errorf("active %s profile %q cannot launch: %w", fileSet.Tool, name, err)
	}
	if result == nil || result.Selected != name {
		return fmt.Errorf("active %s profile %q is not available for launch", fileSet.Tool, name)
	}
	return ctx.Err()
}

// loadRunRetryConfig applies explicit flags last. Changed is essential: the
// displayed flag default must not erase a global or per-provider setting.
func loadRunRetryConfig(cmd *cobra.Command, tool string) (config.WrapConfig, error) {
	global, err := config.Load()
	if err != nil {
		return config.WrapConfig{}, fmt.Errorf("load retry settings: %w", err)
	}
	result := global.Wrap.ForProvider(tool)
	if cmd.Flags().Changed("max-retries") {
		result.MaxRetries, err = cmd.Flags().GetInt("max-retries")
		if err != nil {
			return result, err
		}
	}
	if cmd.Flags().Changed("cooldown") {
		duration, flagErr := cmd.Flags().GetDuration("cooldown")
		if flagErr != nil {
			return result, flagErr
		}
		result.CooldownDuration = config.Duration(duration)
	}
	if err := result.Validate(); err != nil {
		return result, fmt.Errorf("invalid retry settings for %s: %w", tool, err)
	}
	return result, nil
}

func runInputIsTerminal(input io.Reader) bool {
	file, ok := input.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(file.Fd()))
}

// runInputReplayable reports whether non-terminal stdin is known to be finite:
// an in-memory reader or a regular file. Only that input is spooled for replay
// on retry. A pipe may never close (ssh without -n, a harness that keeps its
// end open, streaming input), so it is passed through as in v0.1.22 instead of
// being read to EOF before the CLI starts (#120).
func runInputReplayable(input io.Reader) bool {
	file, ok := input.(*os.File)
	if !ok {
		return true
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	if info.Mode().IsRegular() {
		return true
	}
	// /dev/null is finite too (systemd, ssh -n, `< /dev/null`); replaying its
	// zero bytes keeps rate-limit retries working.
	null, err := os.Stat(os.DevNull)
	return err == nil && os.SameFile(info, null)
}

// runUsesHeadless recognizes native batch forms even when launched from a
// terminal. Without a terminal, commands use pipes instead of a nested PTY.
// Native arguments are never rewritten or given additional permissions.
func runUsesHeadless(tool string, args []string, stdinTerminal bool) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if tool == "gemini" && (arg == "-i" || arg == "--prompt-interactive" || strings.HasPrefix(arg, "--prompt-interactive=")) {
			return false
		}
	}
	if !stdinTerminal {
		return true
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		switch tool {
		case "claude", "cursor":
			if arg == "-p" || arg == "--print" || arg == "--print=true" {
				return true
			}
		case "gemini":
			if arg == "-p" || arg == "--prompt" || strings.HasPrefix(arg, "--prompt=") {
				return true
			}
		}
	}
	if len(args) > 0 {
		switch tool {
		case "codex":
			return args[0] == "exec" || args[0] == "e" || args[0] == "review"
		case "opencode":
			return args[0] == "run"
		}
	}
	return false
}

// runPrecheck checks current usage levels and switches profile if near limit.
// Returns true if a switch was performed.
//
// model is the model the run will use when it can be read off the passed-through
// arguments, so a spent per-model quota counts as "near limit" even when the
// account's general windows are idle (issue #97); "" means unknown, and then
// every model-scoped quota counts.
func runPrecheck(ctx context.Context, tool string, threshold float64, quiet bool, db *caamdb.DB, algorithm rotation.Algorithm, spmCfg *config.SPMConfig, model string) bool {
	// Get current profile's access token
	vaultDir := authfile.DefaultVaultPath()
	if vault != nil {
		vaultDir = vault.BasePath()
	}

	// Get the currently active profile
	fileSet := tools[tool]()
	currentProfile, err := vault.CurrentProfile(fileSet)
	if err != nil {
		fmt.Fprintf(os.Stderr, "caam: precheck could not read current %s auth: %v\n", tool, err)
		return false
	}
	if currentProfile == "" {
		return false // No active profile
	}

	// Grok and Cursor quota reads are fail-closed (docs/native-quotas.md):
	// an unmeasured row is not capacity, and a reported limit stage is
	// exhaustion even without a percentage. Their credentials are read from
	// the same vault the switch will restore from.
	native := tool == "grok" || tool == "cursor"

	// Load credentials for current profile
	credentials, err := usage.LoadProfileCredentials(vaultDir, tool)
	token, ok := credentials[currentProfile]
	if err != nil || !ok {
		if native {
			fmt.Fprintf(os.Stderr, "caam: precheck found no readable %s credential for %s; running without switching\n", tool, currentProfile)
		}
		return false
	}

	// Fetch current usage. A Grok billing read starts the grok CLI, which
	// its own reader bounds at 25s; keep that within reach.
	timeout := 15 * time.Second
	if native {
		timeout = 30 * time.Second
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	fetcher := usage.NewMultiProfileFetcher()
	results := fetcher.FetchAllProfiles(queryCtx, tool, map[string]string{currentProfile: token})

	if len(results) == 0 || results[0].Usage == nil {
		if native {
			fmt.Fprintf(os.Stderr, "caam: precheck could not read %s/%s quota; running without switching\n", tool, currentProfile)
		}
		return false
	}

	currentUsage := results[0].Usage

	nearLimit, measured := precheckNearLimit(tool, currentUsage, threshold, model)
	if !measured {
		// Unknown is neither "near limit" nor "fine". Keep the current
		// account (the historical behaviour) but say so, rather than letting
		// the flag look like it checked something.
		fmt.Fprintf(os.Stderr, "caam: precheck could not measure %s/%s quota (status %s); running without switching\n",
			tool, currentProfile, nativeQuotaStatus(currentUsage))
		return false
	}
	if !nearLimit {
		return false // All good, no switch needed
	}

	// Need to switch - find all profiles
	allProfiles, err := vault.List(tool)
	if err != nil || len(allProfiles) <= 1 {
		return false // Can't switch
	}

	// Find best alternative using usage-aware selection
	allCredentials, err := usage.LoadProfileCredentials(vaultDir, tool)
	if err != nil || len(allCredentials) == 0 {
		return false
	}

	// Fetch usage for all profiles, on a fresh deadline: the read above may
	// have used most of the first one.
	allCtx, allCancel := context.WithTimeout(ctx, timeout)
	defer allCancel()
	allResults := fetcher.FetchAllProfiles(allCtx, tool, allCredentials)

	// Convert to rotation.UsageInfo format
	usageData := make(map[string]*rotation.UsageInfo)
	rawUsage := make(map[string]*usage.UsageInfo)
	for _, r := range allResults {
		if r.Usage == nil {
			continue
		}
		usageData[r.ProfileName] = toRotationUsageInfo(r.ProfileName, r.Usage, model)
		rawUsage[r.ProfileName] = r.Usage
	}

	// A native switch may only land on another account whose quota was
	// measured, is not spent, and is itself below the precheck threshold;
	// the selector's scoring alone would still pick an unknown one. If
	// nothing qualifies, stay put and say so.
	candidates := allProfiles
	if native {
		eligible, _ := rotation.NativeQuotaCandidates(tool, allProfiles, usageData)
		candidates = make([]string, 0, len(eligible))
		for _, name := range eligible {
			if name == currentProfile {
				continue
			}
			if near, measured := precheckNearLimit(tool, rawUsage[name], threshold, model); near || !measured {
				continue
			}
			candidates = append(candidates, name)
		}
		if len(candidates) == 0 {
			fmt.Fprintf(os.Stderr, "caam: %s/%s is near its quota limit, but no other %s profile has measured available quota; not switching\n",
				tool, currentProfile, tool)
			return false
		}
	}

	// Use rotation selector with usage data
	selector := rotation.NewSelector(algorithm, healthStore, db)
	bindRotationVault(selector)
	applyRotationPolicy(selector, spmCfg, "")
	selector.SetUsageData(usageData)

	result, err := selector.Select(tool, candidates, currentProfile)
	if err != nil || result == nil || result.Selected == "" || result.Selected == currentProfile {
		return false // Couldn't find better alternative
	}

	// Switch to the better profile
	if ctx.Err() != nil {
		return false
	}
	switched, err := vault.Switch(fileSet, result.Selected, switchOptionsFromConfig(spmCfg))
	if err != nil {
		fmt.Fprintf(os.Stderr, "caam: precheck could not switch %s to %s: %v\n", tool, result.Selected, err)
		return false
	}

	if !quiet {
		printSwitchPreservation(os.Stderr, tool, switched)
		fmt.Fprintf(os.Stderr, "caam: precheck switched %s/%s -> %s/%s\n",
			tool, currentProfile, tool, result.Selected)
	}

	return true
}

// precheckNearLimit decides whether the account about to run is near its
// limit. measured is false only for a Grok or Cursor row whose quota could
// not be measured: those readers are fail-closed, so such a row is neither
// near the limit nor known to be fine. A reported Grok/Cursor limit stage is
// exhaustion even without a percentage. Claude and Codex keep the historical
// percentage-only rule.
func precheckNearLimit(tool string, u *usage.UsageInfo, threshold float64, model string) (nearLimit, measured bool) {
	if u.IsNearLimitForModel(threshold, model) {
		return true, true
	}
	if tool != "grok" && tool != "cursor" {
		return false, true
	}
	if u.LimitStage != "" {
		return true, true
	}
	if !u.NumericQuotaKnown() {
		return false, false
	}
	return false, true
}

// nativeQuotaStatus names a native quota row's state for a notice without
// repeating provider error text, which can carry credential material.
func nativeQuotaStatus(u *usage.UsageInfo) string {
	if u == nil || u.QuotaStatus == "" {
		return usage.QuotaUnavailable
	}
	return u.QuotaStatus
}
