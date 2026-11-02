package shallow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

// Both names are read by identity.ExtractFromClaudeCredentials. Neither may
// come from the real HOME through the shallow profile's symlink farm.
var claudeIdentityFiles = []string{"meta.json", identity.ClaudeIdentityCacheFile}

// Claude also recognizes an alternate auth document and nested session state.
// Reserving only the primary OAuth file would still expose those host inputs.
var claudePrivateAccountFiles = []string{"meta.json", identity.ClaudeIdentityCacheFile, "auth.json", ".claude.json"}

type claudeCredentialSeed struct {
	credentials []byte
	binding     *identity.ClaudeIdentityBinding
	inputs      map[string][]byte
}

// Capture the selected token generation and its offline proof together. Saved
// metadata defines an expected owner: substituted credentials cannot silently
// turn a named vault profile into a differently owned shallow profile. A stale
// native cache is ordinary renewal uncertainty and contributes no identity.
func captureClaudeCredentialSeed(source string) (*claudeCredentialSeed, error) {
	if source == "" {
		return nil, nil
	}
	credentials, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	seed := &claudeCredentialSeed{credentials: credentials, inputs: map[string][]byte{source: credentials}}
	for _, name := range claudeIdentityFiles {
		path := filepath.Join(filepath.Dir(source), name)
		data, err := claudesettings.Read(path)
		if err != nil {
			return nil, fmt.Errorf("read Claude source identity: %w", err)
		}
		seed.inputs[path] = data
		if data == nil {
			continue
		}
		assessment, err := identity.InspectClaudeIdentityBinding(data, credentials)
		if err != nil {
			return nil, err
		}
		if assessment.State == identity.ClaudeIdentityVerified {
			seed.binding = assessment.Binding
			break
		}
		if assessment.State == identity.ClaudeIdentityCredentialChanged {
			if name == "meta.json" {
				return nil, fmt.Errorf("saved Claude identity does not match the selected credentials; verify the source profile before copying it")
			}
			break
		}
	}
	return seed, seed.checkUnchanged()
}

func (s *claudeCredentialSeed) checkUnchanged() error {
	for path, before := range s.inputs {
		current, err := claudesettings.Read(path)
		if err != nil || (before == nil) != (current == nil) || !bytes.Equal(before, current) {
			return fmt.Errorf("claude credential source changed while creating the shallow profile; retry")
		}
	}
	return nil
}

func claudeSeedOAuthAccount(binding *identity.ClaudeIdentityBinding) json.RawMessage {
	if binding == nil {
		return nil
	}
	account := map[string]string{
		"accountUuid":      binding.Identity.AccountID,
		"organizationUuid": binding.Identity.OrganizationID,
	}
	if binding.Identity.Email != "" {
		account["emailAddress"] = binding.Identity.Email
	}
	if binding.Identity.Organization != "" {
		account["organizationName"] = binding.Identity.Organization
	}
	data, _ := json.Marshal(account)
	return data
}

// Staging is private and unpublished. Only the exact copied token's cached
// proof can supply an account label, even with --from-claude-json. Unrelated
// settings and session fields are preserved; a mutable source label is not
// evidence that it belongs to the selected credentials.
func writeClaudeSeedIdentity(home string, opts CreateOptions) error {
	var binding *identity.ClaudeIdentityBinding
	if opts.claudeSeed != nil {
		binding = opts.claudeSeed.binding
	}
	if opts.SourceClaudeJSON != "" || binding != nil {
		path := filepath.Join(home, ".claude.json")
		before, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		after, err := claudesettings.WithOAuthAccount(before, claudeSeedOAuthAccount(binding))
		if err != nil {
			return fmt.Errorf("normalize copied Claude account metadata: %w", err)
		}
		if !bytes.Equal(before, after) {
			if err := writeFileAtomic(path, after, 0o600); err != nil {
				return err
			}
		}
	}
	if binding == nil {
		return nil
	}
	data, err := marshalClaudeIdentityCache(binding)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(home, ".claude", identity.ClaudeIdentityCacheFile), data, 0o600)
}

func marshalClaudeIdentityCache(binding *identity.ClaudeIdentityBinding) ([]byte, error) {
	return json.Marshal(struct {
		Binding *identity.ClaudeIdentityBinding `json:"claude_identity,omitempty"`
	}{binding})
}

type claudeIdentityRepair struct {
	path        string
	before      []byte
	after       []byte
	info        os.FileInfo
	credential  string
	credentials []byte
}

