package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates/agents"
)

const testCopilotManifest = "{\"name\": \"beads\"}\n"

func newCopilotTestEnv(t *testing.T) (copilotEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return copilotEnv{
		stdout:           stdout,
		stderr:           stderr,
		manifestPath:     filepath.Join(root, filepath.FromSlash(CopilotManifestPath)),
		instructionsPath: filepath.Join(root, filepath.FromSlash(CopilotInstructionsPath)),
		manifest:         testCopilotManifest,
		renderOpts:       agents.DefaultRenderOpts,
	}, stdout, stderr
}

func writeCopilotInstructions(t *testing.T, env copilotEnv, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.instructionsPath, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func readCopilotFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

var copilotMinimalSection = agents.RenderSectionWithOpts(agents.ProfileMinimal, agents.DefaultRenderOpts())

// Install keeps every byte of an unmarked instructions file as the prefix of
// the new one, adding only the line break and blank line that set the section
// off, whatever the file's line endings, and keeps an older bd's static
// template, which it cannot tell from the user's own text.
func TestInstallCopilotKeepsUnmarkedText(t *testing.T) {
	for _, tt := range []struct {
		name, content, sep string
	}{
		{name: "final newline", content: "# Rules\n\nBe kind.\n", sep: "\n"},
		{name: "no final newline", content: "# Rules\n\nBe kind.", sep: "\n\n"},
		{name: "extra final blank lines", content: "# Rules\n\n\n", sep: "\n"},
		{name: "CRLF", content: "# Rules\r\n\r\nBe kind.\r\n", sep: "\n"},
		{name: "legacy static template", content: "# Beads\n\nRun `bd prime` for the full workflow context.\n", sep: "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.content, 0o644)
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			want := tt.content + tt.sep + copilotMinimalSection
			if got := readCopilotFile(t, env.instructionsPath); got != want {
				t.Errorf("instructions are\n%q\nwant\n%q", got, want)
			}
			if got := readCopilotFile(t, env.manifestPath); got != testCopilotManifest {
				t.Errorf("manifest is %q, want %q", got, testCopilotManifest)
			}
		})
	}
}

// A full-profile section, as bd setup claude or codex write, is kept rather
// than traded for the minimal one.
func TestInstallCopilotKeepsFullProfile(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)
	full := "# Rules\n\n" + agents.RenderSectionWithOpts(agents.ProfileFull, agents.DefaultRenderOpts())
	writeCopilotInstructions(t, env, full, 0o644)
	if err := installCopilot(env); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}
	if got := readCopilotFile(t, env.instructionsPath); got != full {
		t.Errorf("install changed a current full-profile section:\n%s", got)
	}
}

// An update keeps a narrowed file mode.
func TestInstallCopilotKeepsFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not POSIX on Windows")
	}
	env, _, _ := newCopilotTestEnv(t)
	writeCopilotInstructions(t, env, "# Private\n", 0o600)
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
		t.Errorf("mode is %v, want 0600", info.Mode().Perm())
	}
}

// Install never writes through a symlink at the instructions path, dangling
// or not, and does not claim the instructions installed.
func TestInstallCopilotSkipsSymlinkedInstructions(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		name := "link to a file"
		if dangling {
			name = "dangling link"
		}
		t.Run(name, func(t *testing.T) {
			env, stdout, stderr := newCopilotTestEnv(t)
			target := filepath.Join(filepath.Dir(filepath.Dir(env.instructionsPath)), "shared.md")
			if !dangling {
				if err := os.WriteFile(target, []byte("# Shared\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, env.instructionsPath); err != nil {
				t.Skipf("symlink: %v", err)
			}
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			if got, err := os.Readlink(env.instructionsPath); err != nil || got != target {
				t.Errorf("instructions link is %q, %v; want it kept, to %q", got, err, target)
			}
			data, err := os.ReadFile(target)
			switch {
			case dangling && !os.IsNotExist(err):
				t.Errorf("install created the dangling link's target: %q, %v", data, err)
			case !dangling && string(data) != "# Shared\n":
				t.Errorf("install changed the link's target: %q, %v", data, err)
			}
			if !strings.Contains(stderr.String(), "is a symlink") {
				t.Errorf("install did not warn of the symlink; stderr:\n%s", stderr)
			}
			if out := stdout.String(); strings.Contains(out, "integration installed") || !strings.Contains(out, "Instructions skipped") {
				t.Errorf("install output should say the instructions were skipped:\n%s", out)
			}
		})
	}
}

// Install refuses a file whose beads markers do not bound a section, and
// writes nothing.
func TestInstallCopilotRefusesMalformedMarkers(t *testing.T) {
	for name, content := range map[string]string{
		"BEGIN without END": "# Rules\n\n" + agentsBeginMarker + "\nbody\n",
		"END without BEGIN": "# Rules\n\n" + agentsEndMarker + "\n",
		"END before BEGIN":  agentsEndMarker + "\n" + agentsBeginMarker + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			env, _, stderr := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, content, 0o644)
			if err := installCopilot(env); err == nil {
				t.Fatalf("installCopilot succeeded; want a refusal")
			}
			if got := readCopilotFile(t, env.instructionsPath); got != content {
				t.Errorf("install changed the instructions to\n%q", got)
			}
			if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
				t.Errorf("install wrote the manifest, stat err = %v", err)
			}
			if !strings.Contains(stderr.String(), "malformed beads section markers") {
				t.Errorf("stderr does not name the malformed markers:\n%s", stderr)
			}
		})
	}
}
