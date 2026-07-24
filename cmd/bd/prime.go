package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	internalbeads "github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/metrics"
)

var (
	primeFullMode     bool
	primeMCPMode      bool
	primeStealthMode  bool
	primeExportMode   bool
	primeMemoriesOnly bool
	primeNoMemories   bool
	primeHookJSONMode bool

	primeMaxMemories       int
	primeMaxMemoryChars    int
	primeMaxMemoriesSet    bool
	primeMaxMemoryCharsSet bool
)

const (
	primeStoreTimeoutEnv     = "BEADS_PRIME_TIMEOUT"
	primeStoreTimeoutDefault = 10 * time.Second
)

var ensureStoreActiveForPrime = ensureStoreActiveWithContext

func primeStoreTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(primeStoreTimeoutEnv))
	if raw == "" {
		return primeStoreTimeoutDefault
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d > 0 {
			return d
		}
		return primeStoreTimeoutDefault
	}
	if d, err := time.ParseDuration(raw + "s"); err == nil {
		if d > 0 {
			return d
		}
		return primeStoreTimeoutDefault
	}
	return primeStoreTimeoutDefault
}

// resolveGlobalPrimePath returns the path to ~/.config/beads/PRIME.md if it
// exists. configDirOverride is used for testing; pass "" for production.
func resolveGlobalPrimePath(configDirOverride string) string {
	var configDir string
	if configDirOverride != "" {
		configDir = configDirOverride
	} else {
		var err error
		configDir, err = os.UserConfigDir()
		if err != nil {
			return ""
		}
	}
	p := filepath.Join(configDir, "beads", "PRIME.md")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

var primeCmd = &cobra.Command{
	Use:     "prime",
	GroupID: "setup",
	Short:   "Output AI-optimized workflow context",
	Long: `Output essential Beads workflow context in AI-optimized markdown format.

Automatically detects if MCP server is active and adapts output:
- MCP mode: Brief workflow reminders (~50 tokens)
- CLI mode: Full command reference (~1-2k tokens)

Designed for Claude Code, Gemini CLI, and Codex SessionStart hooks to prevent
agents from forgetting bd workflow after context compaction.

Config options:
- no-git-ops: When true, outputs stealth mode (no git commands in session close protocol).
  Set via: bd config set no-git-ops true
  Useful when you want to control when commits happen manually.
- agent.profile: Explicit policy profile for git/commit authority wording
  (conservative | minimal | team-maintainer; default conservative).
  Set via: bd config set agent.profile team-maintainer
  Or per-session: BD_AGENT_PROFILE=team-maintainer (env var takes precedence).
  See docs/getting-started/ide-setup.md#policy-profiles for what each profile means.

	Workflow customization:
	- Place a .beads/PRIME.md file in the local clone or resolved workspace to override the default workflow text. Persistent memories (from bd remember) are still appended so memory injection keeps working under a custom template.
	- Use --export to dump the default content for customization.
	- Use --memories-only for hook contexts that should inject only persistent memories; this returns only the memories section even when a custom PRIME.md is present.
	- Use --no-memories to omit the persistent memories section (useful when the memories section is large and would dominate a context budget). --memories-only takes precedence if both are set.

Memory injection caps:
	Large memory sets can exceed what a session-start hook host will ingest,
	and hosts truncate silently. Cap what prime injects with --max-memories N
	and/or --max-memory-chars N (or the prime.max-memories /
	prime.max-memory-chars config keys; an explicit flag wins, and an explicit
	0 forces unlimited). Caps apply at whole-memory boundaries, at least one
	memory is always emitted, and a banner ahead of the entries reports how
	many were elided and how to browse the rest with bd memories.
	--max-memory-chars caps the total bytes of the injected memory entries;
	the section header and elision banner are excluded from the budget.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("prime")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		primeMaxMemoriesSet = cmd.Flags().Changed("max-memories")
		primeMaxMemoryCharsSet = cmd.Flags().Changed("max-memory-chars")

		emit := func(content string) {
			if primeHookJSONMode {
				_ = outputHookJSON(os.Stdout, content)
			} else {
				fmt.Print(content)
			}
		}

		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			// Silent exit with success enables cross-platform hook integration.
			// Under --hook-json still emit a valid empty envelope.
			if primeHookJSONMode {
				_ = outputHookJSON(os.Stdout, "")
			}
			return nil
		}

		// Detect MCP mode (unless overridden by flags)
		mcpMode := isMCPActive()
		if primeFullMode {
			mcpMode = false
		}
		if primeMCPMode {
			mcpMode = true
		}

		stealthMode := primeStealthMode || config.GetBool("no-git-ops")

		// --memories-only is the primary memory-injection path for hook contexts
		// (e.g. PreCompact). It must return ONLY the persistent memories section,
		// regardless of any custom PRIME.md override or --export (GH#3941).
		// Handle it before the custom-PRIME branch so a custom PRIME.md can never
		// suppress memory injection.
		if primeMemoriesOnly {
			var buf bytes.Buffer
			if err := outputMemoriesOnlyContext(&buf); err != nil {
				// Suppress all errors - silent exit with success.
				if primeHookJSONMode {
					_ = outputHookJSON(os.Stdout, "")
				}
				return nil
			}
			emit(buf.String())
			return nil
		}

		// Check for custom PRIME.md override (unless --export flag).
		// A custom PRIME.md replaces the default workflow text, but the persistent
		// memories section is still appended (when present) so `bd remember` keeps
		// working under a custom template — matching the default-template behavior
		// (GH#3941).
		if !primeExportMode {
			if content, ok := readCustomPrimeContent(beadsDir); ok {
				if !primeNoMemories {
					if mem := formatMemoriesForPrime(false); mem != "" {
						content += mem
					}
				}
				emit(content)
				return nil
			}
		}

		var buf bytes.Buffer
		if err := outputPrimeContextWithOptions(&buf, mcpMode, stealthMode, primeMemoriesOnly); err != nil {
			// Errors are suppressed by design for hook integration.
			if primeHookJSONMode {
				_ = outputHookJSON(os.Stdout, "")
			}
			return nil
		}
		// Append the AGENTS.md/CLAUDE.md divergence reminder only when both
		// files are independent regulars carrying the bd marker; otherwise this
		// adds nothing (zero output, negligible cost).
		buf.WriteString(primeDivergenceReminder(""))
		emit(buf.String())
		return nil
	},
}

func init() {
	primeCmd.Flags().BoolVar(&primeFullMode, "full", false, "Force full CLI output (ignore MCP detection)")
	primeCmd.Flags().BoolVar(&primeMCPMode, "mcp", false, "Force MCP mode (minimal output)")
	primeCmd.Flags().BoolVar(&primeStealthMode, "stealth", false, "Stealth mode (no git operations, flush only)")
	primeCmd.Flags().BoolVar(&primeExportMode, "export", false, "Output default content (ignores PRIME.md override)")
	primeCmd.Flags().BoolVar(&primeMemoriesOnly, "memories-only", false, "Output only persistent memories for compact hook contexts")
	primeCmd.Flags().BoolVar(&primeNoMemories, "no-memories", false, "Omit the persistent memories section (ignored when --memories-only is set, which wins)")
	primeCmd.Flags().BoolVar(&primeHookJSONMode, "hook-json", false, "Wrap output in the SessionStart hook JSON envelope (Claude Code, Gemini CLI, Codex)")
	primeCmd.Flags().IntVar(&primeMaxMemories, "max-memories", 0, "Cap injected persistent memories to N entries (0 = unlimited; falls back to the prime.max-memories config key)")
	primeCmd.Flags().IntVar(&primeMaxMemoryChars, "max-memory-chars", 0, "Cap the total bytes of injected memory entries, at whole-memory boundaries; section header and banner are not counted (0 = unlimited; falls back to the prime.max-memory-chars config key)")
	rootCmd.AddCommand(primeCmd)
}

// readCustomPrimeContent returns the contents of a custom PRIME.md override and
// true when one is found. It checks, in priority order: the local .beads/PRIME.md
// (clone-specific customization), the redirected workspace PRIME.md (shared
// customization), then the global ~/.config/beads/PRIME.md. It returns ("", false)
// when no override exists, so callers fall through to the generated default.
func readCustomPrimeContent(beadsDir string) (string, bool) {
	localPrimePath := filepath.Join(".beads", "PRIME.md")
	// Try local first (user's clone-specific customization).
	// #nosec G304 -- path is relative to cwd
	if content, err := os.ReadFile(localPrimePath); err == nil {
		return string(content), true
	}
	// Fall back to redirected location (shared customization).
	redirectedPrimePath := filepath.Join(beadsDir, "PRIME.md")
	// #nosec G304 -- path is constructed from beadsDir which we control
	if content, err := os.ReadFile(redirectedPrimePath); err == nil {
		return string(content), true
	}
	// Fall back to global config (~/.config/beads/PRIME.md).
	if globalPath := resolveGlobalPrimePath(""); globalPath != "" {
		// #nosec G304 -- path constructed from UserConfigDir which we control
		if content, err := os.ReadFile(globalPath); err == nil {
			return string(content), true
		}
	}
	return "", false
}

// outputHookJSON wraps content in the SessionStart hook JSON envelope shared
// by Claude Code, Gemini CLI, and Codex. All three require stdout to be valid
// JSON — no plain text may be emitted alongside it. See:
// https://geminicli.com/docs/hooks/reference/
func outputHookJSON(w io.Writer, content string) error {
	type hookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	}
	envelope := struct {
		HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
	}{
		HookSpecificOutput: hookSpecificOutput{
			HookEventName:     "SessionStart",
			AdditionalContext: content,
		},
	}
	return json.NewEncoder(w).Encode(envelope)
}

// isMCPActive detects if MCP server is currently active
func isMCPActive() bool {
	// Get home directory with fallback
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to HOME environment variable
		home = os.Getenv("HOME")
		if home == "" {
			// Can't determine home directory, assume no MCP
			return false
		}
	}

	settingsPath := filepath.Join(home, ".claude/settings.json")
	// #nosec G304 -- settings path derived from user home directory
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}

	// Check mcpServers section for beads
	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		return false
	}

	// Look for beads server (any key containing "beads")
	for key := range mcpServers {
		if strings.Contains(strings.ToLower(key), "beads") {
			return true
		}
	}

	return false
}

// isEphemeralBranch detects if current branch has no upstream (ephemeral/local-only)
var isEphemeralBranch = func() bool {
	// git rev-parse --abbrev-ref --symbolic-full-name @{u}
	// Returns error code 128 if no upstream configured
	rc, err := internalbeads.GetRepoContext()
	if err != nil {
		return true // Default to ephemeral if we can't determine context
	}
	cmd := rc.GitCmdCWD(context.Background(), "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return cmd.Run() != nil
}

// primeNoPushConfigured reports whether the "no-push" config flag is set
// (stubbable for tests).
var primeNoPushConfigured = func() bool {
	return config.GetBool("no-push")
}

// primeAgentProfile reports the explicit agent.profile knob (gh#3423,
// follow-up to #4220), resolved via BD_AGENT_PROFILE env override / config
// key with a safe fallback to conservative (stubbable for tests).
var primeAgentProfile = func() config.AgentProfile {
	return config.GetAgentProfile()
}

// primeHasGitRemote detects if any git remote is configured (stubbable for tests)
var primeHasGitRemote = func() bool {
	rc, err := internalbeads.GetRepoContext()
	if err != nil {
		return false
	}
	cmd := rc.GitCmdCWD(context.Background(), "remote")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// primeHasSyncRemote detects if a Dolt sync remote is configured (stubbable for tests)
var primeHasSyncRemote = func() bool {
	return resolveSyncRemote() != ""
}

// primeDoltSyncBullets returns the "bd dolt push"/"bd dolt pull" bullet
// lines for the Sync & Collaboration section, in the requested order, and
// an empty string when no Dolt sync remote is configured (doltSync == false,
// gh#4130). This is independent of the git-remote axis (localOnly) that
// drives git push/pull hints — the two axes must not be conflated
// (gh#4230 review).
func primeDoltSyncBullets(doltSync bool, pushFirst bool) string {
	if !doltSync {
		return ""
	}
	if pushFirst {
		return "- `bd dolt push` - Push beads to Dolt remote\n" +
			"- `bd dolt pull` - Pull beads from Dolt remote\n"
	}
	return "- `bd dolt pull` - Pull beads updates from Dolt remote\n" +
		"- `bd dolt push` - Push beads to Dolt remote\n"
}

// getRedirectNotice returns a notice string if beads is redirected
func getRedirectNotice(verbose bool) string {
	redirectInfo := beads.GetRedirectInfo()
	if !redirectInfo.IsRedirected {
		return ""
	}

	if verbose {
		return fmt.Sprintf(`> ⚠️ **Redirected**: Local .beads → %s
> You share issues with other clones using this redirect.

`, redirectInfo.TargetDir)
	}
	return fmt.Sprintf("**Note**: Beads redirected to %s (shared with other clones)\n\n", redirectInfo.TargetDir)
}

// outputPrimeContext outputs workflow context in markdown format
func outputPrimeContext(w io.Writer, mcpMode bool, stealthMode bool) error {
	return outputPrimeContextWithOptions(w, mcpMode, stealthMode, false)
}

func outputPrimeContextWithOptions(w io.Writer, mcpMode bool, stealthMode bool, memoriesOnly bool) error {
	if memoriesOnly {
		return outputMemoriesOnlyContext(w)
	}
	if mcpMode {
		return outputMCPContext(w, stealthMode)
	}
	return outputCLIContext(w, stealthMode)
}

const primeTruncationDirective = "[bd prime] If this output is truncated by your host, read the full persisted hook output before continuing; it may contain project memories and session rules not visible in the preview.\n\n"

func outputMemoriesOnlyContext(w io.Writer) error {
	_, _ = fmt.Fprint(w, primeTruncationDirective)
	if mem := formatMemoriesForPrime(false); mem != "" {
		_, _ = fmt.Fprint(w, mem)
		return nil
	}
	_, _ = fmt.Fprint(w, "# Beads Persistent Memories\n\nNo memories stored. Use `bd remember \"insight\"` to add one.\n")
	return nil
}

