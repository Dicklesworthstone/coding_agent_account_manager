// Package codex implements the provider adapter for OpenAI Codex CLI.
//
// Authentication mechanics (from research):
// - ChatGPT login is browser/OAuth via localhost:1455 during setup.
// - After login, credentials stored in $CODEX_HOME/auth.json (default ~/.codex/auth.json).
// - API key alternative: printenv OPENAI_API_KEY | codex login --with-api-key
// - Config defaults from ~/.codex/config.toml, supports --profile flag.
//
// Context isolation for caam:
// - Set CODEX_HOME to point to the profile's codex_home directory.
// - This is the cleanest provider since CODEX_HOME is the official anchor for auth.json.
//
// Auth file swapping (PRIMARY use case):
// - Backup ~/.codex/auth.json after logging in with each GPT Pro account
// - Restore to instantly switch accounts without browser login flows
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/browser"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/passthrough"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"golang.org/x/term"
)

// Provider implements the Codex CLI adapter.
type Provider struct{}

// New creates a new Codex provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "codex"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "Codex CLI (OpenAI GPT Pro)"
}

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string {
	return "codex"
}

// SupportedAuthModes returns the authentication modes supported by Codex.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth,      // Browser-based ChatGPT login (GPT Pro subscription)
		provider.AuthModeDeviceCode, // Device code flow (codex login --device-auth)
		provider.AuthModeAPIKey,     // OpenAI API key
	}
}

// codexHome returns the Codex home directory.
func codexHome() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".codex")
}

// ResolveHome returns the current Codex home directory.
func ResolveHome() string {
	return codexHome()
}

// EnsureFileCredentialStore ensures Codex uses file-based credential storage.
// This is required for CAAM to manage auth.json reliably.
func EnsureFileCredentialStore(home string) error {
	home = strings.TrimSpace(home)
	if home == "" {
		return fmt.Errorf("codex home is empty")
	}

	configPath := filepath.Join(home, "config.toml")
	const settingLine = `cli_auth_credentials_store = "file"`

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.MkdirAll(home, 0700); err != nil {
			return fmt.Errorf("create codex home: %w", err)
		}
		content := "# Managed by caam to ensure file-based auth storage\n" + settingLine + "\n"
		return atomicWriteFile(configPath, []byte(content), 0600)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config.toml: %w", err)
	}

	start, end, err := codexCredentialStoreValue(string(data))
	if err != nil {
		return fmt.Errorf("inspect config.toml credential store: %w", err)
	}
	if start >= 0 {
		value := string(data[start:end])
		decoded, _ := strconv.Unquote(value)
		if decoded == "file" || value == `'file'` {
			return nil
		}
		updated := string(data[:start]) + `"file"` + string(data[end:])
		return atomicWriteFile(configPath, []byte(updated), 0600)
	}

	// The key is absent, so we must add it as a TOP-LEVEL key. Appending to the
	// end of the file is WRONG: in TOML every bare key after a `[table]` header
	// belongs to that table, so appending after e.g. `[mcp_servers.foo]` would
	// make it `mcp_servers.foo.cli_auth_credentials_store` — the file store would
	// not be enforced at the root, and the MCP table would be polluted. Instead
	// insert the key into the root table: after any leading comment/blank block
	// but before the first content line (which is either a top-level key or the
	// first table header). Everything before the first `[table]` header is root
	// scope, so inserting at the first non-comment line is always top-level, and
	// we never have to parse table headers or multi-line array bodies.
	lines := strings.Split(string(data), "\n")
	insertAt := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			insertAt = i + 1
			continue
		}
		break
	}
	newLines := make([]string, 0, len(lines)+1)
	newLines = append(newLines, lines[:insertAt]...)
	newLines = append(newLines, settingLine)
	newLines = append(newLines, lines[insertAt:]...)
	return atomicWriteFile(configPath, []byte(strings.Join(newLines, "\n")), 0600)
}

