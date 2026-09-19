package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// leakFixturePrefix is the issue-prefix written into the fixture workspace below.
// It is deliberately not a plausible real prefix, so a failure message naming it
// is unambiguous evidence that the fixture config was imported.
const leakFixturePrefix = "zzleak"

// newLeakFixtureRepo builds a self-contained fake module root holding a .beads
// workspace whose config.yaml sets leakFixturePrefix, chdirs into it, and returns
// the .beads path.
//
// It must not depend on the real beads checkout: that checkout's .beads/config.yaml
// is untracked developer state (it happens to carry `issue-prefix: bd` on the machine
// where ga-e6h6i was found), so a test reading it would behave differently on CI and
// on a developer box.
func newLeakFixtureRepo(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/leakfixture\n\ngo 1.24.0\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: "+leakFixturePrefix+"\n"), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	t.Chdir(root)

	return beadsDir
}

// ga-e6h6i: a prior test in this binary that dispatches a real CLI command leaves
// BEADS_DIR pointing at a checkout's .beads via a raw os.Setenv with no restore
// (prepareSelectedCommandContext). initConfigForTest pins that leaked value, so its
// config.Initialize used to re-import the repo config — including issue-prefix, which
// surfaced as "prefix mismatch: database uses bd-" in an unrelated --id test.
//
// The raw os.Setenv here is deliberate: t.Setenv would restore the value on cleanup
// and therefore would not reproduce the unrestored leak this test exists to catch.
func TestInitConfigForTestNeutralizesLeakedRepoBeadsDir(t *testing.T) {
	beadsDir := newLeakFixtureRepo(t)

	prev, had := os.LookupEnv("BEADS_DIR")
	if err := os.Setenv("BEADS_DIR", beadsDir); err != nil {
		t.Fatalf("setenv BEADS_DIR: %v", err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("BEADS_DIR", prev)
			return
		}
		_ = os.Unsetenv("BEADS_DIR")
	})

	initConfigForTest(t)

	if got := config.GetString("issue-prefix"); got != "" {
		t.Fatalf("config.GetString(issue-prefix) = %q after initConfigForTest, want empty: "+
			"a leaked BEADS_DIR still imports the repo config", got)
	}
}

// The other half of ga-e6h6i: running real command dispatch must not leave the
// package-global viper holding the dispatched workspace's issue-prefix for every
// later test that does not call initConfigForTest.
func TestDispatchDoesNotPolluteViperIssuePrefix(t *testing.T) {
	ensureCleanGlobalState(t)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")

	beadsDir := newLeakFixtureRepo(t)
	// Dispatch only selects a workspace that FindBeadsDir accepts.
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"beads","backend":"dolt"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	t.Cleanup(resetCommandContext)

	// --version is a skipsStoreInit command: it reaches dispatch (and its BEADS_DIR
	// side effect) without needing a live store.
	runVersionDispatch(t)

	if got := config.GetString("issue-prefix"); got != "" {
		t.Fatalf("config.GetString(issue-prefix) = %q after command dispatch, want empty: "+
			"dispatch leaked the workspace config into the global viper", got)
	}
}

// runVersionDispatch executes `bd --version` through the shared root command with
// stdout discarded. Cobra's command tree and os.Stdout are process-global, so this
// holds stdioMutex per the policy documented on that mutex.
func runVersionDispatch(t *testing.T) {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	oldStdout := os.Stdout
	os.Stdout = devNull
	defer func() { os.Stdout = oldStdout }()

	rootCmd.SetArgs([]string{"--version"})
	defer rootCmd.SetArgs(nil)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(--version): %v", err)
	}
}
