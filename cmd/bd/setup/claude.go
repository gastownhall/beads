package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/templates/agents"
)

var (
	claudeEnvProvider     = defaultClaudeEnv
	errClaudeHooksMissing = errors.New("claude hooks not installed")
)

const claudeInstructionsFile = "CLAUDE.md"

var claudeAgentsIntegration = agentsIntegration{
	name:         "Claude Code",
	setupCommand: "bd setup claude",
	profile:      agents.ProfileMinimal,
}

// claudeGlobalAgentsIntegration is claudeAgentsIntegration for the user-level
// instructions file, so its hints name the command that maintains that file.
var claudeGlobalAgentsIntegration = agentsIntegration{
	name:         "Claude Code",
	setupCommand: "bd setup claude --global",
	profile:      agents.ProfileMinimal,
}

type claudeEnv struct {
	stdout     io.Writer
	stderr     io.Writer
	homeDir    string
	projectDir string
	// managedDir is the system directory Claude Code reads file-based
	// managed settings from; "" reads none.
	managedDir string
	// localSettingsRoot is the directory whose .claude/settings.local.json Claude Code
	// reads as the local settings of a session started in projectDir (see
	// claudeLocalSettingsRoot); "" when Claude Code keeps that file beside
	// projectDir's .claude/settings.json.
	localSettingsRoot string
	ensureDir         func(string, os.FileMode) error
	readFile          func(string) ([]byte, error)
	writeFile         func(string, []byte) error
}

func defaultClaudeEnv() (claudeEnv, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return claudeEnv{}, fmt.Errorf("home directory: %w", err)
	}
	workDir, err := os.Getwd()
	if err != nil {
		return claudeEnv{}, fmt.Errorf("working directory: %w", err)
	}
	return claudeEnvIn(home, workDir), nil
}

// claudeEnvIn is the environment of a Claude Code session started in
// workDir, an absolute path, for the user whose home directory is home.
func claudeEnvIn(home, workDir string) claudeEnv {
	return claudeEnv{
		stdout:            os.Stdout,
		stderr:            os.Stderr,
		homeDir:           home,
		projectDir:        workDir,
		managedDir:        claudeManagedDir(),
		localSettingsRoot: claudeLocalSettingsRoot(home, workDir),
		ensureDir:         EnsureDir,
		readFile:          os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
	}
}

// claudeManagedDir returns the directory bd reads Claude Code's file-based
// managed settings from: claudeManagedSettingsDir, unless a test has pointed
// it elsewhere with SetClaudeManagedSettingsDirForTest.
var claudeManagedDir = claudeManagedSettingsDir

// SetClaudeManagedSettingsDirForTest makes bd read Claude Code's managed
// settings from dir ("" reads none) until the returned restore runs. It is
// for tests outside this package that resolve the beads plugin through it,
// bd doctor's and the setup tip's, so they neither see the host's policy nor
// need to write a system directory. It is process-global: a test that calls
// it must not run in parallel with one that reads Claude Code's settings.
func SetClaudeManagedSettingsDirForTest(dir string) (restore func()) {
	prev := claudeManagedDir
	claudeManagedDir = func() string { return dir }
	return func() { claudeManagedDir = prev }
}

// claudeManagedSettingsDir is the system directory that holds Claude Code's
// file-based managed settings, managed-settings.json and managed-settings.d/.
func claudeManagedSettingsDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	default:
		return "/etc/claude-code"
	}
}

// claudeLocalSettingsRoot returns the directory whose
// .claude/settings.local.json Claude Code (v2.1.211 and later) reads as the
// local settings of a session started in workDir: the git repository's root,
// or the main checkout's root in a linked worktree. It is "" where Claude
// Code keeps that file beside .claude/settings.json instead: outside a git
// repository, on Windows, and when the root is the home directory. Claude
// Code also keeps it there when another user owns the root, its .git or its
// .claude; git refuses such a repository unless safe.directory allows it, and
// bd does not check further.
func claudeLocalSettingsRoot(home, workDir string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	root, err := claudeMainRepoRoot(workDir)
	if err != nil || root == "" || sameDir(root, home) {
		return ""
	}
	return root
}

// claudeMainRepoRoot returns the main checkout's root of workDir's git
// repository (git.GetMainRepoRoot). In the process's working directory it
// reads the cached git context, as bd setup claude does there, and returns ""
// when that context is another directory's. Any other directory, such as the
// one bd doctor <path> checks, resolves its own repository, which need not be
// the working directory's: a separate checkout, or a repository nested in it.
func claudeMainRepoRoot(workDir string) (string, error) {
	if wd, err := os.Getwd(); err != nil || !sameDir(workDir, wd) {
		return git.GetMainRepoRootFrom(workDir)
	}
	if repoRoot := git.GetRepoRoot(); repoRoot == "" || !dirWithin(workDir, repoRoot) {
		return "", nil
	}
	return git.GetMainRepoRoot()
}

