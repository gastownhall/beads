package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/recipes"
	"github.com/steveyegge/beads/internal/templates/agents"
)

// newCopilotTestEnv returns a Copilot env writing to buffers, in a fresh
// working directory with deterministic render options.
func newCopilotTestEnv(t *testing.T) (copilotEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Chdir(t.TempDir())
	stubDetectRenderOpts(t)
	var out, errOut bytes.Buffer
	return copilotEnv{
		stdout:           &out,
		stderr:           &errOut,
		manifestPath:     copilotManifestPath,
		instructionsPath: copilotInstructionsPath,
		manifestContent:  copilotManifestContent,
		ensureDir:        EnsureDir,
		readFile:         os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
		removeFile: os.Remove,
	}, &out, &errOut
}

func writeCopilotInstructions(t *testing.T, env copilotEnv, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.instructionsPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readCopilotInstructions(t *testing.T, env copilotEnv) string {
	t.Helper()
	data, err := os.ReadFile(env.instructionsPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestInstallCopilotPreservesUnmarkedInstructions pins the install half of
// GH#7431: every byte of an unmarked .github/copilot-instructions.md stays
// an unchanged prefix, the manifest lands with the recipe's content, and
// exactly one minimal section is added.
func TestInstallCopilotPreservesUnmarkedInstructions(t *testing.T) {
	for _, tt := range []struct {
		name string
		user string
	}{
		{name: "plain user text", user: "# Team rules\n\nKeep these rules.\n"},
		{name: "no final newline", user: "# Team rules\n\nKeep these rules."},
		{name: "extra final blank lines", user: "# Team rules\n\nKeep these rules.\n\n\n"},
		{name: "crlf line endings", user: "# Team rules\r\n\r\nKeep these rules.\r\n"},
		{name: "legacy static template", user: recipes.CopilotInstructionsTemplate},
		{name: "legacy template with user notes", user: recipes.CopilotInstructionsTemplate + "\n## Team additions\n\nOur own rule.\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, out, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.user)

			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}

			got := readCopilotInstructions(t, env)
			if !strings.HasPrefix(got, tt.user) {
				t.Fatalf("install changed the existing bytes:\ngot:  %q\nwant prefix: %q", got, tt.user)
			}
			if n := strings.Count(got, "<!-- BEGIN BEADS INTEGRATION"); n != 1 {
				t.Fatalf("install left %d beads sections, want 1:\n%s", n, got)
			}
			if !strings.Contains(got, "profile:minimal") {
				t.Fatalf("managed section is not the minimal profile:\n%s", got)
			}
			if !strings.HasSuffix(got, "<!-- END BEADS INTEGRATION -->\n") {
				t.Fatalf("managed section does not end the file:\n%q", got)
			}

			manifest, err := os.ReadFile(env.manifestPath)
			if err != nil {
				t.Fatalf("plugin manifest not installed: %v", err)
			}
			want, err := copilotManifestContent()
			if err != nil {
				t.Fatal(err)
			}
			if string(manifest) != want {
				t.Fatalf("plugin manifest does not match the recipe content:\ngot:  %s\nwant: %s", manifest, want)
			}
			if !strings.Contains(out.String(), env.manifestPath) || !strings.Contains(out.String(), env.instructionsPath) {
				t.Fatalf("install does not name both files:\n%s", out)
			}
		})
	}
}

// TestInstallCopilotCreatesSectionOnlyFile pins what a fresh install of a
// missing instructions file writes: the rendered minimal section alone, not
// the AGENTS.md scaffolding createNewAgentsFileWithOpts adds.
func TestInstallCopilotCreatesSectionOnlyFile(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)

	if err := installCopilot(env); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}

	want := agents.RenderSectionWithOpts(agents.ProfileMinimal, agents.DefaultRenderOpts())
	if got := readCopilotInstructions(t, env); got != want {
		t.Fatalf("fresh install wrote:\n%q\nwant the section alone:\n%q", got, want)
	}
	if _, err := os.Stat(env.manifestPath); err != nil {
		t.Fatalf("plugin manifest not installed: %v", err)
	}
}