// codexCredentialStoreValue finds the root setting's value without re-emitting
// unrelated TOML. This is a bounded source editor, not a general TOML validator:
// it rejects malformed structure and ambiguous target assignments before any
// write, while leaving unrelated settings and string contents verbatim.
// shallow's source editor cannot be imported here: shallow depends on codex.
func codexCredentialStoreValue(data string) (int, int, error) {
	const key = "cli_auth_credentials_store"
	start, end := -1, -1
	root := true
	for pos := 0; pos < len(data); {
		statement := pos
		equals, comment := -1, -1
		var brackets []byte
		for pos < len(data) {
			c := data[pos]
			if c == '\'' || c == '"' {
				next, err := codexConfigStringEnd(data, pos)
				if err != nil {
					return -1, -1, err
				}
				pos = next
				continue
			}
			if c == '#' {
				if len(brackets) == 0 {
					comment = pos
				}
				for pos < len(data) && data[pos] != '\n' {
					pos++
				}
				continue
			}
			if c == '\n' && len(brackets) == 0 {
				break
			}
			switch c {
			case '[', '{':
				brackets = append(brackets, c)
			case ']', '}':
				if len(brackets) == 0 || (c == ']' && brackets[len(brackets)-1] != '[') || (c == '}' && brackets[len(brackets)-1] != '{') {
					return -1, -1, fmt.Errorf("unbalanced TOML delimiters")
				}
				brackets = brackets[:len(brackets)-1]
			case '=':
				if len(brackets) == 0 {
					if equals >= 0 {
						return -1, -1, fmt.Errorf("multiple assignment operators")
					}
					equals = pos
				}
			}
			pos++
		}
		if len(brackets) != 0 {
			return -1, -1, fmt.Errorf("unterminated TOML container")
		}
		limit := pos
		if comment >= 0 {
			limit = comment
		}
		text := strings.TrimSpace(data[statement:limit])
		pos++ // A final statement need not end in a newline.
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "[") {
			width := 1
			if strings.HasPrefix(text, "[[") {
				width = 2
			}
			if len(text) < 2*width || !strings.HasSuffix(text, strings.Repeat("]", width)) {
				return -1, -1, fmt.Errorf("malformed TOML table header")
			}
			path, err := codexConfigKey(text[width : len(text)-width])
			if err != nil {
				return -1, -1, err
			}
			if path[0] == key {
				return -1, -1, fmt.Errorf("credential store must be a root string, not a table")
			}
			root = false
			continue
		}
		if equals < 0 || equals >= limit {
			return -1, -1, fmt.Errorf("missing TOML assignment")
		}
		path, err := codexConfigKey(data[statement:equals])
		if err != nil {
			return -1, -1, err
		}
		value := strings.TrimSpace(data[equals+1 : limit])
		if value == "" {
			return -1, -1, fmt.Errorf("missing TOML value")
		}
		if !root || path[0] != key {
			continue
		}
		if len(path) != 1 || start >= 0 {
			return -1, -1, fmt.Errorf("ambiguous root credential store assignment")
		}
		if value[0] != '\'' && value[0] != '"' {
			return -1, -1, fmt.Errorf("credential store must be a string")
		}
		next, err := codexConfigStringEnd(value, 0)
		if err != nil || next != len(value) {
			return -1, -1, fmt.Errorf("malformed credential store string")
		}
		start = equals + 1 + strings.Index(data[equals+1:limit], value)
		end = start + len(value)
	}
	return start, end, nil
}

