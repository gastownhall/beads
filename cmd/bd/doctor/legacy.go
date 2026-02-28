package doctor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

// CheckLegacyBeadsSlashCommands detects old /beads:* slash commands in documentation
// and recommends migration to bd prime hooks for better token efficiency.
//
// Old pattern: /beads:quickstart, /beads:ready (~10.5k tokens per session)
// New pattern: bd prime hooks (~50-2k tokens per session)
func CheckLegacyBeadsSlashCommands(repoPath string) DoctorCheck {
	docFiles := []string{
		filepath.Join(repoPath, "AGENTS.md"),
		filepath.Join(repoPath, "CLAUDE.md"),
		filepath.Join(repoPath, ".claude", "CLAUDE.md"),
		// Local-only variants (not committed to repo)
		filepath.Join(repoPath, "claude.local.md"),
		filepath.Join(repoPath, ".claude", "claude.local.md"),
	}

	var filesWithLegacyCommands []string
	legacyPattern := "/beads:"

	for _, docFile := range docFiles {
		content, err := os.ReadFile(docFile) // #nosec G304 - controlled paths from repoPath
		if err != nil {
			continue // File doesn't exist or can't be read
		}

		if strings.Contains(string(content), legacyPattern) {
			filesWithLegacyCommands = append(filesWithLegacyCommands, filepath.Base(docFile))
		}
	}

	if len(filesWithLegacyCommands) == 0 {
		return DoctorCheck{
			Name:    "Legacy Commands",
			Status:  "ok",
			Message: "No legacy beads slash commands detected",
		}
	}

	return DoctorCheck{
		Name:    "Legacy Commands",
		Status:  "warning",
		Message: fmt.Sprintf("Old beads integration detected in %s", strings.Join(filesWithLegacyCommands, ", ")),
		Detail: "Found: /beads:* slash command references (deprecated)\n" +
			"  These commands are token-inefficient (~10.5k tokens per session)",
		Fix: "Migrate to bd prime hooks for better token efficiency:\n" +
			"\n" +
			"Migration Steps:\n" +
			"  1. Run 'bd setup claude' to add SessionStart/PreCompact hooks\n" +
			"  2. Update AGENTS.md/CLAUDE.md:\n" +
			"     - Remove /beads:* slash command references\n" +
			"     - Add: \"Run 'bd prime' for workflow context\" (for users without hooks)\n" +
			"\n" +
			"Benefits:\n" +
			"  • MCP mode: ~50 tokens vs ~10.5k for full MCP scan (99% reduction)\n" +
			"  • CLI mode: ~1-2k tokens with automatic context recovery\n" +
			"  • Hooks auto-refresh context on session start and before compaction\n" +
			"\n" +
			"See: bd setup claude --help",
	}
}

// CheckLegacyMCPToolReferences detects direct MCP tool name references in documentation
// (e.g., mcp__beads_beads__list, mcp__plugin_beads_beads__show) and recommends
// migration to bd prime hooks for better token efficiency.
//
// Old pattern: Document MCP tool names for direct tool calls (~10.5k tokens per scan)
// New pattern: bd prime hooks with CLI commands (~50-2k tokens)
func CheckLegacyMCPToolReferences(repoPath string) DoctorCheck {
	docFiles := []string{
		filepath.Join(repoPath, "AGENTS.md"),
		filepath.Join(repoPath, "CLAUDE.md"),
		filepath.Join(repoPath, ".claude", "CLAUDE.md"),
		// Local-only variants (not committed to repo)
		filepath.Join(repoPath, "claude.local.md"),
		filepath.Join(repoPath, ".claude", "claude.local.md"),
	}

	mcpPatterns := []string{
		"mcp__beads_beads__",
		"mcp__plugin_beads_beads__",
		"mcp_beads_",
	}

	var filesWithMCPRefs []string
	for _, docFile := range docFiles {
		content, err := os.ReadFile(docFile) // #nosec G304 - controlled paths from repoPath
		if err != nil {
			continue
		}

		contentStr := string(content)
		for _, pattern := range mcpPatterns {
			if strings.Contains(contentStr, pattern) {
				filesWithMCPRefs = append(filesWithMCPRefs, filepath.Base(docFile))
				break
			}
		}
	}

	if len(filesWithMCPRefs) == 0 {
		return DoctorCheck{
			Name:    "MCP Tool References",
			Status:  "ok",
			Message: "No MCP tool references in documentation",
		}
	}

	return DoctorCheck{
		Name:    "MCP Tool References",
		Status:  "warning",
		Message: fmt.Sprintf("MCP tool references found in %s", strings.Join(filesWithMCPRefs, ", ")),
		Detail: "Found: Direct MCP tool name references (e.g., mcp__beads_beads__list)\n" +
			"  MCP tool calls consume ~10.5k tokens per session for tool scanning",
		Fix: "Migrate to bd prime hooks for better token efficiency:\n" +
			"\n" +
			"Migration Steps:\n" +
			"  1. Run 'bd setup claude' to add SessionStart/PreCompact hooks\n" +
			"  2. Replace MCP tool references with CLI commands:\n" +
			"     - mcp__beads_beads__list  → bd list\n" +
			"     - mcp__beads_beads__show  → bd show <id>\n" +
			"     - mcp__beads_beads__ready → bd ready\n" +
			"  3. bd prime hooks auto-inject context on session start\n" +
			"\n" +
			"Benefits:\n" +
			"  • bd prime + hooks: ~50-2k tokens vs ~10.5k for MCP tool scan\n" +
			"  • Automatic context recovery on session start and compaction\n" +
			"\n" +
			"See: bd setup claude --help",
	}
}

