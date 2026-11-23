//go:build cgo

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// explicitBeadsDirMode describes how a storage mode's bd subprocesses are
// initialized and run for the explicit-BEADS_DIR regression tests.
type explicitBeadsDirMode struct {
	initArgs []string
	env      func(home string) []string
	proxied  bool
}

// runExplicitBeadsDirBD runs bd in dir with the mode's isolated environment.
// beadsDir, when non-empty, is passed as an explicit BEADS_DIR.
func runExplicitBeadsDirBD(t *testing.T, bd string, mode explicitBeadsDirMode, home, dir, beadsDir string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	env := append(mode.env(home), "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
	if beadsDir != "" {
		env = append(env, "BEADS_DIR="+beadsDir)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// testExplicitBeadsDirIsAuthoritative covers the regression where an explicit
// BEADS_DIR without project files was ignored during discovery: bd walked up
// from CWD, bound the parent workspace, and `bd init` then refused ("already
// initialized") while data commands read and wrote the parent's store.
func testExplicitBeadsDirIsAuthoritative(t *testing.T, bd string, mode explicitBeadsDirMode) {
	t.Helper()
	home := t.TempDir()
	parent := t.TempDir()
	initGitRepoAt(t, parent)
	parentBeadsDir := filepath.Join(parent, ".beads")
	child := filepath.Join(parent, "child")
	explicit := filepath.Join(child, ".beads")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if mode.proxied {
		for _, root := range []string{filepath.Join(parentBeadsDir, "dolt"), filepath.Join(explicit, "dolt")} {
			root := root
			t.Cleanup(func() {
				if _, err := os.Stat(root); err != nil {
					return
				}
				if err := proxy.Shutdown(root); err != nil {
					t.Logf("proxy.Shutdown(%s): %v", root, err)
				}
			})
			shutdownProxyOnInterrupt(t, root)
		}
	}

	initArgs := append([]string{"init", "--quiet", "--non-interactive", "--skip-agents", "--skip-hooks"}, mode.initArgs...)
	if stdout, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, parent, "", append(initArgs, "--prefix", "par")...); err != nil {
		t.Fatalf("parent bd init: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	parentID, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, parent, "", "create", "--silent", "parent issue")
	if err != nil {
		t.Fatalf("parent bd create: %v\nstderr:\n%s", err, stderr)
	}
	parentID = strings.TrimSpace(parentID)

	// A data command against the uninitialized explicit dir must not fall
	// back to the parent workspace.
	stdout, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, child, explicit, "list", "--json")
	if err == nil || strings.Contains(stdout, parentID) {
		t.Errorf("bd list with BEADS_DIR=%s (no workspace yet) should fail without reading the parent store; err=%v\nstdout:\n%s\nstderr:\n%s", explicit, err, stdout, stderr)
	} else if !strings.Contains(stderr, "no beads database found") {
		t.Errorf("bd list with an uninitialized BEADS_DIR: want \"no beads database found\", got stderr:\n%s", stderr)
	}

	// bd init must initialize the explicit dir (existing but empty, as a
	// caller that pre-creates it would leave it), not refuse because of the
	// parent workspace.
	if err := os.MkdirAll(explicit, 0o755); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, child, explicit, append(initArgs, "--prefix", "kid")...); err != nil {
		t.Fatalf("bd init with BEADS_DIR=%s: %v\nstdout:\n%s\nstderr:\n%s", explicit, err, stdout, stderr)
	}
	childCfg, err := configfile.Load(explicit)
	if err != nil || childCfg == nil {
		t.Fatalf("load %s metadata: cfg=%v err=%v", explicit, childCfg, err)
	}
	if childCfg.DoltDatabase != "kid" {
		t.Fatalf("explicit BEADS_DIR dolt_database = %q, want %q", childCfg.DoltDatabase, "kid")
	}
	parentCfg, err := configfile.Load(parentBeadsDir)
	if err != nil || parentCfg == nil || parentCfg.DoltDatabase != "par" {
		t.Fatalf("parent metadata changed: cfg=%+v err=%v", parentCfg, err)
	}

	// Writes through the explicit dir land in the new workspace only.
	childID, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, child, explicit, "create", "--silent", "child issue")
	if err != nil {
		t.Fatalf("bd create with BEADS_DIR=%s: %v\nstderr:\n%s", explicit, err, stderr)
	}
	childID = strings.TrimSpace(childID)
	if !strings.HasPrefix(childID, "kid-") {
		t.Fatalf("bd create with BEADS_DIR=%s minted %q, want a kid- ID", explicit, childID)
	}
	parentList, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, parent, "", "list", "--json")
	if err != nil {
		t.Fatalf("parent bd list: %v\nstderr:\n%s", err, stderr)
	}
	if strings.Contains(parentList, childID) || !strings.Contains(parentList, parentID) {
		t.Fatalf("parent store should hold only %s, got:\n%s", parentID, parentList)
	}
}

