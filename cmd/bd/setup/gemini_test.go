package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates/agents"
)

func newGeminiTestEnv(t *testing.T) (geminiEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	homeDir := filepath.Join(root, "home")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := geminiEnv{
		stdout:     stdout,
		stderr:     stderr,
		homeDir:    homeDir,
		projectDir: projectDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
	}
	return env, stdout, stderr
}

func stubGeminiEnvProvider(t *testing.T, env geminiEnv, err error) {
	t.Helper()
	orig := geminiEnvProvider
	geminiEnvProvider = func() (geminiEnv, error) {
		if err != nil {
			return geminiEnv{}, err
		}
		return env, nil
	}
	t.Cleanup(func() { geminiEnvProvider = orig })
}

func writeGeminiSettings(t *testing.T, path string, settings map[string]interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func readGeminiSettings(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	return settings
}

// geminiUserLevelSection is the managed section a global install writes to
// ~/.gemini/GEMINI.md.
func geminiUserLevelSection() string {
	return agents.RenderSectionWithOpts(agents.ProfileMinimal, agents.UserLevelRenderOpts())
}

// geminiPrimeHookSettings is a settings.json holding the hook a current
// install registers.
func geminiPrimeHookSettings() map[string]interface{} {
	return map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime --hook-json"},
					},
				},
			},
		},
	}
}

func writeGeminiFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// geminiUserLevelInstalls are the installs that manage the user-level
// ~/.gemini/GEMINI.md: the global one, run from a project, and a project one
// run from the home directory or from ~/.gemini.
var geminiUserLevelInstalls = []struct {
	name    string
	project bool
	dir     func(env geminiEnv) string
}{
	{"global install", false, func(env geminiEnv) string { return env.projectDir }},
	{"project install from home", true, func(env geminiEnv) string { return env.homeDir }},
	{"project install from ~/.gemini", true, func(env geminiEnv) string {
		return filepath.Dir(geminiGlobalInstructionsPath(env.homeDir))
	}},
}

func TestInstallGemini_Global(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	err := installGemini(env, false, false)
	if err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	// Verify settings file created
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	settings := readGeminiSettings(t, settingsPath)

	// Verify hooks structure
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("expected hooks map")
	}

	// Check SessionStart hook is registered with the --hook-json variant.
	// Gemini's hook contract requires JSON-on-stdout; --hook-json wraps
	// bd prime's markdown in the SessionStart envelope shape.
	sessionStart, ok := hooks["SessionStart"].([]interface{})
	if !ok || len(sessionStart) == 0 {
		t.Fatal("expected SessionStart hooks")
	}
	hook := sessionStart[0].(map[string]interface{})
	cmds := hook["hooks"].([]interface{})
	cmd := cmds[0].(map[string]interface{})
	if cmd["command"] != "bd prime --hook-json" {
		t.Errorf("expected SessionStart command 'bd prime --hook-json', got: %v", cmd["command"])
	}

	// PreCompress must NOT be registered: per Gemini docs it's advisory-only
	// and does not support additionalContext injection, so re-priming after
	// compression isn't possible there regardless of output format.
	if _, ok := hooks["PreCompress"]; ok {
		t.Error("PreCompress hook should not be registered (advisory-only event, no additionalContext support)")
	}

	// Verify output
	out := stdout.String()
	if !strings.Contains(out, "Installing Gemini CLI hooks globally") {
		t.Errorf("expected global install message, got: %s", out)
	}
	if !strings.Contains(out, "Gemini CLI integration installed") {
		t.Errorf("expected success message, got: %s", out)
	}
	// The managed section goes to Gemini CLI's user-level context file,
	// beside the global settings, holding only the section, worded for a file
	// every project loads: none of the project scaffolding a new project
	// GEMINI.md gets.
	instructions, err := os.ReadFile(geminiGlobalInstructionsPath(env.homeDir))
	if err != nil {
		t.Fatalf("expected user-level %s to be created: %v", geminiInstructionsFile, err)
	}
	if want := geminiUserLevelSection(); string(instructions) != want {
		t.Errorf("user-level %s should hold only the managed section:\ngot:\n%s\nwant:\n%s", geminiInstructionsFile, instructions, want)
	}
	// A global install must not touch the current directory's files.
	if _, err := os.Stat(filepath.Join(env.projectDir, geminiInstructionsFile)); !os.IsNotExist(err) {
		t.Errorf("global install should not create the project's %s (stat err: %v)", geminiInstructionsFile, err)
	}
}

// Run from the home directory, a global install must not create ~/GEMINI.md
// with project scaffolding: its section belongs in ~/.gemini/GEMINI.md.
func TestInstallGemini_GlobalFromHome(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)
	env.projectDir = env.homeDir
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.homeDir, geminiInstructionsFile)); !os.IsNotExist(err) {
		t.Errorf("global install from home should not create ~/%s (stat err: %v)", geminiInstructionsFile, err)
	}
	if data, err := os.ReadFile(geminiGlobalInstructionsPath(env.homeDir)); err != nil || string(data) != geminiUserLevelSection() {
		t.Errorf("expected the user-level section in ~/.gemini/%s, got %q (err %v)", geminiInstructionsFile, data, err)
	}
}

// ~/.gemini/GEMINI.md is the user's own file too: install appends the
// section after one blank line and --remove gives the file back byte for byte.
func TestInstallGemini_GlobalKeepsUserLevelContent(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)
	globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
	original := "# My preferences\n\nAnswer tersely.\n"
	writeGeminiFile(t, globalInstructions, original)
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}
	data, err := os.ReadFile(globalInstructions)
	if err != nil {
		t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
	}
	if want := original + "\n" + geminiUserLevelSection(); string(data) != want {
		t.Errorf("user-level %s after install:\ngot:\n%s\nwant:\n%s", geminiInstructionsFile, data, want)
	}
	if err := removeGemini(env, false); err != nil {
		t.Fatalf("removeGemini: %v", err)
	}
	data, err = os.ReadFile(globalInstructions)
	if err != nil {
		t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
	}
	if string(data) != original {
		t.Errorf("user-level %s after --remove:\ngot:\n%q\nwant:\n%q", geminiInstructionsFile, data, original)
	}
}

