package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/setup"
)

// CheckCursor reports whether beads-managed Cursor agent hooks are installed.
// It mirrors CheckClaude: Go observes and reports; the agent/user decides. The
// hooks (sessionStart/preCompact/postToolUse calling `bd cursor-hook`) are what
// keep beads context alive across compaction in Cursor.
// repoPath is the project root directory.
func CheckCursor(repoPath string) DoctorCheck {
	projectHooks := filepath.Join(repoPath, ".cursor", "hooks.json")
	hasProject := hasBeadsCursorHooks(projectHooks)

	hasGlobal := false
	if home, err := os.UserHomeDir(); err == nil {
		hasGlobal = hasBeadsCursorHooks(filepath.Join(home, ".cursor", "hooks.json"))
	}

	if hasProject || hasGlobal {
		loc := ".cursor/hooks.json"
		if !hasProject && hasGlobal {
			loc = "~/.cursor/hooks.json"
		}
		return DoctorCheck{
			Name:    "Cursor Integration",
			Status:  StatusOK,
			Message: "Hooks installed",
			Detail:  "bd prime auto-injected on sessionStart and recovered after compaction (" + loc + ")",
		}
	}

	// Only nudge when Cursor is actually in use; otherwise stay quiet so
	// non-Cursor users don't see spurious warnings.
	if !isCursorPresent(repoPath) {
		return DoctorCheck{
			Name:    "Cursor Integration",
			Status:  StatusOK,
			Message: "N/A (Cursor not detected)",
		}
	}

	return DoctorCheck{
		Name:    "Cursor Integration",
		Status:  StatusWarning,
		Message: "Hooks not installed",
		Detail: "Cursor will only see the static rule (if any) and won't auto-recover\n" +
			"  beads context after a context compaction.",
		Fix: "Run 'bd setup cursor' to install sessionStart/preCompact/postToolUse hooks\n" +
			"  (or 'bd setup cursor --global' for ~/.cursor/hooks.json across all projects).",
	}
}

// hasBeadsCursorHooks reports whether a hooks.json contains a bd-managed hook
// (a command starting with "bd cursor-hook") for any managed event.
func hasBeadsCursorHooks(path string) bool {
	return len(cursorBeadsHookEvents(path)) > 0
}

// cursorBeadsHookEvents returns the managed events in a hooks.json that have a
// bd-managed hook (a "bd cursor-hook " command). It returns nil if the file is
// absent or malformed (see CheckCursorSettingsHealth for that case). Detection
// delegates to the setup package so the installer and doctor agree on which
// events bd manages and what a bd-managed entry looks like.
func cursorBeadsHookEvents(path string) []string {
	data, err := os.ReadFile(path) // #nosec G304 -- path is constructed from known safe locations (repo root / home .cursor), not user input
	if err != nil {
		return nil
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}

	return setup.CursorManagedHookEvents(config)
}

// CheckCursorSettingsHealth reports malformed .cursor/hooks.json files. A broken
// hooks.json silently disables every Cursor hook, so this is an error (mirrors
// CheckClaudeSettingsHealth). repoPath is the project root directory.
func CheckCursorSettingsHealth(repoPath string) DoctorCheck {
	files := []struct{ path, label string }{
		{filepath.Join(repoPath, ".cursor", "hooks.json"), ".cursor/hooks.json"},
	}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, struct{ path, label string }{
			filepath.Join(home, ".cursor", "hooks.json"), "~/.cursor/hooks.json",
		})
	}

	var malformed []string
	var checked int
	for _, f := range files {
		data, err := os.ReadFile(f.path) // #nosec G304 -- known safe .cursor locations, not user input
		if err != nil {
			continue
		}
		checked++
		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			malformed = append(malformed, fmt.Sprintf("%s: %v", f.label, err))
		}
	}

	if checked == 0 {
		return DoctorCheck{
			Name:    "Cursor Settings Health",
			Status:  StatusOK,
			Message: "No Cursor hooks files found",
		}
	}
	if len(malformed) > 0 {
		return DoctorCheck{
			Name:    "Cursor Settings Health",
			Status:  StatusError,
			Message: fmt.Sprintf("%d malformed hooks file(s)", len(malformed)),
			Detail:  strings.Join(malformed, "\n"),
			Fix:     "Fix the JSON syntax in the listed file(s). A malformed hooks.json silently disables all Cursor hooks.",
		}
	}
	return DoctorCheck{
		Name:    "Cursor Settings Health",
		Status:  StatusOK,
		Message: fmt.Sprintf("%d hooks file(s) valid", checked),
	}
}

// CheckCursorHookCompleteness verifies that when bd-managed Cursor hooks are
// installed, all three lifecycle events are present. bd's compaction recovery
// needs sessionStart (prime on start/after compaction), preCompact (arm
// recovery), and postToolUse (re-inject after compaction); a partial install
// (e.g. a hand-edited hooks.json) silently degrades recovery. Mirrors
// CheckClaudeHookCompleteness. repoPath is the project root directory.
func CheckCursorHookCompleteness(repoPath string) DoctorCheck {
	paths := []string{filepath.Join(repoPath, ".cursor", "hooks.json")}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".cursor", "hooks.json"))
	}

	present := map[string]bool{}
	for _, p := range paths {
		for _, ev := range cursorBeadsHookEvents(p) {
			present[ev] = true
		}
	}

	if len(present) == 0 {
		return DoctorCheck{
			Name:    "Cursor Hook Completeness",
			Status:  StatusOK,
			Message: "N/A (no hooks installed)",
		}
	}

	var missing []string
	for _, ev := range setup.CursorManagedHookEventNames() {
		if !present[ev] {
			missing = append(missing, ev)
		}
	}
	if len(missing) == 0 {
		return DoctorCheck{
			Name:    "Cursor Hook Completeness",
			Status:  StatusOK,
			Message: "All recovery hooks present (sessionStart, preCompact, postToolUse)",
		}
	}
	return DoctorCheck{
		Name:    "Cursor Hook Completeness",
		Status:  StatusWarning,
		Message: "Missing hook event(s): " + strings.Join(missing, ", "),
		Detail:  "sessionStart primes context; preCompact + postToolUse recover beads context after a compaction.",
		Fix:     "Run 'bd setup cursor' to reinstall the full hook set.",
	}
}

// isCursorPresent returns true when Cursor appears to be in use: running inside
// a Cursor hook/agent, the cursor CLI is on PATH, a project .cursor/ dir exists,
// or the user has a ~/.cursor/ directory.
func isCursorPresent(repoPath string) bool {
	// Set by Cursor when running hooks/agents (see Cursor hooks env vars).
	if os.Getenv("CURSOR_PROJECT_DIR") != "" || os.Getenv("CURSOR_TRACE_ID") != "" {
		return true
	}
	for _, bin := range []string{"cursor-agent", "cursor"} {
		if _, err := exec.LookPath(bin); err == nil {
			return true
		}
	}
	if info, err := os.Stat(filepath.Join(repoPath, ".cursor")); err == nil && info.IsDir() {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		if info, err := os.Stat(filepath.Join(home, ".cursor")); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}
