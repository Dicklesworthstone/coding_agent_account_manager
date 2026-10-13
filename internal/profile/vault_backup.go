package profile

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// BackupClaudeToVault bridges a registered isolated profile to a new vault
// snapshot. Validate names without Load: Load extracts identity from files and
// persisted BasePath before the snapshot reader can enforce its safety checks.
func (s *Store) BackupClaudeToVault(vault *authfile.Vault, name, target string) error {
	if s == nil || strings.TrimSpace(s.basePath) == "" {
		return fmt.Errorf("profile store base path is empty")
	}
	if vault == nil {
		return fmt.Errorf("vault is required")
	}
	name, err := validateStoreSegment("name", name)
	if err != nil {
		return err
	}
	return vault.BackupIsolatedClaude(s.ProfilePath("claude", name), target)
}
