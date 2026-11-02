package cmd

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/vaulttransfer"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/version"
)

const (
	exportFormatVersion = 1
	exportManifestName  = "caam-export.json"

	// Tar entries for vaulted files are stored under this prefix.
	tarVaultPrefix = "vault/"

	maxManifestBytes = 1 << 20  // 1 MiB
	maxFileBytes     = 10 << 20 // 10 MiB
)

type vaultExportManifest struct {
	FormatVersion int               `json:"format_version"`
	CreatedAt     time.Time         `json:"created_at"`
	Hostname      string            `json:"hostname,omitempty"`
	CaamVersion   string            `json:"caam_version,omitempty"`
	Items         []vaultExportItem `json:"items"`
}

type vaultExportItem struct {
	Tool    string            `json:"tool"`
	Profile string            `json:"profile"`
	Files   []vaultExportFile `json:"files"`
}

type vaultExportFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   int64  `json:"mode,omitempty"`
}

type exportRequest struct {
	All     bool
	ToolAll bool
	Tool    string
	Profile string
}

type exportTarget struct {
	Tool     string
	Profile  string
	DirPath  string
	snapshot *vaulttransfer.Snapshot
}

type exportFileSpec struct {
	Snapshot     *vaulttransfer.Snapshot
	RelativePath string
	TarPath      string
	Size         int64
}

type importOptions struct {
	Force     bool
	AsTool    string
	AsProfile string
	// The hooks exercise managed lock waits and interrupted publication without
	// weakening the generation checks used by the production rename path.
	BeforeLock    func()
	BeforePublish func(source, destination string)
	Rename        func(string, string) error
}

type importTarget struct {
	Tool        string
	Profile     string
	FinalDir    string
	TempDir     string
	Source      *vaulttransfer.Snapshot
	Destination *vaulttransfer.Snapshot
}

type importArchiveResult struct {
	Manifest *vaultExportManifest
	Profiles []importProfileResult
}

type importProfileResult struct {
	Tool         string
	Profile      string
	Imported     bool
	RecoveryPath string
}

func validateTransferVaultRoot(v *authfile.Vault) error {
	if v == nil || strings.TrimSpace(v.BasePath()) == "" {
		return fmt.Errorf("vault not initialized")
	}
	info, err := os.Lstat(v.BasePath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect vault directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("vault transfer root is not a real directory")
	}
	return nil
}

func resolveExportTargets(v *authfile.Vault, req exportRequest) ([]exportTarget, error) {
	if v == nil {
		return nil, fmt.Errorf("vault not initialized")
	}

	switch {
	case req.All:
		entries, err := os.ReadDir(v.BasePath())
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("vault is empty; create profiles with 'caam backup <tool> <name>'")
			}
			return nil, fmt.Errorf("read vault: %w", err)
		}

		toolsInVault := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			tool := strings.ToLower(strings.TrimSpace(e.Name()))
			if _, ok := tools[tool]; !ok {
				continue
			}
			toolsInVault = append(toolsInVault, tool)
		}
		sort.Strings(toolsInVault)

		var targets []exportTarget
		for _, tool := range toolsInVault {
			profiles, err := v.List(tool)
			if err != nil {
				return nil, fmt.Errorf("list %s profiles: %w", tool, err)
			}
			sort.Strings(profiles)
			for _, profile := range profiles {
				if authfile.IsSystemProfile(profile) {
					continue
				}
				dir, err := vaulttransfer.ProfilePath(v.BasePath(), tool, profile)
				if err != nil {
					return nil, err
				}
				targets = append(targets, exportTarget{
					Tool:    tool,
					Profile: profile,
					DirPath: dir,
				})
			}
		}
		return targets, nil

	case req.ToolAll:
		tool := strings.ToLower(strings.TrimSpace(req.Tool))
		if tool == "" {
			return nil, fmt.Errorf("tool cannot be empty")
		}
		if _, ok := tools[tool]; !ok {
			return nil, fmt.Errorf("unknown tool: %s (supported: %s)", tool, supportedToolsList())
		}

		profiles, err := v.List(tool)
		if err != nil {
			return nil, fmt.Errorf("list %s profiles: %w", tool, err)
		}
		sort.Strings(profiles)

		targets := make([]exportTarget, 0, len(profiles))
		for _, profile := range profiles {
			if authfile.IsSystemProfile(profile) {
				continue
			}
			dir, err := vaulttransfer.ProfilePath(v.BasePath(), tool, profile)
			if err != nil {
				return nil, err
			}
			targets = append(targets, exportTarget{
				Tool:    tool,
				Profile: profile,
				DirPath: dir,
			})
		}
		return targets, nil

	default:
		tool := strings.ToLower(strings.TrimSpace(req.Tool))
		profile := strings.TrimSpace(req.Profile)
		if tool == "" || profile == "" {
			return nil, fmt.Errorf("tool and profile are required")
		}
		if _, ok := tools[tool]; !ok {
			return nil, fmt.Errorf("unknown tool: %s (supported: %s)", tool, supportedToolsList())
		}

		dirPath, err := vaulttransfer.ProfilePath(v.BasePath(), tool, profile)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(dirPath); err != nil || !st.IsDir() {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("profile %s/%s not found in vault; run 'caam ls %s' to see available profiles", tool, profile, tool)
			}
			if err != nil {
				return nil, fmt.Errorf("stat profile: %w", err)
			}
			return nil, fmt.Errorf("profile %s/%s is not a directory", tool, profile)
		}

		return []exportTarget{{Tool: tool, Profile: profile, DirPath: dirPath}}, nil
	}
}

