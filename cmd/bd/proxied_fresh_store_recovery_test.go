//go:build cgo && unix

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// The ways back out of proxiedFreshStoreRefusal, at the front door. The
// refusal is only safe if every remedy it names works from the state it
// refuses, so each test starts from one such state, follows the remedy
// verbatim and proves the workspace serves its issues afterwards.

// TestManagedLocalProxiedLeftoverRootInitRecovers: a root that exists but
// holds no Dolt database, as a failed init leaves it (its config.yaml and
// server.log), is refused by every open, which names `bd init
// --proxied-server`. That init must create the store there instead of
// answering "already initialized", which it did while its guard keyed on the
// directory alone and left no bd command that could get out.
func TestManagedLocalProxiedLeftoverRootInitRecovers(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)

	dir := t.TempDir()
	initGitRepoAt(t, dir)
	beadsDir := filepath.Join(dir, ".beads")
	root := filepath.Join(beadsDir, "dolt")
	t.Cleanup(func() {
		if err := proxy.Shutdown(root); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", root, err)
		}
	})
	metadata, err := composeProxiedServerMetadataJSON(proxiedMetadataInputs{dbName: "lr", projectID: configfile.GenerateProjectID()})
	if err != nil {
		t.Fatalf("compose metadata.json: %v", err)
	}
	port, err := proxy.PickFreePort()
	if err != nil {
		t.Fatalf("pick a port for the leftover config.yaml: %v", err)
	}
	serverConfig, err := renderProxiedServerConfig(port)
	if err != nil {
		t.Fatalf("render the leftover config.yaml: %v", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", root, err)
	}
	for path, body := range map[string][]byte{
		configfile.ConfigPath(beadsDir):              metadata,
		filepath.Join(root, proxiedServerConfigName): serverConfig,
		filepath.Join(root, proxiedServerLogName):    []byte("dolt sql-server never became ready\n"),
	} {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, "list")
	if err == nil {
		t.Fatalf("bd list over a root holding no database succeeded; it must refuse\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	for _, want := range []string{"refusing to start a proxied Dolt server", root, "bd init --proxied-server"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal is missing %q:\n%s", want, stderr)
		}
	}
	if doltserver.HasDoltDatabase(root) {
		t.Fatalf("the refusal created a database under %s", root)
	}

	initCmd := exec.Command(bd, "init", "--proxied-server", "--quiet", "--prefix", "lr",
		"--non-interactive", "--skip-agents", "--skip-hooks")
	initCmd.Dir = dir
	initCmd.Env = bdProxiedEnv(dir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init --proxied-server over the leftover root: %v\n%s", err, out)
	}
	if !doltserver.HasDoltDatabase(root) {
		t.Fatalf("init did not create a database under %s", root)
	}
	issue := bdProxiedCreate(t, bd, dir, "after the leftover root")
	if got := bdProxiedShow(t, bd, dir, issue.ID); got.Title != "after the leftover root" {
		t.Fatalf("issue after init: %+v", got)
	}
}

// TestManagedLocalProxiedDeletedRootRestoresFromBackup: bd's own recovery for
// a managed-local workspace whose root was deleted is its backup. `bd backup
// restore` opens through the root pre-run like every command, so the refusal
// stops it too, and must name the way back instead: `bd init
// --proxied-server`, then the restore. Those two steps must bring the
// backed-up issue back and leave the workspace's tracked config as it was.
func TestManagedLocalProxiedDeletedRootRestoresFromBackup(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "drr", 5*time.Minute)
	issue := bdProxiedCreate(t, bd, p.dir, "survives the deleted root")

	dest := filepath.Join(t.TempDir(), "dolt-backup")
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "init", dest); err != nil {
		t.Fatalf("bd backup init %s: %v\n%s", dest, err, out)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "sync"); err != nil {
		t.Fatalf("bd backup sync: %v\n%s", err, out)
	}
	tracked := customizeTrackedConfig(t, p.beadsDir)

	if err := proxy.Shutdown(p.proxyRoot); err != nil {
		t.Fatalf("stop the topology before deleting its root: %v", err)
	}
	if err := os.RemoveAll(p.proxyRoot); err != nil {
		t.Fatalf("delete the root %s: %v", p.proxyRoot, err)
	}

	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "backup", "restore", dest, "--force")
	if err == nil {
		t.Fatalf("bd backup restore into a deleted root succeeded; the root pre-run must refuse it\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	for _, want := range []string{
		"refusing to start a proxied Dolt server",
		p.proxyRoot,
		"To restore it from a bd backup: bd init --proxied-server, then bd backup restore --force <dir>",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal is missing %q:\n%s", want, stderr)
		}
	}
	if _, err := os.Stat(p.proxyRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refusal left a root behind at %s (stat err=%v)", p.proxyRoot, err)
	}

	initCmd := exec.Command(bd, "init", "--proxied-server", "--quiet", "--prefix", p.prefix,
		"--non-interactive", "--skip-agents", "--skip-hooks")
	initCmd.Dir = p.dir
	initCmd.Env = bdProxiedEnv(p.dir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init --proxied-server over the deleted root: %v\n%s", err, out)
	}
	assertTrackedFilesUnchanged(t, p.beadsDir, tracked)
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "restore", dest, "--force"); err != nil {
		t.Fatalf("bd backup restore %s --force after init: %v\n%s", dest, err, out)
	}
	if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Title != "survives the deleted root" {
		t.Fatalf("issue after init and restore: %+v", got)
	}
	assertTrackedFilesUnchanged(t, p.beadsDir, tracked)
}