// dirWithin reports whether dir is root or lies below it, comparing
// directories rather than path strings, as sameDir does. It resolves dir's
// symlinks first, so a link into the root counts as within it.
func dirWithin(dir, root string) bool {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	for d := filepath.Clean(dir); ; {
		if sameDir(d, root) {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
}

func projectSettingsPath(base string) string {
	return filepath.Join(base, ".claude", "settings.json")
}

func legacyProjectSettingsPath(base string) string {
	return filepath.Join(base, ".claude", "settings.local.json")
}

func globalSettingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// globalClaudeInstructionsPath is Claude Code's user-level instructions file,
// which it loads in every project. A global install keeps its managed beads
// section there, beside the ~/.claude/settings.json its hook goes in.
func globalClaudeInstructionsPath(home string) string {
	return filepath.Join(home, ".claude", claudeInstructionsFile)
}

// marshalSettings renders a Claude settings map as two-space-indented JSON
// with the trailing newline json.MarshalIndent omits, so the file stays
// POSIX-clean and byte-stable across runs.
func marshalSettings(settings map[string]interface{}) ([]byte, error) {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// writeSettingsIfChanged writes data to path only when it differs from what is
// already on disk. GH#5693: bd init / bd setup claude marshaled and wrote
// settings.json on every run, so a no-op run still churned the file's mtime
// and — because MarshalIndent emits no trailing newline — silently stripped it.
func writeSettingsIfChanged(env claudeEnv, path string, data []byte) error {
	if existing, err := env.readFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	return env.writeFile(path, data)
}

func claudeAgentsEnv(env claudeEnv) agentsEnv {
	ae, _ := claudeAgentsEnvRedirect(env)
	return ae
}

// claudeAgentsEnvRedirect is claudeAgentsEnv plus a bool reporting whether the
// AGENTS.md redirect activated, so callers that need to clean up a stale
// CLAUDE.md block (installClaude, removeClaude) can tell the redirected case
// apart from the plain CLAUDE.md-is-authoritative case.
func claudeAgentsEnvRedirect(env claudeEnv) (agentsEnv, bool) {
	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)

	// If CLAUDE.md is a thin stub that imports AGENTS.md via the @-include
	// convention (Claude Code expands @-imports), redirect the managed beads
	// section to AGENTS.md instead of duplicating it in the stub. This matches
	// the shared-authoritative-file pattern used by repos that keep AGENTS.md
	// as the single source of agent instructions.
	agentsFile := config.SafeAgentsFile()
	agentsPath := filepath.Join(env.projectDir, agentsFile)
	if data, err := env.readFile(claudePath); err == nil {
		if isAgentsImportStub(string(data), agentsFile) {
			if _, err := env.readFile(agentsPath); err == nil {
				return agentsEnv{
					agentsPath: agentsPath,
					stdout:     env.stdout,
					stderr:     env.stderr,
				}, true
			}
		}
	}

	return agentsEnv{
		agentsPath: claudePath,
		stdout:     env.stdout,
		stderr:     env.stderr,
	}, false
}

// claudeInstructionsEnv resolves the file an install or removal manages the
// beads section in, with the integration whose hints name its command. A
// global install uses the user-level ~/.claude/CLAUDE.md and leaves the
// current directory's files alone. It renders that file with fixed options:
// every project shares it, so it must not follow one project's config, and
// Claude Code loads it in projects that do not use beads too, so its section
// applies only in those that do. A project install uses the project's
// CLAUDE.md, or AGENTS.md when the bool reports the claudeAgentsEnvRedirect
// redirect.
func claudeInstructionsEnv(env claudeEnv, global bool) (agentsEnv, agentsIntegration, bool) {
	if !global {
		ae, redirected := claudeAgentsEnvRedirect(env)
		return ae, claudeAgentsIntegration, redirected
	}
	return agentsEnv{
		agentsPath:  globalClaudeInstructionsPath(env.homeDir),
		stdout:      env.stdout,
		stderr:      env.stderr,
		sectionOnly: true,
		renderOpts:  agents.UserLevelRenderOpts,
	}, claudeGlobalAgentsIntegration, false
}

// claudeCheckAgentsEnv picks the instructions file --check reports on. Run
// from ~/.claude, the project's CLAUDE.md is the user-level file, so it is
// judged as one. Otherwise it is the project's file, unless the hooks are
// user-level (a global install's, or those of a plugin enabled in the managed
// or user settings) and the project's file has no beads section. Then it is the
// ~/.claude/CLAUDE.md a global install writes, which Claude Code loads
// alongside the project's own instructions, if that has a section, if the
// project's file does not exist, or if the project is the home directory:
// there a project install's hint would create the ~/CLAUDE.md a global install
// exists to avoid. A project install with project hooks, or with the plugin
// enabled only in the project's settings, is judged on the project's file
// alone.
func claudeCheckAgentsEnv(env claudeEnv, userLevelHooks bool) (agentsEnv, agentsIntegration) {
	globalEnv, globalIntegration, _ := claudeInstructionsEnv(env, true)
	if claudeProjectIsUserLevelDir(env) {
		return globalEnv, globalIntegration
	}
	projectEnv := claudeAgentsEnv(env)
	if !userLevelHooks || claudeFileHasBeadsSection(env, projectEnv.agentsPath) {
		return projectEnv, claudeAgentsIntegration
	}
	if claudeFileHasBeadsSection(env, globalEnv.agentsPath) || claudeProjectIsHome(env) {
		return globalEnv, globalIntegration
	}
	if _, err := env.readFile(projectEnv.agentsPath); err != nil {
		return globalEnv, globalIntegration
	}
	return projectEnv, claudeAgentsIntegration
}

// claudeProjectIsHome reports whether the current directory is the home
// directory, where the project's .claude/settings.json is the global one.
func claudeProjectIsHome(env claudeEnv) bool {
	return sameDir(env.projectDir, env.homeDir)
}

// claudeProjectIsUserLevelDir reports whether the current directory is
// ~/.claude, where the project's CLAUDE.md is the user-level one.
func claudeProjectIsUserLevelDir(env claudeEnv) bool {
	return sameDir(env.projectDir, filepath.Dir(globalClaudeInstructionsPath(env.homeDir)))
}

// sameDir reports whether a and b are the same directory, following
// symlinks; it compares cleaned paths when either cannot be stat'd.
func sameDir(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(ai, bi)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func claudeFileHasBeadsSection(env claudeEnv, path string) bool {
	data, err := env.readFile(path)
	return err == nil && containsBeadsMarker(string(data))
}

// stripStaleClaudeBlock removes a beads-managed block left behind in CLAUDE.md
// by an older bd version, once the AGENTS.md redirect is active. Older bd
// releases wrote the managed block directly into CLAUDE.md; a project that has
// since adopted the "@AGENTS.md" import-stub pattern would otherwise carry a
// stale duplicate of that block alongside the one now maintained in AGENTS.md.
func stripStaleClaudeBlock(env claudeEnv) error {
	claudePath := filepath.Join(env.projectDir, claudeInstructionsFile)
	data, err := env.readFile(claudePath)
	if err != nil {
		return nil
	}

	content := string(data)
	if !containsBeadsMarker(content) {
		return nil
	}

	newContent := removeBeadsSection(content)
	if err := env.writeFile(claudePath, []byte(newContent)); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Removed stale beads block from %s (now redirected to %s)\n", claudeInstructionsFile, config.SafeAgentsFile())
	return nil
}

// isAgentsImportStub reports whether content contains an @-include directive
// for the given agents file (e.g. "@AGENTS.md" on its own line), indicating
// the file is a thin stub that imports shared agent instructions from the
// agents file rather than carrying its own content.
//
// Directives inside fenced code blocks do not count. Claude Code does not expand
// an @-import that is shown as code, so a file that merely documents the pattern
// is not a stub — and treating it as one is not a cosmetic misread: the caller
// redirects the managed block to AGENTS.md and then stripStaleClaudeBlock deletes
// the block that was in CLAUDE.md. A fenced example would silently relocate
// content out of the file that was, in fact, authoritative.
//
// Only fences are skipped, not four-space-indented blocks. An indented line is
// ambiguous in a way a fence is not — it is equally the continuation of a list
// item, which is a plausible place to put a real directive — so treating
// indentation as code would trade this false positive for a false negative.
func isAgentsImportStub(content, agentsFile string) bool {
	directives := []string{"@" + agentsFile, "@./" + agentsFile}
	fence := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)

		if marker := codeFenceMarker(trimmed); marker != "" {
			switch {
			case fence == "":
				fence = marker
			case marker == fence:
				// A closing fence must match the character the block opened
				// with, so ``` inside a ~~~ block does not end it.
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}

		for _, directive := range directives {
			if trimmed == directive {
				return true
			}
		}
	}
	return false
}

// codeFenceMarker returns the fence character ("`" or "~") if the already-trimmed
// line opens or closes a fenced code block, and "" otherwise. CommonMark requires
// at least three of the same character; an info string ("```go") may follow.
func codeFenceMarker(trimmed string) string {
	for _, ch := range []string{"`", "~"} {
		if strings.HasPrefix(trimmed, strings.Repeat(ch, 3)) {
			return ch
		}
	}
	return ""
}

func InstallClaude(global bool, stealth bool) error {
	env, err := claudeEnvProvider()
	if err != nil {
		return HandleError("%v", err)
	}
	return installClaude(env, global, stealth)
}

// InstallClaudeProject installs project-local Claude hooks, returning an error
// instead of exiting. Used by bd init to integrate Claude setup automatically.
func InstallClaudeProject(stealth bool) error {
	return InstallClaudeProjectTo(stealth, ProjectInstallOutput{})
}

// InstallClaudeProjectTo is InstallClaudeProject reporting to out.
func InstallClaudeProjectTo(stealth bool, out ProjectInstallOutput) error {
	env, err := claudeEnvProvider()
	if err != nil {
		return err
	}
	env.stdout, env.stderr = out.stdout(), out.stderr()
	return installClaude(env, false, stealth)
}

func installClaude(env claudeEnv, global bool, stealth bool) error {
	var settingsPath string
	if global {
		settingsPath = globalSettingsPath(env.homeDir)
		_, _ = fmt.Fprintln(env.stdout, "Installing Claude hooks globally...")
	} else {
		settingsPath = projectSettingsPath(env.projectDir)
		_, _ = fmt.Fprintln(env.stdout, "Installing Claude hooks for this project...")
	}

	// ~/.claude also holds the user-level CLAUDE.md a global install writes
	// below, whether or not it writes a hook. A project install creates
	// .claude only to write a settings file there.
	if global {
		if err := env.ensureDir(filepath.Dir(settingsPath), 0o755); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
			return err
		}
	}

	settings := make(map[string]interface{})
	settingsExisted := false
	if data, err := env.readFile(settingsPath); err == nil {
		settingsExisted = true
		if err := json.Unmarshal(data, &settings); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: failed to parse settings.json: %v\n", err)
			return err
		}
	} else if !os.IsNotExist(err) {
		// Only a missing file starts from empty settings. One that cannot be
		// read would be replaced by a file holding only bd's hook: the rename
		// that replaces it needs no read access to the file.
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", settingsPath, err)
		return err
	}

	hooks, hadHooks := settings["hooks"].(map[string]interface{})
	if !hadHooks {
		hooks = make(map[string]interface{})
	}

	// GH#955: Clean up any null values left by previous buggy removal
	// Claude Code expects arrays, not null values
	for key, val := range hooks {
		if val == nil {
			delete(hooks, key)
		}
	}

	command := "bd prime --hook-json"
	if stealth {
		command = "bd prime --stealth --hook-json"
	}

	// Migration sweep: remove bare "bd prime" variants registered by older
	// installations. Claude Code's current context-injection contract uses
	// the SessionStart JSON envelope; SessionStart also fires after compaction
	// with source=compact, so PreCompact no longer needs a bd prime hook.
	legacyBareVariants := []string{"bd prime", "bd prime --stealth"}
	sweptSessionStart := false
	for _, legacy := range legacyBareVariants {
		if legacy == command {
			continue
		}
		if removeHookCommand(env.stdout, hooks, "SessionStart", legacy) {
			sweptSessionStart = true
		}
		removeHookCommand(env.stdout, hooks, "PreCompact", legacy)
	}
	removeHookCommand(env.stdout, hooks, "PreCompact", "bd prime --hook-json")
	removeHookCommand(env.stdout, hooks, "PreCompact", "bd prime --stealth --hook-json")

	// GH#3192: Skip writing hooks if the beads plugin is already providing them,
	// so project-level hooks don't fire bd prime twice per session.
	plugin := beadsPlugin(env, global)
	// Whether teammates get the plugin is decided by the settings they
	// share, not by the file that decided it for this user. From the home
	// directory the project's settings are ~/.claude/settings.json, which
	// every other project loads, so those projects take the teammates'
	// place. From ~/.claude the project's settings are the user's own.
	sharedProject := !global && !claudeProjectIsUserLevelDir(env)
	atHome := sharedProject && claudeProjectIsHome(env)
	var shared beadsPluginState
	rootHook := ""
	if sharedProject {
		shared = sharedBeadsPlugin(env)
		rootHook = rootLocalPrimeHook(env)
	}
	// keepLocal keeps a bd prime hook in this directory's local settings,
	// since none is added in its place; pluginManaged removes it, since the
	// plugin runs bd prime here instead.
	offForYou, keepLocal, pluginManaged := false, false, false
	switch {
	case plugin.enabled && sharedProject && sweptSessionStart && !shared.enabled:
		// The plugin is enabled for this user alone, and the shared project
		// settings had an older bd prime hook that teammates without the
		// plugin rely on. Replace it rather than drop it; the double run
		// this leaves for this user is reported below.
		if addHookCommand(hooks, "SessionStart", command) {
			needed := "teammates without it still need the hook"
			if atHome {
				needed = "your other projects still need the hook"
			}
			_, _ = fmt.Fprintf(env.stdout, "✓ Registered SessionStart hook in place of the old one: the beads plugin is enabled only in %s, and %s\n", plugin.path, needed)
		}
	case plugin.enabled:
		pluginManaged = true
		_, _ = fmt.Fprintf(env.stdout, "✓ Beads plugin enabled in %s — hooks are plugin-managed, skipping\n", plugin.path)
	case shared.enabled:
		// Only this user's own settings turn off a plugin the shared
		// settings enable. A hook in the shared file would make every
		// teammate with the plugin run bd prime twice (GH#3192), so none
		// is added and a local hook stays; this is reported below, once the
		// settings are written.
		offForYou, keepLocal = true, true
	case rootHook != "":
		// Claude Code loads the repository root's local settings in every
		// session started in the repository, so their hook, typically the
		// bare bd prime an older install wrote there, already runs bd prime
		// here, and a second hook in this directory's settings would run
		// beside it. A hook in this directory's local settings stays: when
		// it runs a different command from the root's, the double-run
		// warning below names the one to remove.
		keepLocal = true
		_, _ = fmt.Fprintf(env.stdout, "✓ bd prime already runs here from the hook in %s — skipping\n", rootHook)
	default:
		if addHookCommand(hooks, "SessionStart", command) {
			_, _ = fmt.Fprintln(env.stdout, "✓ Registered SessionStart hook")
		} else {
			_, _ = fmt.Fprintln(env.stdout, "✓ Hook already registered: SessionStart")
		}
	}

	// An install that leaves the hooks to the plugin adds no empty "hooks"
	// object, and creates no settings file with nothing in it.
	if hadHooks || len(hooks) > 0 {
		settings["hooks"] = hooks
	}
	hasSettingsFile := settingsExisted || len(settings) > 0
	if hasSettingsFile {
		data, err := marshalSettings(settings)
		if err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: marshal settings: %v\n", err)
			return err
		}
		if err := env.ensureDir(filepath.Dir(settingsPath), 0o755); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
			return err
		}

		if err := writeSettingsIfChanged(env, settingsPath, data); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: write settings: %v\n", err)
			return err
		}
	}

	// Migrate legacy hooks: remove beads hooks from settings.local.json if
	// present. The settings written above now run bd prime here, or the
	// plugin does (pluginManaged); when neither does, the hook stays
	// (keepLocal).
	if !global && !keepLocal {
		legacyPath := legacyProjectSettingsPath(env.projectDir)
		if removeLocalClaudeHooks(env, legacyPath) {
			if pluginManaged {
				_, _ = fmt.Fprintf(env.stdout, "✓ Removed bd prime hooks from %s: the beads plugin runs bd prime here\n", legacyPath)
			} else {
				_, _ = fmt.Fprintf(env.stdout, "✓ Migrated hooks from %s\n", legacyPath)
			}
		}
	}

	if offForYou {
		if path := primeHookHere(env); path != "" {
			_, _ = fmt.Fprintf(env.stdout, "✓ The beads plugin is off for you (%s); bd prime runs here from the hook in %s\n", plugin.path, path)
		} else {
			reportPluginOffForYou(env, plugin, shared, "⚠ ")
		}
	}
	warnIfPrimeRunsTwice(env)
	if global && plugin.enabled {
		warnIfPluginOffHere(env)
	}

	// Install minimal beads section in CLAUDE.md.
	// Hooks handle the heavy lifting via bd prime; CLAUDE.md just needs a pointer.
	agentsEnv, integration, redirected := claudeInstructionsEnv(env, global)
	agentsSkipped := false
	agentsEnv.skipped = &agentsSkipped
	agentsErr := installAgents(agentsEnv, integration)
	if agentsErr != nil {
		// Non-fatal: hooks are already installed
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", agentsFileName(agentsEnv.agentsPath), agentsErr)
	}

	// Only strip the stale CLAUDE.md block once AGENTS.md holds the current
	// section (written now or already current). If installAgents skipped
	// injection (e.g. AGENTS.md is a symlink) or failed (e.g. its markers are
	// malformed), stripping here would delete-before-write and leave the
	// project with no beads section anywhere.
	if redirected && agentsErr == nil && !agentsSkipped {
		if err := stripStaleClaudeBlock(env); err != nil {
			// Non-fatal: the redirect itself already succeeded above
			_, _ = fmt.Fprintf(env.stderr, "Warning: failed to clean stale beads block from %s: %v\n", claudeInstructionsFile, err)
		}
	}

	if agentsSkipped {
		_, _ = fmt.Fprintln(env.stdout, "\n✓ Claude Code hooks installed")
		_, _ = fmt.Fprintf(env.stdout, "  Agent instructions skipped: %s is a symlink\n", claudeInstructionsFile)
	} else {
		_, _ = fmt.Fprintln(env.stdout, "\n✓ Claude Code integration installed")
	}
	if hasSettingsFile {
		_, _ = fmt.Fprintf(env.stdout, "  Settings: %s\n", settingsPath)
	}
	_, _ = fmt.Fprintln(env.stdout, "\nRestart Claude Code for changes to take effect.")
	return nil
}