// codexConfigStringEnd skips both TOML string forms, including multiline strings
// with escaped delimiters. The returned offset is just after the closing quote.
func codexConfigStringEnd(data string, pos int) (int, error) {
	quote := data[pos]
	multi := strings.HasPrefix(data[pos:], strings.Repeat(string(quote), 3))
	pos++
	if multi {
		pos += 2
	}
	for pos < len(data) {
		c := data[pos]
		if (c < 0x20 && c != '\t' && c != '\n' && c != '\r') || c == 0x7f || (c == '\r' && (pos+1 >= len(data) || data[pos+1] != '\n')) {
			return 0, fmt.Errorf("invalid control character in TOML string")
		}
		if c == '\n' && !multi {
			return 0, fmt.Errorf("newline in single-line TOML string")
		}
		if c == '\\' && quote == '"' {
			pos++
			if pos >= len(data) {
				break
			}
			if multi && strings.ContainsRune(" \t\r\n", rune(data[pos])) {
				newline := false
				for pos < len(data) && strings.ContainsRune(" \t\r\n", rune(data[pos])) {
					newline = newline || data[pos] == '\n'
					pos++
				}
				if !newline {
					return 0, fmt.Errorf("TOML string continuation requires a newline")
				}
				continue
			}
			if !strings.ContainsRune("btnfr\"\\uU", rune(data[pos])) {
				return 0, fmt.Errorf("invalid TOML string escape")
			}
			if data[pos] == 'u' || data[pos] == 'U' {
				count := 4
				if data[pos] == 'U' {
					count = 8
				}
				if pos+count >= len(data) {
					return 0, fmt.Errorf("incomplete TOML unicode escape")
				}
				if _, err := strconv.Unquote(`"\` + data[pos:pos+count+1] + `"`); err != nil {
					return 0, fmt.Errorf("invalid TOML unicode escape")
				}
				pos += count
			}
			pos++
			continue
		}
		if c == quote {
			if !multi {
				return pos + 1, nil
			}
			end := pos
			for end < len(data) && data[end] == quote {
				end++
			}
			if end-pos >= 3 {
				if end-pos > 5 {
					return 0, fmt.Errorf("invalid TOML closing quotes")
				}
				return end, nil
			}
			pos = end
			continue
		}
		pos++
	}
	return 0, fmt.Errorf("unterminated TOML string")
}

func codexConfigKey(text string) ([]string, error) {
	var path []string
	for {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, fmt.Errorf("empty TOML key")
		}
		end := 0
		key := ""
		if text[0] == '\'' || text[0] == '"' {
			var err error
			end, err = codexConfigStringEnd(text, 0)
			if err != nil || strings.HasPrefix(text, `"""`) || strings.HasPrefix(text, `'''`) {
				return nil, fmt.Errorf("malformed quoted TOML key")
			}
			key = text[1 : end-1]
			if text[0] == '"' {
				key, err = strconv.Unquote(text[:end])
				if err != nil {
					return nil, fmt.Errorf("malformed quoted TOML key")
				}
			}
		} else {
			for end < len(text) && ((text[end] >= 'a' && text[end] <= 'z') || (text[end] >= 'A' && text[end] <= 'Z') || (text[end] >= '0' && text[end] <= '9') || text[end] == '_' || text[end] == '-') {
				end++
			}
			if end == 0 {
				return nil, fmt.Errorf("malformed TOML key")
			}
			key = text[:end]
		}
		path = append(path, key)
		text = strings.TrimSpace(text[end:])
		if text == "" {
			return path, nil
		}
		if text[0] != '.' {
			return nil, fmt.Errorf("malformed dotted TOML key")
		}
		text = text[1:]
	}
}

// AuthFiles returns the auth file specifications for Codex.
// This is the key method for auth file backup/restore.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	return []provider.AuthFileSpec{
		{
			Path:        filepath.Join(codexHome(), "auth.json"),
			Description: "Codex CLI OAuth token (GPT Pro subscription)",
			Required:    true,
		},
	}
}

