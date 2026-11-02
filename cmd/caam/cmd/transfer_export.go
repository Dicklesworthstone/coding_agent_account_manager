package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/vaulttransfer"
)

type preparedExport struct {
	Manifest  *vaultExportManifest
	Path      string
	directory string
}

type exportOptions struct {
	// BeforeArchive is called after private profile copies have been prepared.
	// The originals must still pass generation checks before the archive is used.
	BeforeArchive func()
}

func (p *preparedExport) Close() error {
	return os.RemoveAll(p.directory)
}

// prepareExportArchive captures and normalizes profiles under the managed vault
// lock. Only a complete, checked archive leaves this helper: failures do not
// stream a partial set of credentials or truncate an existing output file.
func prepareExportArchive(v *authfile.Vault, req exportRequest, opt exportOptions) (*preparedExport, error) {
	if err := validateTransferVaultRoot(v); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "caam-tar-export-")
	if err != nil {
		return nil, fmt.Errorf("create private export directory: %w", err)
	}
	prepared := &preparedExport{Path: filepath.Join(directory, "profiles.tar.gz"), directory: directory}
	err = v.WithMutationLock(func() error {
		targets, err := resolveExportTargets(v, req)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			return fmt.Errorf("no profiles to export")
		}
		originals := make([]*vaulttransfer.Snapshot, len(targets))
		for i := range targets {
			originals[i], err = vaulttransfer.Capture(targets[i].DirPath)
			if err != nil {
				return err
			}
			if !originals[i].Exists() {
				return fmt.Errorf("profile %s/%s disappeared before export", targets[i].Tool, targets[i].Profile)
			}
			for _, entry := range originals[i].Entries() {
				if !entry.Dir && entry.Size > maxFileBytes {
					return fmt.Errorf("refusing to export unusually large file (%d bytes): %s", entry.Size, entry.Name)
				}
			}
		}
		checkOriginals := func() error {
			for i, target := range targets {
				if _, err := vaulttransfer.ProfilePath(v.BasePath(), target.Tool, target.Profile); err != nil {
					return err
				}
				if err := originals[i].Check(); err != nil {
					return fmt.Errorf("profile %s/%s changed during export: %w", target.Tool, target.Profile, err)
				}
			}
			return nil
		}
		for i := range targets {
			destination := filepath.Join(directory, fmt.Sprintf("profile-%d", i))
			targets[i].snapshot, err = vaulttransfer.Prepare(originals[i], destination, targets[i].Tool)
			if err != nil {
				return fmt.Errorf("prepare profile %s/%s: %w", targets[i].Tool, targets[i].Profile, err)
			}
		}
		if opt.BeforeArchive != nil {
			opt.BeforeArchive()
		}
		if err := checkOriginals(); err != nil {
			return err
		}
		manifest, files, err := buildExportManifest(targets)
		if err != nil {
			return err
		}
		archive, err := os.OpenFile(prepared.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("create private export archive: %w", err)
		}
		writeErr := writeExportArchive(archive, manifest, files)
		closeErr := archive.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return fmt.Errorf("close private export archive: %w", closeErr)
		}
		if err := checkOriginals(); err != nil {
			return err
		}
		prepared.Manifest = manifest
		return nil
	})
	if err != nil {
		_ = prepared.Close()
		return nil, err
	}
	return prepared, nil
}

func (p *preparedExport) copyTo(w io.Writer) error {
	f, err := os.Open(p.Path)
	if err != nil {
		return fmt.Errorf("open prepared export: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("write export archive: %w", err)
	}
	return nil
}

// writeExportFile uses a sibling temporary file so any returned preparation or
// write error leaves an existing archive untouched. Export output cannot live
// inside the vault: even a successful replacement there could destroy a login.
func writeExportFile(prepared *preparedExport, vaultPath, outputPath string) error {
	output, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve output file: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return fmt.Errorf("resolve output directory: %w", err)
	}
	base, err := filepath.EvalSymlinks(vaultPath)
	if err != nil {
		return fmt.Errorf("resolve vault directory: %w", err)
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return err
	}
	output = filepath.Join(parent, filepath.Base(output))
	// Windows cannot form a relative path across volumes. Such an output is
	// necessarily outside this vault and remains a valid export destination.
	if strings.EqualFold(filepath.VolumeName(base), filepath.VolumeName(output)) {
		rel, err := filepath.Rel(base, output)
		if err != nil {
			return err
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return fmt.Errorf("export output must be outside the vault")
		}
	}
	if info, statErr := os.Lstat(output); statErr == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("export output is not a regular file")
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect output file: %w", statErr)
	}
	temp, err := os.CreateTemp(parent, ".caam-export-")
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer os.Remove(temp.Name())
	writeErr := prepared.copyTo(temp)
	if writeErr == nil {
		writeErr = temp.Sync()
	}
	closeErr := temp.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("close output: %w", closeErr)
	}
	if err := os.Rename(temp.Name(), output); err != nil {
		return fmt.Errorf("publish output file: %w", err)
	}
	return nil
}