// claudeSettingsUsesRemovedSyncCommand reports whether any hook command references
// bd sync (removed as a real command; GH#3546).
func claudeSettingsUsesRemovedSyncCommand(data []byte) bool {
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}
	for _, raw := range hooks {
		eventHooks, ok := raw.([]interface{})
		if !ok {
			continue
		}
		for _, hook := range eventHooks {
			hookMap, ok := hook.(map[string]interface{})
			if !ok {
				continue
			}
			cmds, ok := hookMap["hooks"].([]interface{})
			if !ok {
				continue
			}
			for _, c := range cmds {
				cmdMap, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				command, _ := cmdMap["command"].(string)
				if strings.Contains(command, "bd sync") {
					return true
				}
			}
		}
	}
	return false
}

// warnIfClaudeHooksUseRemovedSync warns about each settings file Claude Code
// loads hooks from here, the project's (claudeProjectHookPaths) and the
// global one, that has a hook running the removed bd sync.
func warnIfClaudeHooksUseRemovedSync(env claudeEnv) {
	for _, p := range append(claudeProjectHookPaths(env), globalSettingsPath(env.homeDir)) {
		data, err := env.readFile(p)
		if err != nil {
			continue
		}
		if !claudeSettingsUsesRemovedSyncCommand(data) {
			continue
		}
		_, _ = fmt.Fprintf(env.stderr, "Warning: %s contains a hook using removed \"bd sync\". Run bd setup claude to refresh hooks (bd prime / bd dolt push), or edit settings manually.\n", p)
	}
}