// testInitRefusesBeadsDirAtProjectRoot covers the other side of an
// authoritative BEADS_DIR (GH#7034): naming the project root of an initialized
// workspace, instead of its .beads, must not make `bd init` write a second
// workspace into the project root. Init refuses and leaves the root as it
// was, and a data command says which directory BEADS_DIR should have named.
func testInitRefusesBeadsDirAtProjectRoot(t *testing.T, bd string, mode explicitBeadsDirMode) {
	t.Helper()
	home := t.TempDir()
	project := t.TempDir()
	initGitRepoAt(t, project)
	workspace := filepath.Join(project, ".beads")
	if mode.proxied {
		// The refused init must start nothing under the project root either;
		// the cleanup covers a regression that does.
		for _, root := range []string{filepath.Join(workspace, "dolt"), filepath.Join(project, "dolt")} {
			root := root
			t.Cleanup(func() {
				if _, err := os.Stat(root); err != nil {
					return
				}
				if err := proxy.Shutdown(root); err != nil {
					t.Logf("proxy.Shutdown(%s): %v", root, err)
				}
			})
			shutdownProxyOnInterrupt(t, root)
		}
	}

	initArgs := append([]string{"init", "--quiet", "--non-interactive", "--skip-agents", "--skip-hooks"}, mode.initArgs...)
	if stdout, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, project, "", append(initArgs, "--prefix", "real")...); err != nil {
		t.Fatalf("project bd init: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	rootEntries := func() []string {
		t.Helper()
		entries, err := os.ReadDir(project)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return names
	}
	before := rootEntries()

	for _, extra := range [][]string{nil, {"--force"}} {
		args := append(append(append([]string{}, initArgs...), "--prefix", "oops"), extra...)
		stdout, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, project, project, args...)
		if err == nil {
			t.Errorf("bd %s with BEADS_DIR=%s (the project root) succeeded; want a refusal\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), project, stdout, stderr)
		} else if !strings.Contains(stderr, "bd init refuses: BEADS_DIR names") {
			t.Errorf("bd %s with BEADS_DIR=%s: want the project-root refusal, got stderr:\n%s", strings.Join(args, " "), project, stderr)
		}
		if after := rootEntries(); strings.Join(after, " ") != strings.Join(before, " ") {
			t.Fatalf("bd %s with BEADS_DIR=%s changed the project root\nbefore: %v\nafter:  %v", strings.Join(args, " "), project, before, after)
		}
	}
	cfg, err := configfile.Load(workspace)
	if err != nil || cfg == nil || cfg.DoltDatabase != "real" {
		t.Fatalf("project workspace metadata changed: cfg=%+v err=%v", cfg, err)
	}

	// bd import sets a workspace up where discovery finds none, so it too
	// could write into the project root. It refuses before it opens a store:
	// an import that set one up and then failed for want of a prefix left an
	// empty store behind, which discovery took for a database on every later
	// command.
	importFile := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(importFile, []byte(`{"id":"real-1","title":"imported","status":"open","priority":2,"issue_type":"task"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	importIsRefused := func(shape string) {
		t.Helper()
		before := rootEntries()
		stdout, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, project, project, "import", importFile)
		if err == nil {
			t.Errorf("bd import with BEADS_DIR=%s (the project root, %s) succeeded; want a refusal\nstdout:\n%s\nstderr:\n%s", project, shape, stdout, stderr)
		} else if !strings.Contains(stderr, "no beads database found") || !strings.Contains(stderr, "BEADS_DIR is set to") {
			t.Errorf("bd import with BEADS_DIR=%s (the project root, %s): want \"no beads database found\" and the BEADS_DIR note, got stderr:\n%s", project, shape, stderr)
		}
		if after := rootEntries(); strings.Join(after, " ") != strings.Join(before, " ") {
			t.Fatalf("bd import with BEADS_DIR=%s (the project root, %s) changed the project root\nbefore: %v\nafter:  %v", project, shape, before, after)
		}
	}
	importIsRefused("holding nothing else")

	// A data command still finds no workspace in the project root, and now
	// says that BEADS_DIR is the cause.
	stdout, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, project, project, "list", "--json")
	if err == nil {
		t.Errorf("bd list with BEADS_DIR=%s (the project root) should fail; stdout:\n%s", project, stdout)
	} else if !strings.Contains(stderr, "no beads database found") || !strings.Contains(stderr, "BEADS_DIR is set to") {
		t.Errorf("bd list with BEADS_DIR=%s: want \"no beads database found\" and the BEADS_DIR note, got stderr:\n%s", project, stderr)
	}

	// A project root with a config.yaml of its own is one discovery reads as
	// a workspace, so the import refusal must not wait for discovery to find
	// nothing there.
	if err := os.WriteFile(filepath.Join(project, "config.yaml"), []byte("name: my-app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	importIsRefused("holding its own config.yaml")
}

// testBootstrapRefusesBeadsDirAtProjectRoot covers bd bootstrap under the
// same slip (GH#7034). The layout is the one where bootstrap acted on the
// project root: a workspace in the directory above, whose metadata it read,
// and the project with its own workspace beneath it. What it did there
// depended on what else the project root held, so each shape is a row:
// nothing else (refused, but with no word about BEADS_DIR), a config.yaml
// (a database created in the root, named for the workspace above), and a
// config.json that is not beads metadata (taken for legacy metadata and
// replaced by a metadata.json, by --dry-run too). The fourth shape, an
// origin that carries refs/dolt/data, is not built here.
func testBootstrapRefusesBeadsDirAtProjectRoot(t *testing.T, bd string, mode explicitBeadsDirMode) {
	t.Helper()
	home := t.TempDir()
	outer := t.TempDir()
	initGitRepoAt(t, outer)
	project := filepath.Join(outer, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, project)
	outerWorkspace := filepath.Join(outer, ".beads")
	workspace := filepath.Join(project, ".beads")
	if mode.proxied {
		// The refused bootstrap must start nothing under the project root;
		// the cleanup covers a regression that does.
		for _, root := range []string{filepath.Join(outerWorkspace, "dolt"), filepath.Join(workspace, "dolt"), filepath.Join(project, "dolt")} {
			root := root
			t.Cleanup(func() {
				if _, err := os.Stat(root); err != nil {
					return
				}
				if err := proxy.Shutdown(root); err != nil {
					t.Logf("proxy.Shutdown(%s): %v", root, err)
				}
			})
			shutdownProxyOnInterrupt(t, root)
		}
	}

	initArgs := append([]string{"init", "--quiet", "--non-interactive", "--skip-agents", "--skip-hooks"}, mode.initArgs...)
	if stdout, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, outer, "", append(initArgs, "--prefix", "outer")...); err != nil {
		t.Fatalf("outer bd init: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if stdout, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, project, "", append(initArgs, "--prefix", "real")...); err != nil {
		t.Fatalf("project bd init: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	rootEntries := func() []string {
		t.Helper()
		entries, err := os.ReadDir(project)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return names
	}
	// refused runs bd bootstrap with BEADS_DIR at the project root and wants
	// an exit of 1, each mark in the stream named, and the root's
	// entries as they were.
	refused := func(shape, stream string, marks []string, args ...string) {
		t.Helper()
		before := rootEntries()
		stdout, stderr, err := runExplicitBeadsDirBD(t, bd, mode, home, project, project, args...)
		got := stderr
		if stream == "stdout" {
			got = stdout
		}
		if err == nil {
			t.Errorf("bd %s with BEADS_DIR=%s (the project root, %s) succeeded; want a refusal\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), project, shape, stdout, stderr)
		} else {
			for _, mark := range marks {
				if !strings.Contains(got, mark) {
					t.Errorf("bd %s with BEADS_DIR=%s (the project root, %s): want %q on %s, got:\n%s", strings.Join(args, " "), project, shape, mark, stream, got)
				}
			}
		}
		if after := rootEntries(); strings.Join(after, " ") != strings.Join(before, " ") {
			t.Errorf("bd %s with BEADS_DIR=%s (the project root, %s) changed the project root\nbefore: %v\nafter:  %v", strings.Join(args, " "), project, shape, before, after)
		}
	}
	note := []string{"no beads database found", "BEADS_DIR is set to"}
	// The --json form is the no-workspace payload, with a suggestion that
	// names the directory meant, for a caller that reads stdout alone.
	payload := []string{`"action": "none"`, "BEADS_DIR is set to", ".beads, or unset it"}

	refused("holding nothing else", "stderr", note, "bootstrap", "--yes")

	// A project root with a config.yaml of its own is one discovery reads as
	// a workspace. Here --dry-run printed a plan against the project root and
	// exited 0, and the run itself created the database.
	if err := os.WriteFile(filepath.Join(project, "config.yaml"), []byte("name: my-app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused("holding its own config.yaml", "stdout", payload, "bootstrap", "--dry-run", "--json")
	refused("holding its own config.yaml", "stderr", note, "bootstrap", "--yes")

	// A config.json of the project's own, beside the config.yaml, was loaded
	// as legacy beads metadata: bootstrap wrote a metadata.json from it and
	// removed it, before the plan and so under --dry-run as well.
	ownConfig := []byte(`{"port": 8080, "debug": true}` + "\n")
	ownConfigPath := filepath.Join(project, "config.json")
	if err := os.WriteFile(ownConfigPath, ownConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	refused("holding its own config.yaml and config.json", "stderr", note, "bootstrap", "--dry-run")
	if got, err := os.ReadFile(ownConfigPath); err != nil || !bytes.Equal(got, ownConfig) {
		t.Errorf("bd bootstrap --dry-run with BEADS_DIR=%s changed the project's own config.json: got %q, err=%v", project, got, err)
	}

	for dir, want := range map[string]string{workspace: "real", outerWorkspace: "outer"} {
		cfg, err := configfile.Load(dir)
		if err != nil || cfg == nil || cfg.DoltDatabase != want {
			t.Errorf("workspace metadata in %s changed: want database %q, got cfg=%+v err=%v", dir, want, cfg, err)
		}
	}
}

func TestEmbeddedInitExplicitBeadsDirIsAuthoritative(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt init tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	mode := explicitBeadsDirMode{env: bdEnv}
	testExplicitBeadsDirIsAuthoritative(t, bd, mode)
	testInitRefusesBeadsDirAtProjectRoot(t, bd, mode)
	testBootstrapRefusesBeadsDirAtProjectRoot(t, bd, mode)
}

func TestProxiedServerInitExplicitBeadsDirIsAuthoritative(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	mode := explicitBeadsDirMode{
		initArgs: []string{"--proxied-server"},
		env:      bdProxiedEnv,
		proxied:  true,
	}
	testExplicitBeadsDirIsAuthoritative(t, bd, mode)
	testInitRefusesBeadsDirAtProjectRoot(t, bd, mode)
	testBootstrapRefusesBeadsDirAtProjectRoot(t, bd, mode)
}
