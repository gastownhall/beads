package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates/agents"
)

func stubMuxEnvProvider(t *testing.T, env agentsEnv) {
	t.Helper()
	orig := muxEnvProvider
	muxEnvProvider = func() agentsEnv {
		return env
	}
	t.Cleanup(func() { muxEnvProvider = orig })
}

// stubMuxHome points muxUserHomeDir at a new, empty home directory and
// returns it.
func stubMuxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	orig := muxUserHomeDir
	muxUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { muxUserHomeDir = orig })
	return home
}

// muxUserLevelSection is the managed section a global install writes to
// ~/.mux/AGENTS.md.
func muxUserLevelSection() string {
	return agents.RenderSectionWithOpts(agents.ProfileFull, agents.UserLevelRenderOpts())
}

func readMuxFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeMuxFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestInstallMuxCreatesNewFile(t *testing.T) {
	env, stdout, _ := newFactoryTestEnv(t)
	if err := installMux(env, false, false); err != nil {
		t.Fatalf("installMux returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Mux integration installed") {
		t.Error("expected Mux install success message")
	}
	if !strings.Contains(stdout.String(), muxAgentInstructionsURL) {
		t.Error("expected Mux docs URL in install output")
	}
}

func TestInstallMuxReportsSkippedSymlinkRootInstructions(t *testing.T) {
	env, stdout, stderr := newFactoryTestEnv(t)
	target := filepath.Join(filepath.Dir(env.agentsPath), "SHARED_AGENTS.md")
	if err := os.WriteFile(target, []byte("# Shared instructions\n"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, env.agentsPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := installMux(env, false, false); err != nil {
		t.Fatalf("installMux returned error: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Mux hooks installed; managed section skipped") {
		t.Fatalf("expected partial Mux success message, got:\n%s", out)
	}
	if strings.Contains(out, "Mux integration installed") {
		t.Fatalf("should not report full Mux integration success when root instructions are skipped:\n%s", out)
	}
	if !strings.Contains(stderr.String(), "AGENTS.md is a symlink") {
		t.Fatalf("expected symlink warning on stderr, got:\n%s", stderr.String())
	}
	if !FileExists(muxProjectHookPathsFirst(env.agentsPath)) {
		t.Fatalf("expected Mux init hook to be installed")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if strings.Contains(string(data), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("symlink target should remain untouched:\n%s", data)
	}
}

func muxProjectHookPathsFirst(baseAgentsPath string) string {
	initPath, _, _ := muxProjectHookPaths(baseAgentsPath)
	return initPath
}

func TestCheckMuxMissingFile(t *testing.T) {
	env, stdout, _ := newFactoryTestEnv(t)
	err := checkMux(env, false, false)
	if err == nil {
		t.Fatal("expected error for missing AGENTS.md")
	}
	if !strings.Contains(stdout.String(), "bd setup mux") {
		t.Error("expected setup guidance for mux")
	}
}

// --check fails on a hook missing from a workspace and names the setup that
// installs it: only a run from ~/.mux, which is no workspace, skips the hooks.
func TestCheckMuxReportsMissingHook(t *testing.T) {
	stubDetectRenderOpts(t)
	stubMuxHome(t)
	env, stdout, _ := newFactoryTestEnv(t)
	if err := installMux(env, false, false); err != nil {
		t.Fatalf("installMux: %v", err)
	}
	initPath := muxProjectHookPathsFirst(env.agentsPath)
	if err := os.Remove(initPath); err != nil {
		t.Fatalf("remove %s: %v", initPath, err)
	}

	stdout.Reset()
	if err := checkMux(env, false, false); !errors.Is(err, errMuxHooksMissing) {
		t.Fatalf("expected errMuxHooksMissing, got %v\n%s", err, stdout.String())
	}
	if out := stdout.String(); !strings.Contains(out, "✗ Missing Mux hooks: "+initPath+"\n") ||
		!strings.Contains(out, "Run: bd setup mux\n") {
		t.Errorf("expected the missing hook and the setup hint, got:\n%s", out)
	}
}

// bd cannot tell that it runs in ~/.mux when it cannot resolve the home
// directory, so it takes the directory for a workspace: setup installs the
// hooks and the workspace layer, and --check asks for them.
func TestMuxUnresolvedHomeSetsUpTheWorkspace(t *testing.T) {
	stubDetectRenderOpts(t)
	orig := muxUserHomeDir
	muxUserHomeDir = func() (string, error) { return "", errors.New("no home directory") }
	t.Cleanup(func() { muxUserHomeDir = orig })

	env, stdout, _ := newFactoryTestEnv(t)
	if err := installMux(env, true, false); err != nil {
		t.Fatalf("installMux(project=true): %v\n%s", err, stdout.String())
	}
	initPath := muxProjectHookPathsFirst(env.agentsPath)
	for _, path := range []string{initPath, muxProjectAgentsPath(env.agentsPath)} {
		if !FileExists(path) {
			t.Errorf("expected %s installed:\n%s", path, stdout.String())
		}
	}
	if err := checkMux(env, true, false); err != nil {
		t.Fatalf("checkMux(project=true): %v\n%s", err, stdout.String())
	}
	if err := os.Remove(initPath); err != nil {
		t.Fatalf("remove %s: %v", initPath, err)
	}
	if err := checkMux(env, true, false); !errors.Is(err, errMuxHooksMissing) {
		t.Fatalf("expected errMuxHooksMissing, got %v\n%s", err, stdout.String())
	}
}

func TestMuxProjectAgentsPath(t *testing.T) {
	if got, want := muxProjectAgentsPath("AGENTS.md"), filepath.Join(".mux", "AGENTS.md"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	agentsPath := filepath.Join(t.TempDir(), "AGENTS.md")
	if got, want := muxProjectAgentsPath(agentsPath), filepath.Join(filepath.Dir(agentsPath), ".mux", "AGENTS.md"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMuxProjectHookPaths(t *testing.T) {
	initPath, toolPostPath, toolEnvPath := muxProjectHookPaths("AGENTS.md")
	if want := filepath.Join(".mux", "init"); initPath != want {
		t.Fatalf("init path = %q, want %q", initPath, want)
	}
	if want := filepath.Join(".mux", "tool_post"); toolPostPath != want {
		t.Fatalf("tool_post path = %q, want %q", toolPostPath, want)
	}
	if want := filepath.Join(".mux", "tool_env"); toolEnvPath != want {
		t.Fatalf("tool_env path = %q, want %q", toolEnvPath, want)
	}
}

func TestInstallMuxProjectInstallsBothLayers(t *testing.T) {
	stubDetectRenderOpts(t)
	stubMuxHome(t)
	env, _, _ := newFactoryTestEnv(t)
	if err := installMux(env, true, false); err != nil {
		t.Fatalf("installMux(project=true) returned error: %v", err)
	}
	// Both layers belong to the project, so both get its rendering.
	want := createNewAgentsFileWithOpts(agents.ProfileFull, agents.DefaultRenderOpts())
	for _, path := range []string{env.agentsPath, muxProjectAgentsPath(env.agentsPath)} {
		if got := readMuxFile(t, path); got != want {
			t.Errorf("%s should get the project rendering:\ngot:\n%s\nwant:\n%s", path, got, want)
		}
	}
}

func TestCheckMuxProjectRequiresBothLayers(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	if err := installMux(env, false, false); err != nil {
		t.Fatalf("installMux(project=false) returned error: %v", err)
	}
	if err := checkMux(env, true, false); err == nil {
		t.Fatal("expected project check to fail when .mux/AGENTS.md is missing")
	}
}

func TestRemoveMuxProjectRemovesBothLayers(t *testing.T) {
	env, stdout, _ := newFactoryTestEnv(t)
	if err := installMux(env, true, false); err != nil {
		t.Fatalf("installMux(project=true) returned error: %v", err)
	}
	stdout.Reset()
	if err := removeMux(env, true, false); err != nil {
		t.Fatalf("removeMux(project=true) returned error: %v", err)
	}
	// Each removal names its own layer and file.
	for _, header := range []string{
		"Removing " + muxIntegration.name + " integration from " + env.agentsPath + "...\n",
		"Removing " + muxProjectIntegration.name + " integration from " + muxProjectAgentsPath(env.agentsPath) + "...\n",
	} {
		if !strings.Contains(stdout.String(), header) {
			t.Errorf("expected %q, got:\n%s", header, stdout.String())
		}
	}

	for _, path := range []string{env.agentsPath, muxProjectAgentsPath(env.agentsPath)} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("expected %s to remain readable after remove: %v", path, err)
		}
		content := string(data)
		if strings.Contains(content, agentsBeginMarker) || strings.Contains(content, agentsEndMarker) {
			t.Fatalf("expected beads markers removed from %s", path)
		}
	}

	if err := checkMux(env, true, false); err == nil {
		t.Fatal("expected project check to fail after remove")
	}
}

func TestMuxGlobalAgentsPath(t *testing.T) {
	t.Cleanup(func() {
		muxUserHomeDir = os.UserHomeDir
	})
	home := t.TempDir()
	muxUserHomeDir = func() (string, error) {
		return home, nil
	}

	got, err := muxGlobalAgentsPath()
	if err != nil {
		t.Fatalf("muxGlobalAgentsPath returned error: %v", err)
	}
	if want := filepath.Join(home, ".mux", "AGENTS.md"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Mux loads ~/.mux/AGENTS.md in every workspace, so a global install writes
// it with just the managed section, worded for repositories that use beads,
// and leaves the root AGENTS.md with the project's rendering.
func TestInstallMuxGlobalInstallsGlobalLayer(t *testing.T) {
	stubDetectRenderOpts(t)
	env, _, _ := newFactoryTestEnv(t)
	home := stubMuxHome(t)

	if err := installMux(env, false, true); err != nil {
		t.Fatalf("installMux(global=true) returned error: %v", err)
	}

	globalPath := filepath.Join(home, ".mux", "AGENTS.md")
	if got, want := readMuxFile(t, globalPath), muxUserLevelSection(); got != want {
		t.Errorf("global AGENTS.md should hold only the user-level section:\ngot:\n%s\nwant:\n%s", got, want)
	}
	if got, want := readMuxFile(t, env.agentsPath), createNewAgentsFileWithOpts(agents.ProfileFull, agents.DefaultRenderOpts()); got != want {
		t.Errorf("root AGENTS.md should keep the project rendering:\ngot:\n%s\nwant:\n%s", got, want)
	}
	if err := checkMux(env, false, true); err != nil {
		t.Fatalf("checkMux(global=true) returned error: %v", err)
	}
}

// ~/.mux/AGENTS.md is shared by every project, so a global install renders
// and checks it the same way whatever beads config the current directory has:
// otherwise --check --global from one project calls the section another
// project wrote stale, and following its hint flips it back.
func TestMuxGlobalSectionIgnoresProjectConfig(t *testing.T) {
	configs := []struct {
		name string
		opts agents.RenderOpts
	}{
		{"remote", agents.RenderOpts{HasRemote: true}},
		{"no remote", agents.RenderOpts{HasRemote: false}},
		{"no push", agents.RenderOpts{HasRemote: true, NoPush: true}},
	}
	wantHash := agents.CurrentHashWithOpts(agents.ProfileFull, agents.UserLevelRenderOpts())
	orig := detectRenderOptsImpl
	t.Cleanup(func() { detectRenderOptsImpl = orig })
	for _, install := range configs {
		for _, check := range configs {
			t.Run(install.name+" install, "+check.name+" check", func(t *testing.T) {
				home := stubMuxHome(t)
				globalPath := filepath.Join(home, ".mux", "AGENTS.md")
				installEnv, _, _ := newFactoryTestEnv(t)
				installOpts := install.opts
				detectRenderOptsImpl = func() agents.RenderOpts { return installOpts }
				if err := installMux(installEnv, false, true); err != nil {
					t.Fatalf("installMux: %v", err)
				}
				if got := readMuxFile(t, globalPath); !strings.Contains(got, "hash:"+wantHash) {
					t.Errorf("expected hash %s regardless of project config, got:\n%s", wantHash, got)
				}

				// --check --global from another project, set up under its
				// own config.
				checkEnv, stdout, _ := newFactoryTestEnv(t)
				checkOpts := check.opts
				detectRenderOptsImpl = func() agents.RenderOpts { return checkOpts }
				if err := installMux(checkEnv, false, false); err != nil {
					t.Fatalf("installMux (other project): %v", err)
				}
				stdout.Reset()
				if err := checkMux(checkEnv, false, true); err != nil {
					t.Fatalf("checkMux: %v\n%s", err, stdout.String())
				}
				if !strings.Contains(stdout.String(), globalPath+" (current)") {
					t.Errorf("expected the global layer reported current, got:\n%s", stdout.String())
				}
			})
		}
	}
}

// A ~/.mux/AGENTS.md section an older bd rendered for a project is stale:
// --check names the global install, which rewords the section in place and
// keeps everything around it, including the project scaffolding an older
// global install put in a new file.
func TestInstallMuxGlobalRewordsProjectSection(t *testing.T) {
	stubDetectRenderOpts(t)
	for _, tt := range []struct {
		name    string
		section string
		seed    string
	}{
		{
			name:    "beside user text",
			section: agents.RenderSection(agents.ProfileFull),
			seed:    "# Mine\n\n" + agents.RenderSection(agents.ProfileFull),
		},
		{
			name:    "in an older global install's new file",
			section: agents.RenderSectionWithOpts(agents.ProfileFull, agents.RenderOpts{}),
			seed:    createNewAgentsFileWithOpts(agents.ProfileFull, agents.RenderOpts{}),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, stdout, _ := newFactoryTestEnv(t)
			home := stubMuxHome(t)
			globalPath := filepath.Join(home, ".mux", "AGENTS.md")
			writeMuxFile(t, globalPath, tt.seed)
			if err := installMux(env, false, false); err != nil {
				t.Fatalf("installMux: %v", err)
			}

			stdout.Reset()
			if err := checkMux(env, false, true); !errors.Is(err, errBeadsSectionStale) {
				t.Fatalf("expected errBeadsSectionStale, got %v\n%s", err, stdout.String())
			}
			if !strings.Contains(stdout.String(), "Run: bd setup mux --global (to update)") {
				t.Errorf("expected the global setup hint, got:\n%s", stdout.String())
			}

			if err := installMux(env, false, true); err != nil {
				t.Fatalf("installMux(global=true): %v", err)
			}
			want := strings.Replace(tt.seed, tt.section, muxUserLevelSection(), 1)
			if got := readMuxFile(t, globalPath); got != want {
				t.Errorf("global AGENTS.md after the global install:\ngot:\n%s\nwant:\n%s", got, want)
			}
			stdout.Reset()
			if err := checkMux(env, false, true); err != nil {
				t.Fatalf("checkMux: %v\n%s", err, stdout.String())
			}
		})
	}
}

// Run from the home directory, the workspace layer's .mux/AGENTS.md is
// ~/.mux/AGENTS.md, and run from ~/.mux, so is the root AGENTS.md. That file
// is managed as the global layer whichever layer names it, so one run does
// not write it one way and then the other, --check does not call it stale,
// and remove reports it as the global layer's.
func TestMuxLayerAtGlobalPathIsGlobalLayer(t *testing.T) {
	stubDetectRenderOpts(t)
	inHome := func(home string) string { return home }
	inMuxHome := func(home string) string { return filepath.Join(home, ".mux") }
	for _, tt := range []struct {
		name            string
		dir             func(home string) string // the directory bd runs in
		link            bool                     // run through a symlink to dir
		project, global bool
		hooks           bool // the run installs workspace hooks
	}{
		{name: "workspace layer from home", dir: inHome, project: true, global: true, hooks: true},
		{name: "workspace layer alone from home", dir: inHome, project: true, hooks: true},
		{name: "workspace layer from a symlink to home", dir: inHome, link: true, project: true, global: true, hooks: true},
		{name: "root layer from ~/.mux", dir: inMuxHome, global: true},
		{name: "root layer alone from ~/.mux", dir: inMuxHome},
	} {
		t.Run(tt.name, func(t *testing.T) {
			homeDir := stubMuxHome(t)
			dir := tt.dir(homeDir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
			if tt.link {
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(dir, link); err != nil {
					t.Skipf("symlink: %v", err)
				}
				dir = link
			}
			stdout := &bytes.Buffer{}
			env := agentsEnv{agentsPath: filepath.Join(dir, "AGENTS.md"), stdout: stdout, stderr: &bytes.Buffer{}}
			globalPath := filepath.Join(homeDir, ".mux", "AGENTS.md")

			if err := installMux(env, tt.project, tt.global); err != nil {
				t.Fatalf("installMux: %v\n%s", err, stdout.String())
			}
			if got, want := readMuxFile(t, globalPath), muxUserLevelSection(); got != want {
				t.Errorf("global AGENTS.md should hold only the user-level section:\ngot:\n%s\nwant:\n%s", got, want)
			}
			// The home directory's hooks are in ~/.mux; ~/.mux gets none.
			if got := FileExists(muxProjectHookPathsFirst(env.agentsPath)); got != tt.hooks {
				t.Errorf("workspace hooks installed = %v, want %v", got, tt.hooks)
			}
			stdout.Reset()
			if err := checkMux(env, tt.project, tt.global); err != nil {
				t.Fatalf("checkMux: %v\n%s", err, stdout.String())
			}
			// Every layer that reports the global file reports it as the
			// global layer, whose hints name bd setup mux --global.
			current := globalPath + " (current)"
			if n := strings.Count(stdout.String(), current); n == 0 ||
				n != strings.Count(stdout.String(), muxGlobalIntegration.name+" integration installed: "+current) {
				t.Errorf("expected the global file reported as the global layer, got:\n%s", stdout.String())
			}
			stdout.Reset()
			if err := installMux(env, tt.project, tt.global); err != nil {
				t.Fatalf("installMux (rerun): %v\n%s", err, stdout.String())
			}
			if strings.Contains(stdout.String(), "Updated existing beads section") {
				t.Errorf("a rerun rewrote a section:\n%s", stdout.String())
			}

			// Remove takes the section out as the global layer's, both in
			// the layer that names the file and, with --global, in the
			// global layer, and each removal names the global file.
			stdout.Reset()
			if err := removeMux(env, tt.project, tt.global); err != nil {
				t.Fatalf("removeMux: %v\n%s", err, stdout.String())
			}
			if got := readMuxFile(t, globalPath); got != "" {
				t.Errorf("remove should remove the global section, left %q", got)
			}
			want := 1
			if tt.global {
				want++
			}
			if n := strings.Count(stdout.String(), "Removing "+muxGlobalIntegration.name+" integration from "+globalPath+"..."); n != want {
				t.Errorf("expected %d removals reported as the global layer's, got %d:\n%s", want, n, stdout.String())
			}
			// No other file's removal is reported as the global layer's.
			if n := strings.Count(stdout.String(), "Removing "+muxGlobalIntegration.name+" integration from "); n != want {
				t.Errorf("expected only the global file's %d removals reported as the global layer's, got %d:\n%s", want, n, stdout.String())
			}
		})
	}

	// Only a root file named AGENTS.md is the global one: an agents file of
	// another name in ~/.mux is the root layer's own. bd still runs in no
	// workspace there, so it installs no hooks or workspace layer, and
	// --check asks for neither.
	t.Run("root layer with another agents file from ~/.mux", func(t *testing.T) {
		homeDir := stubMuxHome(t)
		dir := filepath.Join(homeDir, ".mux")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		stdout := &bytes.Buffer{}
		env := agentsEnv{agentsPath: filepath.Join(dir, "TEAM.md"), stdout: stdout, stderr: &bytes.Buffer{}}
		if err := installMux(env, false, false); err != nil {
			t.Fatalf("installMux: %v", err)
		}
		if got, want := readMuxFile(t, env.agentsPath), createNewAgentsFileWithOpts(agents.ProfileFull, agents.DefaultRenderOpts()); got != want {
			t.Errorf("TEAM.md should get the project rendering:\ngot:\n%s\nwant:\n%s", got, want)
		}
		if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
			t.Errorf("expected no global AGENTS.md, stat err = %v", err)
		}
		nested := muxProjectDir(env.agentsPath)
		if _, err := os.Stat(nested); !os.IsNotExist(err) {
			t.Errorf("setup from ~/.mux should not create %s, stat err = %v", nested, err)
		}
		stdout.Reset()
		if err := checkMux(env, false, false); err != nil {
			t.Fatalf("checkMux: %v\n%s", err, stdout.String())
		}
		// Nor does it install, or --check ask for, a workspace layer there.
		stdout.Reset()
		if err := installMux(env, true, false); err != nil {
			t.Fatalf("installMux(project=true): %v\n%s", err, stdout.String())
		}
		if err := checkMux(env, true, false); err != nil {
			t.Fatalf("checkMux(project=true): %v\n%s", err, stdout.String())
		}
		if _, err := os.Stat(nested); !os.IsNotExist(err) {
			t.Errorf("setup --project from ~/.mux should not create %s, stat err = %v", nested, err)
		}
		if n := strings.Count(stdout.String(), muxNoWorkspaceLayerNote+"\n"); n != 2 {
			t.Errorf("expected setup and --check each to note the skipped workspace layer, got %d:\n%s", n, stdout.String())
		}
	})
}

// Run from ~/.mux, the root AGENTS.md is the global layer's file, and ~/.mux
// is no workspace: hooks there would land in ~/.mux/.mux, which Mux does not
// load, and so would the workspace layer's AGENTS.md. So --check there passes
// after a --global install from a project, setup there installs no hooks
// and, with --project, no workspace layer, and remove there clears what an
// older bd installed in ~/.mux/.mux, along with the global layer's section,
// which setup there installs. Setup and --check there name the hooks an
// older bd installed in ~/.mux/.mux, and only those.
func TestMuxRunFromMuxHomeHasNoWorkspaceHooks(t *testing.T) {
	stubDetectRenderOpts(t)
	homeDir := stubMuxHome(t)
	muxHome := filepath.Join(homeDir, ".mux")
	nested := filepath.Join(muxHome, ".mux")

	projectEnv, _, _ := newFactoryTestEnv(t)
	if err := installMux(projectEnv, false, true); err != nil {
		t.Fatalf("installMux(global=true) from a project: %v", err)
	}

	// The CLI names the agents file with a bare name, relative to where bd runs.
	t.Chdir(muxHome)
	stdout := &bytes.Buffer{}
	env := agentsEnv{agentsPath: "AGENTS.md", stdout: stdout, stderr: &bytes.Buffer{}}
	for _, project := range []bool{false, true} {
		stdout.Reset()
		if err := checkMux(env, project, false); err != nil {
			t.Fatalf("checkMux(project=%v) from ~/.mux after a --global install: %v\n%s", project, err, stdout.String())
		}
		if err := installMux(env, project, false); err != nil {
			t.Fatalf("installMux(project=%v) from ~/.mux: %v\n%s", project, err, stdout.String())
		}
		if _, err := os.Stat(nested); !os.IsNotExist(err) {
			t.Fatalf("setup (project=%v) from ~/.mux should not create %s, stat err = %v", project, nested, err)
		}
		if err := checkMux(env, project, false); err != nil {
			t.Fatalf("checkMux(project=%v) from ~/.mux: %v\n%s", project, err, stdout.String())
		}
		want := 0
		if project {
			want = 3 // --check, setup and --check again
		}
		if n := strings.Count(stdout.String(), muxNoWorkspaceLayerNote+"\n"); n != want {
			t.Errorf("project=%v: expected the skipped workspace layer noted %d times, got %d:\n%s", project, want, n, stdout.String())
		}
		if strings.Contains(stdout.String(), muxNestedHooksNote) {
			t.Errorf("project=%v: noted hooks in %s, which has none:\n%s", project, nested, stdout.String())
		}
	}

	// Hooks an older bd installed in ~/.mux/.mux: setup and --check there
	// name them, and the cleanup, and leave them where they are.
	if err := installMuxProjectHooks(env); err != nil {
		t.Fatalf("installing an older bd's hooks: %v", err)
	}
	initPath, toolPostPath, toolEnvPath := muxProjectHookPaths(env.agentsPath)
	note := muxNestedHooksNote + strings.Join([]string{initPath, toolPostPath, toolEnvPath}, ", ") + "\n"
	stdout.Reset()
	if err := checkMux(env, false, false); err != nil {
		t.Fatalf("checkMux from ~/.mux with an older bd's hooks: %v\n%s", err, stdout.String())
	}
	if err := installMux(env, false, false); err != nil {
		t.Fatalf("installMux from ~/.mux with an older bd's hooks: %v\n%s", err, stdout.String())
	}
	if n := strings.Count(stdout.String(), note); n != 2 {
		t.Errorf("expected --check and setup each to name the older bd's hooks, got %d notes:\n%s", n, stdout.String())
	}
	if !strings.Contains(stdout.String(), "bd setup mux --remove") {
		t.Errorf("expected the note to name the --remove cleanup:\n%s", stdout.String())
	}
	for _, path := range []string{initPath, toolPostPath, toolEnvPath} {
		if got := readMuxFile(t, path); !strings.Contains(got, muxHookMarkerBegin) {
			t.Errorf("setup from ~/.mux should leave the older bd's hook %s alone, got:\n%s", path, got)
		}
	}
	stdout.Reset()
	if err := removeMux(env, false, false); err != nil {
		t.Fatalf("removeMux from ~/.mux: %v\n%s", err, stdout.String())
	}
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Errorf("remove from ~/.mux should clear %s, stat err = %v", nested, err)
	}
	if got := readMuxFile(t, filepath.Join(muxHome, "AGENTS.md")); got != "" {
		t.Errorf("remove from ~/.mux should remove the global section, left %q", got)
	}
	if !strings.Contains(stdout.String(), "Removing "+muxGlobalIntegration.name+" integration from "+filepath.Join(muxHome, "AGENTS.md")+"...") {
		t.Errorf("expected the removal reported as the global layer's, got:\n%s", stdout.String())
	}

	// --remove --project there also takes out the section an older bd's
	// --project wrote to ~/.mux/.mux/AGENTS.md.
	nestedAgents := filepath.Join(nested, "AGENTS.md")
	writeMuxFile(t, nestedAgents, createNewAgentsFileWithOpts(agents.ProfileFull, agents.DefaultRenderOpts()))
	stdout.Reset()
	if err := removeMux(env, true, false); err != nil {
		t.Fatalf("removeMux(project=true) from ~/.mux: %v\n%s", err, stdout.String())
	}
	if got := readMuxFile(t, nestedAgents); containsBeadsMarker(got) {
		t.Errorf("remove --project from ~/.mux should remove the section from %s, left:\n%s", nestedAgents, got)
	}

	// A hook of the user's own there is not one bd installed: setup does not
	// name it.
	writeMuxFile(t, initPath, "#!/bin/sh\necho mine\n")
	stdout.Reset()
	if err := installMux(env, false, false); err != nil {
		t.Fatalf("installMux from ~/.mux with a custom hook: %v\n%s", err, stdout.String())
	}
	if strings.Contains(stdout.String(), muxNestedHooksNote) {
		t.Errorf("setup from ~/.mux named a hook bd did not install:\n%s", stdout.String())
	}
}

func TestRemoveMuxGlobalRemovesGlobalLayerSection(t *testing.T) {
	for _, tt := range []struct {
		name string
		seed string
		want string
	}{
		// A new global file holds only the section, so removing it
		// leaves the file empty.
		{name: "new file", want: ""},
		{name: "user text", seed: "# Mine\n", want: "# Mine\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newFactoryTestEnv(t)
			home := stubMuxHome(t)
			globalPath := filepath.Join(home, ".mux", "AGENTS.md")
			if tt.seed != "" {
				writeMuxFile(t, globalPath, tt.seed)
			}

			if err := installMux(env, false, true); err != nil {
				t.Fatalf("installMux(global=true) returned error: %v", err)
			}
			if err := removeMux(env, false, true); err != nil {
				t.Fatalf("removeMux(global=true) returned error: %v", err)
			}

			if got := readMuxFile(t, globalPath); got != tt.want {
				t.Fatalf("global AGENTS.md after remove = %q, want %q", got, tt.want)
			}
		})
	}
}

// Remove takes bd's hooks out of a .mux directory and then removes the
// directory if they were all it held, but it never removes a .mux that is a
// symlink, since that unlinks it whatever its target holds, nor a directory
// it took no hook from. Run from the home directory that .mux is ~/.mux,
// which Xum's migration replaces with a link to ~/.xum; with --global, the
// global section in the link's target goes too.
func TestRemoveMuxKeepsMuxDirItDidNotEmpty(t *testing.T) {
	stubDetectRenderOpts(t)
	for _, tt := range []struct {
		name    string
		global  bool
		home    bool // bd runs in the home directory, else in a workspace
		link    bool // .mux is a symlink to a directory beside it
		install bool // set up first, with --global from the home directory
	}{
		{name: "~/.mux linked to ~/.xum, from home", home: true, link: true, install: true},
		{name: "~/.mux linked to ~/.xum, from home with --global", home: true, link: true, install: true, global: true},
		{name: "~/.mux linked to ~/.xum, from home, nothing installed", home: true, link: true},
		{name: "a workspace's .mux linked to a directory holding only bd's hooks", link: true, install: true},
		{name: "a workspace's .mux bd installed no hooks in"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := stubMuxHome(t)
			dir := home
			if !tt.home {
				dir = t.TempDir()
			}
			muxDir := filepath.Join(dir, ".mux")
			target := muxDir
			if tt.link {
				target = filepath.Join(dir, ".xum")
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", target, err)
			}
			if tt.link {
				if err := os.Symlink(target, muxDir); err != nil {
					t.Skipf("symlink: %v", err)
				}
			}
			// The CLI names the agents file with a bare name, relative to
			// where bd runs.
			t.Chdir(dir)
			stdout := &bytes.Buffer{}
			env := agentsEnv{agentsPath: "AGENTS.md", stdout: stdout, stderr: &bytes.Buffer{}}
			if tt.install {
				if err := installMux(env, false, tt.home); err != nil {
					t.Fatalf("installMux: %v\n%s", err, stdout.String())
				}
			}

			stdout.Reset()
			if err := removeMux(env, false, tt.global); err != nil {
				t.Fatalf("removeMux: %v\n%s", err, stdout.String())
			}
			info, err := os.Lstat(muxDir)
			if err != nil {
				t.Fatalf("remove should keep %s, lstat err = %v\n%s", muxDir, err, stdout.String())
			}
			if got := info.Mode()&os.ModeSymlink != 0; got != tt.link {
				t.Errorf("%s is a symlink = %v, want %v", muxDir, got, tt.link)
			}
			if tt.link {
				requireLinkKept(t, muxDir)
			}
			for _, hook := range []string{"init", "tool_post", "tool_env"} {
				path := filepath.Join(target, hook)
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Errorf("remove should remove the hook %s, lstat err = %v", path, err)
				}
			}
			if tt.home && tt.install {
				// The global section is in the link's target, ~/.xum/AGENTS.md.
				got := readMuxFile(t, filepath.Join(target, "AGENTS.md"))
				if containsBeadsMarker(got) == tt.global {
					t.Errorf("global section in %s after remove (global=%v):\n%s", target, tt.global, got)
				}
			}
		})
	}
}