func CheckClaude() error {
	env, err := claudeEnvProvider()
	if err != nil {
		return HandleError("%v", err)
	}
	return checkClaude(env)
}

func checkClaude(env claudeEnv) error {
	warnIfClaudeHooksUseRemovedSync(env)

	projectSettings := projectSettingsPath(env.projectDir)
	globalSettings := globalSettingsPath(env.homeDir)
	legacySettings := legacyProjectSettingsPath(env.projectDir)

	// From the home directory the project's settings are the global ones,
	// so hooks there are user-level and reported as global.
	atHome := claudeProjectIsHome(env)
	userLevelDir := atHome || claudeProjectIsUserLevelDir(env)
	plugin := beadsPlugin(env, false)
	// The local settings files Claude Code loads hooks from here: the
	// current directory's and the repository root's.
	localHook := ""
	for _, path := range claudeProjectHookPaths(env)[1:] {
		if hasBeadsHooks(path) {
			localHook = path
			break
		}
	}
	userLevelHooks := false
	switch {
	case !atHome && hasBeadsHooks(projectSettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Project hooks installed: %s\n", projectSettings)
	case hasBeadsHooks(globalSettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Global hooks installed: %s\n", globalSettings)
		userLevelHooks = true
	case localHook != "":
		_, _ = fmt.Fprintf(env.stdout, "✓ Project hooks installed (legacy): %s\n", localHook)
		// A project install moves only the current directory's file, and
		// only when it adds a hook in its place: with the plugin on it
		// removes the hook, which the double-run warning below names, and it
		// keeps the hook when the plugin is off for this user alone or the
		// repository root's local hook runs bd prime here.
		keepsLocal := !claudeProjectIsUserLevelDir(env) && (sharedBeadsPlugin(env).enabled || rootLocalPrimeHook(env) != "")
		if localHook == legacySettings && !plugin.enabled && !keepsLocal {
			_, _ = fmt.Fprintf(env.stdout, "  Consider running 'bd setup claude' to migrate to .claude/settings.json\n")
		}
	case plugin.enabled:
		// GH#3192: Plugin provides hooks via plugin.json — no project-level hooks needed
		_, _ = fmt.Fprintf(env.stdout, "✓ Hooks provided by beads plugin (plugin-managed): %s\n", plugin.path)
		// Its hooks are user-level only when the managed or user settings
		// enable it (or, as above, from the home directory): enabled in this
		// project's settings alone, they run here only.
		userLevelHooks = atHome || hasBeadsPlugin(env, true)
	default:
		_, _ = fmt.Fprintln(env.stdout, "✗ No hooks installed")
		if shared := sharedBeadsPlugin(env); shared.enabled && !userLevelDir {
			// A project install would not add a hook to the shared
			// settings that enable the plugin for everyone.
			reportPluginOffForYou(env, plugin, shared, "  ")
			return errClaudeHooksMissing
		}
		if userLevelDir && beadsPlugin(env, true).enabled {
			// A global install would leave the hooks to the plugin the
			// managed or user settings enable, which settings here turn off.
			_, _ = fmt.Fprintf(env.stdout, "  To run bd prime here: enable the plugin in %s\n", plugin.path)
		} else if userLevelDir {
			// A project install from $HOME would create the ~/CLAUDE.md a
			// global install exists to avoid; one from ~/.claude would put
			// the project wording, and scaffolding, in the user-level
			// ~/.claude/CLAUDE.md.
			_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup claude --global")
		} else {
			_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup claude")
		}
		return errClaudeHooksMissing
	}

	warnIfPrimeRunsTwice(env)

	agentsEnv, integration := claudeCheckAgentsEnv(env, userLevelHooks)
	return checkAgents(agentsEnv, integration)
}

// warnIfPrimeRunsTwice reports a bd prime hook in a settings file Claude
// Code loads in this directory while the beads plugin is enabled here too.
// Claude Code runs a plugin's hook beside a settings file's, even an
// identical one, so bd prime runs twice per session (GH#3192). A global
// install from a project whose own settings enable the plugin leaves it so:
// its hook is for every other project, where the plugin does not run. With
// the plugin off here, it reports two project settings hooks that differ
// (warnIfSettingsHooksRunPrimeTwice).
func warnIfPrimeRunsTwice(env claudeEnv) {
	plugin := beadsPlugin(env, false)
	if !plugin.enabled {
		warnIfSettingsHooksRunPrimeTwice(env)
		return
	}
	for _, path := range claudeHookSettingsPaths(env) {
		if hasBeadsHooks(path) {
			_, _ = fmt.Fprintln(env.stdout, "⚠ bd prime runs twice per session here")
			_, _ = fmt.Fprintf(env.stdout, "  Both the beads plugin (enabled in %s) and the hook in %s run it\n", plugin.path, path)
			_, _ = fmt.Fprintf(env.stdout, "  To run it once: %s\n", primeTwiceRemedy(env, path))
			return
		}
	}
}

// warnIfSettingsHooksRunPrimeTwice reports two of the project's settings
// files Claude Code loads hooks from here (claudeProjectHookPaths) whose bd
// prime hooks differ, such as an older bare bd prime in the repository
// root's local settings beside a project install's current hook. Claude Code
// runs a hook defined in several settings files once, but each different one
// runs. The remedy keeps the project's shared hook, which teammates rely on,
// and, of the two local files, the repository root's, which Claude Code
// v2.1.211 and later uses, over one an older version left in this directory.
func warnIfSettingsHooksRunPrimeTwice(env claudeEnv) {
	first := ""
	seen := map[string]bool{}
	for _, path := range claudeProjectHookPaths(env) {
		commands := primeHookCommands(path)
		for _, command := range commands {
			if first != "" && !seen[command] {
				extra := path
				if first == legacyProjectSettingsPath(env.projectDir) {
					extra = first
				}
				_, _ = fmt.Fprintln(env.stdout, "⚠ bd prime runs twice per session here")
				_, _ = fmt.Fprintf(env.stdout, "  The hooks in %s and %s run different bd prime commands, and Claude Code runs both\n", first, path)
				_, _ = fmt.Fprintf(env.stdout, "  To run it once: remove the bd prime hook from %s\n", extra)
				return
			}
		}
		for _, command := range commands {
			seen[command] = true
		}
		if first == "" && len(commands) > 0 {
			first = path
		}
	}
}

// primeTwiceRemedy is how to make bd prime run once where both the beads
// plugin and the hook in hookPath run it. A global hook is for every
// project, so it goes only once the user settings enable the plugin.
// Whether a project hook may go is decided by the shared (managed and
// project) settings: when they enable the plugin it runs for every
// teammate, so the hook goes, wherever it is, and when they do not,
// teammates without the plugin rely on a shared hook, so this user turns
// the plugin off instead.
func primeTwiceRemedy(env claudeEnv, hookPath string) string {
	localRoot := env.projectDir
	if env.localSettingsRoot != "" {
		localRoot = env.localSettingsRoot
	}
	turnOff := "turn the plugin off in " + legacyProjectSettingsPath(localRoot)
	project := projectSettingsPath(env.projectDir)
	shared := sharedBeadsPlugin(env)
	switch {
	case hookPath == globalSettingsPath(env.homeDir) && hasBeadsPlugin(env, true):
		return "run bd setup claude --remove --global, then bd setup claude --global"
	case hookPath == globalSettingsPath(env.homeDir):
		return turnOff + ", or enable it in " + globalSettingsPath(env.homeDir) +
			" and run bd setup claude --remove --global, then bd setup claude --global"
	case shared.enabled:
		return "remove the bd prime hook from " + hookPath
	case hookPath == project:
		return turnOff
	default:
		return "remove the bd prime hook from " + hookPath + ", or " + turnOff
	}
}

// reportPluginOffForYou reports that only this user's own settings (plugin,
// resolved for this user) turn off a beads plugin the shared settings
// (shared) enable, so no hook goes in the shared settings, and how this user
// can still have bd prime run here. prefix starts the first line. From the
// home directory the shared settings are ~/.claude/settings.json, which
// turns the plugin on in every other project rather than for teammates.
func reportPluginOffForYou(env claudeEnv, plugin, shared beadsPluginState, prefix string) {
	if claudeProjectIsHome(env) {
		_, _ = fmt.Fprintf(env.stdout, "%sThe beads plugin is off for you (%s) but on in every project through %s, so bd adds no hook there: those projects would run bd prime twice\n", prefix, plugin.path, shared.path)
	} else {
		_, _ = fmt.Fprintf(env.stdout, "%sThe beads plugin is off for you (%s) but on for everyone in %s, so bd adds no hook there: teammates with the plugin would run bd prime twice\n", prefix, plugin.path, shared.path)
	}
	remedy := "enable the plugin in " + plugin.path
	if !hasBeadsPlugin(env, true) {
		remedy += ", or run bd setup claude --global"
	}
	_, _ = fmt.Fprintf(env.stdout, "  To run bd prime here: %s\n", remedy)
}

// warnIfPluginOffHere reports a global install that left the hooks to a
// beads plugin the managed or user settings enable, run from a project whose
// own settings turn it off. Unless a project hook or an older global one
// runs bd prime there, that project needs its own install; when only this
// user's local settings turn off a plugin the shared settings enable, it
// needs the plugin turned back on instead, since a hook in the shared
// settings would run twice for teammates. From the home directory or
// ~/.claude a project install is the global one, so it is not suggested.
func warnIfPluginOffHere(env claudeEnv) {
	if claudeProjectIsHome(env) || claudeProjectIsUserLevelDir(env) {
		return
	}
	here := beadsPlugin(env, false)
	if here.enabled {
		return
	}
	if primeHookHere(env) != "" {
		return
	}
	if shared := sharedBeadsPlugin(env); shared.enabled {
		reportPluginOffForYou(env, here, shared, "⚠ ")
		return
	}
	_, _ = fmt.Fprintf(env.stdout, "⚠ The beads plugin is off in this project (%s), so no hook runs bd prime here\n", here.path)
	_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup claude")
}

// primeHookHere returns the first settings file Claude Code loads in this
// directory that has a bd prime hook: a project file or the global one. It
// returns "" when none has.
func primeHookHere(env claudeEnv) string {
	for _, path := range claudeHookSettingsPaths(env) {
		if hasBeadsHooks(path) {
			return path
		}
	}
	return ""
}

// claudeHookSettingsPaths returns the settings files Claude Code loads hooks
// from in this directory: the project's (claudeProjectHookPaths), then the
// user's ~/.claude/settings.json.
func claudeHookSettingsPaths(env claudeEnv) []string {
	return append(claudeProjectHookPaths(env), globalSettingsPath(env.homeDir))
}

// claudeProjectHookPaths returns the project's settings files Claude Code
// loads hooks from in this directory: .claude/settings.json, the local
// settings beside it and the local settings at the repository root.
func claudeProjectHookPaths(env claudeEnv) []string {
	paths := []string{projectSettingsPath(env.projectDir), legacyProjectSettingsPath(env.projectDir)}
	if root := rootLocalSettingsPath(env); root != "" {
		paths = append(paths, root)
	}
	return paths
}

// rootLocalSettingsPath returns the repository root's
// .claude/settings.local.json when Claude Code reads it here as well as the
// one beside .claude/settings.json (see claudeLocalSettingsRoot), and ""
// when they are the same file or Claude Code reads no root file.
func rootLocalSettingsPath(env claudeEnv) string {
	if env.localSettingsRoot == "" || sameDir(env.localSettingsRoot, env.projectDir) {
		return ""
	}
	return legacyProjectSettingsPath(env.localSettingsRoot)
}

// rootLocalPrimeHook returns the repository root's local settings
// (rootLocalSettingsPath) when they have a bd prime hook and the project's
// .claude/settings.json has none, and "" otherwise. Claude Code loads that
// file in every session started in the repository, so from a subdirectory
// or linked worktree its hook already runs bd prime and a project install
// adds none of its own. A hook already in the project's settings stays, or
// is replaced when it is an older bare one: teammates rely on it.
func rootLocalPrimeHook(env claudeEnv) string {
	root := rootLocalSettingsPath(env)
	if root == "" || !hasBeadsHooks(root) || hasBeadsHooks(projectSettingsPath(env.projectDir)) {
		return ""
	}
	return root
}

func RemoveClaude(global bool) error {
	env, err := claudeEnvProvider()
	if err != nil {
		return HandleError("%v", err)
	}
	return removeClaude(env, global)
}

func removeClaude(env claudeEnv, global bool) error {
	var settingsPath string
	if global {
		settingsPath = globalSettingsPath(env.homeDir)
		_, _ = fmt.Fprintln(env.stdout, "Removing Claude hooks globally...")
	} else {
		settingsPath = projectSettingsPath(env.projectDir)
		_, _ = fmt.Fprintln(env.stdout, "Removing Claude hooks from project...")
	}

	if err := removeClaudeHooks(env, settingsPath); err != nil {
		return err
	}

	// Also clean legacy settings.local.json when removing project hooks
	if !global {
		removeLegacyClaudeHooks(env)
	}

	// From ~/.claude the project's CLAUDE.md is the user-level one, so the
	// removal below takes a global install's section; remove that install's
	// hooks from the global settings too, first, so a settings file that
	// cannot be read or does not parse stops the removal with the section
	// still in place.
	userLevelDir := !global && claudeProjectIsUserLevelDir(env)
	if userLevelDir {
		_, _ = fmt.Fprintln(env.stdout, "Removing Claude hooks globally...")
		if err := removeClaudeHooks(env, globalSettingsPath(env.homeDir)); err != nil {
			return err
		}
	}

	// From ~/.claude the file is resolved as a global removal, and --check
	// there, resolve it, so an @AGENTS.md line in it never takes the redirect
	// below, whose failed cleanup is only a warning.
	agentsEnv, integration, redirected := claudeInstructionsEnv(env, global || userLevelDir)
	if redirected {
		// When redirected, AGENTS.md carries the project-authoritative shared
		// beads section (created by `bd init` or another agent's setup), not a
		// Claude-specific one. Removing Claude integration must not delete it;
		// only clean up any stale block left directly in CLAUDE.md.
		_, _ = fmt.Fprintf(env.stdout, "  Leaving shared beads section in %s untouched (project-authoritative, not Claude-specific)\n", config.SafeAgentsFile())
		if err := stripStaleClaudeBlock(env); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Warning: failed to clean stale beads block from %s: %v\n", claudeInstructionsFile, err)
		}
	} else if err := removeAgents(agentsEnv, integration); err != nil {
		// From ~/.claude the pass above has removed the global hooks, so a
		// section left in place is half an install: fail rather than warn
		// and report success.
		if userLevelDir {
			return err
		}
		// Non-fatal
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", claudeInstructionsFile, err)
	}

	// From the home directory a project removal has just removed the global
	// hooks, since the settings files are the same; remove the user-level
	// section a global install paired with them too.
	if !global && claudeProjectIsHome(env) {
		globalEnv, globalIntegration, _ := claudeInstructionsEnv(env, true)
		if err := removeAgents(globalEnv, globalIntegration); err != nil {
			// Non-fatal
			_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", globalEnv.agentsPath, err)
		}
	}

	_, _ = fmt.Fprintln(env.stdout, "✓ Claude hooks removed")
	return nil
}

// removeClaudeHooks removes the bd prime hooks from the settings file at
// settingsPath, keeping every other setting. It writes the file only when it
// removed a hook: a file with none keeps its bytes and mode, and a symlinked
// one is not refused. A missing file, or one with no hooks, is reported; one
// that cannot be read or does not parse is an error.
func removeClaudeHooks(env claudeEnv, settingsPath string) error {
	data, err := env.readFile(settingsPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintf(env.stdout, "No settings file found at %s\n", settingsPath)
		return nil
	} else if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", settingsPath, err)
		return err
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to parse %s: %v\n", settingsPath, err)
		return err
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		_, _ = fmt.Fprintf(env.stdout, "No hooks found in %s\n", settingsPath)
		return nil
	}
	if !removeBdPrimeHooks(env.stdout, hooks) {
		return nil
	}

	data, err = marshalSettings(settings)
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: marshal settings: %v\n", err)
		return err
	}
	if err := writeSettingsIfChanged(env, settingsPath, data); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write settings: %v\n", err)
		return err
	}
	return nil
}

