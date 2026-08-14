package sync

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
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

// ProgressStage identifies the phase a ProgressEvent describes.
type ProgressStage string

const (
	// StagePlan is emitted once per machine after the profile union is known.
	StagePlan ProgressStage = "plan"
	// StageCompare is emitted before each profile's freshness comparison.
	StageCompare ProgressStage = "compare"
	// StageResult is emitted after a push/pull (or failed) operation completes.
	StageResult ProgressStage = "result"
)

// ProgressEvent is a streaming update emitted during a machine sync so the
// CLI can show per-profile progress instead of going silent for the entire
// SFTP walk (issue #65).
type ProgressEvent struct {
	Stage   ProgressStage
	Machine *Machine
	// Total is the number of profiles that will be examined (StagePlan).
	Total int
	// Index is the 1-based index of the profile being examined (StageCompare).
	Index int
	// Profile is the profile being examined (StageCompare, StageResult).
	Profile ProfileRef
	// Result is the completed operation (StageResult only).
	Result *SyncResult
}

// ProgressFunc receives streaming ProgressEvents. It is called synchronously
// from the sync loop, so implementations should be fast.
type ProgressFunc func(ProgressEvent)

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

	// Progress, when non-nil, receives streaming updates during sync.
	Progress ProgressFunc
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
	return SyncerConfig{
		VaultPath:       authfile.DefaultVaultPath(),
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
	return s.SyncWithMachineFiltered(ctx, m, "", "")
}