// legacyGeminiSaveMemory adds fact to content the way Gemini CLI's
// save_memory tool did through v0.43: at the end of the "## Gemini Added
// Memories" section, which it takes to run to the next "## " heading.
func legacyGeminiSaveMemory(content, fact string) string {
	item := "- " + fact
	idx := strings.Index(content, geminiMemoriesHeading)
	if idx < 0 {
		return strings.TrimRight(content, "\n") + "\n\n" + geminiMemoriesHeading + "\n" + item + "\n"
	}
	start := idx + len(geminiMemoriesHeading)
	end := strings.Index(content[start:], "\n## ")
	if end < 0 {
		end = len(content)
	} else {
		end += start
	}
	section := strings.TrimRight(content[start:end], " \t\r\n") + "\n" + item
	return strings.TrimRight(strings.TrimRight(content[:start], " \t\r\n")+"\n"+strings.TrimLeft(section, " \t\r\n")+"\n"+content[end:], " \t\r\n") + "\n"
}

// Gemini CLI keeps its saved memories in ~/.gemini/GEMINI.md too. The beads
// section goes ahead of them, so a memory saved later lands outside it and
// survives --remove, which gives the user's text back as it was.
func TestInstallGemini_GlobalSectionStaysClearOfGeminiMemories(t *testing.T) {
	for _, seed := range []struct{ name, text string }{
		{"memories only", geminiMemoriesHeading + "\n- I prefer tabs.\n"},
		{"memories after text", "# Prefs\n\nBe terse.\n\n" + geminiMemoriesHeading + "\n- I prefer tabs.\n"},
		{"memories right after a line", "# Prefs\n" + geminiMemoriesHeading + "\n- I prefer tabs.\n"},
		// Gemini CLI's memories start at the heading's first occurrence, even
		// one in prose, so the section goes ahead of the line holding that.
		{"heading mentioned first", "Gemini keeps a " + geminiMemoriesHeading + " list.\n\n" + geminiMemoriesHeading + "\n- I prefer tabs.\n"},
	} {
		for _, install := range geminiUserLevelInstalls {
			t.Run(seed.name+", "+install.name, func(t *testing.T) {
				env, _, _ := newGeminiTestEnv(t)
				env.projectDir = install.dir(env)
				globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
				writeGeminiFile(t, globalInstructions, seed.text)
				if err := installGemini(env, install.project, false); err != nil {
					t.Fatalf("installGemini: %v", err)
				}
				data, err := os.ReadFile(globalInstructions)
				if err != nil {
					t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
				}
				content := string(data)
				if strings.Index(content, "<!-- END BEADS INTEGRATION -->") > strings.Index(content, geminiMemoriesHeading) {
					t.Errorf("beads section should precede Gemini's memories:\n%s", content)
				}
				withMemory := legacyGeminiSaveMemory(content, "My cat is named Miso.")
				writeGeminiFile(t, globalInstructions, withMemory)
				if err := removeGemini(env, install.project); err != nil {
					t.Fatalf("removeGemini: %v", err)
				}
				data, err = os.ReadFile(globalInstructions)
				if err != nil {
					t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
				}
				if want := legacyGeminiSaveMemory(seed.text, "My cat is named Miso."); string(data) != want {
					t.Errorf("after a saved memory and --remove:\ngot:\n%q\nwant:\n%q", data, want)
				}
			})
		}
	}
}

// ~/.gemini/GEMINI.md holds Gemini CLI's saved memories and settings.json
// can hold API keys. An install or --remove that rewrites either must keep
// the mode the owner gave it, not reset it to 0644.
func TestGeminiUserLevelFilesKeepTheirMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions are not supported on Windows")
	}
	seed := "# Prefs\n\n" + geminiMemoriesHeading + "\n- My cat is named Miso.\n"
	for _, install := range geminiUserLevelInstalls {
		t.Run(install.name, func(t *testing.T) {
			env, _, _ := newGeminiTestEnv(t)
			env.projectDir = install.dir(env)
			instructions := geminiGlobalInstructionsPath(env.homeDir)
			settings := geminiGlobalSettingsPath(env.homeDir)
			seedFileMode(t, instructions, seed, 0o600)
			seedFileMode(t, settings, "{\"theme\": \"Default\"}\n", 0o600)

			if err := installGemini(env, install.project, false); err != nil {
				t.Fatalf("installGemini: %v", err)
			}
			data, err := os.ReadFile(instructions)
			if err != nil {
				t.Fatalf("read %s: %v", instructions, err)
			}
			if !containsBeadsMarker(string(data)) {
				t.Fatalf("install did not add the beads section:\n%s", data)
			}
			assertFileMode(t, instructions, 0o600)
			assertFileMode(t, settings, 0o600)

			for _, path := range []string{instructions, settings} {
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatalf("chmod %s: %v", path, err)
				}
			}
			if err := removeGemini(env, install.project); err != nil {
				t.Fatalf("removeGemini: %v", err)
			}
			data, err = os.ReadFile(instructions)
			if err != nil {
				t.Fatalf("read %s: %v", instructions, err)
			}
			if string(data) != seed {
				t.Errorf("--remove left:\n%s\nwant:\n%s", data, seed)
			}
			assertFileMode(t, instructions, 0o600)
			assertFileMode(t, settings, 0o600)
		})
	}
}

// A section that already sits below Gemini CLI's memories, as a project
// install run from ~/.gemini used to leave it, ends them, so the memories
// saved after that land inside it. An install moves it ahead of them, and
// neither that nor --remove drops the memories it caught.
func TestInstallGemini_SectionBelowGeminiMemories(t *testing.T) {
	memories := "# Prefs\n\n" + geminiMemoriesHeading + "\n- I prefer tabs.\n"
	for _, below := range []struct{ name, section string }{
		{"current section", geminiUserLevelSection()},
		{"project-worded section", agents.RenderSection(agents.ProfileMinimal)},
	} {
		caught := legacyGeminiSaveMemory(memories+"\n"+below.section, "My cat is named Miso.")
		if strings.Index(caught, "My cat") < strings.Index(caught, "BEGIN BEADS INTEGRATION") {
			t.Fatalf("expected the memory saved inside the section:\n%s", caught)
		}
		for _, install := range geminiUserLevelInstalls {
			t.Run(below.name+", "+install.name, func(t *testing.T) {
				env, stdout, _ := newGeminiTestEnv(t)
				env.projectDir = install.dir(env)
				globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
				writeGeminiFile(t, globalInstructions, caught)
				if err := installGemini(env, install.project, false); err != nil {
					t.Fatalf("installGemini: %v", err)
				}
				data, err := os.ReadFile(globalInstructions)
				if err != nil {
					t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
				}
				want := "# Prefs\n\n" + geminiUserLevelSection() + "\n" + geminiMemoriesHeading + "\n- I prefer tabs.\n\n- My cat is named Miso.\n"
				if string(data) != want {
					t.Fatalf("user-level %s after install:\ngot:\n%q\nwant:\n%q\nstdout:\n%s", geminiInstructionsFile, data, want, stdout.String())
				}
				if !strings.Contains(stdout.String(), "Moving the beads section ahead of") {
					t.Errorf("expected the move reported, got:\n%s", stdout.String())
				}
				writeGeminiFile(t, globalInstructions, legacyGeminiSaveMemory(want, "My dog is named Pip."))
				if err := removeGemini(env, install.project); err != nil {
					t.Fatalf("removeGemini: %v", err)
				}
				data, err = os.ReadFile(globalInstructions)
				if err != nil {
					t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
				}
				if want := memories + "\n- My cat is named Miso.\n- My dog is named Pip.\n"; string(data) != want {
					t.Errorf("user-level %s after --remove:\ngot:\n%q\nwant:\n%q", geminiInstructionsFile, data, want)
				}
			})
		}
		t.Run(below.name+", removed", func(t *testing.T) {
			env, _, _ := newGeminiTestEnv(t)
			globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
			writeGeminiFile(t, globalInstructions, caught)
			if err := removeGemini(env, false); err != nil {
				t.Fatalf("removeGemini: %v", err)
			}
			data, err := os.ReadFile(globalInstructions)
			if err != nil {
				t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
			}
			if want := memories + "\n- My cat is named Miso.\n"; string(data) != want {
				t.Errorf("user-level %s after --remove:\ngot:\n%q\nwant:\n%q", geminiInstructionsFile, data, want)
			}
		})
	}
}

