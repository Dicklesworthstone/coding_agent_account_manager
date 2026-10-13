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
	if vault == nil {
		return fmt.Errorf("vault is required")
	}
	return s.withVaultBackupSource(name, func(root string) error {
		return vault.BackupIsolatedClaude(root, target)
	})
}

// withVaultBackupSource holds the existing cross-process mutation lock for the
// entire capture and publication, not just its final source check. Profile
// launches, forced imports, deletion and cloning take this same lock. Without
// it, a managed writer could start after the last optimistic check and before
// publication, leaving a new vault snapshot with an already-rotated grant.
//
// The lock is outside the source profile; do not create/remove its session lock
// or clear a stale one. BackupIsolatedClaude still checks native writers that
// do not participate in CAAM's locking protocol.
func (s *Store) withVaultBackupSource(name string, backup func(string) error) error {
	if s == nil || strings.TrimSpace(s.basePath) == "" {
		return fmt.Errorf("profile store base path is empty")
	}
	name, err := validateStoreSegment("name", name)
	if err != nil {
		return err
	}
	unlock, err := s.lockMutation("claude", name)
	if err != nil {
		return fmt.Errorf("hold isolated backup source: %w", err)
	}
	defer unlock()
	return backup(s.ProfilePath("claude", name))
}
