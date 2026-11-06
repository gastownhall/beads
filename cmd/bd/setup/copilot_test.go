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

// writeCopilotManifest installs a stand-in manifest so a check exercises the
// instructions half of the recipe; the real content only matters to install.
func writeCopilotManifest(t *testing.T, env copilotEnv) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(env.manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.manifestPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCheckCopilotStates pins the states --check tells apart: a missing
// manifest, a manifest whose instructions are missing, an unmarked user
// file, a stale section, and a current one.
func TestCheckCopilotStates(t *testing.T) {
	for _, tt := range []struct {
		name        string
		prepare     func(t *testing.T, env copilotEnv)
		wantErr     bool
		wantOut     []string
		forbidOut   []string
	}{
		{
			name:    "nothing installed",
			prepare: func(*testing.T, copilotEnv) {},
			wantErr: true,
			wantOut: []string{"Missing: " + copilotManifestPath},
		},
		{
			name:        "manifest only, instructions missing",
			prepare:     writeCopilotManifest,
			wantErr:     true,
			wantOut:     []string{copilotManifestPath, "copilot-instructions.md", "not found"},
			forbidOut:   []string{"(current)"},
		},
		{
			name: "unmarked user instructions",
			prepare: func(t *testing.T, env copilotEnv) {
				writeCopilotManifest(t, env)
				writeCopilotInstructions(t, env, "# Team rules\n\nKeep these rules.\n")
			},
			wantErr:   true,
			wantOut:   []string{"copilot-instructions.md", "no beads section"},
			forbidOut: []string{"(current)"},
		},
		{
			name: "stale section",
			prepare: func(t *testing.T, env copilotEnv) {
				writeCopilotManifest(t, env)
				writeCopilotInstructions(t, env, "# Team rules\n\n<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:00000000 -->\nOld guidance.\n<!-- END BEADS INTEGRATION -->\n")
			},
			wantErr:   true,
			wantOut:   []string{"stale"},
			forbidOut: []string{"(current)"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, out, _ := newCopilotTestEnv(t)
			tt.prepare(t, env)

			err := checkCopilot(env)
			if got := err != nil; got != tt.wantErr {
				t.Fatalf("checkCopilot err = %v, want error = %v (output:\n%s)", err, tt.wantErr, out)
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("check output lacks %q:\n%s", want, out)
				}
			}
			for _, forbid := range tt.forbidOut {
				if strings.Contains(out.String(), forbid) {
					t.Fatalf("check output must not claim %q:\n%s", forbid, out)
				}
			}
		})
	}

	t.Run("stale section becomes current after install", func(t *testing.T) {
		env, out, _ := newCopilotTestEnv(t)
		writeCopilotInstructions(t, env, "# Team rules\n\n<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:00000000 -->\nOld guidance.\n<!-- END BEADS INTEGRATION -->\n")

		if err := installCopilot(env); err != nil {
			t.Fatalf("installCopilot: %v", err)
		}
		if err := checkCopilot(env); err != nil {
			t.Fatalf("checkCopilot after install: %v (output:\n%s)", err, out)
		}
		for _, path := range []string{env.manifestPath, env.instructionsPath} {
			if !strings.Contains(out.String(), path) {
				t.Fatalf("check output lacks %s:\n%s", path, out)
			}
		}
		if !strings.Contains(out.String(), "(current)") {
			t.Fatalf("check output does not report the section current:\n%s", out)
		}
	})
}