// From the home directory, whose .gemini/settings.json is the global one, and
// from ~/.gemini, whose GEMINI.md is the user-level one, a project install is
// the global install: it registers the hook in ~/.gemini/settings.json and in
// no other settings file, and writes the user-level section alone, without
// the scaffolding of a new project GEMINI.md and not in ~/GEMINI.md. So
// --check from any of those directories judges that pair, and a removal from
// any of them removes the hook and the section together.
func TestGeminiUserLevelInstalls(t *testing.T) {
	for _, install := range geminiUserLevelInstalls {
		for _, removal := range geminiUserLevelInstalls {
			t.Run(install.name+", removed as the "+removal.name, func(t *testing.T) {
				env, stdout, _ := newGeminiTestEnv(t)
				installDir, removalDir := install.dir(env), removal.dir(env)
				env.projectDir = installDir
				if err := installGemini(env, install.project, false); err != nil {
					t.Fatalf("installGemini: %v", err)
				}
				if !strings.Contains(stdout.String(), "Installing Gemini CLI hooks globally") {
					t.Errorf("expected the global install reported, got:\n%s", stdout.String())
				}
				globalSettings := geminiGlobalSettingsPath(env.homeDir)
				if !hasCurrentGeminiHooks(globalSettings) {
					t.Errorf("expected the hook in %s", globalSettings)
				}
				if projectSettings := geminiProjectSettingsPath(installDir); projectSettings != globalSettings {
					if _, err := os.Stat(projectSettings); !os.IsNotExist(err) {
						t.Errorf("expected no %s (stat err: %v)", projectSettings, err)
					}
				}
				globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
				if data, err := os.ReadFile(globalInstructions); err != nil || string(data) != geminiUserLevelSection() {
					t.Errorf("expected only the user-level section in %s, got %q (err %v)", globalInstructions, data, err)
				}
				if _, err := os.Stat(filepath.Join(env.homeDir, geminiInstructionsFile)); !os.IsNotExist(err) {
					t.Errorf("expected no ~/%s (stat err: %v)", geminiInstructionsFile, err)
				}
				env.projectDir = removalDir
				stdout.Reset()
				if err := checkGemini(env); err != nil {
					t.Fatalf("checkGemini: %v\n%s", err, stdout.String())
				}
				if !strings.Contains(stdout.String(), globalInstructions+" (current)") {
					t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
				}
				stdout.Reset()
				if err := removeGemini(env, removal.project); err != nil {
					t.Fatalf("removeGemini: %v", err)
				}
				if !strings.Contains(stdout.String(), "Removing Gemini CLI hooks globally") {
					t.Errorf("expected the global removal reported, got:\n%s", stdout.String())
				}
				if hasGeminiBeadsHooks(globalSettings) {
					t.Errorf("expected the hook removed from %s", globalSettings)
				}
				if data, err := os.ReadFile(globalInstructions); err != nil || containsBeadsMarker(string(data)) {
					t.Errorf("expected the user-level section removed, got %q (err %v)", data, err)
				}
			})
		}
	}
}

// The user-level ~/.gemini/GEMINI.md is shared by every project, so a global
// install renders and checks it the same way whatever beads config the
// current directory has: otherwise --check from one project calls the section
// another project wrote stale, and following its hint flips it back.
func TestGeminiGlobalSectionIgnoresProjectConfig(t *testing.T) {
	configs := []struct {
		name string
		opts agents.RenderOpts
	}{
		{"remote", agents.RenderOpts{HasRemote: true}},
		{"no remote", agents.RenderOpts{HasRemote: false}},
		{"no push", agents.RenderOpts{HasRemote: true, NoPush: true}},
	}
	wantHash := agents.CurrentHashWithOpts(agents.ProfileMinimal, agents.UserLevelRenderOpts())
	orig := detectRenderOptsImpl
	t.Cleanup(func() { detectRenderOptsImpl = orig })
	for _, install := range configs {
		for _, check := range configs {
			t.Run(install.name+" install, "+check.name+" check", func(t *testing.T) {
				env, stdout, _ := newGeminiTestEnv(t)
				installOpts := install.opts
				detectRenderOptsImpl = func() agents.RenderOpts { return installOpts }
				if err := installGemini(env, false, false); err != nil {
					t.Fatalf("installGemini: %v", err)
				}
				globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
				data, err := os.ReadFile(globalInstructions)
				if err != nil {
					t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
				}
				if !strings.Contains(string(data), "hash:"+wantHash) {
					t.Errorf("expected hash %s regardless of project config, got:\n%s", wantHash, data)
				}
				checkOpts := check.opts
				detectRenderOptsImpl = func() agents.RenderOpts { return checkOpts }
				stdout.Reset()
				if err := checkGemini(env); err != nil {
					t.Fatalf("checkGemini: %v\n%s", err, stdout.String())
				}
				if !strings.Contains(stdout.String(), globalInstructions+" (current)") {
					t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
				}
			})
		}
	}
}

