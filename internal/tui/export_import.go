package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/bundle"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
	tea "github.com/charmbracelet/bubbletea"
)

// Export/Import messages

// exportCompleteMsg indicates an export operation completed successfully.
type exportCompleteMsg struct {
	path string
	size int64
}

// exportErrorMsg indicates an export operation failed.
type exportErrorMsg struct {
	err error
}

// importPreviewMsg contains the preview of what will be imported.
type importPreviewMsg struct {
	result *bundle.ImportResult
	path   string
}

// importCompleteMsg indicates an import operation completed successfully.
type importCompleteMsg struct {
	result *bundle.ImportResult
}

// importErrorMsg indicates an import operation failed.
type importErrorMsg struct {
	err error
}

// handleExportVault initiates the export flow with a confirmation dialog.
func (m Model) handleExportVault() (tea.Model, tea.Cmd) {
	// Count total profiles
	totalProfiles := 0
	for _, profiles := range m.profiles {
		totalProfiles += len(profiles)
	}

	if totalProfiles == 0 {
		m.statusMsg = "No profiles to export"
		return m, nil
	}

	// Create confirmation dialog
	m.confirmDialog = NewConfirmDialog(
		"Export Vault",
		fmt.Sprintf("Export all %d profiles to a zip bundle?", totalProfiles),
	)
	m.confirmDialog.SetStyles(m.styles)
	m.confirmDialog.SetLabels("Export", "Cancel")
	m.confirmDialog.SetWidth(m.dialogWidth(50))
	m.state = stateExportConfirm
	m.statusMsg = ""
	return m, nil
}

// handleExportConfirmKeys handles key input for the export confirmation dialog.
func (m Model) handleExportConfirmKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirmDialog == nil {
		m.state = stateList
		return m, nil
	}

	// Update the dialog with the key press
	var cmd tea.Cmd
	m.confirmDialog, cmd = m.confirmDialog.Update(msg)

	// Check dialog result
	switch m.confirmDialog.Result() {
	case DialogResultSubmit:
		if m.confirmDialog.Confirmed() {
			m.confirmDialog = nil
			m.state = stateList
			m.statusMsg = "Exporting vault..."
			return m, m.executeExport()
		}
		// User selected "No" - cancel export
		m.confirmDialog = nil
		m.state = stateList
		m.statusMsg = "Export cancelled"
		return m, nil

	case DialogResultCancel:
		m.confirmDialog = nil
		m.state = stateList
		m.statusMsg = "Export cancelled"
		return m, nil
	}

	return m, cmd
}

// executeExport performs the actual bundle export operation.
func (m Model) executeExport() tea.Cmd {
	return func() tea.Msg {
		// Build export options with defaults
		opts := bundle.DefaultExportOptions()

		// Set output directory to current working directory
		outputDir, err := os.Getwd()
		if err != nil {
			return exportErrorMsg{err: fmt.Errorf("get current directory: %w", err)}
		}
		opts.OutputDir = outputDir
		opts.VerboseFilename = true // Use descriptive filename with timestamp

		// Include all optional content
		opts.IncludeConfig = true
		opts.IncludeProjects = true
		opts.IncludeHealth = true
		opts.IncludeDatabase = false // Skip database by default (can be large)
		opts.IncludeSyncConfig = true

		// Build exporter with paths
		vaultPath := m.vaultPath
		dataPath := filepath.Dir(vaultPath)
		exporter := &bundle.VaultExporter{
			VaultPath:    vaultPath,
			DataPath:     dataPath,
			ConfigPath:   config.ConfigPath(),
			ProjectsPath: project.DefaultPath(),
			HealthPath:   health.DefaultHealthPath(),
		}

		// Perform export
		result, err := exporter.Export(opts)
		if err != nil {
			return exportErrorMsg{err: err}
		}

		return exportCompleteMsg{
			path: result.OutputPath,
			size: result.CompressedSize,
		}
	}
}