// removeLegacyClaudeHooks removes the bd prime hooks from the local settings
// Claude Code loads hooks from here (claudeProjectHookPaths): the project's
// .claude/settings.local.json, where older installs wrote them, and the
// repository root's, so that a hook there does not outlive the removal. It
// names each file it removed them from.
func removeLegacyClaudeHooks(env claudeEnv) {
	for _, legacyPath := range claudeProjectHookPaths(env)[1:] {
		if removeLocalClaudeHooks(env, legacyPath) {
			_, _ = fmt.Fprintf(env.stdout, "✓ Removed bd prime hooks from %s\n", legacyPath)
		}
	}
}

// removeLocalClaudeHooks removes the bd prime hooks from the local settings
// file at legacyPath and reports whether it did. Like removeClaudeHooks it
// writes the file only when it removed a hook. It is best effort: a file
// that is missing or does not parse is left as it is, and so is one that
// cannot be written, with a warning that names it.
func removeLocalClaudeHooks(env claudeEnv, legacyPath string) bool {
	legacyData, err := env.readFile(legacyPath)
	if err != nil {
		return false
	}
	var legacySettings map[string]interface{}
	if json.Unmarshal(legacyData, &legacySettings) != nil {
		return false
	}
	legacyHooks, ok := legacySettings["hooks"].(map[string]interface{})
	if !ok || !removeBdPrimeHooks(env.stdout, legacyHooks) {
		return false
	}
	migrated, err := marshalSettings(legacySettings)
	if err == nil {
		err = writeSettingsIfChanged(env, legacyPath, migrated)
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to remove bd prime hooks from %s: %v\n", legacyPath, err)
		return false
	}
	return true
}

