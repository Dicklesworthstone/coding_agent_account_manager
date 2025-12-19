package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SyncDirection indicates the direction of a sync operation.
type SyncDirection string

const (
	// SyncPush indicates pushing local data to remote.
	SyncPush SyncDirection = "push"
	// SyncPull indicates pulling remote data to local.
	SyncPull SyncDirection = "pull"
	// SyncSkip indicates no sync is needed (already in sync).
	SyncSkip SyncDirection = "skip"
)

// SyncOperation represents a planned sync operation.
type SyncOperation struct {
	// Provider is the auth provider (claude, codex, gemini).
	Provider string

	// Profile is the profile name.
	Profile string

	// Direction indicates push or pull.
	Direction SyncDirection

	// Machine is the target machine for the operation.
	Machine *Machine

	// LocalFreshness is the freshness of the local token.
	LocalFreshness *TokenFreshness

	// RemoteFreshness is the freshness of the remote token.
	RemoteFreshness *TokenFreshness
}

// SyncResult represents the result of a sync operation.
type SyncResult struct {
	// Operation is the sync operation that was executed.
	Operation *SyncOperation

	// Success indicates if the operation succeeded.
	Success bool

	// BytesSent is the number of bytes sent during the operation.
	BytesSent int64

	// BytesReceived is the number of bytes received during the operation.
	BytesReceived int64

	// Duration is how long the operation took.
	Duration time.Duration

	// Error is any error that occurred.
	Error error
}

// Syncer performs sync operations between local and remote machines.
type Syncer struct {
	// pool manages SSH connections.
	pool *ConnectionPool

	// state is the sync state (queue, history, etc.).
	state *SyncState

	// vaultPath is the local vault directory path.
	vaultPath string

	// remoteVaultPath is the remote vault directory path pattern.
	remoteVaultPath string
}

// SyncerConfig configures a Syncer instance.
type SyncerConfig struct {
	// VaultPath is the local vault directory.
	VaultPath string

	// RemoteVaultPath is the remote vault directory.
	// If empty, defaults to ~/.local/share/caam/vault
	RemoteVaultPath string

	// ConnectOptions configures SSH connections.
	ConnectOptions ConnectOptions
}

// DefaultSyncerConfig returns a default configuration.
func DefaultSyncerConfig() SyncerConfig {
	homeDir, _ := os.UserHomeDir()
	vaultPath := filepath.Join(homeDir, ".local", "share", "caam", "vault")

	return SyncerConfig{
		VaultPath:       vaultPath,
		RemoteVaultPath: ".local/share/caam/vault",
		ConnectOptions:  DefaultConnectOptions(),
	}
}

// NewSyncer creates a new Syncer with the given configuration.
func NewSyncer(config SyncerConfig) (*Syncer, error) {
	state, err := LoadSyncState()
	if err != nil {
		return nil, fmt.Errorf("load sync state: %w", err)
	}

	if config.VaultPath == "" {
		config.VaultPath = DefaultSyncerConfig().VaultPath
	}
	if config.RemoteVaultPath == "" {
		config.RemoteVaultPath = DefaultSyncerConfig().RemoteVaultPath
	}

	return &Syncer{
		pool:            NewConnectionPool(config.ConnectOptions),
		state:           state,
		vaultPath:       config.VaultPath,
		remoteVaultPath: config.RemoteVaultPath,
	}, nil
}

// Close releases all resources held by the Syncer.
func (s *Syncer) Close() error {
	s.pool.CloseAll()
	return s.state.Save()
}

