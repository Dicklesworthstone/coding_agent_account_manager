// Package discovery scans for existing AI tool auth files and extracts identity info.
//
// This package enables the "zero friction" first-run experience by:
// 1. Discovering existing auth files for Claude, Codex, and Gemini
// 2. Extracting user identity (email) from auth tokens
// 3. Providing this info to the init wizard for guided setup
package discovery

import (
	"os"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// Tool represents an AI coding tool.
type Tool string

const (
	ToolClaude Tool = "claude"
	ToolCodex  Tool = "codex"
	ToolGemini Tool = "gemini"
)

// AllTools returns all supported tools.
func AllTools() []Tool {
	return []Tool{ToolClaude, ToolCodex, ToolGemini}
}

// DiscoveredAuth represents an existing auth session found on the system.
type DiscoveredAuth struct {
	Tool     Tool   // Which tool (claude, codex, gemini)
	Path     string // Path to the primary auth file
	Identity string // Extracted identity (email or account ID)
	Valid    bool   // Whether the auth appears valid
}

// ScanResult holds all discovered auth sessions.
type ScanResult struct {
	Found     []DiscoveredAuth      // Auth files that were found
	NotFound  []Tool                // Tools with no auth files
	ToolPaths map[Tool][]string     // All auth file paths checked per tool
}

// Scan checks all supported tools for existing auth files.
// This is the main entry point for auth discovery.
func Scan() *ScanResult {
	result := &ScanResult{
		ToolPaths: make(map[Tool][]string),
	}

	for _, tool := range AllTools() {
		discovered := scanTool(tool)
		if discovered != nil {
			result.Found = append(result.Found, *discovered)
		} else {
			result.NotFound = append(result.NotFound, tool)
		}
		result.ToolPaths[tool] = getToolPaths(tool)
	}

	return result
}

// ScanTool checks a specific tool for existing auth files.
func ScanTool(tool Tool) *DiscoveredAuth {
	return scanTool(tool)
}

func scanTool(tool Tool) *DiscoveredAuth {
	fileSet := getAuthFileSet(tool)
	if fileSet == nil {
		return nil
	}

	// Check if any required auth file exists
	var primaryPath string
	for _, spec := range fileSet.Files {
		if spec.Required {
			if _, err := os.Stat(spec.Path); err == nil {
				primaryPath = spec.Path
				break
			}
		}
	}

	if primaryPath == "" {
		return nil
	}

	// Extract identity from the auth file
	identity, valid := ExtractIdentity(tool, primaryPath)

	return &DiscoveredAuth{
		Tool:     tool,
		Path:     primaryPath,
		Identity: identity,
		Valid:    valid,
	}
}

func getAuthFileSet(tool Tool) *authfile.AuthFileSet {
	switch tool {
	case ToolClaude:
		set := authfile.ClaudeAuthFiles()
		return &set
	case ToolCodex:
		set := authfile.CodexAuthFiles()
		return &set
	case ToolGemini:
		set := authfile.GeminiAuthFiles()
		return &set
	default:
		return nil
	}
}

func getToolPaths(tool Tool) []string {
	fileSet := getAuthFileSet(tool)
	if fileSet == nil {
		return nil
	}

	paths := make([]string, len(fileSet.Files))
	for i, spec := range fileSet.Files {
		paths[i] = spec.Path
	}
	return paths
}

// HasAuthFiles returns true if the tool has existing auth files.
func HasAuthFiles(tool Tool) bool {
	fileSet := getAuthFileSet(tool)
	if fileSet == nil {
		return false
	}
	return authfile.HasAuthFiles(*fileSet)
}