// removeBdPrimeHooks removes every bd prime variant from the SessionStart and
// PreCompact hooks, reporting each removal to w. It reports whether it
// removed any.
func removeBdPrimeHooks(w io.Writer, hooks map[string]interface{}) bool {
	removed := false
	for _, v := range []string{"bd prime", "bd prime --stealth", "bd prime --hook-json", "bd prime --stealth --hook-json"} {
		// The call goes before ||, so an earlier removal never skips one.
		removed = removeHookCommand(w, hooks, "SessionStart", v) || removed
		removed = removeHookCommand(w, hooks, "PreCompact", v) || removed
	}
	return removed
}

// addHookCommand adds a hook command to an event if not already present
// Returns true if hook was added, false if already exists
func addHookCommand(hooks map[string]interface{}, event, command string) bool {
	// Get or create event array
	eventHooks, ok := hooks[event].([]interface{})
	if !ok {
		eventHooks = []interface{}{}
	}

	// Check if bd hook already registered
	for _, hook := range eventHooks {
		hookMap, ok := hook.(map[string]interface{})
		if !ok {
			continue
		}
		commands, ok := hookMap["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, cmd := range commands {
			cmdMap, ok := cmd.(map[string]interface{})
			if !ok {
				continue
			}
			if cmdMap["command"] == command {
				return false
			}
		}
	}

	// Add bd hook to array
	newHook := map[string]interface{}{
		"matcher": "",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": command,
			},
		},
	}

	eventHooks = append(eventHooks, newHook)
	hooks[event] = eventHooks
	return true
}

