package setup

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	cursorRulesPath = ".cursor/rules/beads.mdc"
	cursorMCPPath   = ".cursor/mcp.json"
)

const cursorRulesTemplate = `---
description: Beads (bd) issue tracker — agent safety rails and workflow
alwaysApply: true
# @beads-managed — regenerate with: bd setup cursor
---

# Beads Issue Tracking

This project uses **bd** for ALL task tracking. Run ` + "`bd prime`" + ` at session start for context; use ` + "`bd remember`" + ` for reusable lessons.

## Agent Safety (WILL HANG if violated)

- **NEVER use ` + "`bd edit`" + `** — opens $EDITOR, blocks agent indefinitely. Use ` + "`bd update <id> --description \"value\"`" + ` instead
- **NEVER use interactive shell commands** without ` + "`-f`" + ` flag (cp, mv, rm may have -i aliases)
- **Stdin for special characters** (backticks, nested quotes, !):
  ` + "`echo 'text' | bd create \"Title\" --description=-`" + `

## Prohibited Patterns

- Do NOT use TodoWrite, TaskCreate, or markdown TODO lists — use ` + "`bd create`" + `
- Do NOT use ` + "`bd claim`" + ` (does not exist) — use ` + "`bd update <id> --claim`" + `
- Do NOT use priority words (high/medium/low) — use numeric 0-4 (0=critical, 4=backlog)

## Quick Reference
` + "```bash" + `
bd prime                              # Load complete workflow context
bd ready                              # Show issues ready to work (no blockers)
bd list --status=open                 # List all open issues
bd create --title="..." --type=task  # Create new issue
bd update <id> --claim               # Claim work atomically
bd unclaim <id>                    # Release stuck issue (agent crashed)
bd close <id>                         # Mark complete
bd dep add <issue> <depends-on>       # Add dependency (issue depends on depends-on)
bd dolt push                               # Sync with Dolt remote
` + "```" + `

## Workflow

1. ` + "`bd ready`" + ` — find unblocked work
2. ` + "`bd update <id> --claim`" + ` — claim atomically
3. Do the work (always use ` + "`--json`" + ` flag when parsing bd output)
4. ` + "`bd close <id>`" + ` — mark complete
5. Discovered new work? ` + "`bd create \"...\" --deps discovered-from:<parent-id>`" + `

## Session Close (MANDATORY before ending)

1. Close completed issues: ` + "`bd close <id1> <id2> ...`" + `
2. File remaining work: ` + "`bd create \"...\" --deps discovered-from:<id>`" + `
3. Sync beads: ` + "`bd dolt push`" + `
4. Push code: ` + "`git push`" + ` — work is NOT complete until push succeeds
`

type cursorEnv struct {
	stdout io.Writer
	stderr io.Writer
}

func defaultCursorEnv() cursorEnv {
	return cursorEnv{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

// cursorMCPServerCore holds only the fields we inspect for detection.
type cursorMCPServerCore struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// cursorMCPConfig preserves all unknown fields on round-trip via json.RawMessage.
type cursorMCPConfig struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

func cursorMCPBeadsRaw() json.RawMessage {
	entry := cursorMCPServerCore{Command: "bd", Args: []string{"mcp"}}
	raw, _ := json.Marshal(entry)
	return raw
}

func decodeMCPServerCore(raw json.RawMessage) (cursorMCPServerCore, bool) {
	var core cursorMCPServerCore
	if err := json.Unmarshal(raw, &core); err != nil {
		return core, false
	}
	return core, true
}

func cursorHasBeadsMCP(config cursorMCPConfig) bool {
	if _, exists := config.MCPServers["beads"]; exists {
		return true
	}
	for _, raw := range config.MCPServers {
		if core, ok := decodeMCPServerCore(raw); ok {
			if core.Command == "bd" && slices.Contains(core.Args, "mcp") {
				return true
			}
		}
	}
	return false
}

// cursorBeadsEntryIsOurs returns true if the "beads" key was installed by bd setup cursor.
func cursorBeadsEntryIsOurs(config cursorMCPConfig) bool {
	raw, exists := config.MCPServers["beads"]
	if !exists {
		return false
	}
	core, ok := decodeMCPServerCore(raw)
	if !ok {
		return false
	}
	return core.Command == "bd" && slices.Contains(core.Args, "mcp")
}

func parseCursorMCPConfig(data []byte) (cursorMCPConfig, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return cursorMCPConfig{MCPServers: map[string]json.RawMessage{}}, nil
	}
	var config cursorMCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return cursorMCPConfig{}, err
	}
	if config.MCPServers == nil {
		config.MCPServers = map[string]json.RawMessage{}
	}
	return config, nil
}

func mergeCursorMCP(env cursorEnv) (bool, error) {
	if FileExists(cursorMCPPath) {
		data, err := os.ReadFile(cursorMCPPath)
		if err != nil {
			return false, fmt.Errorf("read MCP config: %w", err)
		}
		config, err := parseCursorMCPConfig(data)
		if err != nil {
			return false, fmt.Errorf("%s contains invalid JSON — fix it manually or remove it, then rerun: bd setup cursor", cursorMCPPath)
		}
		if cursorHasBeadsMCP(config) {
			_, _ = fmt.Fprintf(env.stdout, "✓ Beads MCP already configured — skipping %s\n", cursorMCPPath)
			return false, nil
		}
		config.MCPServers["beads"] = cursorMCPBeadsRaw()
		merged, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return false, fmt.Errorf("marshal MCP config: %w", err)
		}
		if err := atomicWriteFile(cursorMCPPath, append(merged, '\n')); err != nil {
			return false, fmt.Errorf("write MCP config: %w", err)
		}
		return true, nil
	}

	config := cursorMCPConfig{
		MCPServers: map[string]json.RawMessage{
			"beads": cursorMCPBeadsRaw(),
		},
	}
	created, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal MCP config: %w", err)
	}
	if err := EnsureDir(filepath.Dir(cursorMCPPath), 0755); err != nil {
		return false, err
	}
	if err := atomicWriteFile(cursorMCPPath, append(created, '\n')); err != nil {
		return false, fmt.Errorf("write MCP config: %w", err)
	}
	return true, nil
}