// SyncWithMachine synchronizes all profiles with a single machine.
func (s *Syncer) SyncWithMachine(ctx context.Context, m *Machine) ([]*SyncResult, error) {
	results := []*SyncResult{}

	// 1. Connect to remote
	client, err := s.pool.Get(m)
	if err != nil {
		m.SetError(err.Error())
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	// 2. Get local profiles
	localProfiles, err := s.listLocalProfiles()
	if err != nil {
		return nil, fmt.Errorf("list local profiles: %w", err)
	}

	// 3. Get remote profiles
	remoteProfiles, err := s.listRemoteProfiles(client)
	if err != nil {
		return nil, fmt.Errorf("list remote profiles: %w", err)
	}

	// 4. Merge profile lists (union)
	allProfiles := mergeProfileLists(localProfiles, remoteProfiles)

	// 5. For each profile, compare and sync
	for _, p := range allProfiles {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		op, err := s.determineSyncOperation(client, m, p)
		if err != nil {
			// Log error but continue with other profiles
			results = append(results, &SyncResult{
				Operation: &SyncOperation{
					Provider:  p.Provider,
					Profile:   p.Profile,
					Direction: SyncSkip,
					Machine:   m,
				},
				Success: false,
				Error:   err,
			})
			continue
		}

		if op == nil || op.Direction == SyncSkip {
			continue // Already in sync
		}

		result := s.executeOperation(client, op)
		results = append(results, result)

		// Record in history
		action := string(op.Direction)
		s.state.AddToHistory(HistoryEntry{
			Timestamp: time.Now(),
			Trigger:   "manual",
			Provider:  op.Provider,
			Profile:   op.Profile,
			Machine:   m.Name,
			Action:    action,
			Success:   result.Success,
			Error:     errorToString(result.Error),
			Duration:  result.Duration,
		})

		// Update queue
		if result.Success {
			s.state.RemoveFromQueue(op.Provider, op.Profile, m.ID)
		} else {
			s.state.AddToQueue(op.Provider, op.Profile, m.ID, errorToString(result.Error))
		}
	}

	return results, nil
}

// SyncProfile synchronizes a specific profile with all machines.
func (s *Syncer) SyncProfile(ctx context.Context, provider, profile string) ([]*SyncResult, error) {
	if s.state.Pool == nil || s.state.Pool.IsEmpty() {
		return nil, nil
	}

	var allResults []*SyncResult

	for _, m := range s.state.Pool.Machines {
		select {
		case <-ctx.Done():
			return allResults, ctx.Err()
		default:
		}

		client, err := s.pool.Get(m)
		if err != nil {
			m.SetError(err.Error())
			allResults = append(allResults, &SyncResult{
				Operation: &SyncOperation{
					Provider:  provider,
					Profile:   profile,
					Direction: SyncSkip,
					Machine:   m,
				},
				Success: false,
				Error:   err,
			})
			s.state.AddToQueue(provider, profile, m.ID, err.Error())
			continue
		}

		p := ProfileRef{Provider: provider, Profile: profile}
		op, err := s.determineSyncOperation(client, m, p)
		if err != nil {
			allResults = append(allResults, &SyncResult{
				Operation: &SyncOperation{
					Provider:  provider,
					Profile:   profile,
					Direction: SyncSkip,
					Machine:   m,
				},
				Success: false,
				Error:   err,
			})
			continue
		}

		if op == nil || op.Direction == SyncSkip {
			continue
		}

		result := s.executeOperation(client, op)
		allResults = append(allResults, result)

		// Record in history
		s.state.AddToHistory(HistoryEntry{
			Timestamp: time.Now(),
			Trigger:   "manual",
			Provider:  provider,
			Profile:   profile,
			Machine:   m.Name,
			Action:    string(op.Direction),
			Success:   result.Success,
			Error:     errorToString(result.Error),
			Duration:  result.Duration,
		})

		if result.Success {
			s.state.RemoveFromQueue(provider, profile, m.ID)
		} else {
			s.state.AddToQueue(provider, profile, m.ID, errorToString(result.Error))
		}
	}

	return allResults, nil
}

// SyncAll synchronizes all profiles with all machines.
func (s *Syncer) SyncAll(ctx context.Context) ([]*SyncResult, error) {
	if s.state.Pool == nil || s.state.Pool.IsEmpty() {
		return nil, nil
	}

	var allResults []*SyncResult

	for _, m := range s.state.Pool.Machines {
		select {
		case <-ctx.Done():
			return allResults, ctx.Err()
		default:
		}

		results, err := s.SyncWithMachine(ctx, m)
		if err != nil {
			// Machine-level error, continue with others
			continue
		}
		allResults = append(allResults, results...)
	}

	return allResults, nil
}

// determineSyncOperation determines what sync operation is needed for a profile.
func (s *Syncer) determineSyncOperation(client *SSHClient, m *Machine, p ProfileRef) (*SyncOperation, error) {
	localFresh, localErr := s.getLocalFreshness(p)
	remoteFresh, remoteErr := s.getRemoteFreshness(client, p)

	// Check if errors are "not found" vs other errors
	localNotFound := localErr != nil && os.IsNotExist(localErr)
	remoteNotFound := remoteErr != nil && os.IsNotExist(remoteErr)
	localOtherErr := localErr != nil && !os.IsNotExist(localErr)
	remoteOtherErr := remoteErr != nil && !os.IsNotExist(remoteErr)

	op := &SyncOperation{
		Provider:        p.Provider,
		Profile:         p.Profile,
		Machine:         m,
		LocalFreshness:  localFresh,
		RemoteFreshness: remoteFresh,
	}

	switch {
	case localOtherErr && remoteOtherErr:
		// Both have non-"not found" errors
		return nil, fmt.Errorf("local error: %v, remote error: %v", localErr, remoteErr)

	case localOtherErr:
		// Local has a real error (not "not found"), can't sync
		return nil, fmt.Errorf("local error: %v", localErr)

	case remoteOtherErr:
		// Remote has a real error (not "not found"), can't sync
		return nil, fmt.Errorf("remote error: %v", remoteErr)

	case localNotFound && remoteNotFound:
		// Neither exists
		op.Direction = SyncSkip
		return op, nil

	case localNotFound && remoteFresh != nil:
		// Only exists on remote: pull
		op.Direction = SyncPull
		return op, nil

	case localFresh != nil && remoteNotFound:
		// Only exists locally: push
		op.Direction = SyncPush
		return op, nil

	case CompareFreshness(localFresh, remoteFresh):
		// Local is fresher: push
		op.Direction = SyncPush
		return op, nil

	case CompareFreshness(remoteFresh, localFresh):
		// Remote is fresher: pull
		op.Direction = SyncPull
		return op, nil

	default:
		// Equal freshness: no action
		op.Direction = SyncSkip
		return op, nil
	}
}

// executeOperation executes a sync operation.
func (s *Syncer) executeOperation(client *SSHClient, op *SyncOperation) *SyncResult {
	start := time.Now()

	result := &SyncResult{
		Operation: op,
	}

	switch op.Direction {
	case SyncPush:
		err := s.pushProfile(client, op.Provider, op.Profile)
		result.Error = err
		result.Success = err == nil

	case SyncPull:
		err := s.pullProfile(client, op.Provider, op.Profile)
		result.Error = err
		result.Success = err == nil

	case SyncSkip:
		result.Success = true
	}

	result.Duration = time.Since(start)
	return result
}

// pushProfile pushes a local profile to the remote machine.
func (s *Syncer) pushProfile(client *SSHClient, provider, profile string) error {
	localPath := filepath.Join(s.vaultPath, provider, profile)
	remotePath := filepath.Join(s.remoteVaultPath, provider, profile)

	// Read local files
	files, err := s.readLocalProfileFiles(localPath)
	if err != nil {
		return fmt.Errorf("read local files: %w", err)
	}

	// Write to remote
	for filename, data := range files {
		remoteFilePath := filepath.Join(remotePath, filename)
		if err := client.WriteFile(remoteFilePath, data, 0600); err != nil {
			return fmt.Errorf("write remote file %s: %w", filename, err)
		}
	}

	return nil
}

// pullProfile pulls a remote profile to the local machine.
func (s *Syncer) pullProfile(client *SSHClient, provider, profile string) error {
	localPath := filepath.Join(s.vaultPath, provider, profile)
	remotePath := filepath.Join(s.remoteVaultPath, provider, profile)

	// List remote files
	remoteFiles, err := client.ListDir(remotePath)
	if err != nil {
		return fmt.Errorf("list remote files: %w", err)
	}

	// Ensure local directory exists
	if err := os.MkdirAll(localPath, 0700); err != nil {
		return fmt.Errorf("create local directory: %w", err)
	}

	// Read remote files and write locally
	for _, fi := range remoteFiles {
		if fi.IsDir() {
			continue
		}

		remoteFilePath := filepath.Join(remotePath, fi.Name())
		data, err := client.ReadFile(remoteFilePath)
		if err != nil {
			return fmt.Errorf("read remote file %s: %w", fi.Name(), err)
		}

		localFilePath := filepath.Join(localPath, fi.Name())
		if err := os.WriteFile(localFilePath, data, 0600); err != nil {
			return fmt.Errorf("write local file %s: %w", fi.Name(), err)
		}
	}

	return nil
}

// readLocalProfileFiles reads all files from a local profile directory.
func (s *Syncer) readLocalProfileFiles(profilePath string) (map[string][]byte, error) {
	files := make(map[string][]byte)

	entries, err := os.ReadDir(profilePath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(profilePath, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}

		files[entry.Name()] = data
	}

	return files, nil
}

// getLocalFreshness gets the freshness of a local profile.
func (s *Syncer) getLocalFreshness(p ProfileRef) (*TokenFreshness, error) {
	profilePath := filepath.Join(s.vaultPath, p.Provider, p.Profile)

	// Check if directory exists
	if _, err := os.Stat(profilePath); os.IsNotExist(err) {
		return nil, err
	}

	// Find auth files
	var authFiles []string
	entries, err := os.ReadDir(profilePath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			authFiles = append(authFiles, filepath.Join(profilePath, entry.Name()))
		}
	}

	return ExtractFreshnessFromFiles(p.Provider, p.Profile, authFiles)
}