func TestInstallGemini_Project(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	err := installGemini(env, true, false)
	if err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	// Verify settings file created in project dir
	settingsPath := geminiProjectSettingsPath(env.projectDir)
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		t.Fatalf("expected project settings file at %s", settingsPath)
	}

	out := stdout.String()
	if !strings.Contains(out, "Installing Gemini CLI hooks for this project") {
		t.Errorf("expected project install message, got: %s", out)
	}
	instructionsPath := filepath.Join(env.projectDir, geminiInstructionsFile)
	if _, err := os.Stat(instructionsPath); err != nil {
		t.Fatalf("expected %s to be created: %v", geminiInstructionsFile, err)
	}
}

func TestInstallGemini_Stealth(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	err := installGemini(env, false, true)
	if err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})
	hook := sessionStart[0].(map[string]interface{})
	cmds := hook["hooks"].([]interface{})
	cmd := cmds[0].(map[string]interface{})

	if cmd["command"] != "bd prime --stealth --hook-json" {
		t.Errorf("expected stealth command 'bd prime --stealth --hook-json', got: %v", cmd["command"])
	}
}

func TestInstallGemini_Idempotent(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	// Install twice
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("second install: %v", err)
	}

	// Should only have one hook per event
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})

	if len(sessionStart) != 1 {
		t.Errorf("expected 1 SessionStart hook, got %d", len(sessionStart))
	}
}

// TestInstallGemini_NoOpRerunKeepsNewlineAndSkipsWrite pins GH#6185: like
// Claude's (#5944), the Gemini installer must leave settings.json ending in a
// newline and must not rewrite it when a re-run changes nothing.
func TestInstallGemini_NoOpRerunKeepsNewlineAndSkipsWrite(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	writes := 0
	write := env.writeFile
	env.writeFile = func(path string, data []byte) error {
		if path == settingsPath {
			writes++
		}
		return write(path, data)
	}

	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("settings.json must end in a newline, got %q", data)
	}

	writes = 0
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if writes != 0 {
		t.Fatalf("no-op re-run wrote settings.json %d time(s), want 0", writes)
	}
}

// TestInstallGemini_MigratesLegacyHooks verifies that re-running bd setup gemini
// on a pre-fix installation (which had bare "bd prime" on SessionStart and/or
// PreCompress) results in exactly one canonical "bd prime --hook-json" entry
// on SessionStart and no PreCompress entries. Leaving stale entries alongside
// the new one would cause Gemini to invoke both, and the legacy command emits
// raw markdown that violates Gemini's strict stdout-must-be-JSON contract.
func TestInstallGemini_MigratesLegacyHooks(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	// Seed a settings file that mirrors a pre-fix installation.
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	legacy := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime"},
					},
				},
			},
			"PreCompress": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime"},
					},
				},
			},
		},
	}
	writeGeminiSettings(t, settingsPath, legacy)

	// Re-run setup — must behave as a clean upgrade, not an accumulation.
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})

	// SessionStart: exactly one entry, the canonical --hook-json command.
	sessionStart, ok := hooks["SessionStart"].([]interface{})
	if !ok || len(sessionStart) != 1 {
		t.Fatalf("expected exactly 1 SessionStart hook after migration, got %v", hooks["SessionStart"])
	}
	hook := sessionStart[0].(map[string]interface{})
	cmds := hook["hooks"].([]interface{})
	cmd := cmds[0].(map[string]interface{})
	if cmd["command"] != "bd prime --hook-json" {
		t.Errorf("SessionStart command = %q, want 'bd prime --hook-json'", cmd["command"])
	}

	// PreCompress: must be absent or empty.
	if pc, ok := hooks["PreCompress"].([]interface{}); ok && len(pc) > 0 {
		t.Errorf("expected PreCompress cleared after migration, got %d entries", len(pc))
	}
}

func TestInstallGemini_PreservesExistingSettings(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	// Create settings with existing content
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	existingSettings := map[string]interface{}{
		"someOtherSetting": "value",
		"hooks": map[string]interface{}{
			"SomeOtherHook": []interface{}{
				map[string]interface{}{"custom": "hook"},
			},
		},
	}
	writeGeminiSettings(t, settingsPath, existingSettings)

	// Install Gemini hooks
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	// Verify existing settings preserved
	settings := readGeminiSettings(t, settingsPath)
	if settings["someOtherSetting"] != "value" {
		t.Error("existing setting was not preserved")
	}

	hooks := settings["hooks"].(map[string]interface{})
	if hooks["SomeOtherHook"] == nil {
		t.Error("existing hook was not preserved")
	}
}

// TestInstallGemini_UnreadableSettings: a settings file the install cannot
// read is not a missing one. Taken for one, it was replaced by a file holding
// only bd's hook, and every other setting in it was lost: the rename that
// replaces it needs write access to the directory, not read access to the
// file. The install now stops before it writes anything. The read error is
// injected, since a chmod 000 file is still readable to root.
func TestInstallGemini_UnreadableSettings(t *testing.T) {
	for _, project := range []bool{false, true} {
		name := "global"
		if project {
			name = "project"
		}
		t.Run(name, func(t *testing.T) {
			env, stdout, stderr := newGeminiTestEnv(t)
			settingsPath := geminiGlobalSettingsPath(env.homeDir)
			instructionsPath := geminiGlobalInstructionsPath(env.homeDir)
			if project {
				settingsPath = geminiProjectSettingsPath(env.projectDir)
				instructionsPath = filepath.Join(env.projectDir, geminiInstructionsFile)
			}
			const original = "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"gemini-api-key\"\n    }\n  }\n}\n"
			writeGeminiFile(t, settingsPath, original)
			readFile := env.readFile
			env.readFile = func(path string) ([]byte, error) {
				if path == settingsPath {
					return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
				}
				return readFile(path)
			}
			var written []string
			writeFile := env.writeFile
			env.writeFile = func(path string, data []byte) error {
				written = append(written, path)
				return writeFile(path, data)
			}

			if err := installGemini(env, project, false); !errors.Is(err, os.ErrPermission) {
				t.Errorf("installGemini error = %v, want %v\nstdout:\n%s", err, os.ErrPermission, stdout.String())
			}
			if !strings.Contains(stderr.String(), "failed to read "+settingsPath) {
				t.Errorf("expected the read error to name %s, got:\n%s", settingsPath, stderr.String())
			}
			if len(written) != 0 {
				t.Errorf("expected nothing written, wrote %v", written)
			}
			if data, err := os.ReadFile(settingsPath); err != nil || string(data) != original {
				t.Errorf("expected %s kept, got %q (%v)", settingsPath, data, err)
			}
			if _, err := os.Stat(instructionsPath); !os.IsNotExist(err) {
				t.Errorf("expected no %s written, stat error = %v", instructionsPath, err)
			}
		})
	}
}

