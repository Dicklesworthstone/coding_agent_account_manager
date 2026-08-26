package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
)

// `caam login <tool> <profile>` writes the new credential to the isolated
// profile only. caam keeps two other credential layers that the login does not
// touch: the vault snapshot (`caam backup`) and shallow runtimes
// (`caam shallow-profile create`). When a login rotates the refresh token, the
// generation still sitting in those layers is revoked upstream while it keeps
// looking healthy on disk (its access-token expiry is untouched by revocation).
// Issue #76 describes the outage that follows when a session or reconciler
// then serves that copy.
//
// This file makes the login tell the truth about it: after a successful login
// it names every caam layer still holding a different generation, flags the
// ones holding exactly the generation the login replaced, and prints the
// commands that converge them. It never writes credentials itself; moving a
// credential between layers stays an explicit operator action.

// staleLoginLayer is a caam credential location that still holds a different
// generation than the one the login just wrote.
type staleLoginLayer struct {
	// Kind is "vault" or "shallow".
	Kind string
	// Label identifies the layer to the operator: the vault profile as
	// "<tool>/<profile>", or the shallow profile name.
	Label string
	// Path is the credential file inside that layer.
	Path string
	// Replaced is true when the layer holds byte-for-byte the generation the
	// login replaced, i.e. the copy a refresh-token rotation has most likely
	// revoked.
	Replaced bool
}

// loginLayerCandidate is a credential location worth comparing after a login.
type loginLayerCandidate struct {
	Kind  string
	Label string
	Path  string
	// NameMatched marks layers that share the profile's name (same-named vault
	// profile, or a shallow profile named "<profile>" / "<tool>-<profile>").
	// Those are reported whenever they differ from the fresh login; layers
	// with other names are reported only when they hold the replaced
	// generation, because they may legitimately belong to another account.
	NameMatched bool
}

// loginProfileAuthPath returns the primary credential file that `caam login`
// writes for an isolated profile, and whether the layer check applies to the
// tool. Only Codex is covered: it is the tool whose login rotates the refresh
// token, and the only shallow-capable tool `caam login` can drive.
func loginProfileAuthPath(tool string, prof *profile.Profile) (string, bool) {
	if prof == nil {
		return "", false
	}
	switch tool {
	case "codex":
		return filepath.Join(prof.CodexHomePath(), "auth.json"), true
	default:
		return "", false
	}
}

// collectLoginLayerCandidates gathers the vault and shallow credential files
// for tool that could still hold an older generation for profile name. A nil
// vault skips the vault layer; a nil manager resolves the default shallow
// base directory (and is skipped when that cannot be resolved).
func collectLoginLayerCandidates(tool, name string, v *authfile.Vault, mgr *shallow.Manager) []loginLayerCandidate {
	var out []loginLayerCandidate

	if v != nil {
		if base := loginPrimaryAuthBasename(tool); base != "" {
			out = append(out, loginLayerCandidate{
				Kind:        "vault",
				Label:       tool + "/" + name,
				Path:        v.BackupPath(tool, name, base),
				NameMatched: true,
			})
		}
	}

	rel, err := shallow.PrimaryCredentialRel(tool)
	if err != nil {
		return out // tool has no shallow layout; nothing more to compare
	}
	if mgr == nil {
		mgr, err = shallow.NewManager("", "")
		if err != nil {
			return out
		}
	}
	profiles, err := mgr.List()
	if err != nil {
		return out
	}
	for _, p := range profiles {
		if p.Meta == nil || shallow.NormalizeProvider(p.Meta.Provider) != tool {
			continue
		}
		out = append(out, loginLayerCandidate{
			Kind:        "shallow",
			Label:       p.Name,
			Path:        filepath.Join(p.Path, filepath.FromSlash(rel)),
			NameMatched: p.Name == name || p.Name == tool+"-"+name,
		})
	}
	return out
}

// loginPrimaryAuthBasename returns the file name of a tool's required auth
// file as stored in the vault (the vault keeps files by basename).
func loginPrimaryAuthBasename(tool string) string {
	getFileSet, ok := tools[tool]
	if !ok {
		return ""
	}
	for _, f := range getFileSet().Files {
		if f.Required {
			return filepath.Base(f.Path)
		}
	}
	return ""
}

// findStaleLoginLayers compares each candidate against the credential the
// login just wrote (fresh) and the one it replaced (prev; nil when the profile
// had no credential before). Missing or unreadable candidates are not stale:
// there is nothing there to serve a revoked generation from.
func findStaleLoginLayers(prev, fresh []byte, candidates []loginLayerCandidate) []staleLoginLayer {
	if len(fresh) == 0 {
		return nil
	}
	var out []staleLoginLayer
	for _, c := range candidates {
		data, err := os.ReadFile(c.Path)
		if err != nil || bytes.Equal(data, fresh) {
			continue
		}
		replaced := len(prev) > 0 && bytes.Equal(data, prev)
		if !replaced && !c.NameMatched {
			continue
		}
		out = append(out, staleLoginLayer{Kind: c.Kind, Label: c.Label, Path: c.Path, Replaced: replaced})
	}
	return out
}

// printStaleLoginLayers writes the post-login warning. It prints nothing when
// every layer is converged.
func printStaleLoginLayers(w io.Writer, tool, name string, prof *profile.Profile, profileAuthPath string, stale []staleLoginLayer) {
	if len(stale) == 0 {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nWarning: this login updated only the isolated profile credential:\n  %s\n", profileAuthPath)
	fmt.Fprintf(&b, "Other caam layers for %s still hold a different credential generation:\n", tool)
	for _, s := range stale {
		note := "differs from this login"
		if s.Replaced {
			note = "holds the generation this login replaced; the provider has likely revoked it"
		}
		fmt.Fprintf(&b, "  %-8s %-24s %s\n           (%s)\n", s.Kind, s.Label, s.Path, note)
	}
	b.WriteString("Sessions served from those layers keep using the old credential until they are converged:\n")

	for _, s := range stale {
		switch s.Kind {
		case "vault":
			if tool == "codex" && prof != nil {
				fmt.Fprintf(&b, "  CODEX_HOME=%s caam backup %s %s\n", shellQuote(prof.CodexHomePath()), tool, shellQuote(name))
			} else {
				fmt.Fprintf(&b, "  caam backup %s %s   # after making this profile's credential the live one\n", tool, shellQuote(name))
			}
		case "shallow":
			fmt.Fprintf(&b, "  caam shallow-profile create %s --tool %s --from-file %s --force\n", shellQuote(s.Label), tool, shellQuote(profileAuthPath))
		}
	}
	io.WriteString(w, b.String())
}