// getRemoteFreshness gets the freshness of a remote profile.
func (s *Syncer) getRemoteFreshness(client *SSHClient, p ProfileRef) (*TokenFreshness, error) {
	remotePath := filepath.Join(s.remoteVaultPath, p.Provider, p.Profile)

	// Check if remote directory exists
	exists, err := client.FileExists(remotePath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, os.ErrNotExist
	}

	// List remote files
	files, err := client.ListDir(remotePath)
	if err != nil {
		return nil, err
	}

	// Read remote auth files
	authFiles := make(map[string][]byte)
	for _, fi := range files {
		if fi.IsDir() {
			continue
		}

		filePath := filepath.Join(remotePath, fi.Name())
		data, err := client.ReadFile(filePath)
		if err != nil {
			continue // Skip files we can't read
		}

		authFiles[filePath] = data
	}

	if len(authFiles) == 0 {
		return nil, fmt.Errorf("no auth files found in remote profile")
	}

	freshness, err := ExtractFreshnessFromBytes(p.Provider, p.Profile, authFiles)
	if err != nil {
		return nil, err
	}

	freshness.Source = client.machine.Name
	return freshness, nil
}

// listLocalProfiles lists all profiles in the local vault.
func (s *Syncer) listLocalProfiles() ([]ProfileRef, error) {
	var profiles []ProfileRef

	providers := []string{"claude", "codex", "gemini"}

	for _, provider := range providers {
		providerPath := filepath.Join(s.vaultPath, provider)

		entries, err := os.ReadDir(providerPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		for _, entry := range entries {
			if entry.IsDir() {
				profiles = append(profiles, ProfileRef{
					Provider: provider,
					Profile:  entry.Name(),
				})
			}
		}
	}

	return profiles, nil
}

// listRemoteProfiles lists all profiles in the remote vault.
func (s *Syncer) listRemoteProfiles(client *SSHClient) ([]ProfileRef, error) {
	var profiles []ProfileRef

	providers := []string{"claude", "codex", "gemini"}

	for _, provider := range providers {
		providerPath := filepath.Join(s.remoteVaultPath, provider)

		entries, err := client.ListDir(providerPath)
		if err != nil {
			// Directory might not exist
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				profiles = append(profiles, ProfileRef{
					Provider: provider,
					Profile:  entry.Name(),
				})
			}
		}
	}

	return profiles, nil
}