// TestInstallCopilotFullProfilePrecedence pins that a file already holding a
// full-profile section keeps it, as the shared helper preserves the
// higher-information profile.
func TestInstallCopilotFullProfilePrecedence(t *testing.T) {
	env, out, _ := newCopilotTestEnv(t)
	full := agents.RenderSectionWithOpts(agents.ProfileFull, agents.DefaultRenderOpts())
	writeCopilotInstructions(t, env, "# Mine\n\n"+full)

	if err := installCopilot(env); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}

	got := readCopilotInstructions(t, env)
	if !strings.HasPrefix(got, "# Mine\n\n") {
		t.Fatalf("install changed the text before the section:\n%s", got)
	}
	if !strings.Contains(got, "profile:full") || strings.Contains(got, "profile:minimal") {
		t.Fatalf("the full profile was not preserved:\n%s", got)
	}
	if !strings.Contains(out.String(), "full profile") {
		t.Fatalf("install does not say the full profile was preserved:\n%s", out)
	}
}

// TestInstallCopilotSymlinkInstructionsSkipped pins that a destination
// symlink is neither followed nor overwritten: the link and its target
// survive, the warning says what to do, and the skipped instructions are not
// claimed installed (GH#7431 data-loss vector).
func TestInstallCopilotSymlinkInstructionsSkipped(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target string
		create bool
	}{
		{name: "link to a real file", target: "../elsewhere/real.md", create: true},
		{name: "dangling link", target: "../elsewhere/nowhere.md"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, out, errOut := newCopilotTestEnv(t)
			if err := os.MkdirAll(filepath.Join("elsewhere"), 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.create {
				if err := os.WriteFile(filepath.Join("elsewhere", "real.md"), []byte("target\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tt.target, env.instructionsPath); err != nil {
				t.Skipf("symlink: %v", err)
			}

			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}

			if info, err := os.Lstat(env.instructionsPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the instructions symlink did not survive: %v", err)
			}
			if tt.create {
				if got, err := os.ReadFile(filepath.Join("elsewhere", "real.md")); err != nil || string(got) != "target\n" {
					t.Fatalf("the symlink target changed: %q, %v", got, err)
				}
			}
			if !strings.Contains(errOut.String(), "symlink") || !strings.Contains(errOut.String(), copilotIntegration.setupCommand) {
				t.Fatalf("the skip warning lacks the link or the remedy:\n%s", errOut)
			}
			if strings.Contains(out.String(), "integration installed") {
				t.Fatalf("the skipped instructions were claimed installed:\n%s", out)
			}
			if _, err := os.Stat(env.manifestPath); err != nil {
				t.Fatalf("plugin manifest not installed: %v", err)
			}
		})
	}
}

// TestInstallCopilotMalformedMarkersRefused pins that a file whose beads
// markers are incomplete or reversed is an error, not a section update that
// mangles user text around the markers.
func TestInstallCopilotMalformedMarkersRefused(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
	}{
		{
			name:    "end marker missing",
			content: "before\n<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:00000000 -->\nbody\n",
		},
		{
			name:    "end marker before begin",
			content: "before\n<!-- END BEADS INTEGRATION -->\n<!-- BEGIN BEADS INTEGRATION -->\nbody\n<!-- END BEADS INTEGRATION -->\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, _, errOut := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.content)

			if err := installCopilot(env); err == nil {
				t.Fatal("installCopilot succeeded on malformed markers; want an error")
			}
			if got := readCopilotInstructions(t, env); got != tt.content {
				t.Fatalf("install changed the instructions despite the error:\ngot:  %q\nwant: %q", got, tt.content)
			}
			if !strings.Contains(errOut.String(), "Error") {
				t.Fatalf("the refusal printed no error:\n%s", errOut)
			}
		})
	}
}

// TestInstallCopilotPreservesFileMode pins that updating the instructions
// file keeps the permission bits its owner set (atomicWriteFile).
func TestInstallCopilotPreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not POSIX on Windows")
	}
	env, _, _ := newCopilotTestEnv(t)
	writeCopilotInstructions(t, env, "# Team rules\n\nKeep these rules.\n")
	if err := os.Chmod(env.instructionsPath, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := installCopilot(env); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}

	info, err := os.Stat(env.instructionsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permission bits changed: got %v, want 0600", info.Mode().Perm())
	}
}