// handleImportBundle initiates the import flow with a file path input dialog.
func (m Model) handleImportBundle() (tea.Model, tea.Cmd) {
	// Create text input dialog for bundle path
	dialog := NewTextInputDialog(
		"Import Bundle",
		"Enter path to bundle zip file:",
	)
	dialog.SetStyles(m.styles)
	dialog.SetPlaceholder("~/backup.zip or /path/to/bundle.zip")
	dialog.SetWidth(m.dialogWidth(60))
	m.backupDialog = dialog // Reuse backup dialog field
	m.state = stateImportPath
	m.statusMsg = ""
	return m, nil
}

// handleImportPathKeys handles key input for the import path dialog.
func (m Model) handleImportPathKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.backupDialog == nil {
		m.state = stateList
		return m, nil
	}

	// Update the dialog with the key press
	var cmd tea.Cmd
	m.backupDialog, cmd = m.backupDialog.Update(msg)

	// Check dialog result
	switch m.backupDialog.Result() {
	case DialogResultSubmit:
		bundlePath := m.backupDialog.Value()
		m.backupDialog = nil
		return m.validateAndPreviewImport(bundlePath)

	case DialogResultCancel:
		m.backupDialog = nil
		m.state = stateList
		m.statusMsg = "Import cancelled"
		return m, nil
	}

	return m, cmd
}

// validateAndPreviewImport validates the bundle path and loads a preview.
func (m Model) validateAndPreviewImport(bundlePath string) (tea.Model, tea.Cmd) {
	bundlePath = strings.TrimSpace(bundlePath)
	if bundlePath == "" {
		m.state = stateList
		m.statusMsg = "Bundle path cannot be empty"
		return m, nil
	}

	// Expand ~ to home directory
	if len(bundlePath) > 0 && bundlePath[0] == '~' {
		home, err := os.UserHomeDir()
		if err == nil {
			bundlePath = filepath.Join(home, bundlePath[1:])
		}
	}

	// Check if file exists
	if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
		m.state = stateList
		m.statusMsg = fmt.Sprintf("File not found: %s", bundlePath)
		return m, nil
	}

	// Check if it's encrypted (we don't support encrypted bundles in TUI yet)
	encrypted, err := bundle.IsEncrypted(bundlePath)
	if err != nil {
		m.state = stateList
		m.statusMsg = fmt.Sprintf("Cannot read bundle: %v", err)
		return m, nil
	}

	if encrypted {
		m.state = stateList
		m.statusMsg = "Encrypted bundles not supported in TUI. Use: caam bundle import"
		return m, nil
	}

	m.statusMsg = "Loading bundle preview..."
	m.pendingProfile = bundlePath // Reuse pendingProfile to store bundle path
	return m, m.loadImportPreview(bundlePath)
}

// loadImportPreview loads a preview of what would be imported.
func (m Model) loadImportPreview(bundlePath string) tea.Cmd {
	return func() tea.Msg {
		// Build import options with dry-run to get preview
		opts := bundle.DefaultImportOptions()
		opts.DryRun = true
		opts.Mode = bundle.ImportModeSmart

		// Set paths
		vaultPath := m.vaultPath
		dataPath := filepath.Dir(vaultPath)
		opts.VaultPath = vaultPath
		opts.ConfigPath = config.ConfigPath()
		opts.ProjectsPath = project.DefaultPath()
		opts.HealthPath = health.DefaultHealthPath()
		opts.DatabasePath = filepath.Join(dataPath, "caam.db")
		opts.SyncPath = filepath.Join(dataPath, "sync")

		// Create importer and get preview
		importer := &bundle.VaultImporter{
			BundlePath: bundlePath,
		}

		result, err := importer.Import(opts)
		if err != nil {
			return importErrorMsg{err: err}
		}

		return importPreviewMsg{result: result, path: bundlePath}
	}
}