// formatMemoriesForPrime queries memories from the k/v store and formats them for injection.
// Returns empty string if no memories or if store is unavailable.
func formatMemoriesForPrime(compact bool) string {
	// Try to initialize store if not already active (prime may run before other commands)
	if store == nil {
		timeout := primeStoreTimeout()
		ctx := context.Background()
		var cancel context.CancelFunc
		if timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		if err := ensureStoreActiveForPrime(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return formatPrimeMemoryTimeout(compact, timeout)
			}
			return "" // Silently skip — store unavailable
		}
	}
	if store == nil {
		return ""
	}
	ctx := context.Background()
	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		return ""
	}

	fullPrefix := kvPrefix + memoryPrefix
	memories := make(map[string]string)
	for k, v := range allConfig {
		if strings.HasPrefix(k, fullPrefix) {
			memories[strings.TrimPrefix(k, fullPrefix)] = v
		}
	}
	if len(memories) == 0 {
		return ""
	}
	maxCount, maxChars := primeMemoryCaps()
	return renderPrimeMemories(memories, compact, maxCount, maxChars)
}

// primeConfigInt reads an integer config key (stubbable for tests).
var primeConfigInt = func(key string) int {
	return config.GetInt(key)
}

// primeMemoryCaps resolves the memory-injection caps. An explicitly passed
// flag wins, including an explicit 0 meaning "force unlimited"; otherwise the
// prime.max-memories / prime.max-memory-chars config keys apply. 0 or unset
// means uncapped.
func primeMemoryCaps() (maxCount, maxChars int) {
	maxCount = primeMaxMemories
	if !primeMaxMemoriesSet && maxCount == 0 {
		maxCount = primeConfigInt("prime.max-memories")
	}
	maxChars = primeMaxMemoryChars
	if !primeMaxMemoryCharsSet && maxChars == 0 {
		maxChars = primeConfigInt("prime.max-memory-chars")
	}
	if maxCount < 0 {
		maxCount = 0
	}
	if maxChars < 0 {
		maxChars = 0
	}
	return maxCount, maxChars
}