// TestCheckCopilotRefusesUnusableFile pins that --check does not report an
// integration installed whose instructions file it cannot classify: a
// destination symlink (dangling included) and malformed markers are errors
// naming the remedy.
func TestCheckCopilotRefusesUnusableFile(t *testing.T) {
	for _, tt := range []struct {
		name    string
		prepare func(t *testing.T, env copilotEnv)
		wantOut string
	}{
		{
			name: "symlink to a real file",
			prepare: func(t *testing.T, env copilotEnv) {
				writeCopilotManifest(t, env)
				if err := os.MkdirAll("elsewhere", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join("elsewhere", "real.md"), []byte("target\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../elsewhere/real.md", env.instructionsPath); err != nil {
					t.Skipf("symlink: %v", err)
				}
			},
			wantOut: "symbolic link",
		},
		{
			name: "dangling symlink",
			prepare: func(t *testing.T, env copilotEnv) {
				writeCopilotManifest(t, env)
				if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../elsewhere/nowhere.md", env.instructionsPath); err != nil {
					t.Skipf("symlink: %v", err)
				}
			},
			wantOut: "symbolic link",
		},
		{
			name: "end marker missing",
			prepare: func(t *testing.T, env copilotEnv) {
				writeCopilotManifest(t, env)
				writeCopilotInstructions(t, env, "<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:00000000 -->\nbody\n")
			},
			wantOut: "malformed",
		},
		{
			name: "end marker before begin",
			prepare: func(t *testing.T, env copilotEnv) {
				writeCopilotManifest(t, env)
				writeCopilotInstructions(t, env, "<!-- END BEADS INTEGRATION -->\n<!-- BEGIN BEADS INTEGRATION -->\nbody\n<!-- END BEADS INTEGRATION -->\n")
			},
			wantOut: "malformed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, out, _ := newCopilotTestEnv(t)
			tt.prepare(t, env)

			if err := checkCopilot(env); err == nil {
				t.Fatalf("checkCopilot succeeded; want a refusal (output:\n%s)", out)
			}
			if !strings.Contains(out.String(), tt.wantOut) || !strings.Contains(out.String(), copilotIntegration.setupCommand) {
				t.Fatalf("refusal lacks %q or the remedy:\n%s", tt.wantOut, out)
			}
			if strings.Contains(out.String(), "(current)") {
				t.Fatalf("refusal must not report the integration installed:\n%s", out)
			}
		})
	}
}

// TestRemoveCopilotKeepsUserText pins the removal half of GH#7431: after an
// install appended a section to a user file, --remove gives the user's bytes
// back exactly (the shared helper's round trip of its own append), the
// manifest goes, and the .github directory stays.
func TestRemoveCopilotKeepsUserText(t *testing.T) {
	const userContent = "# Team rules\n\nKeep these rules.\n"
	env, _, _ := newCopilotTestEnv(t)
	writeCopilotInstructions(t, env, userContent)
	if err := installCopilot(env); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}

	if err := removeCopilot(env); err != nil {
		t.Fatalf("removeCopilot: %v", err)
	}

	if got := readCopilotInstructions(t, env); got != userContent {
		t.Fatalf("--remove changed the user's text:\ngot:  %q\nwant: %q", got, userContent)
	}
	if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
		t.Fatalf("plugin manifest still present: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(env.manifestPath)); !os.IsNotExist(err) {
		t.Fatalf("the emptied .copilot-plugin directory still present: %v", err)
	}
	if info, err := os.Stat(filepath.Dir(env.instructionsPath)); err != nil || !info.IsDir() {
		t.Fatalf("the .github directory was removed: %v", err)
	}
}

// TestRemoveCopilotAdjacentBreakNormalization pins that removing the section
// from a user file keeps its text while normalizing the blank lines that set
// the section off, as the shared helper documents: a file with extra final
// blank lines comes back with one final line break, and a CRLF file keeps
// its CRLF terminator.
func TestRemoveCopilotAdjacentBreakNormalization(t *testing.T) {
	for _, tt := range []struct {
		name string
		user string
		want string
	}{
		{
			name: "extra final blank lines collapse to one",
			user: "# Team rules\n\nKeep these rules.\n\n\n",
			want: "# Team rules\n\nKeep these rules.\n",
		},
		{
			name: "crlf terminator kept",
			user: "# Team rules\r\n\r\nKeep these rules.\r\n",
			want: "# Team rules\r\n\r\nKeep these rules.\r\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.user)
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}

			if err := removeCopilot(env); err != nil {
				t.Fatalf("removeCopilot: %v", err)
			}

			if got := readCopilotInstructions(t, env); got != tt.want {
				t.Fatalf("user text after --remove:\ngot:  %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// TestRemoveCopilotDeletesSectionOnlyFile pins the section-only lifecycle:
// what a fresh install of a missing instructions file created, --remove
// deletes along with the manifest, directories included when that left them
// empty.
func TestRemoveCopilotDeletesSectionOnlyFile(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)
	if err := installCopilot(env); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}

	if err := removeCopilot(env); err != nil {
		t.Fatalf("removeCopilot: %v", err)
	}

	for _, path := range []string{env.instructionsPath, env.manifestPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still present: %v", path, err)
		}
	}
	for _, dir := range []string{filepath.Dir(env.instructionsPath), filepath.Dir(env.manifestPath)} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("the emptied %s directory still present: %v", dir, err)
		}
	}
}

