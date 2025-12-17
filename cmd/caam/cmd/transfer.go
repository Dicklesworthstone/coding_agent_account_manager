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
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
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
	Tool    string
	Profile string
	DirPath string
}

type exportFileSpec struct {
	SrcPath string
	TarPath string
	Mode    int64
	Size    int64
	ModTime time.Time
}

type importOptions struct {
	Force     bool
	AsTool    string
	AsProfile string
}

type importTarget struct {
	Tool      string
	Profile   string
	FinalDir  string
	TempDir   string
	SeenFiles map[string]struct{}
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
				return nil, fmt.Errorf("vault is empty")
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
				targets = append(targets, exportTarget{
					Tool:    tool,
					Profile: profile,
					DirPath: v.ProfilePath(tool, profile),
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
			return nil, fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
		}

		profiles, err := v.List(tool)
		if err != nil {
			return nil, fmt.Errorf("list %s profiles: %w", tool, err)
		}
		sort.Strings(profiles)

		targets := make([]exportTarget, 0, len(profiles))
		for _, profile := range profiles {
			targets = append(targets, exportTarget{
				Tool:    tool,
				Profile: profile,
				DirPath: v.ProfilePath(tool, profile),
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
			return nil, fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
		}

		dirPath := v.ProfilePath(tool, profile)
		if st, err := os.Stat(dirPath); err != nil || !st.IsDir() {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("profile %s/%s not found in vault", tool, profile)
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
		item := vaultExportItem{
			Tool:    t.Tool,
			Profile: t.Profile,
			Files:   nil,
		}

		err := filepath.WalkDir(t.DirPath, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("unsupported file type in vault: %s", p)
			}

			rel, err := filepath.Rel(t.DirPath, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			tarPath := path.Join(strings.TrimSuffix(tarVaultPrefix, "/"), t.Tool, t.Profile, rel)

			hash, size, mode, modTime, err := sha256File(p)
			if err != nil {
				return err
			}

			item.Files = append(item.Files, vaultExportFile{
				Path:   tarPath,
				SHA256: hash,
				Size:   size,
				Mode:   int64(mode.Perm()),
			})
			allFiles = append(allFiles, exportFileSpec{
				SrcPath: p,
				TarPath: tarPath,
				Mode:    int64(mode.Perm()),
				Size:    size,
				ModTime: modTime,
			})
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("walk profile %s/%s: %w", t.Tool, t.Profile, err)
		}

		sort.Slice(item.Files, func(i, j int) bool { return item.Files[i].Path < item.Files[j].Path })
		manifest.Items = append(manifest.Items, item)
	}

	sort.Slice(allFiles, func(i, j int) bool { return allFiles[i].TarPath < allFiles[j].TarPath })
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
		if f.Size > maxFileBytes {
			return fmt.Errorf("refusing to export unusually large file (%d bytes): %s", f.Size, f.SrcPath)
		}

		src, err := os.Open(f.SrcPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", f.SrcPath, err)
		}

		hdr := &tar.Header{
			Name:    f.TarPath,
			Mode:    f.Mode,
			Size:    f.Size,
			ModTime: f.ModTime,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			src.Close()
			return fmt.Errorf("write tar header: %w", err)
		}
		if _, err := io.Copy(tw, src); err != nil {
			src.Close()
			return fmt.Errorf("write tar body: %w", err)
		}
		if err := src.Close(); err != nil {
			return fmt.Errorf("close %s: %w", f.SrcPath, err)
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
	if manifest.FormatVersion != exportFormatVersion {
		return nil, fmt.Errorf("unsupported export format version: %d", manifest.FormatVersion)
	}
	if len(manifest.Items) == 0 {
		return nil, fmt.Errorf("manifest contains no items")
	}

	seen := make(map[string]struct{})
	for _, item := range manifest.Items {
		if err := validateVaultSegment("tool", item.Tool); err != nil {
			return nil, err
		}
		if _, ok := tools[item.Tool]; !ok {
			return nil, fmt.Errorf("unsupported tool in archive: %s", item.Tool)
		}
		if err := validateVaultSegment("profile", item.Profile); err != nil {
			return nil, err
		}
		key := item.Tool + "/" + item.Profile
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate item in manifest: %s", key)
		}
		seen[key] = struct{}{}
		if len(item.Files) == 0 {
			return nil, fmt.Errorf("manifest item has no files: %s", key)
		}
		for _, f := range item.Files {
			if !strings.HasPrefix(f.Path, strings.TrimSuffix(tarVaultPrefix, "/")+"/") {
				return nil, fmt.Errorf("invalid file path in manifest: %s", f.Path)
			}
			if f.SHA256 == "" {
				return nil, fmt.Errorf("missing sha256 for %s", f.Path)
			}
		}
	}

	return &manifest, nil
}

func importArchive(r io.Reader, v *authfile.Vault, opt importOptions) (*vaultExportManifest, error) {
	if r == nil {
		return nil, fmt.Errorf("reader is nil")
	}
	if v == nil {
		return nil, fmt.Errorf("vault not initialized")
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

	renameFrom := ""
	renameToTool := ""
	renameToProfile := ""
	if opt.AsTool != "" || opt.AsProfile != "" {
		if opt.AsTool == "" || opt.AsProfile == "" {
			return nil, fmt.Errorf("--as must include both tool and profile")
		}
		if len(manifest.Items) != 1 {
			return nil, fmt.Errorf("--as requires an archive containing exactly one profile")
		}
		if err := validateVaultSegment("tool", opt.AsTool); err != nil {
			return nil, err
		}
		if _, ok := tools[opt.AsTool]; !ok {
			return nil, fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", opt.AsTool)
		}
		if err := validateVaultSegment("profile", opt.AsProfile); err != nil {
			return nil, err
		}
		renameFrom = manifest.Items[0].Tool + "/" + manifest.Items[0].Profile
		renameToTool = opt.AsTool
		renameToProfile = opt.AsProfile
	}

	expected := make(map[string]vaultExportFile, 16)
	targets := make(map[string]*importTarget)
	for _, item := range manifest.Items {
		origKey := item.Tool + "/" + item.Profile
		targetTool := item.Tool
		targetProfile := item.Profile
		if renameFrom != "" && origKey == renameFrom {
			targetTool = renameToTool
			targetProfile = renameToProfile
		}

		finalDir := v.ProfilePath(targetTool, targetProfile)
		if st, err := os.Stat(finalDir); err == nil && st.IsDir() {
			if !opt.Force {
				return nil, fmt.Errorf("profile already exists: %s/%s (use --force or --as)", targetTool, targetProfile)
			}
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat %s/%s: %w", targetTool, targetProfile, err)
		}

		tk := targetTool + "/" + targetProfile
		if _, ok := targets[tk]; !ok {
			targets[tk] = &importTarget{
				Tool:      targetTool,
				Profile:   targetProfile,
				FinalDir:  finalDir,
				TempDir:   "",
				SeenFiles: make(map[string]struct{}),
			}
		}

		for _, f := range item.Files {
			expected[f.Path] = f
		}
	}

	cleanup := func() {
		for _, t := range targets {
			if t.TempDir != "" {
				_ = os.RemoveAll(t.TempDir)
			}
		}
	}

	extracted := make(map[string]struct{}, len(expected))
	for {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			cleanup()
			return nil, fmt.Errorf("read tar header: %w", err)
		}

		name, err := cleanTarName(hdr.Name)
		if err != nil {
			cleanup()
			return nil, err
		}

		if name == exportManifestName {
			cleanup()
			return nil, fmt.Errorf("duplicate manifest entry")
		}
		if !strings.HasPrefix(name, strings.TrimSuffix(tarVaultPrefix, "/")+"/") {
			cleanup()
			return nil, fmt.Errorf("unexpected entry in archive: %s", name)
		}

		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			cleanup()
			return nil, fmt.Errorf("unsupported tar entry type for %s", name)
		}
		if hdr.Size < 0 || hdr.Size > maxFileBytes {
			cleanup()
			return nil, fmt.Errorf("file size out of bounds for %s: %d", name, hdr.Size)
		}

		exp, ok := expected[name]
		if !ok {
			cleanup()
			return nil, fmt.Errorf("unexpected file in archive: %s", name)
		}

		origTool, origProfile, rel, err := splitVaultTarPath(name)
		if err != nil {
			cleanup()
			return nil, err
		}

		targetTool := origTool
		targetProfile := origProfile
		if renameFrom != "" && (origTool+"/"+origProfile) == renameFrom {
			targetTool = renameToTool
			targetProfile = renameToProfile
		}

		tk := targetTool + "/" + targetProfile
		tgt := targets[tk]
		if tgt == nil {
			cleanup()
			return nil, fmt.Errorf("no target mapping for %s/%s", targetTool, targetProfile)
		}

		if tgt.TempDir == "" {
			toolDir := filepath.Join(v.BasePath(), targetTool)
			if err := os.MkdirAll(toolDir, 0700); err != nil {
				cleanup()
				return nil, fmt.Errorf("create tool dir: %w", err)
			}
			tmpDir, err := os.MkdirTemp(toolDir, ".import_tmp_"+targetProfile+"_")
			if err != nil {
				cleanup()
				return nil, fmt.Errorf("create temp dir: %w", err)
			}
			tgt.TempDir = tmpDir
		}

		destPath, err := safeJoinPath(tgt.TempDir, filepath.FromSlash(rel))
		if err != nil {
			cleanup()
			return nil, err
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0700); err != nil {
			cleanup()
			return nil, fmt.Errorf("create dir: %w", err)
		}

		hash, err := writeFileAtomicWithHash(destPath, tr, hdr.Size, 0600)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("write %s: %w", destPath, err)
		}
		if hash != exp.SHA256 {
			cleanup()
			return nil, fmt.Errorf("checksum mismatch for %s", name)
		}

		extracted[name] = struct{}{}
		tgt.SeenFiles[name] = struct{}{}
	}

	if len(extracted) != len(expected) {
		cleanup()
		return nil, fmt.Errorf("archive incomplete: extracted %d/%d files", len(extracted), len(expected))
	}

	// Promote temp dirs to final location atomically per profile.
	for _, tgt := range targets {
		if tgt.TempDir == "" {
			continue
		}

		if opt.Force {
			_ = os.RemoveAll(tgt.FinalDir)
		}
		if err := os.MkdirAll(filepath.Dir(tgt.FinalDir), 0700); err != nil {
			cleanup()
			return nil, fmt.Errorf("create final parent dir: %w", err)
		}
		if err := os.Rename(tgt.TempDir, tgt.FinalDir); err != nil {
			cleanup()
			return nil, fmt.Errorf("finalize %s/%s: %w", tgt.Tool, tgt.Profile, err)
		}
		tgt.TempDir = ""
	}

	cleanup()
	return manifest, nil
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
	if err := validateVaultSegment("tool", tool); err != nil {
		return "", "", "", err
	}
	if err := validateVaultSegment("profile", profile); err != nil {
		return "", "", "", err
	}

	rel = strings.Join(parts[3:], "/")
	if rel == "" {
		return "", "", "", fmt.Errorf("invalid vault path (missing file): %s", clean)
	}
	return tool, profile, rel, nil
}

func validateVaultSegment(kind, val string) error {
	val = strings.TrimSpace(val)
	if val == "" {
		return fmt.Errorf("%s cannot be empty", kind)
	}
	if val == "." || val == ".." {
		return fmt.Errorf("invalid %s: %q", kind, val)
	}
	if strings.ContainsAny(val, "/\\") {
		return fmt.Errorf("invalid %s: %q", kind, val)
	}
	return nil
}

func cleanTarName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty tar entry name")
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("invalid tar entry name")
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("absolute tar entry not allowed: %s", name)
	}
	if strings.Contains(name, "\\") {
		return "", fmt.Errorf("invalid tar entry name: %s", name)
	}
	segments := strings.Split(name, "/")
	for _, s := range segments {
		if s == ".." {
			return "", fmt.Errorf("path traversal in tar entry: %s", name)
		}
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid tar entry name: %s", name)
	}
	return clean, nil
}

func safeJoinPath(root, rel string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("root path is empty")
	}
	if rel == "" {
		return "", fmt.Errorf("relative path is empty")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths not allowed: %s", rel)
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

func sha256File(p string) (hash string, size int64, mode fs.FileMode, modTime time.Time, err error) {
	st, err := os.Stat(p)
	if err != nil {
		return "", 0, 0, time.Time{}, err
	}
	if !st.Mode().IsRegular() {
		return "", 0, 0, time.Time{}, fmt.Errorf("not a regular file: %s", p)
	}

	f, err := os.Open(p)
	if err != nil {
		return "", 0, 0, time.Time{}, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, 0, time.Time{}, err
	}
	return hex.EncodeToString(h.Sum(nil)), st.Size(), st.Mode(), st.ModTime(), nil
}

func writeFileAtomicWithHash(dst string, r io.Reader, size int64, mode os.FileMode) (string, error) {
	if size < 0 || size > maxFileBytes {
		return "", fmt.Errorf("invalid size: %d", size)
	}
	if dst == "" {
		return "", fmt.Errorf("destination path is empty")
	}

	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
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