// renderPrimeMemories formats memories for injection, applying the given
// caps. maxCount bounds how many memories are emitted; maxChars bounds the
// total bytes of the emitted memory entries (the section header and elision
// banner are not counted against this budget). Both are 0 when uncapped.
// Caps apply at whole-memory boundaries and at least one memory is always
// emitted, so a single oversized memory can exceed maxChars rather than
// vanish. Keys are emitted in sorted order (the memory store keeps no
// timestamps, so alphabetical is the only stable order available); when
// entries are elided a banner ahead of the entries says how many and how to
// reach the rest, so a capped prime never silently drops context. The banner
// names only the cap that actually fired.
func renderPrimeMemories(memories map[string]string, compact bool, maxCount, maxChars int) string {
	keys := sortedKeys(memories)

	entries := make([]string, 0, len(keys))
	used := 0
	var countCapHit, charCapHit bool
	for _, k := range keys {
		if maxCount > 0 && len(entries) >= maxCount {
			countCapHit = true
			break
		}
		var entry string
		if compact {
			v := strings.ReplaceAll(memories[k], "\n", " ")
			v = truncate(v, 150)
			entry = fmt.Sprintf("- **%s**: %s\n", k, v)
		} else {
			entry = fmt.Sprintf("### %s\n%s\n\n", k, memories[k])
		}
		if maxChars > 0 && len(entries) > 0 && used+len(entry) > maxChars {
			charCapHit = true
			break
		}
		entries = append(entries, entry)
		used += len(entry)
	}

	elided := len(keys) - len(entries)
	var noteCount, noteChars int
	if countCapHit {
		noteCount = maxCount
	}
	if charCapHit {
		noteChars = maxChars
	}
	var sb strings.Builder
	if compact {
		if elided > 0 {
			sb.WriteString(fmt.Sprintf("\n## Memories (showing %d of %d)\n", len(entries), len(keys)))
			sb.WriteString(fmt.Sprintf("- %d more not shown (%s); browse with `bd memories <keyword>`\n", elided, primeMemoryCapNote(noteCount, noteChars)))
		} else {
			sb.WriteString("\n## Memories\n")
		}
	} else {
		if elided > 0 {
			sb.WriteString(fmt.Sprintf("\n## Persistent Memories (showing %d of %d, alphabetical)\n\n", len(entries), len(keys)))
		} else {
			sb.WriteString(fmt.Sprintf("\n## Persistent Memories (%d)\n\n", len(keys)))
		}
		sb.WriteString("Stored via `bd remember`. Update in place with `bd remember --key <key> \"new content\"`. Search with `bd memories <keyword>`. Remove with `bd forget <key>`.\n\n")
		if elided > 0 {
			sb.WriteString(fmt.Sprintf("> %d more memories are not shown here (%s). Browse the full set with `bd memories <keyword>` or recall one with `bd remember <key>`.\n\n", elided, primeMemoryCapNote(noteCount, noteChars)))
		}
	}
	for _, entry := range entries {
		sb.WriteString(entry)
	}
	return sb.String()
}

