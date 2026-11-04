package setup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates/agents"
)

// The tests in this file pin the agent artifacts `bd setup claude` writes, and
// what its --check and --remove report and leave behind: the hook events and
// commands in the Claude settings files, the CLAUDE.md / AGENTS.md files with
// their managed section, and the output of each step. Each scenario is a
// transcript golden under testdata/, so a change to any of them (a new hook
// event, a changed hook command, new template text, a different --check or
// --remove outcome) shows up as a reviewed golden diff.
//
// A managed section is shown by its BEGIN marker line (profile and hash) and a
// pointer to the internal/templates/agents golden that pins its bytes
// (TestManagedSectionGolden), after checking the file holds exactly that
// section. A template change therefore shows up once, as that golden's diff,
// and here as the new marker hash in every file it lands in. A section that
// matches no rendered variant is shown in full. The `bd init` side of the same
// artifacts is pinned by TestEmbeddedInitAgentArtifactsGolden in cmd/bd.
//
// Regenerate with
//
//	BD_UPDATE_GOLDEN=1 go test ./cmd/bd/setup/ -run TestClaudeSetupArtifactsGolden

// claudeArtifactPaths are the files bd setup claude can create, change or
// delete, relative to the root holding the test's project and home dirs.
var claudeArtifactPaths = []string{
	"home/.claude/CLAUDE.md",
	"home/.claude/settings.json",
	"home/CLAUDE.md",
	"project/.claude/settings.json",
	"project/.claude/settings.local.json",
	"project/AGENTS.md",
	"project/CLAUDE.md",
}

// managedSectionGoldens maps each managed section bd renders to the
// internal/templates/agents golden file that pins its bytes.
var managedSectionGoldens = []struct {
	golden  string
	profile agents.Profile
	opts    agents.RenderOpts
}{
	{golden: "managed-section-minimal.md", profile: agents.ProfileMinimal, opts: agents.RenderOpts{HasRemote: true}},
	{golden: "managed-section-minimal-no-remote.md", profile: agents.ProfileMinimal, opts: agents.RenderOpts{}},
	{golden: "managed-section-minimal-user.md", profile: agents.ProfileMinimal, opts: agents.UserLevelRenderOpts()},
	{golden: "managed-section-full.md", profile: agents.ProfileFull, opts: agents.RenderOpts{HasRemote: true}},
	{golden: "managed-section-full-no-remote.md", profile: agents.ProfileFull, opts: agents.RenderOpts{}},
	{golden: "managed-section-full-user.md", profile: agents.ProfileFull, opts: agents.UserLevelRenderOpts()},
}

// elideManagedSection replaces the body of a managed section that is
// byte-identical to a rendered variant with a pointer to that variant's
// golden, keeping the BEGIN marker line (profile and hash) and END marker.
func elideManagedSection(content string) string {
	const endMarker = "<!-- END BEADS INTEGRATION -->\n"
	begin := strings.Index(content, "<!-- BEGIN BEADS INTEGRATION")
	if begin < 0 {
		return content
	}
	rel := strings.Index(content[begin:], endMarker)
	if rel < 0 {
		return content
	}
	end := begin + rel + len(endMarker)
	section := content[begin:end]
	for _, v := range managedSectionGoldens {
		if section == agents.RenderSectionWithOpts(v.profile, v.opts) {
			marker, _, _ := strings.Cut(section, "\n")
			return content[:begin] + marker + "\n[managed section body: byte-identical to internal/templates/agents/testdata/" + v.golden + "]\n" + endMarker + content[end:]
		}
	}
	return content
}

type claudeArtifactStep struct {
	title string
	run   func(env claudeEnv) error
}

func claudeInstallStep(global, stealth bool) claudeArtifactStep {
	title := "bd setup claude"
	if global {
		title += " --global"
	}
	if stealth {
		title += " --stealth"
	}
	return claudeArtifactStep{title: title, run: func(env claudeEnv) error { return installClaude(env, global, stealth) }}
}

func claudeCheckStep() claudeArtifactStep {
	return claudeArtifactStep{title: "bd setup claude --check", run: checkClaude}
}

func claudeRemoveStep(global bool) claudeArtifactStep {
	title := "bd setup claude --remove"
	if global {
		title += " --global"
	}
	return claudeArtifactStep{title: title, run: func(env claudeEnv) error { return removeClaude(env, global) }}
}

