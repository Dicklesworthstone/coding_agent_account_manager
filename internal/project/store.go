package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type StoreData struct {
	Version      int                          `json:"version"`
	Associations map[string]map[string]string `json:"associations,omitempty"`
	Defaults     map[string]string            `json:"defaults,omitempty"`
}

type Resolved struct {
	Profiles map[string]string
	Sources  map[string]string
}

type Store struct {
	path string
	mu   sync.RWMutex
}

func NewStore(path string) *Store {
	if path == "" {
		path = DefaultPath()
	}
	return &Store{path: path}
}

func DefaultPath() string {
	if caamHome := os.Getenv("CAAM_HOME"); caamHome != "" {
		return filepath.Join(caamHome, "projects.json")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".caam", "projects.json")
	}
	return filepath.Join(homeDir, ".caam", "projects.json")
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Load() (*StoreData, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return newStoreData(), nil
		}
		return nil, fmt.Errorf("read projects file: %w", err)
	}

	parsed := newStoreData()
	if err := json.Unmarshal(data, parsed); err != nil {
		// Corrupt config should not crash caam; return empty store.
		return newStoreData(), nil
	}

	normalizeStoreData(parsed)
	return parsed, nil
}

func (s *Store) Save(store *StoreData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if store == nil {
		store = newStoreData()
	}
	normalizeStoreData(store)

	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create projects dir: %w", err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal projects data: %w", err)
	}

	tmpPath := s.path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp projects file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp projects file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp projects file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp projects file: %w", err)
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp projects file: %w", err)
	}

	return nil
}

func (s *Store) SetAssociation(projectPath, provider, profile string) error {
	provider = normalizeProvider(provider)
	profile = strings.TrimSpace(profile)
	if provider == "" {
		return fmt.Errorf("provider cannot be empty")
	}
	if profile == "" {
		return fmt.Errorf("profile cannot be empty")
	}

	key, err := normalizeKey(projectPath)
	if err != nil {
		return err
	}

	store, err := s.Load()
	if err != nil {
		return err
	}

	if store.Associations[key] == nil {
		store.Associations[key] = make(map[string]string)
	}
	store.Associations[key][provider] = profile

	return s.Save(store)
}

func (s *Store) RemoveAssociation(projectPath, provider string) error {
	provider = normalizeProvider(provider)
	if provider == "" {
		return fmt.Errorf("provider cannot be empty")
	}
	key, err := normalizeKey(projectPath)
	if err != nil {
		return err
	}

	store, err := s.Load()
	if err != nil {
		return err
	}

	if store.Associations[key] == nil {
		return nil
	}

	delete(store.Associations[key], provider)
	if len(store.Associations[key]) == 0 {
		delete(store.Associations, key)
	}

	return s.Save(store)
}

func (s *Store) DeleteProject(projectPath string) error {
	key, err := normalizeKey(projectPath)
	if err != nil {
		return err
	}

	store, err := s.Load()
	if err != nil {
		return err
	}

	delete(store.Associations, key)
	return s.Save(store)
}

func (s *Store) SetDefault(provider, profile string) error {
	provider = normalizeProvider(provider)
	profile = strings.TrimSpace(profile)
	if provider == "" {
		return fmt.Errorf("provider cannot be empty")
	}
	if profile == "" {
		return fmt.Errorf("profile cannot be empty")
	}

	store, err := s.Load()
	if err != nil {
		return err
	}
	store.Defaults[provider] = profile
	return s.Save(store)
}

func (s *Store) Resolve(dir string) (*Resolved, error) {
	absDir, err := normalizeKey(dir)
	if err != nil {
		return nil, err
	}

	store, err := s.Load()
	if err != nil {
		return nil, err
	}

	resolved := &Resolved{
		Profiles: make(map[string]string),
		Sources:  make(map[string]string),
	}

	for _, candidate := range parentDirs(absDir) {
		// Exact match at this directory has highest precedence at this depth.
		if assoc, ok := store.Associations[candidate]; ok {
			applyIfUnset(resolved, assoc, candidate)
		}

		// Then apply glob matches for this directory by specificity.
		for _, m := range matchingGlobs(store.Associations, candidate) {
			applyIfUnset(resolved, store.Associations[m.pattern], m.pattern)
		}
	}

	for provider, profile := range store.Defaults {
		normalizedProvider := normalizeProvider(provider)
		profile = strings.TrimSpace(profile)
		if normalizedProvider == "" || profile == "" {
			continue
		}
		if _, ok := resolved.Profiles[normalizedProvider]; ok {
			continue
		}
		resolved.Profiles[normalizedProvider] = profile
		resolved.Sources[normalizedProvider] = "<default>"
	}

	return resolved, nil
}

func applyIfUnset(resolved *Resolved, assoc map[string]string, source string) {
	if resolved == nil || len(assoc) == 0 {
		return
	}

	for provider, profile := range assoc {
		provider = normalizeProvider(provider)
		profile = strings.TrimSpace(profile)
		if provider == "" || profile == "" {
			continue
		}
		if _, ok := resolved.Profiles[provider]; ok {
			continue
		}
		resolved.Profiles[provider] = profile
		resolved.Sources[provider] = source
	}
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func parentDirs(path string) []string {
	cleaned := filepath.Clean(path)
	dirs := make([]string, 0, 8)
	for {
		dirs = append(dirs, cleaned)
		parent := filepath.Dir(cleaned)
		if parent == cleaned {
			break
		}
		cleaned = parent
	}
	return dirs
}

func normalizeKey(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	cleaned := filepath.Clean(path)

	// If it's a glob pattern, preserve it as-is (don't absolutize)
	if isGlob(cleaned) {
		return cleaned, nil
	}

	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}

	return abs, nil
}

func newStoreData() *StoreData {
	return &StoreData{
		Version:      1,
		Associations: make(map[string]map[string]string),
		Defaults:     make(map[string]string),
	}
}

func normalizeStoreData(store *StoreData) {
	if store.Version < 1 {
		store.Version = 1
	}
	if store.Associations == nil {
		store.Associations = make(map[string]map[string]string)
	}
	if store.Defaults == nil {
		store.Defaults = make(map[string]string)
	}

	store.Defaults = normalizeProviderMap(store.Defaults)
	store.Associations = normalizeAssociations(store.Associations)
}

func normalizeProviderMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return make(map[string]string)
	}

	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]string, len(in))
	for _, k := range keys {
		provider := normalizeProvider(k)
		profile := strings.TrimSpace(in[k])
		if provider == "" || profile == "" {
			continue
		}
		out[provider] = profile
	}
	return out
}

func normalizeAssociations(in map[string]map[string]string) map[string]map[string]string {
	if len(in) == 0 {
		return make(map[string]map[string]string)
	}

	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]map[string]string, len(in))
	for _, k := range keys {
		assoc := in[k]
		if len(assoc) == 0 {
			continue
		}

		normalizedKey, err := normalizeKey(k)
		if err != nil {
			continue
		}

		normalizedAssoc := normalizeProviderMap(assoc)
		if len(normalizedAssoc) == 0 {
			continue
		}

		// Merge in case multiple keys normalize to the same path.
		existing := out[normalizedKey]
		if existing == nil {
			out[normalizedKey] = normalizedAssoc
			continue
		}
		for provider, profile := range normalizedAssoc {
			existing[provider] = profile
		}
	}
	return out
}