// TestManagedLocalProxiedTeamServerCloneInitKeepsTrackedConfig: a clone of a
// team-server workspace carries dolt_team_server in its tracked metadata.json
// and no sidecar. Its refusal names no init remedy, because bd never creates
// a team-server database. An operator who runs `bd init --proxied-server`
// there anyway must not turn the workspace into a bd-managed one, as init
// did when it rewrote metadata.json without the marker and minted a local
// database: init keeps the team-server posture, so it creates no database,
// says the server must provision it, and rewrites neither tracked file.
func TestManagedLocalProxiedTeamServerCloneInitKeepsTrackedConfig(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)

	dir := t.TempDir()
	initGitRepoAt(t, dir)
	beadsDir := filepath.Join(dir, ".beads")
	root := filepath.Join(beadsDir, "dolt")
	t.Cleanup(func() {
		if err := proxy.Shutdown(root); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", root, err)
		}
	})
	metadata, err := composeProxiedServerMetadataJSON(proxiedMetadataInputs{
		dbName: "team", projectID: configfile.GenerateProjectID(), teamServer: true,
	})
	if err != nil {
		t.Fatalf("compose metadata.json: %v", err)
	}
	tracked := map[string][]byte{
		configfile.ConfigFileName: metadata,
		"config.yaml":             append(renderInitConfigYAML("", false), "\n# Team settings\noutput:\n  title-length: 40\n"...),
	}
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", beadsDir, err)
	}
	for name, body := range tracked {
		if err := os.WriteFile(filepath.Join(beadsDir, name), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, "list")
	if err == nil {
		t.Fatalf("bd list in a team-server clone without its sidecar succeeded; it must refuse\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	for _, want := range []string{"refusing to start a proxied Dolt server", "bd never creates a team-server database"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal is missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "bd init") {
		t.Errorf("the refusal sends a team-server clone to bd init:\n%s", stderr)
	}

	initCmd := exec.Command(bd, "init", "--proxied-server", "--quiet",
		"--non-interactive", "--skip-agents", "--skip-hooks")
	initCmd.Dir = dir
	initCmd.Env = bdProxiedEnv(dir)
	out, err := initCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd init --proxied-server in a team-server clone succeeded; bd must not create a team-server database\n%s", out)
	}
	if !strings.Contains(string(out), "must be provisioned on the server") {
		t.Errorf("init's refusal does not say the server must provision the database:\n%s", out)
	}
	assertTrackedFilesUnchanged(t, beadsDir, tracked)
	if _, err := os.Stat(filepath.Join(root, "team")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("init created the team-server database under %s (stat err=%v)", root, err)
	}
}

// customizeTrackedConfig gives a workspace the tracked settings a team adds
// after init, one in config.yaml and one in metadata.json, and returns both
// files' bytes for assertTrackedFilesUnchanged.
func customizeTrackedConfig(t *testing.T, beadsDir string) map[string][]byte {
	t.Helper()
	yamlPath := filepath.Join(beadsDir, "config.yaml")
	configYAML, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read %s: %v", yamlPath, err)
	}
	configYAML = append(configYAML, "\n# Team settings\noutput:\n  title-length: 40\n"...)
	if err := os.WriteFile(yamlPath, configYAML, 0o600); err != nil {
		t.Fatalf("write %s: %v", yamlPath, err)
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load %s: %v", configfile.ConfigFileName, err)
	}
	cfg.StaleClosedIssuesDays = 30
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save %s: %v", configfile.ConfigFileName, err)
	}
	metadata, err := os.ReadFile(configfile.ConfigPath(beadsDir))
	if err != nil {
		t.Fatalf("read %s: %v", configfile.ConfigFileName, err)
	}
	return map[string][]byte{"config.yaml": configYAML, configfile.ConfigFileName: metadata}
}

// assertTrackedFilesUnchanged fails for each file in want whose bytes changed.
// `bd init --proxied-server` is the remedy the refusal names and commits
// .beads/, so a rewrite here is a commit reverting the team's config.
func assertTrackedFilesUnchanged(t *testing.T, beadsDir string, want map[string][]byte) {
	t.Helper()
	for name, body := range want {
		got, err := os.ReadFile(filepath.Join(beadsDir, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, body) {
			t.Errorf("%s was rewritten:\nbefore:\n%s\nafter:\n%s", name, body, got)
		}
	}
}