// TestCheckGemini_LegacyInstall verifies that checkGemini returns
// errGeminiHooksLegacy and emits an upgrade advisory when the settings file
// contains a pre-fix "bd prime" registration (without --hook-json).
// Legacy hooks emit raw markdown that violates Gemini's JSON stdout contract,
// so --check must distinguish them from a working current install.
func TestCheckGemini_LegacyInstall(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	// Seed a pre-fix installation: bare "bd prime" on SessionStart.
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	legacy := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime"},
					},
				},
			},
		},
	}
	writeGeminiSettings(t, settingsPath, legacy)

	err := checkGemini(env)
	if err != errGeminiHooksLegacy {
		t.Errorf("expected errGeminiHooksLegacy, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "legacy") {
		t.Errorf("expected 'legacy' in output, got: %s", out)
	}
	if !strings.Contains(out, "bd setup gemini") {
		t.Errorf("expected upgrade instruction in output, got: %s", out)
	}
}

func TestCheckGemini_NotInstalled(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	err := checkGemini(env)
	if err != errGeminiHooksMissing {
		t.Errorf("expected errGeminiHooksMissing, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "No hooks installed") {
		t.Errorf("expected 'No hooks installed' message, got: %s", out)
	}
}

func TestCheckGemini_GlobalInstalled(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	// Install hooks first
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	// Reset stdout
	stdout.Reset()

	// Check should pass
	err := checkGemini(env)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Global hooks installed") {
		t.Errorf("expected 'Global hooks installed' message, got: %s", out)
	}
}

func TestCheckGemini_ProjectInstalled(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	// Install project hooks
	if err := installGemini(env, true, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	stdout.Reset()

	err := checkGemini(env)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Project hooks installed") {
		t.Errorf("expected 'Project hooks installed' message, got: %s", out)
	}
}

func TestCheckGemini_MissingInstructions(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}

	if err := os.Remove(geminiGlobalInstructionsPath(env.homeDir)); err != nil {
		t.Fatalf("remove user-level %s: %v", geminiInstructionsFile, err)
	}

	stdout.Reset()
	err := checkGemini(env)
	if err != errAgentsFileMissing {
		t.Fatalf("expected errAgentsFileMissing, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, geminiInstructionsFile+" not found") {
		t.Fatalf("expected missing %s message, got: %s", geminiInstructionsFile, out)
	}
	// With global hooks and no instructions file in the project, the missing
	// section is the user-level one the global install writes.
	if !strings.Contains(out, "Run: bd setup gemini\n") {
		t.Errorf("expected the global setup hint, got:\n%s", out)
	}
}