// CheckAgentDocumentation checks if agent documentation (AGENTS.md or CLAUDE.md) exists
// and recommends adding it if missing, suggesting bd onboard or bd setup claude.
// Also supports local-only variants (claude.local.md) that are gitignored.
func CheckAgentDocumentation(repoPath string) DoctorCheck {
	docFiles := []string{
		filepath.Join(repoPath, "AGENTS.md"),
		filepath.Join(repoPath, "CLAUDE.md"),
		filepath.Join(repoPath, ".claude", "CLAUDE.md"),
		// Local-only variants (not committed to repo)
		filepath.Join(repoPath, "claude.local.md"),
		filepath.Join(repoPath, ".claude", "claude.local.md"),
	}

	var foundDocs []string
	for _, docFile := range docFiles {
		if _, err := os.Stat(docFile); err == nil {
			foundDocs = append(foundDocs, filepath.Base(docFile))
		}
	}

	if len(foundDocs) > 0 {
		return DoctorCheck{
			Name:    "Agent Documentation",
			Status:  "ok",
			Message: fmt.Sprintf("Documentation found: %s", strings.Join(foundDocs, ", ")),
		}
	}

	return DoctorCheck{
		Name:    "Agent Documentation",
		Status:  "warning",
		Message: "No agent documentation found",
		Detail: "Missing: AGENTS.md or CLAUDE.md\n" +
			"  Documenting workflow helps AI agents work more effectively",
		Fix: "Add agent documentation:\n" +
			"  • Run 'bd onboard' to create AGENTS.md with workflow guidance\n" +
			"  • Or run 'bd setup claude' to add Claude-specific documentation\n" +
			"\n" +
			"For local-only documentation (not committed to repo):\n" +
			"  • Create claude.local.md or .claude/claude.local.md\n" +
			"  • Add 'claude.local.md' to your .gitignore\n" +
			"\n" +
			"Recommended: Include bd workflow in your project documentation so\n" +
			"AI agents understand how to track issues and manage dependencies",
	}
}

// CheckDatabaseConfig verifies that the configured database and JSONL paths
// match what actually exists on disk.
func CheckDatabaseConfig(repoPath string) DoctorCheck {
	beadsDir := filepath.Join(repoPath, ".beads")

	// Load config
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		// No config or error reading - use defaults
		return DoctorCheck{
			Name:    "Database Config",
			Status:  "ok",
			Message: "Using default configuration",
		}
	}

	// Dolt backend stores data on the server — no local .db or .jsonl files expected
	if cfg.GetBackend() == configfile.BackendDolt {
		return DoctorCheck{
			Name:    "Database Config",
			Status:  "ok",
			Message: "Dolt backend (data on server)",
		}
	}

	var issues []string

	// Check if configured database exists
	if cfg.Database != "" {
		dbPath := cfg.DatabasePath(beadsDir)
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			// Check if other .db files exist
			entries, _ := os.ReadDir(beadsDir) // Best effort: nil entries means no legacy files to check
			var otherDBs []string
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".db") {
					otherDBs = append(otherDBs, entry.Name())
				}
			}
			if len(otherDBs) > 0 {
				issues = append(issues, fmt.Sprintf("Configured database '%s' not found, but found: %s",
					cfg.Database, strings.Join(otherDBs, ", ")))
			}
		}
	}

	if len(issues) == 0 {
		return DoctorCheck{
			Name:    "Database Config",
			Status:  "ok",
			Message: "Configuration matches existing files",
		}
	}

	return DoctorCheck{
		Name:    "Database Config",
		Status:  "warning",
		Message: "Configuration mismatch detected",
		Detail:  strings.Join(issues, "\n  "),
		Fix: "Run 'bd doctor --fix' to auto-detect and fix mismatches, or manually:\n" +
			"  1. Check which files are actually being used\n" +
			"  2. Update metadata.json to match the actual filenames\n" +
			"  3. Or rename the files to match the configuration",
	}
}