// SyncWithMachineFiltered synchronizes profiles with a single machine,
// optionally restricted to a provider and/or a profile name. Empty filters
// mean "all". This is what wires the `caam sync --provider/--profile` flags
// (issue #65: they were declared but never applied).
func (s *Syncer) SyncWithMachineFiltered(ctx context.Context, m *Machine, providerFilter, profileFilter string) ([]*SyncResult, error) {
	results := []*SyncResult{}

	// 1. Connect to remote
	client, err := s.pool.Get(m)
	if err != nil {
		m.SetError(err.Error())
		s.mirrorMachine(m)
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

	// 4. Merge profile lists (union), then apply filters
	allProfiles := filterProfiles(mergeProfileLists(localProfiles, remoteProfiles), providerFilter, profileFilter)

	s.emit(ProgressEvent{Stage: StagePlan, Machine: m, Total: len(allProfiles)})

	// 5. For each profile, compare and sync
	for i, p := range allProfiles {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		s.emit(ProgressEvent{Stage: StageCompare, Machine: m, Index: i + 1, Total: len(allProfiles), Profile: p})

		op, err := s.determineSyncOperation(client, m, p)
		if err != nil {
			// Log error but continue with other profiles
			failed := &SyncResult{
				Operation: &SyncOperation{
					Provider:  p.Provider,
					Profile:   p.Profile,
					Direction: SyncSkip,
					Machine:   m,
				},
				Success: false,
				Error:   err,
			}
			results = append(results, failed)
			s.emit(ProgressEvent{Stage: StageResult, Machine: m, Profile: p, Result: failed})
			continue
		}

		if op == nil || op.Direction == SyncSkip {
			continue // Already in sync
		}

		result := s.executeOperation(client, op)
		results = append(results, result)
		s.emit(ProgressEvent{Stage: StageResult, Machine: m, Profile: p, Result: result})

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

	// The machine was reachable and the profile walk completed: record the
	// sync on the caller's Machine and mirror it into the Syncer's own pool
	// so it is persisted on Close(). Before this, RecordSync was never called
	// from any production path and `caam sync status` showed "never" forever
	// (issue #65).
	m.RecordSync()
	s.mirrorMachine(m)

	return results, nil
}

// mirrorMachine copies the status/last-sync fields of m onto the Syncer's own
// pool entry with the same ID. Callers (the CLI) load their own SyncState, so
// the Machine pointers they pass in are distinct objects from the ones this
// Syncer's state will persist on Close(); without mirroring, status updates
// were lost and pool.json kept "last_sync": "0001-01-01T00:00:00Z".
func (s *Syncer) mirrorMachine(m *Machine) {
	if s.state == nil || s.state.Pool == nil || m == nil {
		return
	}
	own := s.state.Pool.GetMachine(m.ID)
	if own == nil || own == m {
		return
	}
	own.Status = m.Status
	own.LastSync = m.LastSync
	own.LastError = m.LastError
	own.LastErrorAt = m.LastErrorAt
}

// RecordFullSync marks the timestamp of a completed full (unfiltered) sync in
// the Syncer's own pool, which is persisted on Close().
func (s *Syncer) RecordFullSync() {
	if s.state == nil || s.state.Pool == nil {
		return
	}
	s.state.Pool.RecordFullSync()
}

// emit sends a progress event if a Progress callback is registered.
func (s *Syncer) emit(ev ProgressEvent) {
	if s.Progress != nil {
		s.Progress(ev)
	}
}

// filterProfiles restricts a profile list to a provider and/or profile name.
// Empty filters pass everything through. Matching is exact (case-sensitive),
// consistent with vault directory names.
func filterProfiles(profiles []ProfileRef, provider, profile string) []ProfileRef {
	if provider == "" && profile == "" {
		return profiles
	}
	var out []ProfileRef
	for _, p := range profiles {
		if provider != "" && p.Provider != provider {
			continue
		}
		if profile != "" && p.Profile != profile {
			continue
		}
		out = append(out, p)
	}
	return out
}

// SyncProfileWithMachine syncs a specific profile with a specific machine.
// This is useful for queue processing where we only want to retry the failed machine.
func (s *Syncer) SyncProfileWithMachine(ctx context.Context, provider, profile string, m *Machine) (*SyncResult, error) {
	if m == nil {
		return nil, fmt.Errorf("machine is nil")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	client, err := s.pool.Get(m)
	if err != nil {
		m.SetError(err.Error())
		s.mirrorMachine(m)
		return &SyncResult{
			Operation: &SyncOperation{
				Provider:  provider,
				Profile:   profile,
				Direction: SyncSkip,
				Machine:   m,
			},
			Success: false,
			Error:   err,
		}, nil
	}

	p := ProfileRef{Provider: provider, Profile: profile}
	op, err := s.determineSyncOperation(client, m, p)
	if err != nil {
		return &SyncResult{
			Operation: &SyncOperation{
				Provider:  provider,
				Profile:   profile,
				Direction: SyncSkip,
				Machine:   m,
			},
			Success: false,
			Error:   err,
		}, nil
	}

	if op == nil || op.Direction == SyncSkip {
		return &SyncResult{
			Operation: &SyncOperation{
				Provider:  provider,
				Profile:   profile,
				Direction: SyncSkip,
				Machine:   m,
			},
			Success: true,
		}, nil
	}

	result := s.executeOperation(client, op)

	// Record in history
	s.state.AddToHistory(HistoryEntry{
		Timestamp: time.Now(),
		Trigger:   "retry",
		Provider:  provider,
		Profile:   profile,
		Machine:   m.Name,
		Action:    string(op.Direction),
		Success:   result.Success,
		Error:     errorToString(result.Error),
		Duration:  result.Duration,
	})

	if result.Success {
		m.RecordSync()
		s.mirrorMachine(m)
	}

	return result, nil
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
			m.RecordSync()
			s.mirrorMachine(m)
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
	// Use posixJoin for remote paths since SFTP always uses forward slashes
	remotePath := posixJoin(s.remoteVaultPath, provider, profile)

	// Read local files
	files, err := s.readLocalProfileFiles(localPath)
	if err != nil {
		return fmt.Errorf("read local files: %w", err)
	}

	// Write to remote
	for filename, data := range files {
		remoteFilePath := posixJoin(remotePath, filename)
		if err := client.WriteFile(remoteFilePath, data, 0600); err != nil {
			return fmt.Errorf("write remote file %s: %w", filename, err)
		}
	}

	return nil
}

// pullProfile pulls a remote profile to the local machine.
func (s *Syncer) pullProfile(client *SSHClient, provider, profile string) error {
	localPath := filepath.Join(s.vaultPath, provider, profile)
	// Use posixJoin for remote paths since SFTP always uses forward slashes
	remotePath := posixJoin(s.remoteVaultPath, provider, profile)

	// List remote files
	remoteFiles, err := client.ListDir(remotePath)
	if err != nil {
		return fmt.Errorf("list remote files: %w", err)
	}

	// Ensure local directory exists
	if err := os.MkdirAll(localPath, 0700); err != nil {
		return fmt.Errorf("create local directory: %w", err)
	}

	// Read remote files and write locally using atomic writes
	for _, fi := range remoteFiles {
		if fi.IsDir() {
			continue
		}

		remoteFilePath := posixJoin(remotePath, fi.Name())
		data, err := client.ReadFile(remoteFilePath)
		if err != nil {
			return fmt.Errorf("read remote file %s: %w", fi.Name(), err)
		}

		localFilePath := filepath.Join(localPath, fi.Name())
		if err := atomicWriteFile(localFilePath, data, 0600); err != nil {
			return fmt.Errorf("write local file %s: %w", fi.Name(), err)
		}
	}

	return nil
}

// atomicWriteFile writes data to a file atomically using temp file + fsync + rename.
// This prevents data corruption if the operation is interrupted.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)

	// Generate unique temp file name
	tmpName := fmt.Sprintf(".caam_tmp_%s", localRandomString(8))
	tmpPath := filepath.Join(dir, tmpName)

	// Write to temp file
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}

	// Sync to disk before rename to ensure durability
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// localRandomString generates a random string for temp file names.
func localRandomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
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
	// Use posixJoin for remote paths since SFTP always uses forward slashes
	remotePath := posixJoin(s.remoteVaultPath, p.Provider, p.Profile)

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

		filePath := posixJoin(remotePath, fi.Name())
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

	providers := []string{"claude", "codex", "gemini", "opencode", "cursor"}

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

	providers := []string{"claude", "codex", "gemini", "opencode", "cursor"}

	for _, provider := range providers {
		// Use posixJoin for remote paths since SFTP always uses forward slashes
		providerPath := posixJoin(s.remoteVaultPath, provider)

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