func removeCursorMCPBeads(env cursorEnv) (bool, error) {
	if !FileExists(cursorMCPPath) {
		return false, nil
	}
	data, err := os.ReadFile(cursorMCPPath)
	if err != nil {
		return false, fmt.Errorf("read MCP config: %w", err)
	}
	config, err := parseCursorMCPConfig(data)
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Warning: %s is invalid JSON; leaving it unchanged\n", cursorMCPPath)
		return false, nil
	}
	if !cursorBeadsEntryIsOurs(config) {
		return false, nil
	}
	delete(config.MCPServers, "beads")
	if len(config.MCPServers) == 0 {
		if err := os.Remove(cursorMCPPath); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("remove MCP config: %w", err)
		}
		return true, nil
	}
	remaining, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal MCP config: %w", err)
	}
	if err := atomicWriteFile(cursorMCPPath, append(remaining, '\n')); err != nil {
		return false, fmt.Errorf("write MCP config: %w", err)
	}
	return true, nil
}

func installCursor(env cursorEnv) error {
	_, _ = fmt.Fprintln(env.stdout, "Installing Cursor integration...")

	if err := EnsureDir(filepath.Dir(cursorRulesPath), 0755); err != nil {
		return err
	}
	if err := atomicWriteFile(cursorRulesPath, []byte(cursorRulesTemplate)); err != nil {
		return fmt.Errorf("write rules: %w", err)
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Rules installed: %s\n", cursorRulesPath)

	mcpWritten, err := mergeCursorMCP(env)
	if err != nil {
		return err
	}
	if mcpWritten {
		_, _ = fmt.Fprintf(env.stdout, "✓ MCP configured: %s\n", cursorMCPPath)
	}

	_, _ = fmt.Fprintln(env.stdout, "\nRestart Cursor for changes to take effect.")
	return nil
}

func checkCursor(env cursorEnv) error {
	rulesExists := FileExists(cursorRulesPath)
	mcpExists := FileExists(cursorMCPPath)
	mcpConfigured := false

	if mcpExists {
		data, err := os.ReadFile(cursorMCPPath)
		if err != nil {
			return fmt.Errorf("read MCP config: %w", err)
		}
		config, err := parseCursorMCPConfig(data)
		if err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Warning: %s is invalid JSON\n", cursorMCPPath)
		} else {
			mcpConfigured = cursorHasBeadsMCP(config)
		}
	}

	switch {
	case rulesExists && mcpConfigured:
		_, _ = fmt.Fprintln(env.stdout, "✓ Cursor integration installed")
		_, _ = fmt.Fprintf(env.stdout, "  Rules: %s\n", cursorRulesPath)
		_, _ = fmt.Fprintf(env.stdout, "  MCP: %s\n", cursorMCPPath)
		return nil
	case rulesExists && !mcpConfigured:
		_, _ = fmt.Fprintln(env.stdout, "⚠ Partial Cursor integration (rules only)")
		_, _ = fmt.Fprintf(env.stdout, "  Rules: %s\n", cursorRulesPath)
		if mcpExists {
			_, _ = fmt.Fprintf(env.stdout, "  MCP: %s (missing beads entry)\n", cursorMCPPath)
		} else {
			_, _ = fmt.Fprintln(env.stdout, "  Missing: MCP config")
		}
		return HandleErrorWithHint("partial Cursor integration", "Run: bd setup cursor")
	case !rulesExists && mcpConfigured:
		_, _ = fmt.Fprintln(env.stdout, "⚠ Partial Cursor integration (MCP only)")
		_, _ = fmt.Fprintf(env.stdout, "  MCP: %s\n", cursorMCPPath)
		_, _ = fmt.Fprintln(env.stdout, "  Missing: rules file")
		return HandleErrorWithHint("partial Cursor integration", "Run: bd setup cursor")
	default:
		return HandleErrorWithHint("Cursor integration not installed", "Run: bd setup cursor")
	}
	return nil
}

func removeCursor(env cursorEnv) error {
	_, _ = fmt.Fprintln(env.stdout, "Removing Cursor integration...")

	removed := false
	if err := os.Remove(cursorRulesPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("remove rules: %w", err)
		}
	} else {
		removed = true
	}

	mcpRemoved, err := removeCursorMCPBeads(env)
	if err != nil {
		return err
	}
	if mcpRemoved {
		removed = true
	}

	_ = os.Remove(filepath.Dir(cursorRulesPath))
	_ = os.Remove(filepath.Dir(cursorMCPPath))

	if !removed {
		_, _ = fmt.Fprintln(env.stdout, "No Cursor integration files found")
		return nil
	}

	_, _ = fmt.Fprintln(env.stdout, "✓ Removed Cursor integration")
	return nil
}

// InstallCursor installs Cursor IDE integration.
func InstallCursor() error {
	return installCursor(defaultCursorEnv())
}

// CheckCursor checks if Cursor integration is installed.
func CheckCursor() error {
	return checkCursor(defaultCursorEnv())
}

// RemoveCursor removes Cursor integration.
func RemoveCursor() error {
	return removeCursor(defaultCursorEnv())
}