// TestRemoveCopilotLeavesUserFilesAlone pins that --remove never takes a
// file that holds no beads section: unmarked instructions keep their bytes,
// a blank unmarked file is not deleted as if it were an emptied one, and a
// missing instructions file is only reported.
func TestRemoveCopilotLeavesUserFilesAlone(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
	}{
		{name: "unmarked user file", content: "# Team rules\n\nKeep these rules.\n"},
		{name: "blank unmarked file", content: "\n"},
		{name: "empty unmarked file", content: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.content)

			if err := removeCopilot(env); err != nil {
				t.Fatalf("removeCopilot: %v", err)
			}
			if got := readCopilotInstructions(t, env); got != tt.content {
				t.Fatalf("the unmarked file changed:\ngot:  %q\nwant: %q", got, tt.content)
			}
		})
	}

	t.Run("missing instructions file", func(t *testing.T) {
		env, out, _ := newCopilotTestEnv(t)
		writeCopilotManifest(t, env)

		if err := removeCopilot(env); err != nil {
			t.Fatalf("removeCopilot: %v", err)
		}
		if _, err := os.Stat(env.instructionsPath); !os.IsNotExist(err) {
			t.Fatalf("removeCopilot created %s", env.instructionsPath)
		}
		if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
			t.Fatalf("plugin manifest still present: %v", err)
		}
		if !strings.Contains(out.String(), "No copilot-instructions.md file found") {
			t.Fatalf("the missing instructions file was not reported:\n%s", out)
		}
	})
}

// TestRemoveCopilotNothingInstalled pins the nothing-to-do case.
func TestRemoveCopilotNothingInstalled(t *testing.T) {
	env, out, _ := newCopilotTestEnv(t)

	if err := removeCopilot(env); err != nil {
		t.Fatalf("removeCopilot: %v", err)
	}
	if !strings.Contains(out.String(), "No integration files found") {
		t.Fatalf("remove with nothing installed does not say so:\n%s", out)
	}
}

// TestRemoveCopilotSymlinkUntouched pins that --remove leaves a destination
// symlink and its target alone while still taking the manifest bd owns.
func TestRemoveCopilotSymlinkUntouched(t *testing.T) {
	env, out, _ := newCopilotTestEnv(t)
	writeCopilotManifest(t, env)
	if err := os.MkdirAll("elsewhere", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("elsewhere", "real.md"), []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../elsewhere/real.md", env.instructionsPath); err != nil {
		t.Skipf("symlink: %v", err)
	}

	if err := removeCopilot(env); err != nil {
		t.Fatalf("removeCopilot: %v", err)
	}

	if info, err := os.Lstat(env.instructionsPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the instructions symlink did not survive: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join("elsewhere", "real.md")); err != nil || string(got) != "target\n" {
		t.Fatalf("the symlink target changed: %q, %v", got, err)
	}
	if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
		t.Fatalf("plugin manifest still present: %v", err)
	}
	if !strings.Contains(out.String(), "symbolic link") {
		t.Fatalf("the symlink note is missing:\n%s", out)
	}
}

// TestRemoveCopilotMalformedMarkersRefused pins that markers that are
// incomplete or reversed are an error that leaves the file unchanged.
func TestRemoveCopilotMalformedMarkersRefused(t *testing.T) {
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
			env, out, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.content)

			if err := removeCopilot(env); err == nil {
				t.Fatal("removeCopilot succeeded on malformed markers; want an error")
			}
			if got := readCopilotInstructions(t, env); got != tt.content {
				t.Fatalf("--remove changed the instructions despite the error:\ngot:  %q\nwant: %q", got, tt.content)
			}
			if !strings.Contains(out.String(), "malformed") {
				t.Fatalf("the refusal does not name the problem:\n%s", out)
			}
		})
	}
}