func buildExportManifest(targets []exportTarget) (*vaultExportManifest, []exportFileSpec, error) {
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("no profiles to export")
	}

	hostname, _ := os.Hostname()
	manifest := &vaultExportManifest{
		FormatVersion: exportFormatVersion,
		CreatedAt:     time.Now().UTC(),
		Hostname:      hostname,
		CaamVersion:   version.Short(),
		Items:         make([]vaultExportItem, 0, len(targets)),
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Tool != targets[j].Tool {
			return targets[i].Tool < targets[j].Tool
		}
		return targets[i].Profile < targets[j].Profile
	})

	allFiles := make([]exportFileSpec, 0, 8)
	for _, t := range targets {
		if err := vaulttransfer.ValidateProfileRef(t.Tool, t.Profile); err != nil {
			return nil, nil, err
		}
		if t.snapshot == nil {
			return nil, nil, fmt.Errorf("profile %s/%s has not been captured for export", t.Tool, t.Profile)
		}
		item := vaultExportItem{
			Tool:    t.Tool,
			Profile: t.Profile,
			Files:   nil,
		}

		for _, entry := range t.snapshot.Entries() {
			if entry.Dir {
				continue
			}
			rel := filepath.ToSlash(entry.Name)
			if err := vaulttransfer.ValidateRelativePath(rel); err != nil {
				return nil, nil, err
			}
			if entry.Size > maxFileBytes {
				return nil, nil, fmt.Errorf("refusing to export unusually large file (%d bytes): %s", entry.Size, rel)
			}
			tarPath := tarVaultPrefix + t.Tool + "/" + t.Profile + "/" + rel

			item.Files = append(item.Files, vaultExportFile{
				Path:   tarPath,
				SHA256: entry.Checksum,
				Size:   entry.Size,
				Mode:   0600,
			})
			allFiles = append(allFiles, exportFileSpec{
				Snapshot:     t.snapshot,
				RelativePath: rel,
				TarPath:      tarPath,
				Size:         entry.Size,
			})
		}
		if len(item.Files) == 0 {
			return nil, nil, fmt.Errorf("profile %s/%s contains no files", t.Tool, t.Profile)
		}

		sort.Slice(item.Files, func(i, j int) bool { return item.Files[i].Path < item.Files[j].Path })
		manifest.Items = append(manifest.Items, item)
	}

	sort.Slice(allFiles, func(i, j int) bool { return allFiles[i].TarPath < allFiles[j].TarPath })
	if err := validateExportManifest(manifest); err != nil {
		return nil, nil, err
	}
	return manifest, allFiles, nil
}

func writeExportArchive(w io.Writer, manifest *vaultExportManifest, files []exportFileSpec) error {
	if w == nil {
		return fmt.Errorf("writer is nil")
	}
	if manifest == nil {
		return fmt.Errorf("manifest is nil")
	}

	gzw := gzip.NewWriter(w)
	defer gzw.Close()

	tw := tar.NewWriter(gzw)
	defer tw.Close()

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if len(manifestBytes) > maxManifestBytes {
		return fmt.Errorf("manifest too large (%d bytes)", len(manifestBytes))
	}

	if err := writeTarBytes(tw, exportManifestName, 0600, time.Now(), manifestBytes); err != nil {
		return err
	}

	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.TarPath, Mode: 0600, Size: f.Size, ModTime: manifest.CreatedAt}); err != nil {
			return fmt.Errorf("write tar header: %w", err)
		}
		exists, err := f.Snapshot.WriteFile(filepath.FromSlash(f.RelativePath), tw)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("captured export file disappeared: %s", f.TarPath)
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar writer: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return fmt.Errorf("close gzip writer: %w", err)
	}
	return nil
}