// removeHookCommand removes a specific command from an event's hook entries.
// Only the matching command object is removed; sibling commands in the same
// hook entry are preserved. A hook entry is dropped only when its command list
// becomes empty after filtering. Each removal is reported to w, the caller's
// stdout: bd init --quiet hands the installers a discarding writer, so a
// print to the process stdout here would escape it. It reports whether it
// removed the command from any entry.
func removeHookCommand(w io.Writer, hooks map[string]interface{}, event, command string) bool {
	eventHooks, ok := hooks[event].([]interface{})
	if !ok {
		return false
	}

	// Initialize as empty slice (not nil) to avoid JSON null serialization.
	filtered := make([]interface{}, 0, len(eventHooks))
	removedAny := false
	for _, hook := range eventHooks {
		hookMap, ok := hook.(map[string]interface{})
		if !ok {
			filtered = append(filtered, hook)
			continue
		}

		commands, ok := hookMap["hooks"].([]interface{})
		if !ok {
			filtered = append(filtered, hook)
			continue
		}

		// Filter only the matching command; preserve any siblings.
		remaining := make([]interface{}, 0, len(commands))
		removed := false
		for _, cmd := range commands {
			cmdMap, ok := cmd.(map[string]interface{})
			if !ok {
				remaining = append(remaining, cmd)
				continue
			}
			if cmdMap["command"] == command {
				removed = true
				continue
			}
			remaining = append(remaining, cmd)
		}

		if removed {
			removedAny = true
			_, _ = fmt.Fprintf(w, "✓ Removed %s hook\n", event)
		}

		// Drop the hook entry only when it has no commands left.
		if len(remaining) > 0 {
			hookMap["hooks"] = remaining
			filtered = append(filtered, hookMap)
		}
	}

	// GH#955: Delete the key entirely if no hooks remain, rather than
	// leaving an empty array. This is cleaner and avoids potential
	// issues with empty arrays in settings.
	if len(filtered) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = filtered
	}
	return removedAny
}

// hasBeadsPlugin reports whether the beads Claude Code plugin is enabled where
// its hooks would cover an install. The plugin declares its own SessionStart
// hooks in plugin.json, so the hooks an install writes would duplicate them.
func hasBeadsPlugin(env claudeEnv, global bool) bool {
	return beadsPlugin(env, global).enabled
}

// BeadsPluginEnabled reports whether Claude Code enables the beads plugin in
// a session started in dir, resolved as a project install resolves it
// (beadsPlugin): by Claude Code's settings precedence, counting only the
// plugin named beads. bd doctor reports the plugin from it, so it agrees
// with bd setup claude --check run in dir. It reports false when the home
// directory or dir's absolute path cannot be found.
func BeadsPluginEnabled(dir string) bool {
	env, ok := claudeSessionEnv(dir)
	return ok && hasBeadsPlugin(env, false)
}

// ClaudeHooksInstalled reports what bd setup claude --check, run in dir,
// reports first: whether a Claude Code session started there runs bd prime,
// from a hook in a settings file it loads there (primeHookHere) or from the
// beads plugin (BeadsPluginEnabled). The setup tip asks it. It reports false
// when the home directory or dir's absolute path cannot be found.
func ClaudeHooksInstalled(dir string) bool {
	env, ok := claudeSessionEnv(dir)
	return ok && (primeHookHere(env) != "" || hasBeadsPlugin(env, false))
}