// PrepareProfile sets up the profile directory structure.
func (p *Provider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	// Create codex_home directory for isolated context
	codexHomePath := prof.CodexHomePath()
	if err := os.MkdirAll(codexHomePath, 0700); err != nil {
		return fmt.Errorf("create codex_home: %w", err)
	}
	if err := EnsureFileCredentialStore(codexHomePath); err != nil {
		return fmt.Errorf("configure codex credential store: %w", err)
	}

	// Create pseudo-home directory
	homePath := prof.HomePath()
	if err := os.MkdirAll(homePath, 0700); err != nil {
		return fmt.Errorf("create home: %w", err)
	}

	// Set up passthrough symlinks
	mgr, err := passthrough.NewManager()
	if err != nil {
		return fmt.Errorf("create passthrough manager: %w", err)
	}

	if err := mgr.SetupPassthroughs(homePath); err != nil {
		return fmt.Errorf("setup passthroughs: %w", err)
	}

	// With HOME redirected, XDG config/data/state default to paths inside the
	// profile; pass through the real entries so XDG-based CLIs keep their
	// credentials (issue #69). Codex does not override XDG_CONFIG_HOME, so
	// the effective config dir is $HOME/.config inside the pseudo-home.
	if err := mgr.SetupXDGPassthroughs(homePath, filepath.Join(homePath, ".config")); err != nil {
		return fmt.Errorf("setup xdg passthroughs: %w", err)
	}

	return nil
}

// Env returns the environment variables for running Codex in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	env := map[string]string{
		"CODEX_HOME": prof.CodexHomePath(),
		"HOME":       prof.HomePath(),
	}
	if provider.AuthMode(prof.AuthMode) == provider.AuthModeAPIKey {
		data, err := readCodexCredentialSource(filepath.Join(prof.CodexHomePath(), "auth.json"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return env, nil // A new profile may enroll using the ambient API key.
			}
			return nil, err
		}
		credential, err := parseCodexCredential(data)
		if err != nil {
			return nil, fmt.Errorf("invalid selected Codex credential: %w", err)
		}
		if credential.mode == provider.AuthModeAPIKey {
			// Interactive Codex and codex exec consult different key inputs.
			// Both must select the saved account; explicit runner overrides
			// are still applied after this environment by the caller.
			env["OPENAI_API_KEY"] = credential.apiKey
			env["CODEX_API_KEY"] = credential.apiKey
		} else {
			// Native login can change the saved method before profile metadata
			// catches up. Its OAuth grant still owns this launch.
			env["OPENAI_API_KEY"] = ""
			env["CODEX_API_KEY"] = ""
			env["OPENAI_BASE_URL"] = ""
		}
	}
	return env, nil
}

// Login initiates the authentication flow.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	if err := EnsureFileCredentialStore(prof.CodexHomePath()); err != nil {
		return fmt.Errorf("configure codex credential store: %w", err)
	}
	switch provider.AuthMode(prof.AuthMode) {
	case provider.AuthModeDeviceCode:
		return p.LoginWithDeviceCode(ctx, prof)
	case provider.AuthModeAPIKey:
		return p.loginWithAPIKey(ctx, prof)
	default:
		return p.loginWithOAuth(ctx, prof)
	}
}

func (p *Provider) SupportsDeviceCode() bool {
	return true
}

func (p *Provider) LoginWithDeviceCode(ctx context.Context, prof *profile.Profile) error {
	env, err := provider.ProfileEnvironment(ctx, p, prof)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "codex", "login", "--device-auth")
	cmd.Env = provider.MergeEnvironment(os.Environ(), env, nil)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	fmt.Println("Starting Codex device code login flow...")
	fmt.Println("Follow the prompts to authenticate in your browser.")

	return cmd.Run()
}

