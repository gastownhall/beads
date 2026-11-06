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
	t.Run("malformed markers", func(t *testing.T) {
		env, _, stderr := newCopilotTestEnv(t)
		if err := os.MkdirAll(filepath.Dir(env.manifestPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(env.manifestPath, []byte(testCopilotManifest), 0o644); err != nil {
			t.Fatal(err)
		}
		writeCopilotInstructions(t, env, "# Rules\n\n"+agentsBeginMarker+"\nbody\n", 0o644)
		if err := checkCopilot(env); err == nil {
			t.Errorf("checkCopilot passed malformed markers")
		}
		if !strings.Contains(stderr.String(), env.instructionsPath) || !strings.Contains(stderr.String(), "malformed beads section markers") {
			t.Errorf("--check does not name the file and its malformed markers:\n%s", stderr)
		}
	})
}

func copilotText(s string) *string { return &s }

// --remove takes the section out of a file install appended it to, giving
// back the user's text up to removeBeadsSection's normalization of the line
// breaks next to the section, and keeps unmarked text such as an older bd's
// static template.
func TestRemoveCopilotKeepsUserText(t *testing.T) {
	for _, tt := range []struct{ name, content, want string }{
		{name: "final newline", content: "# Rules\n\nBe kind.\n", want: "# Rules\n\nBe kind.\n"},
		{name: "no final newline", content: "# Rules\n\nBe kind.", want: "# Rules\n\nBe kind.\n"},
		{name: "extra final blank lines", content: "# Rules\n\n\n", want: "# Rules\n"},
		{name: "CRLF", content: "# Rules\r\n\r\nBe kind.\r\n", want: "# Rules\r\n\r\nBe kind.\r\n"},
		{name: "legacy static template", content: "# Beads\n\nRun `bd prime` for the full workflow context.\n", want: "# Beads\n\nRun `bd prime` for the full workflow context.\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.content, 0o644)
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			if err := removeCopilot(env); err != nil {
				t.Fatalf("removeCopilot: %v", err)
			}
			if got := readCopilotFile(t, env.instructionsPath); got != tt.want {
				t.Errorf("instructions are %q, want %q", got, tt.want)
			}
			if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
				t.Errorf("--remove kept the manifest, stat err = %v", err)
			}
		})
	}
}

// --remove leaves a file it holds no section in, however empty, a symlink and
// its target, and a file whose markers are malformed, which it refuses
// before removing anything.
func TestRemoveCopilotLeavesFilesWithoutASection(t *testing.T) {
	for _, tt := range []struct {
		name, content string
		fails         bool
	}{
		{name: "unmarked", content: "# Rules\n"},
		{name: "empty", content: ""},
		{name: "BEGIN without END", content: "# Rules\n\n" + agentsBeginMarker + "\nbody\n", fails: true},
		{name: "END before BEGIN", content: agentsEndMarker + "\n" + agentsBeginMarker + "\n", fails: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.content, 0o644)
			if err := os.MkdirAll(filepath.Dir(env.manifestPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(env.manifestPath, []byte(testCopilotManifest), 0o644); err != nil {
				t.Fatal(err)
			}
			err := removeCopilot(env)
			if (err != nil) != tt.fails {
				t.Errorf("removeCopilot = %v, want failure %v", err, tt.fails)
			}
			if got := readCopilotFile(t, env.instructionsPath); got != tt.content {
				t.Errorf("--remove changed the instructions to %q", got)
			}
			if _, err := os.Stat(env.manifestPath); (err == nil) != tt.fails {
				t.Errorf("manifest stat err = %v; want it kept only when --remove fails", err)
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		env, _, _ := newCopilotTestEnv(t)
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
		if err := removeCopilot(env); err != nil {
			t.Fatalf("removeCopilot: %v", err)
		}
		if got, err := os.Readlink(env.instructionsPath); err != nil || got != target {
			t.Errorf("instructions link is %q, %v; want it kept, to %q", got, err, target)
		}
		if got := readCopilotFile(t, target); got != copilotMinimalSection {
			t.Errorf("--remove changed the link's target to %q", got)
		}
	})
}

// A file install created with the section alone goes with the section, and
// so does its directory when that leaves it empty, but not one holding
// workflows.
func TestRemoveCopilotDeletesSectionOnlyFile(t *testing.T) {
	for _, workflows := range []bool{false, true} {
		name := "empty .github"
		if workflows {
			name = ".github holding workflows"
		}
		t.Run(name, func(t *testing.T) {
			env, _, _ := newCopilotTestEnv(t)
			github := filepath.Dir(env.instructionsPath)
			if workflows {
				if err := os.MkdirAll(filepath.Join(github, "workflows"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			if got := readCopilotFile(t, env.instructionsPath); got != copilotMinimalSection {
				t.Fatalf("install created %q, want the section alone", got)
			}
			if err := removeCopilot(env); err != nil {
				t.Fatalf("removeCopilot: %v", err)
			}
			if _, err := os.Lstat(env.instructionsPath); !os.IsNotExist(err) {
				t.Errorf("--remove kept the section-only file, lstat err = %v", err)
			}
			if _, err := os.Stat(github); (err == nil) != workflows {
				t.Errorf(".github stat err = %v; want it kept only when it holds workflows", err)
			}
		})
	}
}