// ClaudeHookSettingsPaths returns the settings files a Claude Code session
// started in dir loads hooks from, the ones bd setup claude --check looks for
// a bd prime hook in there (primeHookHere): the project's
// .claude/settings.json, the .claude/settings.local.json beside it, the
// repository root's when Claude Code reads that one too, and the user's
// ~/.claude/settings.json. bd doctor checks the hooks in them. It returns nil
// when the home directory or dir's absolute path cannot be found.
func ClaudeHookSettingsPaths(dir string) []string {
	env, ok := claudeSessionEnv(dir)
	if !ok {
		return nil
	}
	return claudeHookSettingsPaths(env)
}

// claudeSessionEnv is claudeEnvIn for dir, made absolute, and the user's
// home directory; ok is false when either cannot be found.
func claudeSessionEnv(dir string) (env claudeEnv, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return claudeEnv{}, false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return claudeEnv{}, false
	}
	return claudeEnvIn(home, abs), true
}

// beadsPluginState is the beads plugin's enablement as Claude Code resolves it.
type beadsPluginState struct {
	enabled bool
	// path is the settings file that decided it: the one that enabled it,
	// or, when it is not enabled, the first that turned it off ("" when no
	// file names it).
	path string
}

// beadsPlugin resolves the beads plugin's enablement for an install. Claude
// Code reads enabledPlugins from managed settings, the local
// .claude/settings.local.json, the project's .claude/settings.json and the
// user's ~/.claude/settings.json, in that order of precedence. A project
// install resolves all four, as a session started in the project does. A
// global install's hook is for every project, and Claude Code runs a plugin
// that a project's own settings enable in that project only, so it resolves
// the managed and user settings alone.
func beadsPlugin(env claudeEnv, global bool) beadsPluginState {
	return resolveBeadsPlugin(env.readFile, claudePluginSettingsPaths(env, global))
}

// claudePluginSettingsPaths returns the settings files beadsPlugin resolves,
// highest precedence first. The local settings are the repository root's
// (see claudeLocalSettingsRoot), then one an older Claude Code kept in the
// starting directory, which it still reads below the root's.
func claudePluginSettingsPaths(env claudeEnv, global bool) []string {
	paths := claudeManagedSettingsPaths(env.managedDir)
	if !global {
		if root := rootLocalSettingsPath(env); root != "" {
			paths = append(paths, root)
		}
		paths = append(paths, legacyProjectSettingsPath(env.projectDir), projectSettingsPath(env.projectDir))
	}
	return append(paths, globalSettingsPath(env.homeDir))
}

// sharedBeadsPlugin resolves the beads plugin from the settings everyone
// who works in the project shares: the managed settings and the project's
// .claude/settings.json. The user's ~/.claude/settings.json and the local
// .claude/settings.local.json files are personal, so they are left out.
func sharedBeadsPlugin(env claudeEnv) beadsPluginState {
	return resolveBeadsPlugin(env.readFile, append(claudeManagedSettingsPaths(env.managedDir), projectSettingsPath(env.projectDir)))
}

// claudeManagedSettingsPaths returns the file-based managed settings in dir,
// highest precedence first. Claude Code merges managed-settings.json, then
// each *.json in managed-settings.d in name order, skipping dot files, and a
// later file's value replaces an earlier one's. bd assumes these files are
// the managed source in force: it cannot see server-managed settings or an
// MDM profile, which rank above them, and under the default
// managedSourcesBehavior ("first-wins") Claude Code ignores the files
// entirely, enabledPlugins included, when a higher source delivers any
// policy key.
func claudeManagedSettingsPaths(dir string) []string {
	if dir == "" {
		return nil
	}
	var paths []string
	dropIns := filepath.Join(dir, "managed-settings.d")
	if entries, err := os.ReadDir(dropIns); err == nil {
		for i := len(entries) - 1; i >= 0; i-- {
			name := entries[i].Name()
			if entries[i].IsDir() || strings.HasPrefix(name, ".") || filepath.Ext(name) != ".json" {
				continue
			}
			paths = append(paths, filepath.Join(dropIns, name))
		}
	}
	return append(paths, filepath.Join(dir, "managed-settings.json"))
}

// resolveBeadsPlugin resolves the beads plugin across settings files given
// highest precedence first, as Claude Code resolves enabledPlugins: each
// plugin id is decided by the first file that sets it to true or false, and a
// file that does not name it, is missing or does not parse leaves it to the
// next. The plugin is enabled when any of its ids resolves to true.
func resolveBeadsPlugin(readFile func(string) ([]byte, error), paths []string) beadsPluginState {
	decided := map[string]bool{}
	var state beadsPluginState
	for _, path := range paths {
		for id, enabled := range beadsPluginEntries(readFile, path) {
			if _, ok := decided[id]; ok {
				continue
			}
			decided[id] = enabled
			switch {
			case enabled && !state.enabled:
				state = beadsPluginState{enabled: true, path: path}
			case !enabled && state.path == "":
				state.path = path
			}
		}
	}
	return state
}

// beadsPluginEntries returns the beads plugin ids a settings file's
// enabledPlugins sets, with their values.
func beadsPluginEntries(readFile func(string) ([]byte, error), path string) map[string]bool {
	data, err := readFile(path)
	if err != nil {
		return nil
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}
	enabledPlugins, ok := settings["enabledPlugins"].(map[string]interface{})
	if !ok {
		return nil
	}
	entries := map[string]bool{}
	for id, value := range enabledPlugins {
		// enabledPlugins keys are "<pluginName>@<marketplace>". Match the
		// plugin-name segment exactly: a substring test (GH#4244) mistakes any
		// "*beads*" plugin (e.g. design-to-beads) for the beads hook plugin and
		// wrongly skips the SessionStart hook write.
		name, _, _ := strings.Cut(strings.ToLower(id), "@")
		if enabled, ok := value.(bool); ok && name == "beads" {
			entries[id] = enabled
		}
	}
	return entries
}

// hasBeadsHooks checks if a settings file has bd prime hooks
func hasBeadsHooks(settingsPath string) bool {
	return len(primeHookCommands(settingsPath)) > 0
}

// primeHookCommands returns the bd prime commands a settings file's
// SessionStart and PreCompact hooks run.
func primeHookCommands(settingsPath string) []string {
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- settingsPath is constructed from known safe locations (user home/.claude), not user input
	if err != nil {
		return nil
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Check SessionStart and PreCompact for "bd prime"
	var primeCommands []string
	for _, event := range []string{"SessionStart", "PreCompact"} {
		eventHooks, ok := hooks[event].([]interface{})
		if !ok {
			continue
		}

		for _, hook := range eventHooks {
			hookMap, ok := hook.(map[string]interface{})
			if !ok {
				continue
			}
			commands, ok := hookMap["hooks"].([]interface{})
			if !ok {
				continue
			}
			for _, cmd := range commands {
				cmdMap, ok := cmd.(map[string]interface{})
				if !ok {
					continue
				}
				// Recognize both current (--hook-json) and legacy bare variants.
				switch command, _ := cmdMap["command"].(string); command {
				case "bd prime", "bd prime --stealth",
					"bd prime --hook-json", "bd prime --stealth --hook-json":
					primeCommands = append(primeCommands, command)
				}
			}
		}
	}

	return primeCommands
}