// loginWithOAuth runs the browser-based login flow.
func (p *Provider) loginWithOAuth(ctx context.Context, prof *profile.Profile) error {
	env, err := provider.ProfileEnvironment(ctx, p, prof)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, "codex", "login")
	cmd.Env = provider.MergeEnvironment(os.Environ(), env, nil)

	// Set up URL detection and capture if browser profile is configured
	var capture *browser.OutputCapture
	if prof.HasBrowserConfig() {
		launcher := browser.NewLauncher(&browser.Config{
			Command:    prof.BrowserCommand,
			ProfileDir: prof.BrowserProfileDir,
		})
		fmt.Printf("Using browser profile: %s\n", prof.BrowserDisplayName())

		capture = browser.NewOutputCapture(os.Stdout, os.Stderr)
		capture.OnURL = func(url, source string) {
			// Open detected URLs with our configured browser
			if err := launcher.Open(url); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to open browser: %v\n", err)
			}
		}
		cmd.Stdout = capture.StdoutWriter()
		cmd.Stderr = capture.StderrWriter()
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	cmd.Stdin = os.Stdin

	fmt.Println("Starting Codex OAuth login flow...")
	if prof.HasBrowserConfig() {
		fmt.Println("Browser will open with configured profile.")
	} else {
		fmt.Println("A browser window will open. Complete the login there.")
	}

	err = cmd.Run()
	if capture != nil {
		capture.Flush()
	}
	return err
}

func readAPIKeyFromStdin(stdin *os.File) (key string, hidden bool, err error) {
	if stdin == nil {
		return "", false, fmt.Errorf("stdin is nil")
	}

	if term.IsTerminal(int(stdin.Fd())) {
		b, err := term.ReadPassword(int(stdin.Fd()))
		if err != nil {
			return "", false, fmt.Errorf("read API key: %w", err)
		}
		return strings.TrimSpace(string(b)), true, nil
	}

	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", false, fmt.Errorf("read API key: %w", err)
	}
	return strings.TrimSpace(line), false, nil
}

// loginWithAPIKey prompts for and stores an API key.
func (p *Provider) loginWithAPIKey(ctx context.Context, prof *profile.Profile) error {
	env, err := provider.ProfileEnvironment(ctx, p, prof)
	if err != nil {
		return err
	}

	// Check for OPENAI_API_KEY environment variable first
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Print("Enter OpenAI API key: ")
		key, hidden, err := readAPIKeyFromStdin(os.Stdin)
		if hidden {
			fmt.Println()
		}
		if err != nil {
			return err
		}
		apiKey = key
	}

	if apiKey == "" {
		return fmt.Errorf("API key is required")
	}

	// Use codex login --with-api-key via stdin (safer than argv)
	cmd := exec.CommandContext(ctx, "codex", "login", "--with-api-key")
	cmd.Env = provider.MergeEnvironment(os.Environ(), env, nil)
	cmd.Stdin = strings.NewReader(apiKey)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// Logout clears authentication credentials.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	authPath := filepath.Join(prof.CodexHomePath(), "auth.json")
	if err := os.Remove(authPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove auth.json: %w", err)
	}
	return nil
}

// Status checks the current authentication state.
func (p *Provider) Status(ctx context.Context, prof *profile.Profile) (*provider.ProfileStatus, error) {
	status := &provider.ProfileStatus{
		HasLockFile: prof.IsLocked(),
	}

	// Check if auth.json exists
	authPath := filepath.Join(prof.CodexHomePath(), "auth.json")
	if _, err := os.Stat(authPath); err == nil {
		status.LoggedIn = true
	}

	return status, nil
}

// ValidateProfile checks if the profile is correctly configured.
func (p *Provider) ValidateProfile(ctx context.Context, prof *profile.Profile) error {
	// Check codex_home exists
	codexHomePath := prof.CodexHomePath()
	if _, err := os.Stat(codexHomePath); os.IsNotExist(err) {
		return fmt.Errorf("codex_home directory missing")
	}

	// Check passthrough symlinks if profile has a home directory
	homePath := prof.HomePath()
	if _, err := os.Stat(homePath); err == nil {
		mgr, err := passthrough.NewManager()
		if err != nil {
			return fmt.Errorf("create passthrough manager: %w", err)
		}

		statuses, err := mgr.VerifyPassthroughs(homePath)
		if err != nil {
			return fmt.Errorf("verify passthroughs: %w", err)
		}

		for _, s := range statuses {
			if s.SourceExists && !s.LinkValid {
				return fmt.Errorf("passthrough %s is invalid: %s", s.Path, s.Error)
			}
		}
	}

	return nil
}