// primeMemoryCapNote names the active cap(s) for the elision banner.
func primeMemoryCapNote(maxCount, maxChars int) string {
	var parts []string
	if maxCount > 0 {
		parts = append(parts, fmt.Sprintf("max-memories=%d", maxCount))
	}
	if maxChars > 0 {
		parts = append(parts, fmt.Sprintf("max-memory-chars=%d", maxChars))
	}
	return "capped by " + strings.Join(parts, ", ")
}

func formatPrimeMemoryTimeout(compact bool, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = primeStoreTimeoutDefault
	}
	msg := fmt.Sprintf("Skipped: timed out after %s opening beads storage. Another bd process or stale storage lock may be blocking memory injection; run `bd doctor` and stop stuck bd processes before retrying.", timeout.Round(time.Millisecond))
	if compact {
		return "\n## Memories\n- " + msg + "\n"
	}
	return "\n## Persistent Memories\n\n" + msg + "\n"
}

// outputMCPContext outputs minimal context for MCP users
func outputMCPContext(w io.Writer, stealthMode bool) error {
	ephemeral := isEphemeralBranch()
	noPush := primeNoPushConfigured()
	// localOnly reflects only the git-remote axis (drives git push/pull
	// hints and remote-sync authority wording). Dolt sync-remote presence
	// (doltSync, below) is a separate axis that only gates the literal
	// `bd dolt push`/`bd dolt pull` hint lines (gh#4130) — the two must not
	// be conflated (gh#4230 review).
	localOnly := !primeHasGitRemote()
	doltSync := primeHasSyncRemote()

	var closeProtocol string
	var profileRule string
	if stealthMode {
		// Stealth mode is an explicit no-git context.
		closeProtocol = "Before saying \"done\": bd close <completed-ids>"
		profileRule = "Git authority: no git operations in this context"
	} else if localOnly {
		if primeAgentProfile() == config.ProfileTeamMaintainer {
			closeProtocol = "Before saying \"done\": bd close <completed-ids>; run checks; run git status and commit local changes as routine work (agent.profile=team-maintainer); do not push, pull, or run remote sync."
			profileRule = "Git authority: local-only/no-remote. No git remote configured. Profile: team-maintainer active (agent.profile=team-maintainer) - local commits are routine; do not push, pull, or run remote sync. Explicit no-commit instructions still override."
		} else {
			closeProtocol = "Before saying \"done\": bd close <completed-ids>; run checks; report git status and proposed handoff (local-only/no remote sync)"
			profileRule = "Git authority: local-only/no-remote. No git remote configured. Do not push, pull, or run remote sync. Local git operations follow active user, orchestrator, and repository authority."
		}
	} else if ephemeral {
		closeProtocol = "Before saying \"done\": bd close <completed-ids>; run checks; report git status and proposed handoff (no push - ephemeral branch)"
		profileRule = "Profile model: conservative by default; commit only with explicit user/orchestrator authority"
	} else if noPush {
		closeProtocol = "Before saying \"done\": bd close <completed-ids>; run checks; report git status and proposed handoff (push disabled)"
		profileRule = "Profile model: conservative by default; push only with explicit user/orchestrator authority"
	} else if primeAgentProfile() == config.ProfileTeamMaintainer {
		// Explicit agent.profile=team-maintainer knob: commit/sync/push are
		// routine work here, not conditional on a per-session "enabled" ask.
		// Hard constraints above (stealth/local-only/ephemeral/no-push) still
		// take precedence over this profile.
		if doltSync {
			closeProtocol = "Before saying \"done\": bd close <completed-ids>; run checks; commit, bd dolt push, and git push as part of routine work (agent.profile=team-maintainer), unless current instructions say otherwise."
		} else {
			closeProtocol = "Before saying \"done\": bd close <completed-ids>; run checks; commit and git push as part of routine work (agent.profile=team-maintainer), unless current instructions say otherwise."
		}
		profileRule = "Profile: team-maintainer active (agent.profile=team-maintainer) - commit, sync, and push are routine; explicit no-commit/no-push instructions still override."
	} else {
		closeProtocol = "Before saying \"done\": bd close <completed-ids>; run checks. Then follow the active profile — conservative reports handoff; team-maintainer may commit/sync/push when explicitly enabled."
		profileRule = "Default: do not commit, push, or run dolt remote sync without explicit authority. Team-maintainer behavior is opt-in and still subordinate to user/orchestrator instructions."
	}

	redirectNotice := getRedirectNotice(false)
	var memories string
	if !primeNoMemories {
		memories = formatMemoriesForPrime(true)
	}

	context := primeTruncationDirective + `# Beads Issue Tracker Active

` + redirectNotice
	if memories != "" {
		context += memories + "\n"
	}

	context += `# 🚨 SESSION CLOSE PROTOCOL 🚨

` + closeProtocol + `

## Core Rules
- **Default**: Use beads for ALL task tracking (` + "`bd create`" + `, ` + "`bd ready`" + `, ` + "`bd close`" + `)
- **Prohibited**: Do NOT use TodoWrite, TaskCreate, or markdown files for task tracking
- **Workflow**: Create beads issue BEFORE writing code, mark in_progress when starting
- **Memory**: Use ` + "`bd remember`" + ` for persistent knowledge. Do NOT use MEMORY.md files.
- Persistence you don't need beats lost context
- ` + profileRule + `

Start: Check ` + "`ready`" + ` tool for available work.
`
	_, _ = fmt.Fprint(w, context)

	return nil
}