func readAndValidateManifest(tr *tar.Reader) (*vaultExportManifest, error) {
	hdr, err := tr.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("empty archive")
		}
		return nil, fmt.Errorf("read tar header: %w", err)
	}

	name, err := cleanTarName(hdr.Name)
	if err != nil {
		return nil, err
	}
	if name != exportManifestName {
		return nil, fmt.Errorf("expected %q as first entry, got %q", exportManifestName, name)
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil, fmt.Errorf("manifest entry is not a file: %q", name)
	}
	if hdr.Size < 0 || hdr.Size > maxManifestBytes {
		return nil, fmt.Errorf("manifest size out of bounds: %d", hdr.Size)
	}

	body, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var manifest vaultExportManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := validateExportManifest(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func validateExportManifest(manifest *vaultExportManifest) error {
	if manifest.FormatVersion != exportFormatVersion {
		return fmt.Errorf("unsupported export format version: %d", manifest.FormatVersion)
	}
	if len(manifest.Items) == 0 {
		return fmt.Errorf("manifest contains no items")
	}

	seen := make(map[string]struct{})
	seenFiles := make(map[string]struct{})
	portablePaths := make(map[string]string)
	for _, item := range manifest.Items {
		if err := vaulttransfer.ValidateProfileRef(item.Tool, item.Profile); err != nil {
			return err
		}
		if _, ok := tools[item.Tool]; !ok {
			return fmt.Errorf("unsupported tool in archive: %s", item.Tool)
		}
		key := item.Tool + "/" + item.Profile
		if _, ok := seen[strings.ToLower(key)]; ok {
			return fmt.Errorf("duplicate item in manifest: %s", key)
		}
		seen[strings.ToLower(key)] = struct{}{}
		if len(item.Files) == 0 {
			return fmt.Errorf("manifest item has no files: %s", key)
		}
		for _, f := range item.Files {
			tool, profile, _, err := splitVaultTarPath(f.Path)
			if err != nil {
				return err
			}
			if tool != item.Tool || profile != item.Profile {
				return fmt.Errorf("manifest file %s does not belong to %s", f.Path, key)
			}
			fileKey := strings.ToLower(f.Path)
			if _, exists := seenFiles[fileKey]; exists {
				return fmt.Errorf("duplicate file in manifest: %s", f.Path)
			}
			seenFiles[fileKey] = struct{}{}
			parts := strings.Split(f.Path, "/")
			for i := range parts {
				prefix := strings.Join(parts[:i+1], "/")
				lower := strings.ToLower(prefix)
				if previous, exists := portablePaths[lower]; exists && previous != prefix {
					return fmt.Errorf("case-conflicting archive paths: %s and %s", previous, prefix)
				}
				portablePaths[lower] = prefix
			}
			digest, err := hex.DecodeString(f.SHA256)
			if err != nil || len(digest) != sha256.Size {
				return fmt.Errorf("invalid sha256 for %s", f.Path)
			}
			if f.Size < 0 || f.Size > maxFileBytes {
				return fmt.Errorf("file size out of bounds for %s: %d", f.Path, f.Size)
			}
		}
	}
	for file := range seenFiles {
		parts := strings.Split(file, "/")
		for i := 1; i < len(parts); i++ {
			if _, exists := seenFiles[strings.Join(parts[:i], "/")]; exists {
				return fmt.Errorf("archive file is also used as a directory: %s", file)
			}
		}
	}
	return nil
}

func importArchive(r io.Reader, v *authfile.Vault, opt importOptions) (*importArchiveResult, error) {
	if r == nil {
		return nil, fmt.Errorf("reader is nil")
	}
	if err := validateTransferVaultRoot(v); err != nil {
		return nil, err
	}

	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	manifest, err := readAndValidateManifest(tr)
	if err != nil {
		return nil, err
	}

	if opt.AsTool != "" || opt.AsProfile != "" {
		if opt.AsTool == "" || opt.AsProfile == "" {
			return nil, fmt.Errorf("--as must include both tool and profile")
		}
		if len(manifest.Items) != 1 {
			return nil, fmt.Errorf("--as requires an archive containing exactly one profile")
		}
		if err := vaulttransfer.ValidateProfileRef(opt.AsTool, opt.AsProfile); err != nil {
			return nil, err
		}
		if _, ok := tools[opt.AsTool]; !ok {
			return nil, fmt.Errorf("unknown tool: %s (supported: %s)", opt.AsTool, supportedToolsList())
		}
		if opt.AsTool != manifest.Items[0].Tool {
			return nil, fmt.Errorf("--as cannot change a profile's provider")
		}
	}

	// Extraction remains outside the routable vault. No destination is changed
	// until every payload and every incoming Claude binding has been checked.
	stage, err := os.MkdirTemp("", "caam-tar-import-")
	if err != nil {
		return nil, fmt.Errorf("create private import directory: %w", err)
	}
	defer os.RemoveAll(stage)
	result := &importArchiveResult{Manifest: manifest}
	expected := make(map[string]vaultExportFile, 16)
	targets := make(map[string]*importTarget)
	ordered := make([]*importTarget, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		origKey := item.Tool + "/" + item.Profile
		targetTool := item.Tool
		targetProfile := item.Profile
		if opt.AsTool != "" {
			targetTool = opt.AsTool
			targetProfile = opt.AsProfile
		}
		target := &importTarget{
			Tool:    targetTool,
			Profile: targetProfile,
			TempDir: filepath.Join(stage, item.Tool, item.Profile),
		}
		targets[origKey] = target
		ordered = append(ordered, target)
		for _, f := range item.Files {
			expected[f.Path] = f
		}
	}

	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Tool+"/"+ordered[i].Profile < ordered[j].Tool+"/"+ordered[j].Profile
	})

	extracted := make(map[string]struct{}, len(expected))
	for {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read tar header: %w", err)
		}

		name, err := cleanTarName(hdr.Name)
		if err != nil {
			return nil, err
		}

		if name == exportManifestName {
			return nil, fmt.Errorf("duplicate manifest entry")
		}
		if !strings.HasPrefix(name, strings.TrimSuffix(tarVaultPrefix, "/")+"/") {
			return nil, fmt.Errorf("unexpected entry in archive: %s", name)
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("unsupported tar entry type for %s", name)
		}
		if hdr.Size < 0 || hdr.Size > maxFileBytes {
			return nil, fmt.Errorf("file size out of bounds for %s: %d", name, hdr.Size)
		}

		exp, ok := expected[name]
		if !ok {
			return nil, fmt.Errorf("unexpected file in archive: %s", name)
		}
		if _, duplicate := extracted[name]; duplicate {
			return nil, fmt.Errorf("duplicate file in archive: %s", name)
		}
		if hdr.Size != exp.Size {
			return nil, fmt.Errorf("archive size does not match manifest for %s", name)
		}

		origTool, origProfile, rel, err := splitVaultTarPath(name)
		if err != nil {
			return nil, err
		}
		tgt := targets[origTool+"/"+origProfile]
		if tgt == nil {
			return nil, fmt.Errorf("no target mapping for %s/%s", origTool, origProfile)
		}

		destPath, err := safeJoinPath(tgt.TempDir, filepath.FromSlash(rel))
		if err != nil {
			return nil, err
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0700); err != nil {
			return nil, fmt.Errorf("create dir: %w", err)
		}

		hash, err := writeFileAtomicWithHash(destPath, tr, hdr.Size, 0600)
		if err != nil {
			return nil, fmt.Errorf("write %s: %w", destPath, err)
		}
		if !strings.EqualFold(hash, exp.SHA256) {
			return nil, fmt.Errorf("checksum mismatch for %s", name)
		}

		extracted[name] = struct{}{}
	}

	if len(extracted) != len(expected) {
		return nil, fmt.Errorf("archive incomplete: extracted %d/%d files", len(extracted), len(expected))
	}
	// tar.Reader reaches EOF at its terminator, before gzip necessarily verifies
	// its checksum. Drain the stream before any destination publication.
	if _, err := io.Copy(io.Discard, gr); err != nil {
		return nil, fmt.Errorf("validate gzip stream: %w", err)
	}
	for i, target := range ordered {
		captured, err := vaulttransfer.Capture(target.TempDir)
		if err != nil {
			return result, err
		}
		// Normalize every profile before touching the destination vault. A later
		// malformed settings file must not leave earlier items already installed.
		preparedPath := filepath.Join(stage, ".prepared", fmt.Sprintf("profile-%d", i))
		target.Source, err = vaulttransfer.Prepare(captured, preparedPath, target.Tool)
		if err != nil {
			return result, fmt.Errorf("invalid incoming profile %s/%s: %w", target.Tool, target.Profile, err)
		}
	}
	if opt.BeforeLock != nil {
		opt.BeforeLock()
	}
	err = v.WithMutationLock(func() error {
		// Capture destinations only after waiting for the managed vault lock.
		// All conflicts are preflighted, so --force is always an explicit choice.
		for _, target := range ordered {
			target.FinalDir, err = vaulttransfer.ProfilePath(v.BasePath(), target.Tool, target.Profile)
			if err != nil {
				return err
			}
			target.Destination, err = vaulttransfer.Capture(target.FinalDir)
			if err != nil {
				return err
			}
			if target.Destination.Exists() && !opt.Force {
				return fmt.Errorf("profile already exists: %s/%s (use --force or --as)", target.Tool, target.Profile)
			}
		}
		checkSources := func() error {
			for _, target := range ordered {
				if _, err := vaulttransfer.ProfilePath(v.BasePath(), target.Tool, target.Profile); err != nil {
					return err
				}
				if err := target.Source.Check(); err != nil {
					return err
				}
			}
			return nil
		}
		for _, target := range ordered {
			recovery, publishErr := vaulttransfer.Publish(target.Source, target.Destination, &vaulttransfer.Options{
				Provider:      target.Tool,
				BeforePublish: opt.BeforePublish,
				Rename:        opt.Rename,
				CheckSource:   checkSources,
			})
			result.Profiles = append(result.Profiles, importProfileResult{
				Tool:         target.Tool,
				Profile:      target.Profile,
				Imported:     publishErr == nil,
				RecoveryPath: recovery,
			})
			if publishErr != nil {
				return fmt.Errorf("publish %s/%s: %w", target.Tool, target.Profile, publishErr)
			}
		}
		return nil
	})
	return result, err
}