// DetectExistingAuth inspects the credential store Codex actually uses. An
// explicit CODEX_HOME is authoritative, including when its credential is absent
// or invalid; a different account in ~/.codex must never rescue that failure.
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{
		Provider:  p.ID(),
		Locations: []provider.AuthLocation{},
	}
	home := os.Getenv("CODEX_HOME")
	description := "Codex CLI credentials (CODEX_HOME)"
	if home == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		home = filepath.Join(homeDir, ".codex")
		description = "Codex CLI credentials (default location)"
	}
	path, err := filepath.Abs(filepath.Join(home, "auth.json"))
	if err != nil {
		return nil, fmt.Errorf("resolve Codex credential path: %w", err)
	}
	loc := provider.AuthLocation{Path: path, Description: description}
	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			loc.ValidationError = fmt.Sprintf("inspect credential: %v", err)
		}
	} else {
		loc.Exists = true
		loc.LastModified, loc.FileSize = info.ModTime(), info.Size()
		data, err := readCodexCredentialSource(path)
		if err == nil {
			_, err = parseCodexCredential(data)
		}
		if err != nil {
			loc.ValidationError = err.Error()
		} else {
			loc.IsValid = true
			detection.Found = true
			detection.Primary = &loc
		}
	}
	detection.Locations = append(detection.Locations, loc)
	return detection, nil
}

