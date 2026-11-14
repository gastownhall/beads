//go:build cgo && unix

package main

import (
	"encoding/json"
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

// TestManagedLocalProxiedUnjoinedCloneRefusesFreshStore is the front-door
// form of proxiedFreshStoreRefusal. A repository tracks .beads/metadata.json
// (dolt_mode=proxied-server) but not the sidecar or the store; a clone of it
// therefore carries the mode and nothing to serve. In that clone bd must
// refuse rather than mint a private, empty database, must leave no root
// behind, and the remedy it names (`bd init --proxied-server`) must then
// create the store where the refusal said and work, without rewriting the
// tracked config it commits and without touching the source workspace.
func TestManagedLocalProxiedUnjoinedCloneRefusesFreshStore(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	src := bdManagedLocalInit(t, bd, "src", 5*time.Minute)
	srcIssue := bdProxiedCreate(t, bd, src.dir, "source sentinel")

	clone := t.TempDir()
	initGitRepoAt(t, clone)
	cloneBeads := filepath.Join(clone, ".beads")
	if err := os.MkdirAll(cloneBeads, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", cloneBeads, err)
	}
	// What such a repository tracks: the workspace config, never the sidecar
	// (it names a machine-local server) and never the store. This one keeps
	// its store under a custom dolt_data_dir, which its .gitignore ignores
	// as the template ignores dolt/, and has team settings in config.yaml.
	srcCfg, err := configfile.Load(src.beadsDir)
	if err != nil || srcCfg == nil {
		t.Fatalf("load the source's %s: %v", configfile.ConfigFileName, err)
	}
	srcCfg.DoltDataDir = "data"
	metadata, err := json.MarshalIndent(srcCfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", configfile.ConfigFileName, err)
	}
	configYAML, err := os.ReadFile(filepath.Join(src.beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read the source's config.yaml: %v", err)
	}
	tracked := map[string][]byte{
		configfile.ConfigFileName: metadata,
		"config.yaml":             append(configYAML, "\n# Team settings\noutput:\n  title-length: 40\n"...),
	}
	for name, body := range tracked {
		if err := os.WriteFile(filepath.Join(cloneBeads, name), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(cloneBeads, ".gitignore"), []byte("data/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	cloneRoot := filepath.Join(cloneBeads, "data")
	t.Cleanup(func() {
		if err := proxy.Shutdown(cloneRoot); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", cloneRoot, err)
		}
	})

	stdout, stderr, err := bdProxiedRunBuffers(t, bd, clone, "list")
	if err == nil {
		t.Fatalf("bd list in an unjoined clone succeeded; it must refuse\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	for _, want := range []string{
		"refusing to start a proxied Dolt server",
		cloneRoot,
		"named by dolt_data_dir in " + configfile.ConfigFileName,
		"there is no " + configfile.ProxiedServerClientInfoFileName,
		"bd init --proxied-server",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal is missing %q:\n%s", want, stderr)
		}
	}
	if _, err := os.Stat(cloneRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refusal left a root behind at %s (stat err=%v)", cloneRoot, err)
	}

	// The named remedy creates the store at the root the refusal named, keeps
	// the tracked config it commits, and later opens see the store.
	initCmd := exec.Command(bd, "init", "--proxied-server", "--quiet", "--prefix", "cl",
		"--non-interactive", "--skip-agents", "--skip-hooks")
	initCmd.Dir = clone
	initCmd.Env = bdProxiedEnv(clone)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init --proxied-server in the refused clone: %v\n%s", err, out)
	}
	if !doltserver.HasDoltDatabase(cloneRoot) {
		t.Fatalf("init did not create a database under %s, the root the refusal named", cloneRoot)
	}
	if defaultRoot := filepath.Join(cloneBeads, "dolt"); doltserver.HasDoltDatabase(defaultRoot) {
		t.Fatalf("init created a database under the default root %s instead of dolt_data_dir's", defaultRoot)
	}
	assertTrackedFilesUnchanged(t, cloneBeads, tracked)
	cloneIssue := bdProxiedCreate(t, bd, clone, "clone sentinel")
	if got := bdProxiedShow(t, bd, clone, cloneIssue.ID); got.Title != "clone sentinel" {
		t.Fatalf("clone issue after init: %+v", got)
	}

	// The source workspace saw none of it.
	if got := bdProxiedShow(t, bd, src.dir, srcIssue.ID); got.Title != "source sentinel" {
		t.Fatalf("source issue after the clone's init: %+v", got)
	}
	bdProxiedShowFail(t, bd, src.dir, cloneIssue.ID)
}