func TestCheckGeminiScenarios(t *testing.T) {
	stale := "<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:00000000 -->\nOld.\n<!-- END BEADS INTEGRATION -->\n"

	// A global install is checked from any directory: with no beads section
	// in the project, the one in ~/.gemini/GEMINI.md is the one Gemini loads.
	t.Run("global install from another directory", func(t *testing.T) {
		env, stdout, _ := newGeminiTestEnv(t)
		writeGeminiSettings(t, geminiGlobalSettingsPath(env.homeDir), geminiPrimeHookSettings())
		globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
		writeGeminiFile(t, globalInstructions, geminiUserLevelSection())
		if err := checkGemini(env); err != nil {
			t.Fatalf("checkGemini: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), globalInstructions+" (current)") {
			t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
		}
	})

	// The common global case: the project has its own GEMINI.md with no
	// beads section, and the user-level file carries the section.
	t.Run("global install beside project instructions without a section", func(t *testing.T) {
		env, stdout, _ := newGeminiTestEnv(t)
		writeGeminiSettings(t, geminiGlobalSettingsPath(env.homeDir), geminiPrimeHookSettings())
		globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
		writeGeminiFile(t, globalInstructions, geminiUserLevelSection())
		writeGeminiFile(t, filepath.Join(env.projectDir, geminiInstructionsFile), "# Mine\n")
		if err := checkGemini(env); err != nil {
			t.Fatalf("checkGemini: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), globalInstructions+" (current)") {
			t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
		}
	})

	// A project GEMINI.md without a section, and none in the user-level
	// file, is still reported as the project's: adding the section there is
	// one way to finish the setup, and the hint names the command that does.
	t.Run("project instructions without a section", func(t *testing.T) {
		env, stdout, _ := newGeminiTestEnv(t)
		writeGeminiSettings(t, geminiGlobalSettingsPath(env.homeDir), geminiPrimeHookSettings())
		writeGeminiFile(t, filepath.Join(env.projectDir, geminiInstructionsFile), "# Mine\n")
		if err := checkGemini(env); !errors.Is(err, errBeadsSectionMissing) {
			t.Fatalf("expected errBeadsSectionMissing, got %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Run: bd setup gemini --project (to add beads section)") {
			t.Errorf("expected the project setup hint, got:\n%s", stdout.String())
		}
	})

	// From the home directory a ~/GEMINI.md without a section is not the
	// file to fix: the project hint would add the section there.
	t.Run("home instructions without a section checked from home", func(t *testing.T) {
		env, stdout, _ := newGeminiTestEnv(t)
		env.projectDir = env.homeDir
		writeGeminiSettings(t, geminiGlobalSettingsPath(env.homeDir), geminiPrimeHookSettings())
		writeGeminiFile(t, filepath.Join(env.homeDir, geminiInstructionsFile), "# Mine\n")
		if err := checkGemini(env); !errors.Is(err, errAgentsFileMissing) {
			t.Fatalf("expected errAgentsFileMissing, got %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Run: bd setup gemini\n") {
			t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
		}
	})

	// Nor is a ~/GEMINI.md with a section, such as an earlier global install
	// run from home left: from home --check judges the user-level file, so it
	// does not pass before a global install has written that.
	for _, userLevel := range []struct {
		name    string
		content string
		want    error
		output  func(env geminiEnv) string
	}{
		{"stale", stale, errBeadsSectionStale, func(env geminiEnv) string {
			return geminiGlobalInstructionsPath(env.homeDir) + "\n  Run: bd setup gemini (to update)"
		}},
		{"missing", "", errAgentsFileMissing, func(geminiEnv) string { return "Run: bd setup gemini\n" }},
	} {
		t.Run("home section with a "+userLevel.name+" user-level one checked from home", func(t *testing.T) {
			env, stdout, _ := newGeminiTestEnv(t)
			env.projectDir = env.homeDir
			writeGeminiSettings(t, geminiGlobalSettingsPath(env.homeDir), geminiPrimeHookSettings())
			writeGeminiFile(t, filepath.Join(env.homeDir, geminiInstructionsFile), agents.RenderSectionWithOpts(agents.ProfileMinimal, detectRenderOpts()))
			if userLevel.content != "" {
				writeGeminiFile(t, geminiGlobalInstructionsPath(env.homeDir), userLevel.content)
			}
			if err := checkGemini(env); !errors.Is(err, userLevel.want) {
				t.Fatalf("expected %v, got %v\n%s", userLevel.want, err, stdout.String())
			}
			if !strings.Contains(stdout.String(), userLevel.output(env)) {
				t.Errorf("expected the user-level file and the global setup hint, got:\n%s", stdout.String())
			}
		})
	}

	t.Run("stale user-level section", func(t *testing.T) {
		env, stdout, _ := newGeminiTestEnv(t)
		writeGeminiSettings(t, geminiGlobalSettingsPath(env.homeDir), geminiPrimeHookSettings())
		writeGeminiFile(t, geminiGlobalInstructionsPath(env.homeDir), stale)
		if err := checkGemini(env); !errors.Is(err, errBeadsSectionStale) {
			t.Fatalf("expected errBeadsSectionStale, got %v", err)
		}
		if !strings.Contains(stdout.String(), "Run: bd setup gemini (to update)") {
			t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
		}
	})

	// A section below Gemini CLI's memories goes on catching the memories
	// saved after it, so it is stale however current its text: the hint
	// names the install, which moves it ahead of them.
	t.Run("current user-level section below Gemini's memories", func(t *testing.T) {
		env, stdout, _ := newGeminiTestEnv(t)
		writeGeminiSettings(t, geminiGlobalSettingsPath(env.homeDir), geminiPrimeHookSettings())
		globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
		writeGeminiFile(t, globalInstructions, geminiMemoriesHeading+"\n- I prefer tabs.\n\n"+geminiUserLevelSection())
		if err := checkGemini(env); !errors.Is(err, errBeadsSectionStale) {
			t.Fatalf("expected errBeadsSectionStale, got %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), globalInstructions+"\n  Run: bd setup gemini (to update)") {
			t.Errorf("expected the user-level file and the global setup hint, got:\n%s", stdout.String())
		}
		if err := installGemini(env, false, false); err != nil {
			t.Fatalf("installGemini: %v", err)
		}
		stdout.Reset()
		if err := checkGemini(env); err != nil {
			t.Fatalf("checkGemini after the install: %v\n%s", err, stdout.String())
		}
	})

	// A project install is judged on the project's file alone: the
	// user-level section does not stand in for a missing project one, and
	// the hint names the project install.
	t.Run("project hooks ignore the user-level section", func(t *testing.T) {
		env, stdout, _ := newGeminiTestEnv(t)
		writeGeminiSettings(t, geminiProjectSettingsPath(env.projectDir), geminiPrimeHookSettings())
		writeGeminiFile(t, geminiGlobalInstructionsPath(env.homeDir), geminiUserLevelSection())
		if err := checkGemini(env); !errors.Is(err, errAgentsFileMissing) {
			t.Fatalf("expected errAgentsFileMissing, got %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Run: bd setup gemini --project\n") {
			t.Errorf("expected the project setup hint, got:\n%s", stdout.String())
		}
	})

	// A stale section left by a project install names the project install:
	// the default command would write the user-level file and leave it stale.
	t.Run("stale project section", func(t *testing.T) {
		env, stdout, _ := newGeminiTestEnv(t)
		writeGeminiSettings(t, geminiProjectSettingsPath(env.projectDir), geminiPrimeHookSettings())
		writeGeminiFile(t, filepath.Join(env.projectDir, geminiInstructionsFile), stale)
		if err := checkGemini(env); !errors.Is(err, errBeadsSectionStale) {
			t.Fatalf("expected errBeadsSectionStale, got %v", err)
		}
		if !strings.Contains(stdout.String(), "Run: bd setup gemini --project (to update)") {
			t.Errorf("expected the project setup hint, got:\n%s", stdout.String())
		}
	})

	// With global hooks the project's own section still wins over the
	// user-level one, as it does for Claude Code.
	t.Run("project section preferred", func(t *testing.T) {
		env, stdout, _ := newGeminiTestEnv(t)
		writeGeminiSettings(t, geminiGlobalSettingsPath(env.homeDir), geminiPrimeHookSettings())
		writeGeminiFile(t, geminiGlobalInstructionsPath(env.homeDir), stale)
		projectInstructions := filepath.Join(env.projectDir, geminiInstructionsFile)
		writeGeminiFile(t, projectInstructions, agents.RenderSectionWithOpts(agents.ProfileMinimal, detectRenderOpts()))
		if err := checkGemini(env); err != nil {
			t.Fatalf("checkGemini: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), projectInstructions+" (current)") {
			t.Errorf("expected the project file reported, got:\n%s", stdout.String())
		}
	})

	// From the home directory the project's settings are the global ones and
	// its GEMINI.md is ~/GEMINI.md, so a global install must still be judged
	// on the user-level files.
	t.Run("global install checked from home", func(t *testing.T) {
		env, _, _ := newGeminiTestEnv(t)
		env.projectDir = env.homeDir
		if err := installGemini(env, false, false); err != nil {
			t.Fatalf("installGemini: %v", err)
		}
		stdout := &bytes.Buffer{}
		env.stdout = stdout
		if err := checkGemini(env); err != nil {
			t.Fatalf("checkGemini: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Global hooks installed") {
			t.Errorf("expected global hooks reported, got:\n%s", stdout.String())
		}
		if !strings.Contains(stdout.String(), geminiGlobalInstructionsPath(env.homeDir)+" (current)") {
			t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
		}
	})

	// From ~/.gemini the project's GEMINI.md is the user-level file: it is
	// judged with the fixed global render options, not this directory's.
	t.Run("global install checked from the user-level directory", func(t *testing.T) {
		env, _, _ := newGeminiTestEnv(t)
		if err := installGemini(env, false, false); err != nil {
			t.Fatalf("installGemini: %v", err)
		}
		orig := detectRenderOptsImpl
		t.Cleanup(func() { detectRenderOptsImpl = orig })
		detectRenderOptsImpl = func() agents.RenderOpts { return agents.RenderOpts{HasRemote: false} }
		env.projectDir = filepath.Dir(geminiGlobalInstructionsPath(env.homeDir))
		stdout := &bytes.Buffer{}
		env.stdout = stdout
		if err := checkGemini(env); err != nil {
			t.Fatalf("checkGemini: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), geminiGlobalInstructionsPath(env.homeDir)+" (current)") {
			t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
		}
	})

	// The home directory and ~/.gemini are recognized when reached through a
	// symlink too: sameDir compares the directories, not their path strings.
	for _, link := range []struct {
		name   string
		target func(home string) string
	}{
		{"home", func(home string) string { return home }},
		{"the user-level directory", func(home string) string { return filepath.Dir(geminiGlobalInstructionsPath(home)) }},
	} {
		t.Run("global install checked from a symlink to "+link.name, func(t *testing.T) {
			env, _, _ := newGeminiTestEnv(t)
			if err := installGemini(env, false, false); err != nil {
				t.Fatalf("installGemini: %v", err)
			}
			linkDir := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(link.target(env.homeDir), linkDir); err != nil {
				t.Skipf("symlink: %v", err)
			}
			env.projectDir = linkDir
			stdout := &bytes.Buffer{}
			env.stdout = stdout
			if err := checkGemini(env); err != nil {
				t.Fatalf("checkGemini: %v\n%s", err, stdout.String())
			}
			if !strings.Contains(stdout.String(), geminiGlobalInstructionsPath(env.homeDir)+" (current)") {
				t.Errorf("expected the user-level file reported current, got:\n%s", stdout.String())
			}
		})
	}
}

// A section worded for a project, such as a project install run from
// ~/.gemini leaves there, is stale in the user-level file: --check names the
// global install, which rewords the section in place and keeps the user's
// own text.
func TestInstallGeminiGlobalRewordsProjectSection(t *testing.T) {
	stubDetectRenderOpts(t)
	env, stdout, _ := newGeminiTestEnv(t)
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}
	globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
	writeGeminiFile(t, globalInstructions, "# Mine\n\n"+agents.RenderSection(agents.ProfileMinimal))
	stdout.Reset()
	if err := checkGemini(env); !errors.Is(err, errBeadsSectionStale) {
		t.Fatalf("expected errBeadsSectionStale, got %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Run: bd setup gemini (to update)") {
		t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
	}
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}
	data, err := os.ReadFile(globalInstructions)
	if err != nil {
		t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
	}
	if want := "# Mine\n\n" + geminiUserLevelSection(); string(data) != want {
		t.Errorf("user-level %s after the global install:\ngot:\n%s\nwant:\n%s", geminiInstructionsFile, data, want)
	}
	stdout.Reset()
	if err := checkGemini(env); err != nil {
		t.Fatalf("checkGemini: %v\n%s", err, stdout.String())
	}
}

func TestRemoveGemini_Global(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	// Install first
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}
	section := agents.RenderSection(agents.ProfileMinimal)
	globalInstructions := geminiGlobalInstructionsPath(env.homeDir)
	writeGeminiFile(t, globalInstructions, "# Mine\n\n"+section)
	// The project's own section, as a project install leaves it, belongs to
	// that install: a global removal must leave it alone.
	projectInstructions := filepath.Join(env.projectDir, geminiInstructionsFile)
	writeGeminiFile(t, projectInstructions, section)

	stdout.Reset()

	// Remove
	if err := removeGemini(env, false); err != nil {
		t.Fatalf("removeGemini: %v", err)
	}

	// Verify hooks removed
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})

	sessionStart, ok := hooks["SessionStart"].([]interface{})
	if ok && len(sessionStart) > 0 {
		t.Error("SessionStart hooks should be empty")
	}
	instructions, err := os.ReadFile(globalInstructions)
	if err != nil {
		t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
	}
	if strings.Contains(string(instructions), "BEGIN BEADS INTEGRATION") || string(instructions) != "# Mine\n" {
		t.Errorf("expected only the beads section removed from user-level %s, got:\n%q", geminiInstructionsFile, instructions)
	}
	project, err := os.ReadFile(projectInstructions)
	if err != nil {
		t.Fatalf("read project %s: %v", geminiInstructionsFile, err)
	}
	if string(project) != section {
		t.Errorf("global removal changed the project's %s:\n%s", geminiInstructionsFile, project)
	}

	out := stdout.String()
	if !strings.Contains(out, "Gemini CLI hooks removed") {
		t.Errorf("expected removal message, got: %s", out)
	}
}

// From the home directory a project removal removes the global hooks, whose
// settings file is the same, so it removes their user-level section too.
func TestRemoveGemini_ProjectFromHome(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)
	env.projectDir = env.homeDir
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}
	if err := removeGemini(env, true); err != nil {
		t.Fatalf("removeGemini: %v", err)
	}
	data, err := os.ReadFile(geminiGlobalSettingsPath(env.homeDir))
	if err != nil {
		t.Fatalf("read global settings: %v", err)
	}
	if strings.Contains(string(data), "bd prime") {
		t.Error("expected bd prime hooks removed")
	}
	instructions, err := os.ReadFile(geminiGlobalInstructionsPath(env.homeDir))
	if err != nil {
		t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
	}
	if containsBeadsMarker(string(instructions)) {
		t.Errorf("expected the user-level section removed, got:\n%s\nstdout:\n%s", instructions, stdout.String())
	}
}

// A project removal elsewhere leaves the user-level section a global install
// wrote alone.
func TestRemoveGemini_ProjectLeavesUserLevelSection(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("global installGemini: %v", err)
	}
	if err := installGemini(env, true, false); err != nil {
		t.Fatalf("project installGemini: %v", err)
	}
	if err := removeGemini(env, true); err != nil {
		t.Fatalf("removeGemini: %v", err)
	}
	instructions, err := os.ReadFile(geminiGlobalInstructionsPath(env.homeDir))
	if err != nil {
		t.Fatalf("read user-level %s: %v", geminiInstructionsFile, err)
	}
	if string(instructions) != geminiUserLevelSection() {
		t.Errorf("project removal changed the user-level %s:\n%s", geminiInstructionsFile, instructions)
	}
	project, err := os.ReadFile(filepath.Join(env.projectDir, geminiInstructionsFile))
	if err != nil {
		t.Fatalf("read project %s: %v", geminiInstructionsFile, err)
	}
	if containsBeadsMarker(string(project)) {
		t.Errorf("expected the project section removed, got:\n%s", project)
	}
}

func TestRemoveGemini_NoSettingsFile(t *testing.T) {
	env, stdout, _ := newGeminiTestEnv(t)

	// Remove without installing first
	err := removeGemini(env, false)
	if err != nil {
		t.Errorf("expected no error for missing file, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "No settings file found") {
		t.Errorf("expected 'No settings file found' message, got: %s", out)
	}
}

// TestRemoveGemini_UnreadableSettings: a settings file the removal cannot
// read is not a missing one. It was reported as "No settings file found", and
// the removal went on to take the beads section out of GEMINI.md and report
// success with the hook still in place. Like bd setup claude --remove, it now
// fails with the section kept.
func TestRemoveGemini_UnreadableSettings(t *testing.T) {
	env, stdout, stderr := newGeminiTestEnv(t)
	if err := installGemini(env, false, false); err != nil {
		t.Fatalf("installGemini: %v", err)
	}
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	instructionsPath := geminiGlobalInstructionsPath(env.homeDir)
	settings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	readFile := env.readFile
	env.readFile = func(path string) ([]byte, error) {
		if path == settingsPath {
			return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
		}
		return readFile(path)
	}
	stdout.Reset()

	if err := removeGemini(env, false); !errors.Is(err, os.ErrPermission) {
		t.Errorf("removeGemini error = %v, want %v\nstdout:\n%s", err, os.ErrPermission, stdout.String())
	}
	if !strings.Contains(stderr.String(), "failed to read "+settingsPath) {
		t.Errorf("expected the read error to name %s, got:\n%s", settingsPath, stderr.String())
	}
	if out := stdout.String(); strings.Contains(out, "No settings file found") || strings.Contains(out, "hooks removed") {
		t.Errorf("expected neither a missing file nor a removal reported, got:\n%s", out)
	}
	if data, err := os.ReadFile(settingsPath); err != nil || string(data) != string(settings) {
		t.Errorf("expected %s kept, got %q (%v)", settingsPath, data, err)
	}
	if data, err := os.ReadFile(instructionsPath); err != nil || string(data) != geminiUserLevelSection() {
		t.Errorf("expected the user-level section kept, got %q (%v)", data, err)
	}
}

func TestRemoveGemini_PreservesOtherHooks(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	// Create settings with other hooks
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime"},
					},
				},
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "other-command"},
					},
				},
			},
		},
	}
	writeGeminiSettings(t, settingsPath, existingSettings)

	// Remove bd prime hooks
	if err := removeGemini(env, false); err != nil {
		t.Fatalf("removeGemini: %v", err)
	}

	// Verify other hooks preserved
	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})

	if len(sessionStart) != 1 {
		t.Errorf("expected 1 remaining hook, got %d", len(sessionStart))
	}

	// Verify it's the other command, not bd prime
	hook := sessionStart[0].(map[string]interface{})
	cmds := hook["hooks"].([]interface{})
	cmd := cmds[0].(map[string]interface{})
	if cmd["command"] == "bd prime" || cmd["command"] == "bd prime --stealth" {
		t.Error("bd prime hook should have been removed")
	}
}