// outputCLIContext outputs full CLI reference for non-MCP users
func outputCLIContext(w io.Writer, stealthMode bool) error {
	ephemeral := isEphemeralBranch()
	noPush := primeNoPushConfigured()
	// localOnly reflects only the git-remote axis (drives git push/pull
	// hints and remote-sync authority wording). Dolt sync-remote presence
	// (doltSync, below) is a separate axis that only gates the literal
	// `bd dolt push`/`bd dolt pull` hint lines (gh#4130) — the two must not
	// be conflated (gh#4230 review).
	localOnly := !primeHasGitRemote()
	doltSync := primeHasSyncRemote()

	var closeProtocol string
	var closeNote string
	var syncSection string
	var completingWorkflow string
	var gitWorkflowRule string
	var profileRule string

	if stealthMode {
		// Stealth mode is an explicit no-git context.
		closeProtocol = `[ ] bd close <id1> <id2> ...   (close completed issues)`
		syncSection = `### Sync & Collaboration
- ` + "`bd search <query>`" + ` - Search issues by keyword`
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
` + "```"
		gitWorkflowRule = "Git workflow: stealth mode (no git ops)"
		profileRule = "Git authority: no git operations in this context"
	} else if localOnly {
		closeNote = "**Note:** No git remote configured. Do not push, pull, or run remote sync. Local git operations follow active user, orchestrator, and repository authority."
		syncSection = `### Sync & Collaboration
- ` + "`bd search <query>`" + ` - Search issues by keyword`
		if primeAgentProfile() == config.ProfileTeamMaintainer {
			closeProtocol = `[ ] 1. bd close <id1> <id2> ...   (close completed issues)
[ ] 2. run quality gates        (tests, linters, builds when relevant)
[ ] 3. git status               (check what changed)
[ ] 4. team-maintainer: commit local changes; do not push or run remote sync`
			completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
git status                  # Check changed files
git add <files> && git commit -m "..."
# Local-only/no-remote: do not push, pull, or run remote sync
` + "```"
			gitWorkflowRule = "Git workflow: local-only/no-remote; team-maintainer commits locally but does not push or run remote sync"
			profileRule = "Git authority: local-only/no-remote. Profile: team-maintainer active (agent.profile=team-maintainer) - local commits are routine; explicit no-commit instructions still override."
		} else {
			closeProtocol = `[ ] 1. bd close <id1> <id2> ...   (close completed issues)
[ ] 2. run quality gates        (tests, linters, builds when relevant)
[ ] 3. git status               (check what changed)
[ ] 4. report handoff           (local-only/no remote sync; wait for authority)`
			completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
git status                  # Report changed files and proposed commands
# Local-only/no-remote: do not push, pull, or run remote sync
` + "```"
			gitWorkflowRule = "Git workflow: local-only/no-remote; no push, pull, or remote sync"
			profileRule = "Git authority: local-only/no-remote. Local git operations follow active user, orchestrator, and repository authority."
		}
	} else if ephemeral {
		closeProtocol = `[ ] 1. bd close <id1> <id2> ...   (close completed issues)
[ ] 2. run quality gates        (tests, linters, builds when relevant)
[ ] 3. git status               (check what changed)
[ ] 4. report handoff           (changed files, validation, proposed commit if authorized)`
		closeNote = "**Note:** This is an ephemeral branch (no upstream). Do not push it unless the user or orchestrator explicitly says to."
		syncSection = "### Sync & Collaboration\n" +
			primeDoltSyncBullets(doltSync, false) +
			"- `bd search <query>` - Search issues by keyword"
		doltPullStep := ""
		if doltSync {
			doltPullStep = "bd dolt pull                # Pull latest beads from main\n"
		}
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
` + doltPullStep + `git status                  # Report changed files and proposed commit; wait for authority
# Merge to main locally only when the active instructions grant that authority
` + "```"
		gitWorkflowRule = "Git workflow: conservative by default on ephemeral branches"
		profileRule = "Profile model: conservative/minimal report handoff; team-maintainer may commit only when explicitly enabled"
	} else if noPush {
		closeProtocol = `[ ] 1. bd close <id1> <id2> ...   (close completed issues)
[ ] 2. run quality gates        (tests, linters, builds when relevant)
[ ] 3. git status               (check what changed)
[ ] 4. report handoff           (push disabled; wait for explicit authority)`
		closeNote = "**Note:** Push disabled via config. Do not push unless the user or orchestrator explicitly says to."
		syncSection = "### Sync & Collaboration\n" +
			primeDoltSyncBullets(doltSync, true) +
			"- `bd search <query>` - Search issues by keyword"
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
git status                  # Report changed files and proposed commands
# Do not push unless current instructions explicitly allow it
` + "```"
		gitWorkflowRule = "Git workflow: push disabled; report handoff unless explicitly authorized"
		profileRule = "Profile model: conservative/minimal report handoff; team-maintainer still respects no-push/user instructions"
	} else if primeAgentProfile() == config.ProfileTeamMaintainer {
		// Explicit agent.profile=team-maintainer knob: commit/sync/push are
		// routine work here, not conditional on a per-session "enabled" ask.
		// Hard constraints above (stealth/local-only/ephemeral/no-push) still
		// take precedence over this profile.
		closeProtocol = `[ ] 1. bd close <id1> <id2> ...   (close completed issues)
[ ] 2. run quality gates        (tests, linters, builds when relevant)
[ ] 3. git status               (check what changed)
[ ] 4. team-maintainer: commit, sync, push as part of routine work (unless current instructions say otherwise)`
		closeNote = "**Policy:** agent.profile=team-maintainer is active. Commit, sync, and push as part of routine work; explicit \"do not commit\"/\"do not push\" instructions still override."
		syncSection = "### Sync & Collaboration\n" +
			primeDoltSyncBullets(doltSync, true) +
			"- `bd search <query>` - Search issues by keyword"
		doltPushStep := ""
		if doltSync {
			doltPushStep = "bd dolt push\n"
		}
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
git status                  # Check changed files
# team-maintainer: commit, sync, push are routine unless instructions forbid it
git add . && git commit -m "..."
` + doltPushStep + `git push
` + "```"
		gitWorkflowRule = "Git workflow: team-maintainer active - commit/push are routine unless explicitly restricted"
		profileRule = "Profile: team-maintainer active (agent.profile=team-maintainer) - commit, sync, and push are routine; explicit no-commit/no-push instructions still override."
	} else {
		closeProtocol = `[ ] 1. bd close <id1> <id2> ...   (close completed issues)
[ ] 2. run quality gates        (tests, linters, builds when relevant)
[ ] 3. git status               (check what changed)
[ ] 4. follow active profile    (conservative: report handoff; team-maintainer: commit/sync/push if enabled)`
		closeNote = "**Policy:** Conservative is the default. Commit, sync, or push only when the active user, orchestrator, or repository profile grants that authority."
		syncSection = "### Sync & Collaboration\n" +
			primeDoltSyncBullets(doltSync, true) +
			"- `bd search <query>` - Search issues by keyword"
		doltPushComment := ""
		if doltSync {
			doltPushComment = "# bd dolt push\n"
		}
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
git status                  # Check changed files
# Conservative/minimal/default: report status and proposed commands; wait for approval
# Team-maintainer opt-in only, unless current instructions forbid it:
# git add . && git commit -m "..."
` + doltPushComment + `# git push
` + "```"
		gitWorkflowRule = "Git workflow: conservative by default; commit/push only with explicit user/orchestrator or team-maintainer authority"
		profileRule = "Default: do not commit, push, or run dolt remote sync without explicit authority. Team-maintainer behavior is opt-in and still subordinate to user/orchestrator instructions."
	}

	redirectNotice := getRedirectNotice(true)
	var memories string
	if !primeNoMemories {
		memories = formatMemoriesForPrime(false)
	}

	context := primeTruncationDirective + `# Beads Workflow Context

> **Context Recovery**: Run ` + "`bd prime`" + ` after compaction, clear, or new session
> Hooks auto-call this in Claude Code and Codex when a beads workspace is resolved

` + redirectNotice
	if memories != "" {
		context += memories + "\n"
	}

	context += `# 🚨 SESSION CLOSE PROTOCOL 🚨

**CRITICAL**: Before saying "done" or "complete", you MUST run this checklist:

` + "```" + `
` + closeProtocol + `
` + "```" + `

` + closeNote + `

## Core Rules
- **Default**: Use beads for ALL task tracking (` + "`bd create`" + `, ` + "`bd ready`" + `, ` + "`bd close`" + `)
- **Prohibited**: Do NOT use TodoWrite, TaskCreate, or markdown files for task tracking
- **Workflow**: Create beads issue BEFORE writing code, mark in_progress when starting
- **Memory**: Use ` + "`bd remember \"insight\"`" + ` for persistent knowledge across sessions. Do NOT use MEMORY.md files — they fragment across accounts. Search with ` + "`bd memories <keyword>`" + `.
- Persistence you don't need beats lost context
- ` + profileRule + `
- ` + gitWorkflowRule + `
- Session management: check ` + "`bd ready`" + ` for available work

## Essential Commands

### Finding Work
- ` + "`bd ready`" + ` - Show issues ready to work (no blockers)
- ` + "`bd list --status=open`" + ` - All open issues
- ` + "`bd list --status=in_progress`" + ` - Your active work
- ` + "`bd show <id>`" + ` - Detailed issue view with dependencies

### Creating & Updating
- ` + "`bd create --title=\"Summary of this issue\" --description=\"Why this issue exists and what needs to be done\" --type=task|bug|feature --priority=2`" + ` - New issue
  - Priority: 0-4 or P0-P4 (0=critical, 2=medium, 4=backlog). NOT "high"/"medium"/"low"
- ` + "`bd create ... --parent=<id>`" + ` - Hierarchical child (task under epic, subtask under task; inherits parent labels)
- ` + "`bd update <id> --claim`" + ` - Claim work
- ` + "`bd unclaim <id>`" + ` - Release stuck issue (agent crashed)
- ` + "`bd update <id> --assignee=username`" + ` - Assign to someone
- ` + "`bd update <id> --title/--description/--notes/--design`" + ` - Update fields inline
- ` + "`bd close <id>`" + ` - Mark complete
- ` + "`bd close <id1> <id2> ...`" + ` - Close multiple issues at once (more efficient)
- ` + "`bd close <id> --reason=\"explanation\"`" + ` - Close with reason
- **Tip**: When creating multiple issues/tasks/epics, use parallel subagents for efficiency
- **WARNING**: Do NOT use ` + "`bd edit`" + ` - it opens $EDITOR (vim/nano) which blocks agents

### Dependencies & Blocking
- ` + "`bd dep add <issue> <depends-on>`" + ` - Add dependency (issue depends on depends-on)
- ` + "`bd blocked`" + ` - Show all blocked issues
- ` + "`bd show <id>`" + ` - See what's blocking/blocked by this issue

` + syncSection + `

### Project Health
- ` + "`bd stats`" + ` - Project statistics (open/closed/blocked counts)
- ` + "`bd doctor`" + ` - Check for issues (sync problems, missing hooks)
- ` + "`bd doctor --check=conventions`" + ` - Check for convention drift (lint, stale, orphans)

### Quality Tools
- ` + "`bd create --validate`" + ` - Check description has required sections
- ` + "`bd create --acceptance=\"criteria\"`" + ` - Set acceptance criteria (checked by --validate)
- ` + "`bd create --design=\"decisions\"`" + ` - Record design decisions
- ` + "`bd create --notes=\"context\"`" + ` - Add supplementary notes
- ` + "`bd config set validation.on-create warn`" + ` - Auto-validate on every create
- ` + "`bd lint`" + ` - Check existing issues for missing sections

### Lifecycle & Hygiene
- ` + "`bd defer <id> --until=\"date\"`" + ` - Defer work to a future date
- ` + "`bd supersede <id> --with=<new-id>`" + ` - Mark issue as superseded
- ` + "`bd close <id> --suggest-next`" + ` - Show newly unblocked issues after closing
- ` + "`bd stale`" + ` - Find issues with no recent activity
- ` + "`bd orphans`" + ` - Find issues with broken dependencies
- ` + "`bd preflight`" + ` - Pre-PR checks (lint, stale, orphans)
- ` + "`bd human <id>`" + ` - Flag for human decision (list/respond/dismiss)

### Structured Workflows
- ` + "`bd formula list`" + ` - See available workflow templates
- ` + "`bd mol pour <name>`" + ` - Start structured workflow from formula

## Common Workflows

**Starting work:**
` + "```bash" + `
bd ready           # Find available work
bd show <id>       # Review issue details
bd update <id> --claim  # Claim it
` + "```" + `

` + completingWorkflow + `

**Creating dependent work:**
` + "```bash" + `
# Run bd create commands in parallel (use subagents for many items)
bd create --title="Implement feature X" --description="Why this issue exists and what needs to be done" --type=feature
bd create --title="Write tests for X" --description="Why this issue exists and what needs to be done" --type=task
bd dep add beads-yyy beads-xxx  # Tests depend on Feature (Feature blocks tests)
` + "```" + `
`
	_, _ = fmt.Fprint(w, context)

	return nil
}