// handleImportConfirmKeys handles key input for the import confirmation dialog.
func (m Model) handleImportConfirmKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirmDialog == nil {
		m.state = stateList
		return m, nil
	}

	// Update the dialog with the key press
	var cmd tea.Cmd
	m.confirmDialog, cmd = m.confirmDialog.Update(msg)

	// Check dialog result
	switch m.confirmDialog.Result() {
	case DialogResultSubmit:
		if m.confirmDialog.Confirmed() && m.pendingProfile != "" {
			bundlePath := m.pendingProfile
			m.confirmDialog = nil
			m.pendingProfile = ""
			m.state = stateList
			m.statusMsg = "Importing bundle..."
			return m, m.executeImport(bundlePath)
		}
		// User selected "No" - cancel import
		m.confirmDialog = nil
		m.pendingProfile = ""
		m.state = stateList
		m.statusMsg = "Import cancelled"
		return m, nil

	case DialogResultCancel:
		m.confirmDialog = nil
		m.pendingProfile = ""
		m.state = stateList
		m.statusMsg = "Import cancelled"
		return m, nil
	}

	return m, cmd
}

// executeImport performs the actual bundle import operation.
func (m Model) executeImport(bundlePath string) tea.Cmd {
	return func() tea.Msg {
		// Build import options
		opts := bundle.DefaultImportOptions()
		opts.Mode = bundle.ImportModeSmart
		opts.Force = true // Don't prompt for confirmation (we already did)

		// Set paths
		vaultPath := m.vaultPath
		dataPath := filepath.Dir(vaultPath)
		opts.VaultPath = vaultPath
		opts.ConfigPath = config.ConfigPath()
		opts.ProjectsPath = project.DefaultPath()
		opts.HealthPath = health.DefaultHealthPath()
		opts.DatabasePath = filepath.Join(dataPath, "caam.db")
		opts.SyncPath = filepath.Join(dataPath, "sync")

		// Create importer and execute
		importer := &bundle.VaultImporter{
			BundlePath: bundlePath,
		}

		result, err := importer.Import(opts)
		if err != nil {
			return importErrorMsg{err: err}
		}

		return importCompleteMsg{result: result}
	}
}

// handleExportComplete processes the export completion message.
func (m Model) handleExportComplete(msg exportCompleteMsg) (tea.Model, tea.Cmd) {
	m.statusMsg = fmt.Sprintf("Exported to: %s (%s)", msg.path, bundle.FormatSize(msg.size))
	return m, nil
}

// handleExportError processes the export error message.
func (m Model) handleExportError(msg exportErrorMsg) (tea.Model, tea.Cmd) {
	m.statusMsg = fmt.Sprintf("Export failed: %v", msg.err)
	return m, nil
}

// handleImportPreview processes the import preview message and shows confirmation.
func (m Model) handleImportPreview(msg importPreviewMsg) (tea.Model, tea.Cmd) {
	// Build preview message
	previewText := fmt.Sprintf(
		"Import Preview:\n  Add: %d new profiles\n  Update: %d profiles\n  Skip: %d profiles\n\nProceed with import?",
		msg.result.NewProfiles,
		msg.result.UpdatedProfiles,
		msg.result.SkippedProfiles,
	)

	// Show confirmation dialog with preview
	m.confirmDialog = NewConfirmDialog("Import Bundle", previewText)
	m.confirmDialog.SetStyles(m.styles)
	m.confirmDialog.SetLabels("Import", "Cancel")
	m.confirmDialog.SetWidth(m.dialogWidth(55))
	m.state = stateImportConfirm
	m.statusMsg = ""
	return m, nil
}

// handleImportComplete processes the import completion message.
func (m Model) handleImportComplete(msg importCompleteMsg) (tea.Model, tea.Cmd) {
	r := msg.result
	m.statusMsg = fmt.Sprintf("Import complete: %d added, %d updated, %d skipped",
		r.NewProfiles, r.UpdatedProfiles, r.SkippedProfiles)

	// Reload profiles to show the imported ones
	return m, m.loadProfiles
}

// handleImportError processes the import error message.
func (m Model) handleImportError(msg importErrorMsg) (tea.Model, tea.Cmd) {
	m.statusMsg = fmt.Sprintf("Import failed: %v", msg.err)
	return m, nil
}