func splitVaultTarPath(name string) (tool, profile, rel string, err error) {
	clean, err := cleanTarName(name)
	if err != nil {
		return "", "", "", err
	}
	if !strings.HasPrefix(clean, strings.TrimSuffix(tarVaultPrefix, "/")+"/") {
		return "", "", "", fmt.Errorf("not a vault path: %s", clean)
	}

	parts := strings.Split(clean, "/")
	if len(parts) < 4 {
		return "", "", "", fmt.Errorf("invalid vault path: %s", clean)
	}

	tool = parts[1]
	profile = parts[2]
	if err := vaulttransfer.ValidateProfileRef(tool, profile); err != nil {
		return "", "", "", err
	}

	rel = strings.Join(parts[3:], "/")
	if rel == "" {
		return "", "", "", fmt.Errorf("invalid vault path (missing file): %s", clean)
	}
	return tool, profile, rel, nil
}

func cleanTarName(name string) (string, error) {
	if err := vaulttransfer.ValidateRelativePath(name); err != nil {
		return "", fmt.Errorf("invalid tar entry name: %w", err)
	}
	return name, nil
}

func safeJoinPath(root, rel string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("root path is empty")
	}
	if err := vaulttransfer.ValidateRelativePath(filepath.ToSlash(rel)); err != nil {
		return "", err
	}

	cleanRel := filepath.Clean(rel)
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path traversal not allowed: %s", rel)
	}

	full := filepath.Join(root, cleanRel)
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("absolute root: %w", err)
	}
	rootAbs = filepath.Clean(rootAbs)
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes root: %s", rel)
	}
	return fullAbs, nil
}

func writeTarBytes(tw *tar.Writer, name string, mode int64, modTime time.Time, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(data)),
		ModTime: modTime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write tar data: %w", err)
	}
	return nil
}

func writeFileAtomicWithHash(dst string, r io.Reader, size int64, mode os.FileMode) (string, error) {
	if size < 0 || size > maxFileBytes {
		return "", fmt.Errorf("invalid size: %d", size)
	}
	if dst == "" {
		return "", fmt.Errorf("destination path is empty")
	}

	f, err := os.CreateTemp(filepath.Dir(dst), ".caam-tar-file-")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return "", err
	}

	h := sha256.New()
	w := io.MultiWriter(f, h)

	n, err := io.CopyN(w, r, size)
	if err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if n != size {
		f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("short write: %d/%d", n, size)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
