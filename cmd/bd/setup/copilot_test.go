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
		{name: "legacy static template", content: recipes.CopilotInstructionsTemplate, sep: "\n"},
		{name: "legacy template with user notes", content: recipes.CopilotInstructionsTemplate + "\n## Team additions\n\nOur own rule.\n", sep: "\n"},
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
		"BEGIN without END":  "# Rules\n\n" + agentsBeginMarker + "\nbody\n",
		"END without BEGIN":  "# Rules\n\n" + agentsEndMarker + "\n",
		"END before BEGIN":   agentsEndMarker + "\n" + agentsBeginMarker + "\n",
		"stray inline BEGIN": "# My rules\n\nSee " + agentsBeginMarker + " for the old note.\n\nKEEP-ME-1 user paragraph.\n",
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

// --check names each state of the integration, and passes only for the
// manifest beside a current beads section.
func TestCheckCopilotStates(t *testing.T) {
	stale := "# Rules\n\n" + agentsBeginMarker + "\nold\n" + agentsEndMarker + "\n"
	for _, tt := range []struct {
		name         string
		manifest     bool
		instructions *string
		want         string
		pass         bool
	}{
		{name: "nothing installed", want: filepath.FromSlash(CopilotManifestPath) + "\n"},
		{name: "missing manifest", instructions: &copilotMinimalSection, want: filepath.FromSlash(CopilotManifestPath) + "\n"},
		{name: "missing instructions", manifest: true, want: filepath.FromSlash(CopilotInstructionsPath) + "\n"},
		{name: "unmarked instructions", manifest: true, instructions: copilotText("# Rules\n"), want: "exists but no beads section found"},
		{name: "stale section", manifest: true, instructions: &stale, want: "installed but stale"},
		{name: "current section", manifest: true, instructions: &copilotMinimalSection, want: "(current)", pass: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, stdout, _ := newCopilotTestEnv(t)
			if tt.manifest {
				if err := os.MkdirAll(filepath.Dir(env.manifestPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(env.manifestPath, []byte(testCopilotManifest), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.instructions != nil {
				writeCopilotInstructions(t, env, *tt.instructions, 0o644)
			}
			err := checkCopilot(env)
			if (err == nil) != tt.pass {
				t.Errorf("checkCopilot = %v, want pass %v", err, tt.pass)
			}
			out := stdout.String()
			if !strings.Contains(out, tt.want) {
				t.Errorf("--check output does not say %q:\n%s", tt.want, out)
			}
			if !tt.manifest && !strings.Contains(out, "integration not installed") {
				t.Errorf("--check output does not say the integration is not installed:\n%s", out)
			}
			if tt.pass && !strings.Contains(out, env.manifestPath) {
				t.Errorf("--check output does not name %s:\n%s", env.manifestPath, out)
			}
			if !tt.manifest || tt.pass {
				return
			}
			// Install makes the integration current.
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			stdout.Reset()
			if err := checkCopilot(env); err != nil {
				t.Errorf("checkCopilot after install = %v:\n%s", err, stdout)
			}
		})
	}
}

// --check fails, naming the file, for a symlinked instructions file, which
// install skips, and for markers that bound no section, and follows neither.
func TestCheckCopilotRefusesUnusableInstructions(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		env, stdout, _ := newCopilotTestEnv(t)
		if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(filepath.Dir(env.instructionsPath), "real.md")
		if err := os.WriteFile(target, []byte(copilotMinimalSection), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, env.instructionsPath); err != nil {
			t.Skipf("symlink: %v", err)
		}
		if err := checkCopilot(env); err == nil {
			t.Errorf("checkCopilot passed a symlinked instructions file:\n%s", stdout)
		}
		if out := stdout.String(); !strings.Contains(out, env.instructionsPath+" is a symlink") || !strings.Contains(out, "bd setup copilot") {
			t.Errorf("--check output does not name the symlink and the remedy:\n%s", out)
		}
	})
	t.Run("dangling symlink", func(t *testing.T) {
		env, stdout, _ := newCopilotTestEnv(t)
		writeCopilotManifest(t, env)
		if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(filepath.Dir(env.instructionsPath), "nowhere.md")
		if err := os.Symlink(target, env.instructionsPath); err != nil {
			t.Skipf("symlink: %v", err)
		}
		if err := checkCopilot(env); err == nil {
			t.Errorf("checkCopilot passed a dangling instructions symlink:\n%s", stdout)
		}
		out := stdout.String()
		if !strings.Contains(out, env.instructionsPath+" is a symlink") || !strings.Contains(out, "bd setup copilot") {
			t.Errorf("--check output does not name the symlink and the remedy:\n%s", out)
		}
		if strings.Contains(out, "(current)") || strings.Contains(out, "Missing:") {
			t.Errorf("--check misreports a dangling symlink:\n%s", out)
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Errorf("--check created the dangling link's target, lstat err = %v", err)
		}
	})
	for name, content := range map[string]string{
		"BEGIN without END": "# Rules\n\n" + agentsBeginMarker + "\nbody\n",
		"END without BEGIN": "# Rules\n\n" + agentsEndMarker + "\n",
		"END before BEGIN":  agentsEndMarker + "\n" + agentsBeginMarker + "\nbody\n" + agentsEndMarker + "\n",
	} {
		t.Run("malformed markers/"+name, func(t *testing.T) {
			env, stdout, stderr := newCopilotTestEnv(t)
			writeCopilotManifest(t, env)
			writeCopilotInstructions(t, env, content, 0o644)
			if err := checkCopilot(env); err == nil {
				t.Errorf("checkCopilot passed malformed markers")
			}
			if !strings.Contains(stderr.String(), env.instructionsPath) || !strings.Contains(stderr.String(), "malformed beads section markers") {
				t.Errorf("--check does not name the file and its malformed markers:\n%s", stderr)
			}
			if strings.Contains(stdout.String(), "(current)") {
				t.Errorf("--check reported malformed markers current:\n%s", stdout)
			}
			if got := readCopilotFile(t, env.instructionsPath); got != content {
				t.Errorf("--check changed the instructions to\n%q", got)
			}
		})
	}
}

// --check fails, naming the manifest path and the error, when the manifest
// cannot be inspected for a reason other than its absence, rather than
// reporting the integration installed or merely missing.
func TestCheckCopilotReportsManifestStatError(t *testing.T) {
	env, stdout, stderr := newCopilotTestEnv(t)
	// A regular file where the manifest's directory belongs makes stat of the
	// manifest fail with ENOTDIR, not ENOENT.
	if err := os.WriteFile(filepath.Dir(env.manifestPath), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.manifestPath); err == nil || os.IsNotExist(err) {
		t.Skipf("stat through a file is %v here, not a non-ENOENT error", err)
	}
	writeCopilotInstructions(t, env, copilotMinimalSection, 0o644)
	if err := checkCopilot(env); err == nil {
		t.Fatalf("checkCopilot passed a manifest it could not inspect:\n%s", stdout)
	}
	if !strings.Contains(stderr.String(), "failed to inspect "+env.manifestPath) {
		t.Errorf("--check does not name the manifest it could not inspect:\n%s", stderr)
	}
	if out := stdout.String(); strings.Contains(out, "(current)") || strings.Contains(out, "Missing:") {
		t.Errorf("--check misreports an uninspectable manifest:\n%s", out)
	}
}

// writeCopilotManifest installs the test manifest so a check exercises the
// instructions half of the recipe.
func writeCopilotManifest(t *testing.T, env copilotEnv) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(env.manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.manifestPath, []byte(testCopilotManifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copilotText(s string) *string { return &s }