// mergeProfileLists merges two lists of profiles, removing duplicates.
func mergeProfileLists(a, b []ProfileRef) []ProfileRef {
	seen := make(map[string]bool)
	var result []ProfileRef

	for _, p := range a {
		key := p.Provider + "/" + p.Profile
		if !seen[key] {
			seen[key] = true
			result = append(result, p)
		}
	}

	for _, p := range b {
		key := p.Provider + "/" + p.Profile
		if !seen[key] {
			seen[key] = true
			result = append(result, p)
		}
	}

	return result
}

// errorToString converts an error to a string, returning empty for nil.
func errorToString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// SyncStats contains statistics about sync operations.
type SyncStats struct {
	Total     int
	Pushed    int
	Pulled    int
	Skipped   int
	Failed    int
	BytesSent int64
	BytesRecv int64
	Duration  time.Duration
}

// AggregateResults computes statistics from sync results.
func AggregateResults(results []*SyncResult) SyncStats {
	stats := SyncStats{}

	for _, r := range results {
		stats.Total++

		if !r.Success {
			stats.Failed++
			continue
		}

		switch r.Operation.Direction {
		case SyncPush:
			stats.Pushed++
		case SyncPull:
			stats.Pulled++
		case SyncSkip:
			stats.Skipped++
		}

		stats.BytesSent += r.BytesSent
		stats.BytesRecv += r.BytesReceived
		stats.Duration += r.Duration
	}

	return stats
}