func TestHasGeminiBeadsHooks(t *testing.T) {
	makeSessionStart := func(command string) map[string]interface{} {
		return map[string]interface{}{
			"hooks": map[string]interface{}{
				"SessionStart": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{"type": "command", "command": command},
						},
					},
				},
			},
		}
	}
	makePreCompress := func(command string) map[string]interface{} {
		return map[string]interface{}{
			"hooks": map[string]interface{}{
				"PreCompress": []interface{}{
					map[string]interface{}{
						"matcher": "",
						"hooks": []interface{}{
							map[string]interface{}{"type": "command", "command": command},
						},
					},
				},
			},
		}
	}

	tests := []struct {
		name     string
		settings map[string]interface{}
		want     bool
	}{
		// Current canonical commands (--hook-json)
		{"current bd prime --hook-json on SessionStart", makeSessionStart("bd prime --hook-json"), true},
		{"current bd prime --stealth --hook-json on SessionStart", makeSessionStart("bd prime --stealth --hook-json"), true},

		// Legacy commands — still detected so pre-hook-json installations show
		// the upgrade advisory rather than "not installed".
		{"legacy bd prime on SessionStart", makeSessionStart("bd prime"), true},
		{"legacy bd prime --stealth on SessionStart", makeSessionStart("bd prime --stealth"), true},

		// PreCompress is no longer in scope — even legacy installations there
		// must NOT be reported as installed (we want users to re-run setup).
		{"bd prime on PreCompress only — not detected", makePreCompress("bd prime"), false},
		{"bd prime --hook-json on PreCompress only — not detected", makePreCompress("bd prime --hook-json"), false},

		// Unrelated commands
		{"unrelated command", makeSessionStart("some-other-command"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			settingsPath := filepath.Join(tmpDir, "settings.json")
			data, err := json.Marshal(tt.settings)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := hasGeminiBeadsHooks(settingsPath); got != tt.want {
				t.Errorf("hasGeminiBeadsHooks(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}

	// Edge cases: missing file and empty settings
	t.Run("missing file", func(t *testing.T) {
		tmpDir := t.TempDir()
		if hasGeminiBeadsHooks(filepath.Join(tmpDir, "missing.json")) {
			t.Error("expected false for missing file")
		}
	})
	t.Run("empty settings", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if hasGeminiBeadsHooks(settingsPath) {
			t.Error("expected false for empty settings")
		}
	})
}

// TestRemoveGemini_CleansAllVariants verifies that removeGemini cleans up
// every known command variant from BOTH SessionStart and PreCompress —
// migration safety for pre-fix installations that registered legacy commands
// on PreCompress.
func TestRemoveGemini_CleansAllVariants(t *testing.T) {
	env, _, _ := newGeminiTestEnv(t)

	// Seed a settings file with a current --hook-json registration on SessionStart
	// and legacy bare-command registrations on PreCompress.
	settingsPath := geminiGlobalSettingsPath(env.homeDir)
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime --hook-json"},
					},
				},
			},
			"PreCompress": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime"},
					},
				},
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "bd prime --stealth"},
					},
				},
			},
		},
	}
	writeGeminiSettings(t, settingsPath, existing)

	if err := removeGemini(env, false); err != nil {
		t.Fatalf("removeGemini: %v", err)
	}

	settings := readGeminiSettings(t, settingsPath)
	hooks := settings["hooks"].(map[string]interface{})

	if ss, ok := hooks["SessionStart"].([]interface{}); ok && len(ss) > 0 {
		t.Errorf("expected SessionStart cleared, got %d entries", len(ss))
	}
	if pc, ok := hooks["PreCompress"].([]interface{}); ok && len(pc) > 0 {
		t.Errorf("expected legacy PreCompress entries cleared, got %d entries", len(pc))
	}
}

func TestGeminiSettingsPaths(t *testing.T) {
	projectPath := geminiProjectSettingsPath("/my/project")
	if want := filepath.Join("/my/project", ".gemini", "settings.json"); projectPath != want {
		t.Errorf("project path = %q, want %q", projectPath, want)
	}

	globalPath := geminiGlobalSettingsPath("/home/user")
	if want := filepath.Join("/home/user", ".gemini", "settings.json"); globalPath != want {
		t.Errorf("global path = %q, want %q", globalPath, want)
	}
}
