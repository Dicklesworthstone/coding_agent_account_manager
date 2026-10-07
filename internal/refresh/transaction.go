package refresh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	profilepkg "github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

// RefreshOption supplies the isolated profile store used by the caller.
type RefreshOption func(*refreshOptions)

type refreshOptions struct {
	profiles *profilepkg.Store
	// activity receives refresh outcomes; nil uses the default database.
	activity caamdb.EventLogger
}

// WithActivityLog records refresh outcomes in the given log instead of the
// default activity database.
func WithActivityLog(log caamdb.EventLogger) RefreshOption {
	return func(options *refreshOptions) { options.activity = log }
}

// recordActivity appends a refresh outcome to the activity log so history
// and usage reports show refreshes and their failures. Best-effort: the log
// must never fail or delay a refresh beyond opening the database.
func (o refreshOptions) recordActivity(event caamdb.Event) {
	if o.activity != nil {
		_ = o.activity.Log(event)
		return
	}
	db, err := caamdb.Open()
	if err != nil {
		return
	}
	defer db.Close()
	_ = db.Log(event)
}

func clearCredentialExpiry(auth map[string]interface{}) {
	for _, field := range []string{"expires_at", "expiresAt", "expiry", "expires_in", "expiresIn"} {
		delete(auth, field)
	}
}

func WithProfileStore(store *profilepkg.Store) RefreshOption {
	return func(options *refreshOptions) { options.profiles = store }
}

// credentialSnapshot binds a request and its publication to one file generation.
// The logical path is retained to detect changed symlinks; publication follows
// the originally selected regular file instead of replacing an adoption link.
type credentialSnapshot struct {
	path, canonical string
	data            []byte
	info            os.FileInfo
}

func readCredentialSnapshot(path string) (*credentialSnapshot, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("credential source is not a regular file")
	}
	f, err := os.Open(canonical)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, ErrCredentialChanged
	}
	links, err := refreshFileLinkCount(f, opened)
	if err != nil || links > 1 {
		return nil, fmt.Errorf("credential source must be a private regular file")
	}
	const maxCredentialSize = 4 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(f, maxCredentialSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCredentialSize {
		return nil, fmt.Errorf("credential source exceeds size limit")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return nil, fmt.Errorf("credential source must contain a JSON object")
	}
	return &credentialSnapshot{path: abs, canonical: canonical, data: data, info: opened}, nil
}

func (s *credentialSnapshot) unchanged() error {
	current, err := readCredentialSnapshot(s.path)
	if err != nil || current.canonical != s.canonical || !os.SameFile(current.info, s.info) ||
		current.info.Mode() != s.info.Mode() || !bytes.Equal(current.data, s.data) {
		return ErrCredentialChanged
	}
	return nil
}

// publish stages exactly the checked snapshot's successor. It never rereads a
// replacement login as the base document, and checks again after fsync. Native
// writers do not share CAAM's lock; these checks detect intervening changes,
// rather than claiming a filesystem-wide transaction with arbitrary writers.
func (s *credentialSnapshot) publish(data []byte) error {
	if err := s.unchanged(); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.canonical), ".caam-refresh-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
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
	if err := s.unchanged(); err != nil {
		return err
	}
	return os.Rename(temp, s.canonical)
}

var errRefreshLockBusy = errors.New("credential refresh already in progress")

func acquireRefreshLocks(ctx context.Context, source *credentialSnapshot, deliveries []refreshDelivery) (func(), error) {
	sources := []*credentialSnapshot{source}
	for _, delivery := range deliveries {
		sources = append(sources, delivery.source)
	}
	// A destination can itself be another invocation's source. A stable order
	// avoids holding A while waiting for B as that invocation waits for A.
	sort.Slice(sources, func(i, j int) bool { return sources[i].canonical < sources[j].canonical })
	var held []func()
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i]()
		}
	}
	for _, snapshot := range sources {
		unlock, err := acquireRefreshLock(ctx, snapshot)
		if err != nil {
			release()
			return nil, err
		}
		held = append(held, unlock)
	}
	return release, nil
}

func acquireRefreshLock(ctx context.Context, source *credentialSnapshot) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := source.canonical + ".caam-refresh.lock"
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refresh lock is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	current, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		f.Close()
		return nil, fmt.Errorf("refresh lock changed while opening")
	}
	for {
		if err := tryRefreshLock(f); err == nil {
			return func() { _ = unlockRefreshFile(f); _ = f.Close() }, nil
		} else if !errors.Is(err, errRefreshLockBusy) {
			f.Close()
			return nil, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// credentialGeneration ignores unrelated configuration and JSON formatting,
// but includes both access and renewal tokens: a shared refresh token alone
// does not prove that an access token has not since rotated.
func credentialGeneration(provider string, data []byte) []byte {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	if provider == "codex" {
		if tokens, ok := raw["tokens"]; ok {
			var nested map[string]json.RawMessage
			if json.Unmarshal(tokens, &nested) != nil {
				return nil
			}
			raw = nested
		}
	}
	selected := make(map[string]json.RawMessage)
	for _, key := range []string{"access_token", "accessToken", "refresh_token", "refreshToken", "id_token", "account_id", "client_id", "client_secret"} {
		if value, ok := raw[key]; ok {
			selected[key] = value
		}
	}
	if len(selected) == 0 {
		return nil
	}
	encoded, _ := json.Marshal(selected)
	return encoded
}

type refreshDelivery struct {
	source *credentialSnapshot
	label  string
}

func captureRefreshDeliveries(provider, name string, source *credentialSnapshot, options refreshOptions) []refreshDelivery {
	var candidates []refreshDelivery
	add := func(path, label string) {
		target, err := readCredentialSnapshot(path)
		if err != nil || target.canonical == source.canonical || os.SameFile(target.info, source.info) {
			return
		}
		generation := credentialGeneration(provider, source.data)
		if len(generation) == 0 || !bytes.Equal(generation, credentialGeneration(provider, target.data)) {
			return
		}
		for _, candidate := range candidates {
			if os.SameFile(candidate.source.info, target.info) {
				return
			}
		}
		candidates = append(candidates, refreshDelivery{source: target, label: label})
	}
	if files, ok := authfile.GetAuthFileSet(provider); ok {
		for _, spec := range files.Files {
			if provider == "codex" && filepath.Base(spec.Path) == "auth.json" {
				add(spec.Path, "active")
			} else if provider == "gemini" && filepath.Base(spec.Path) == "settings.json" {
				if _, selected, err := readGeminiADC(filepath.Dir(spec.Path)); err == nil {
					add(selected, "active")
				}
			}
		}
	}
	if options.profiles != nil {
		if prof, err := options.profiles.Load(provider, name); err == nil {
			switch provider {
			case "codex":
				add(filepath.Join(prof.CodexHomePath(), "auth.json"), "isolated")
			case "gemini":
				if _, selected, err := readGeminiADC(filepath.Join(prof.HomePath(), ".gemini")); err == nil {
					add(selected, "isolated")
				}
			}
		}
	}
	return candidates
}