// CheckFreshClone detects if this is a fresh clone that needs 'bd init'.
// A fresh clone has JSONL with issues but no database file.
func CheckFreshClone(repoPath string) DoctorCheck {
	backend, beadsDir := getBackendAndBeadsDir(repoPath)

	// Check if .beads/ exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Fresh Clone",
			Status:  "ok",
			Message: "N/A (no .beads directory)",
		}
	}

	// Find the JSONL file
	var jsonlPath string
	var jsonlName string
	for _, name := range []string{"issues.jsonl", "beads.jsonl"} {
		testPath := filepath.Join(beadsDir, name)
		if _, err := os.Stat(testPath); err == nil {
			jsonlPath = testPath
			jsonlName = name
			break
		}
	}

	// No JSONL file - not a fresh clone situation
	if jsonlPath == "" {
		return DoctorCheck{
			Name:    "Fresh Clone",
			Status:  "ok",
			Message: "N/A (no JSONL file)",
		}
	}

	// Check if database exists (backend-aware)
	switch backend {
	case configfile.BackendDolt:
		// Dolt is directory-backed: treat .beads/dolt as the DB existence signal.
		if info, err := os.Stat(getDatabasePath(beadsDir)); err == nil && info.IsDir() {
			return DoctorCheck{
				Name:    "Fresh Clone",
				Status:  "ok",
				Message: "Database exists",
			}
		}
	default:
		// SQLite (default): check configured .db file path.
		var dbPath string
		if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
			dbPath = cfg.DatabasePath(beadsDir)
		} else {
			// Fall back to canonical database name
			dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
		}
		if _, err := os.Stat(dbPath); err == nil {
			return DoctorCheck{
				Name:    "Fresh Clone",
				Status:  "ok",
				Message: "Database exists",
			}
		}
	}

	// Check if JSONL has any issues (empty JSONL = not really a fresh clone)
	issueCount, prefix := countJSONLIssuesAndPrefix(jsonlPath)
	if issueCount == 0 {
		return DoctorCheck{
			Name:    "Fresh Clone",
			Status:  "ok",
			Message: fmt.Sprintf("JSONL exists but is empty (%s)", jsonlName),
		}
	}

	// This is a fresh clone! JSONL has issues but no database.
	fixCmd := "bd init"
	if prefix != "" {
		fixCmd = fmt.Sprintf("bd init --prefix %s", prefix)
	}

	return DoctorCheck{
		Name:    "Fresh Clone",
		Status:  "warning",
		Message: fmt.Sprintf("Fresh clone detected (%d issues in %s, no database)", issueCount, jsonlName),
		Detail: "This appears to be a freshly cloned repository.\n" +
			"  The JSONL file contains issues but no local database exists.\n" +
			"  Run 'bd init' to create the database and import existing issues.",
		Fix: fmt.Sprintf("Run '%s' to initialize the database and import issues", fixCmd),
	}
}

// countJSONLIssuesAndPrefix counts issues in a JSONL file and detects the most common prefix.
func countJSONLIssuesAndPrefix(jsonlPath string) (int, string) {
	file, err := os.Open(jsonlPath) //nolint:gosec
	if err != nil {
		return 0, ""
	}
	defer file.Close()

	count := 0
	prefixCounts := make(map[string]int)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var issue struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(line, &issue); err != nil {
			continue
		}

		if issue.ID != "" {
			count++
			// Extract prefix (everything before the last dash)
			if lastDash := strings.LastIndex(issue.ID, "-"); lastDash > 0 {
				prefix := issue.ID[:lastDash]
				prefixCounts[prefix]++
			}
		}
	}

	// Find most common prefix
	var mostCommonPrefix string
	maxCount := 0
	for prefix, cnt := range prefixCounts {
		if cnt > maxCount {
			maxCount = cnt
			mostCommonPrefix = prefix
		}
	}

	return count, mostCommonPrefix
}