func TestClaudeSetupArtifactsGolden(t *testing.T) {
	// The managed section's `bd dolt push` lines depend on whether a Dolt
	// remote is configured; these scenarios render the with-remote variant
	// (TestEmbeddedInitAgentArtifactsGolden pins the no-remote one).
	stubDetectRenderOpts(t)

	userSettings := `{
  "permissions": {
    "allow": [
      "Bash(go test:*)"
    ]
  },
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "echo session started"
          }
        ]
      }
    ],
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "notify-send done"
          }
        ]
      }
    ]
  }
}
`

	// What an older bd wrote to settings.local.json, before hooks moved to
	// settings.json and to the SessionStart JSON envelope.
	legacyLocalSettings := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "bd prime"
          }
        ]
      }
    ],
    "PreCompact": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "bd prime"
          }
        ]
      }
    ]
  }
}
`

	// The entry Claude Code writes to a project's .claude/settings.json for
	// a project-scope install of the beads plugin.
	projectPluginSettings := `{
  "enabledPlugins": {
    "beads@beads-marketplace": true
  }
}
`

	// The settings.json a current install writes.
	installedSettings := `{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "command": "bd prime --hook-json",
            "type": "command"
          }
        ],
        "matcher": ""
      }
    ]
  }
}
`

	scenarios := []struct {
		name string
		// dir is the directory bd runs in, relative to the root; empty
		// means project.
		dir   string
		seed  map[string]string
		steps []claudeArtifactStep
	}{
		{
			name: "project",
			steps: []claudeArtifactStep{
				claudeInstallStep(false, false),
				claudeCheckStep(),
				claudeInstallStep(false, false),
				claudeRemoveStep(false),
				claudeCheckStep(),
			},
		},
		{
			name: "project-stealth",
			steps: []claudeArtifactStep{
				claudeInstallStep(false, true),
				claudeCheckStep(),
				claudeRemoveStep(false),
				claudeCheckStep(),
			},
		},
		{
			name: "global",
			steps: []claudeArtifactStep{
				claudeInstallStep(true, false),
				claudeCheckStep(),
				claudeRemoveStep(true),
				claudeCheckStep(),
			},
		},
		{
			// Run from the home directory, whose .claude/settings.json is
			// the global one: --check reports the global install's hooks as
			// global, and --remove takes the hooks and both instructions
			// files' sections, naming each file.
			name: "global-from-home",
			dir:  "home",
			steps: []claudeArtifactStep{
				claudeInstallStep(true, false),
				claudeCheckStep(),
				claudeRemoveStep(false),
				claudeCheckStep(),
			},
		},
		{
			// Run from ~/.claude, whose CLAUDE.md is the user-level file:
			// with no hooks --check names the global install, since a
			// project install there would write project scaffolding into
			// ~/.claude/CLAUDE.md. A plain --remove takes that install's
			// section, so it takes its hooks from the global settings too,
			// as --remove --global does.
			name: "global-from-user-level-dir",
			dir:  "home/.claude",
			steps: []claudeArtifactStep{
				claudeCheckStep(),
				claudeInstallStep(true, false),
				claudeCheckStep(),
				claudeRemoveStep(true),
				claudeCheckStep(),
				claudeInstallStep(true, false),
				claudeRemoveStep(false),
				claudeCheckStep(),
			},
		},
		{
			// A global install and removal run inside a project that has its
			// own install, as bd init leaves it: the global steps use the
			// user-level files under home/.claude only, and the project's
			// hooks and section stay in place and current throughout.
			name: "global-beside-project-install",
			seed: map[string]string{
				"home/.claude/CLAUDE.md":        "# My preferences\n\nAnswer tersely.\n",
				"project/.claude/settings.json": installedSettings,
				"project/CLAUDE.md":             "# My Project\n\n" + agents.RenderSectionWithOpts(agents.ProfileMinimal, agents.DefaultRenderOpts()),
			},
			steps: []claudeArtifactStep{
				claudeInstallStep(true, false),
				claudeCheckStep(),
				claudeRemoveStep(true),
				claudeCheckStep(),
			},
		},
		{
			// User-owned settings and instructions: install merges into
			// them and --remove takes back only what bd added.
			name: "existing-user-files",
			seed: map[string]string{
				"project/.claude/settings.json": userSettings,
				"project/CLAUDE.md":             "# My Project\n\nHouse rules live here.\n",
			},
			steps: []claudeArtifactStep{
				claudeInstallStep(false, false),
				claudeCheckStep(),
				claudeRemoveStep(false),
				claudeCheckStep(),
			},
		},
		{
			// A section written by an older bd (its hash no longer matches
			// the template): --check reports it stale and install rewrites
			// it in place.
			name: "stale-section",
			seed: map[string]string{
				"project/.claude/settings.json": installedSettings,
				"project/CLAUDE.md":             "# My Project\n\n<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:00000000 -->\nOlder beads guidance.\n<!-- END BEADS INTEGRATION -->\n\n## Notes\n",
			},
			steps: []claudeArtifactStep{
				claudeCheckStep(),
				claudeInstallStep(false, false),
				claudeCheckStep(),
			},
		},
		{
			// A legacy install in settings.local.json: install migrates
			// its hook to settings.json.
			name: "legacy-local-settings",
			seed: map[string]string{
				"project/.claude/settings.local.json": legacyLocalSettings,
			},
			steps: []claudeArtifactStep{
				claudeCheckStep(),
				claudeInstallStep(false, false),
				claudeCheckStep(),
				claudeRemoveStep(false),
			},
		},
		{
			// CLAUDE.md imports AGENTS.md: the managed section goes to
			// AGENTS.md, and --remove leaves that shared section in place.
			name: "agents-import-stub",
			seed: map[string]string{
				"project/AGENTS.md": "# Agent Instructions\n\nShared by every agent.\n",
				"project/CLAUDE.md": "@AGENTS.md\n",
			},
			steps: []claudeArtifactStep{
				claudeInstallStep(false, false),
				claudeCheckStep(),
				claudeRemoveStep(false),
				claudeCheckStep(),
			},
		},
		{
			// The beads plugin enabled in the project's settings only: its
			// hooks run in this project alone, so a project install leaves
			// them to the plugin, --check judges the project's CLAUDE.md, and
			// a global install still writes its hook for every other project.
			name: "project-plugin",
			seed: map[string]string{
				"project/.claude/settings.json": projectPluginSettings,
			},
			steps: []claudeArtifactStep{
				claudeCheckStep(),
				claudeInstallStep(false, false),
				claudeCheckStep(),
				claudeInstallStep(true, false),
				claudeCheckStep(),
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			env, _, _ := newClaudeTestEnv(t)
			root := filepath.Dir(env.projectDir)
			if sc.dir != "" {
				env.projectDir = filepath.Join(root, filepath.FromSlash(sc.dir))
				if err := os.MkdirAll(env.projectDir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", sc.dir, err)
				}
			}
			for rel, content := range sc.seed {
				path := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("mkdir for seed %s: %v", rel, err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatalf("seed %s: %v", rel, err)
				}
			}

			var transcript strings.Builder
			fmt.Fprintf(&transcript, "# bd setup claude artifact transcript: %s\n", sc.name)
			fmt.Fprintf(&transcript, "# Regenerate with BD_UPDATE_GOLDEN=1 go test ./cmd/bd/setup/ -run TestClaudeSetupArtifactsGolden\n")
			previous := map[string]string{}
			writeClaudeArtifactChanges(t, &transcript, "seed", root, previous)
			for _, step := range sc.steps {
				out, errOut, err := runClaudeArtifactStep(t, env, step.run)
				fmt.Fprintf(&transcript, "\n=== %s\n", step.title)
				fmt.Fprintf(&transcript, "result: %s\n", claudeStepResult(err))
				writeTranscriptBlock(&transcript, "stdout", normalizeArtifactOutput(out, root))
				writeTranscriptBlock(&transcript, "stderr", normalizeArtifactOutput(errOut, root))
				writeClaudeArtifactChanges(t, &transcript, "files", root, previous)
			}

			assertArtifactGolden(t, filepath.Join("testdata", "claude-setup-"+sc.name+".golden"), transcript.String())
		})
	}
}

// TestClaudePluginManifestHookEvents pins the hook events and commands the
// beads Claude Code plugin declares. With the plugin enabled bd setup claude
// writes no hooks of its own (GH#3192), so these are the hooks such a user
// runs.
func TestClaudePluginManifestHookEvents(t *testing.T) {
	var manifest struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	readJSONFile(t, filepath.Join("..", "..", "..", "plugins", "beads", ".claude-plugin", "plugin.json"), &manifest)

	var got []string
	for event, entries := range manifest.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				got = append(got, fmt.Sprintf("%s matcher=%q %s: %s", event, entry.Matcher, hook.Type, hook.Command))
			}
		}
	}
	sort.Strings(got)
	want := []string{
		`PreCompact matcher="" command: bd prime`,
		`SessionStart matcher="" command: bd prime`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Claude plugin hooks changed:\ngot:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// runClaudeArtifactStep runs one step with env.stdout and the process stdout
// sent to the same file, so the transcript keeps the order a user sees:
// addHookCommand prints with fmt.Printf rather than to env.stdout. Swapping
// os.Stdout is why these tests must not run in parallel.
func runClaudeArtifactStep(t *testing.T, env claudeEnv, run func(claudeEnv) error) (stdout, stderr string, err error) {
	t.Helper()
	capture, createErr := os.CreateTemp(t.TempDir(), "stdout-*")
	if createErr != nil {
		t.Fatalf("create stdout capture: %v", createErr)
	}
	errBuf := &bytes.Buffer{}
	env.stdout, env.stderr = capture, errBuf

	origStdout := os.Stdout
	os.Stdout = capture
	func() {
		defer func() { os.Stdout = origStdout }()
		err = run(env)
	}()

	if closeErr := capture.Close(); closeErr != nil {
		t.Fatalf("close stdout capture: %v", closeErr)
	}
	data, readErr := os.ReadFile(capture.Name())
	if readErr != nil {
		t.Fatalf("read stdout capture: %v", readErr)
	}
	return string(data), errBuf.String(), err
}

func claudeStepResult(err error) string {
	if err == nil {
		return "ok"
	}
	return "error: " + err.Error()
}

// writeClaudeArtifactChanges appends every artifact file created, modified or
// deleted since previous (which it then updates), with its full contents.
func writeClaudeArtifactChanges(t *testing.T, b *strings.Builder, label, root string, previous map[string]string) {
	t.Helper()
	changed := false
	for _, rel := range claudeArtifactPaths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		before, existed := previous[rel]
		switch {
		case os.IsNotExist(err):
			if existed {
				fmt.Fprintf(b, "--- %s: deleted $ROOT/%s\n", label, rel)
				delete(previous, rel)
				changed = true
			}
		case err != nil:
			t.Fatalf("read %s: %v", rel, err)
		case !existed || before != string(data):
			verb := "created"
			if existed {
				verb = "modified"
			}
			shown := string(data)
			if strings.HasSuffix(rel, ".md") {
				shown = elideManagedSection(shown)
			}
			writeTranscriptBlock(b, fmt.Sprintf("%s: %s $ROOT/%s", label, verb, rel), shown)
			previous[rel] = string(data)
			changed = true
		}
	}
	switch {
	case changed:
	case label == "seed":
		fmt.Fprintf(b, "--- %s: none\n", label)
	default:
		fmt.Fprintf(b, "--- %s: unchanged\n", label)
	}
}

// writeTranscriptBlock appends a titled block, marking empty content and a
// missing trailing newline so neither is lost in the golden.
func writeTranscriptBlock(b *strings.Builder, title, content string) {
	fmt.Fprintf(b, "--- %s\n", title)
	switch {
	case content == "":
		b.WriteString("(empty)\n")
	case !strings.HasSuffix(content, "\n"):
		b.WriteString(content)
		b.WriteString("\n(no newline at end of file)\n")
	default:
		b.WriteString(content)
	}
}

// normalizeArtifactOutput replaces the test's temporary root with $ROOT and
// uses forward slashes, so the transcript is the same on every host.
func normalizeArtifactOutput(s, root string) string {
	s = strings.ReplaceAll(s, root, "$ROOT")
	if filepath.Separator != '/' {
		s = strings.ReplaceAll(s, string(filepath.Separator), "/")
	}
	return s
}

// assertArtifactGolden compares got with the golden file at path, or rewrites
// the file when BD_UPDATE_GOLDEN=1.
func assertArtifactGolden(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("BD_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		t.Logf("updated golden %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with BD_UPDATE_GOLDEN=1): %v", path, err)
	}
	if got == string(want) {
		return
	}
	wantLines, gotLines := strings.Split(string(want), "\n"), strings.Split(got, "\n")
	line := func(lines []string, i int) string {
		if i >= len(lines) {
			return "(end of output)"
		}
		return fmt.Sprintf("%q", lines[i])
	}
	i := 0
	for i < len(wantLines) && i < len(gotLines) && wantLines[i] == gotLines[i] {
		i++
	}
	t.Errorf("Claude agent artifacts drifted from %s (regenerate with BD_UPDATE_GOLDEN=1 if intended)\nfirst difference at line %d:\n  want: %s\n  got:  %s\n--- got ---\n%s", path, i+1, line(wantLines, i), line(gotLines, i), got)
}