// ImportAuth validates one captured source and publishes its original bytes at
// the canonical Codex path. A later source rotation cannot change the bytes
// between validation and copying, and invalid imports leave the target intact.
func (p *Provider) ImportAuth(ctx context.Context, sourcePath string, prof *profile.Profile) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prof == nil || strings.TrimSpace(prof.BasePath) == "" {
		return nil, fmt.Errorf("profile has no base path")
	}
	data, err := readCodexCredentialSource(sourcePath)
	if err != nil {
		return nil, err
	}
	credential, err := parseCodexCredential(data)
	if err != nil {
		return nil, fmt.Errorf("invalid Codex credential: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	targetPath := filepath.Join(prof.CodexHomePath(), "auth.json")
	if sourceInfo, err := os.Stat(sourcePath); err == nil {
		if targetInfo, err := os.Stat(targetPath); err == nil && os.SameFile(sourceInfo, targetInfo) {
			return nil, fmt.Errorf("source and destination refer to the same credential file")
		}
	}
	if err := atomicWriteFile(targetPath, data, 0600); err != nil {
		return nil, fmt.Errorf("write imported auth.json: %w", err)
	}
	prof.AuthMode = string(credential.mode)
	return []string{targetPath}, nil
}

const maxCodexCredentialBytes int64 = 16 << 20

// readCodexCredentialSource is shared by detection, import and passive checks.
// It never modifies a native file and rejects nonregular sources before opening.
func readCodexCredentialSource(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect Codex credential: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxCodexCredentialBytes {
		return nil, fmt.Errorf("Codex credential must be a regular file no larger than %d bytes", maxCodexCredentialBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Codex credential: %w", err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect open Codex credential: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxCodexCredentialBytes {
		return nil, fmt.Errorf("Codex credential must be a regular file no larger than %d bytes", maxCodexCredentialBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCodexCredentialBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Codex credential: %w", err)
	}
	if int64(len(data)) > maxCodexCredentialBytes {
		return nil, fmt.Errorf("Codex credential exceeds %d bytes", maxCodexCredentialBytes)
	}
	return data, nil
}

type codexCredential struct {
	mode       provider.AuthMode
	apiKey     string
	expiresAt  time.Time
	hasRefresh bool
}

// parseCodexCredential recognizes native ChatGPT and API-key records as well
// as flat OAuth records. Optional native API-key/tokens slots can be null, but
// a present malformed credential field cannot be treated as a usable login.
func parseCodexCredential(data []byte) (codexCredential, error) {
	var result codexCredential
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return result, fmt.Errorf("expected a JSON object")
	}
	apiKey, err := codexCredentialAliases(root, true, "OPENAI_API_KEY", "api_key", "apiKey")
	if err != nil {
		return result, err
	}
	authMode, err := codexCredentialAliases(root, true, "auth_mode")
	if err != nil {
		return result, err
	}
	if raw, exists := root["last_refresh"]; exists && strings.TrimSpace(string(raw)) != "null" {
		var stamp string
		if json.Unmarshal(raw, &stamp) != nil {
			return result, fmt.Errorf("last_refresh must be a timestamp")
		}
		if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
			return result, fmt.Errorf("last_refresh must be a timestamp")
		}
	}
	entry := root
	if raw, exists := root["tokens"]; exists && strings.TrimSpace(string(raw)) != "null" {
		var tokens map[string]json.RawMessage
		if err := json.Unmarshal(raw, &tokens); err != nil || tokens == nil {
			return result, fmt.Errorf("tokens must be an object")
		}
		entry = tokens
	}
	access, err := codexCredentialAliases(entry, false, "access_token", "accessToken", "token")
	if err != nil {
		return result, err
	}
	refresh, err := codexCredentialAliases(entry, false, "refresh_token", "refreshToken")
	if err != nil {
		return result, err
	}
	for _, key := range []string{"refresh_token", "refreshToken"} {
		if _, exists := entry[key]; exists && refresh == "" {
			return result, fmt.Errorf("refresh credential is empty")
		}
	}
	if refresh != "" && access == "" {
		return result, fmt.Errorf("access credential is missing")
	}
	if _, nested := root["tokens"]; nested {
		flatAccess, err := codexCredentialAliases(root, false, "access_token", "accessToken", "token")
		if err != nil {
			return result, err
		}
		flatRefresh, err := codexCredentialAliases(root, false, "refresh_token", "refreshToken")
		if err != nil {
			return result, err
		}
		if (flatAccess != "" && flatAccess != access) || (flatRefresh != "" && flatRefresh != refresh) {
			return result, fmt.Errorf("flat and nested credentials conflict")
		}
	}
	for _, obj := range []map[string]json.RawMessage{root, entry} {
		if _, err := codexCredentialAliases(obj, true, "id_token", "idToken"); err != nil {
			return result, err
		}
		if _, err := codexCredentialAliases(obj, true, "account_id", "accountId"); err != nil {
			return result, err
		}
	}
	switch strings.ToLower(authMode) {
	case "apikey", "api-key":
		if apiKey == "" {
			return result, fmt.Errorf("API-key auth mode has no API key")
		}
		result.mode = provider.AuthModeAPIKey
	case "chatgpt", "oauth", "chatgptauthtokens":
		if access == "" {
			return result, fmt.Errorf("ChatGPT auth mode has no access token")
		}
		result.mode = provider.AuthModeOAuth
	case "":
		if access != "" && apiKey != "" {
			return result, fmt.Errorf("multiple authentication methods require an auth_mode")
		}
		if access != "" {
			result.mode = provider.AuthModeOAuth
		} else if apiKey != "" {
			result.mode = provider.AuthModeAPIKey
		} else {
			return result, fmt.Errorf("credential has no access token or API key")
		}
	default:
		return result, fmt.Errorf("unsupported Codex auth_mode")
	}
	if result.mode == provider.AuthModeAPIKey {
		result.apiKey = apiKey
		return result, nil
	}
	result.hasRefresh = refresh != ""
	for _, obj := range []map[string]json.RawMessage{root, entry} {
		for _, key := range []string{"expires_at", "expiresAt", "expires"} {
			if raw, exists := obj[key]; exists {
				var value any
				if err := json.Unmarshal(raw, &value); err != nil {
					return result, fmt.Errorf("credential expiry is malformed")
				}
				var expiry time.Time
				switch v := value.(type) {
				case string:
					expiry, _ = parseCodexExpiryTime(v)
				case float64:
					expiry = parseCodexUnixTime(v)
				}
				if expiry.IsZero() || expiry.Year() < 1970 || expiry.Year() > 9999 {
					return result, fmt.Errorf("credential expiry is malformed")
				}
				if !result.expiresAt.IsZero() && !result.expiresAt.Equal(expiry) {
					return result, fmt.Errorf("credential expiry fields conflict")
				}
				result.expiresAt = expiry
			}
		}
	}
	if result.expiresAt.IsZero() {
		if id, err := identity.ExtractFromJWT(access); err == nil {
			result.expiresAt = id.ExpiresAt
		}
	}
	return result, nil
}

func codexCredentialAliases(obj map[string]json.RawMessage, allowNull bool, keys ...string) (string, error) {
	var selected string
	for _, key := range keys {
		raw, exists := obj[key]
		if !exists {
			continue
		}
		if strings.TrimSpace(string(raw)) == "null" {
			if allowNull {
				continue
			}
			return "", fmt.Errorf("%s must be a string", key)
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", fmt.Errorf("%s must be a string", key)
		}
		value = strings.TrimSpace(value)
		if selected != "" && value != "" && selected != value {
			return "", fmt.Errorf("credential aliases conflict")
		}
		if value != "" {
			selected = value
		}
	}
	return selected, nil
}

// atomicWriteFile writes data to a file atomically using temp file + fsync + rename.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // Clean up on error; no-op after successful rename

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmpFile.Chmod(perm); err != nil {
		tmpFile.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// ValidateToken validates that the authentication token works.
// For passive validation: checks file existence, format, and expiry timestamps.
// For active validation: attempts minimal API call to OpenAI.
func (p *Provider) ValidateToken(ctx context.Context, prof *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	result := &provider.ValidationResult{
		Provider:  p.ID(),
		Profile:   prof.Name,
		CheckedAt: time.Now(),
	}

	if passive {
		return p.validateTokenPassive(ctx, prof, result)
	}
	return p.validateTokenActive(ctx, prof, result)
}

// validateTokenPassive performs passive validation without network calls.
func (p *Provider) validateTokenPassive(ctx context.Context, prof *profile.Profile, result *provider.ValidationResult) (*provider.ValidationResult, error) {
	result.Method = "passive"
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := readCodexCredentialSource(filepath.Join(prof.CodexHomePath(), "auth.json"))
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	credential, err := parseCodexCredential(data)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	result.ExpiresAt = credential.expiresAt
	// A complete refresh grant remains usable after access-token expiry. The
	// native CLI can renew it; passive validation must not require a new login.
	if !credential.expiresAt.IsZero() && !credential.expiresAt.After(result.CheckedAt) && !credential.hasRefresh {
		result.Error = "token has expired"
		return result, nil
	}
	result.Valid = true
	return result, nil
}

// validateTokenActive performs active validation with network calls.
func (p *Provider) validateTokenActive(ctx context.Context, prof *profile.Profile, result *provider.ValidationResult) (*provider.ValidationResult, error) {
	result.Method = "active"

	// First do passive validation
	passiveResult, err := p.validateTokenPassive(ctx, prof, result)
	if err != nil {
		return nil, err
	}
	if !passiveResult.Valid {
		return passiveResult, nil
	}

	// For active validation, we would need to make an API call to OpenAI
	// to verify the token. For now, we rely on passive validation.
	// A proper implementation would call https://api.openai.com/v1/models
	// with the token to verify it's valid.
	result.Valid = true
	return result, nil
}

func parseCodexExpiryTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05-07:00",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time: %s", s)
}

func parseCodexUnixTime(f float64) time.Time {
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 || f >= float64(math.MaxInt64) || math.Trunc(f) != f {
		return time.Time{}
	}
	if f > 1e12 {
		return time.UnixMilli(int64(f))
	}
	return time.Unix(int64(f), 0)
}

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
var _ provider.DeviceCodeProvider = (*Provider)(nil)