// Prepare only recognized links created by the historical symlink farm.
// Private account files stay untouched, and arbitrary links fail closed. Host
// metadata never becomes the profile's expected-owner meta.json. Inherited
// alternate credentials become absent, and nested state becomes private.
func (m *Manager) prepareClaudeIdentityRepairs(home string) ([]claudeIdentityRepair, error) {
	credential := filepath.Join(home, ".claude", ".credentials.json")
	if err := m.checkClaudeIdentityParents(home); err != nil {
		return nil, err
	}
	if err := claudesettings.CheckPrivate(credential, filepath.Join(m.realHome, ".claude", ".credentials.json")); err != nil {
		return nil, err
	}
	credentials, err := claudesettings.Read(credential)
	if err != nil {
		return nil, err
	}
	var repairs []claudeIdentityRepair
	for _, name := range claudePrivateAccountFiles {
		path := filepath.Join(home, ".claude", name)
		host := filepath.Join(m.realHome, ".claude", name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		shared := false
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return nil, err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			shared = filepath.Clean(target) == filepath.Clean(host)
		} else if info.Mode().IsRegular() {
			if hostInfo, err := os.Stat(host); err == nil {
				shared = os.SameFile(info, hostInfo)
			}
		}
		if !shared {
			if _, err := claudesettings.PreparePrivate(path, host, credential); err != nil {
				return nil, err
			}
			continue
		}
		before, err := claudesettings.Read(path)
		if err != nil {
			return nil, err
		}
		var binding *identity.ClaudeIdentityBinding
		if name == identity.ClaudeIdentityCacheFile && before != nil {
			// Shared or malformed metadata contributes nothing unless it is
			// a complete provider proof for these exact private tokens.
			binding, _ = identity.DecodeClaudeIdentityBinding(before, credentials)
		}
		after, err := marshalClaudeIdentityCache(binding)
		if err != nil {
			return nil, err
		}
		repairs = append(repairs, claudeIdentityRepair{
			path: path, before: before, after: after, info: info,
			credential: credential, credentials: credentials,
		})
	}
	return repairs, nil
}

func (m *Manager) checkClaudeIdentityParents(home string) error {
	for _, dir := range []string{home, filepath.Join(home, ".claude")} {
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) && dir != home {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("claude identity directory must be private: %s", dir)
		}
	}
	return nil
}

func (r claudeIdentityRepair) checkUnchanged() error {
	info, err := os.Lstat(r.path)
	if err != nil || !os.SameFile(info, r.info) {
		return fmt.Errorf("claude identity cache changed before isolation repair; retry")
	}
	for path, before := range map[string][]byte{r.path: r.before, r.credential: r.credentials} {
		current, err := claudesettings.Read(path)
		if err != nil || (before == nil) != (current == nil) || !bytes.Equal(before, current) {
			return fmt.Errorf("claude credentials or identity cache changed before isolation repair; retry")
		}
	}
	return nil
}

func (m *Manager) applyClaudeIdentityRepairs(home string, repairs []claudeIdentityRepair) error {
	for _, repair := range repairs {
		guard := func() error {
			if err := m.checkClaudeIdentityParents(home); err != nil {
				return err
			}
			if err := claudesettings.CheckPrivate(repair.credential, filepath.Join(m.realHome, ".claude", ".credentials.json")); err != nil {
				return err
			}
			return repair.checkUnchanged()
		}
		if filepath.Base(repair.path) == "auth.json" {
			if err := retireClaudeInheritedAuth(repair, guard); err != nil {
				return err
			}
			continue
		}
		if err := writeFileAtomicGuarded(repair.path, repair.after, 0o600, guard); err != nil {
			return fmt.Errorf("detach shared Claude account file: %w", err)
		}
	}
	return nil
}

// An empty auth.json is still an installed but invalid credential artifact.
// Retire only the inherited alias. Move it aside first so a native login that
// races the final guard is preserved rather than unlinked as though it were
// the old host link. Neither path ever writes the alias's referent.
func retireClaudeInheritedAuth(repair claudeIdentityRepair, guard func() error) error {
	if err := guard(); err != nil {
		return err
	}
	placeholder, err := os.CreateTemp(filepath.Dir(repair.path), ".caam-inherited-auth-")
	if err != nil {
		return err
	}
	backup := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(backup)
		return err
	}
	if err := guard(); err != nil {
		_ = os.Remove(backup)
		return err
	}
	if err := os.Rename(repair.path, backup); err != nil {
		_ = os.Remove(backup)
		return err
	}
	info, err := os.Lstat(backup)
	if err != nil || !os.SameFile(info, repair.info) {
		// Link restores an unexpected entry only if the destination remains
		// absent. Never overwrite a second native login during recovery.
		if err := os.Link(backup, repair.path); err == nil {
			_ = os.Remove(backup)
			return fmt.Errorf("claude alternate credentials changed during isolation repair; retry")
		}
		return fmt.Errorf("claude alternate credentials changed during isolation repair; preserved at %s", backup)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("remove inherited Claude authentication alias: %w", err)
	}
	return nil
}

func (m *Manager) validateClaudeIdentityBoundary(home string) error {
	credential := filepath.Join(home, ".claude", ".credentials.json")
	for _, name := range claudePrivateAccountFiles {
		if _, err := claudesettings.PreparePrivate(
			filepath.Join(home, ".claude", name), filepath.Join(m.realHome, ".claude", name), credential,
		); err != nil {
			return err
		}
	}
	return nil
}
