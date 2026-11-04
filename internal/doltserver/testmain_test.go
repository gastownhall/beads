//go:build !integration || windows

package doltserver_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
)

// suiteRootPrefix is this suite's os.MkdirTemp pattern minus the random tail.
// It deliberately matches testmain_integration_test.go's prefix so an
// abandoned root from either TestMain gets reclaimed; the owner marker written
// below is what keeps a LIVE sibling run's root untouched.
const suiteRootPrefix = "beads-doltserver-tests-"

// TestMain gives every direct non-integration run of this package an isolated
// home so it cannot read or write the operator's dolt and beads configuration.
//
// It redirects HOME, USERPROFILE and XDG_CONFIG_HOME, pins DOLT_ROOT_PATH
// (dolt reads its global config from there before HOME), and clears the
// operator's whole BEADS_* namespace (see scrubOperatorBeadsEnv) so config
// resolution cannot read the developer's own beads settings.
//
// Redirecting HOME also hides the operator's dolt identity, and without one
// `dolt init` fails "Author identity unknown", which sends the tests that
// shell out to dolt into a skip rather than running them.
// configureDoltIdentity puts an identity back inside the isolated home.
//
// Integration-tagged runs use the stronger suite-root TestMain in
// testmain_integration_test.go instead.
func TestMain(m *testing.M) {
	if os.Getenv(fakeDoltEnv) != "" {
		os.Exit(fakeDolt(os.Args[1:]))
	}
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	// Before creating this run's own root, per SweepDeadSuiteRoots' contract.
	doltserver.SweepDeadSuiteRoots(os.TempDir(), suiteRootPrefix)

	root, err := os.MkdirTemp("", suiteRootPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doltserver tests: create isolated home: %v\n", err)
		return 1
	}
	defer os.RemoveAll(root)
	if err := doltserver.WriteSuiteOwnerMarker(root); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not claim suite temp root %s: %v\n", root, err)
	}

	_ = os.Setenv("HOME", root)
	_ = os.Setenv("USERPROFILE", root)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	_ = os.Setenv("DOLT_ROOT_PATH", root)
	scrubOperatorBeadsEnv()
	_ = os.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	_ = os.Setenv("BEADS_TEST_MODE", "1")
	_ = os.Setenv("BEADS_TEST_PDEATHSIG", "1")
	config.ResetForTesting()
	configureDoltIdentity(root)

	code := m.Run()

	// Suite-scoped, never the global SweepOrphanedTestServers arm: that one is
	// deprecated for TestMain precisely because it can reap a still-running
	// foreign suite's servers and consume that suite's leak evidence.
	if killed := doltserver.SweepSuiteTestServers(root); len(killed) > 0 {
		fmt.Fprintf(os.Stderr, "doltserver tests: swept %d orphaned dolt sql-server process(es)\n", len(killed))
	}
	config.ResetForTesting()
	return code
}

// scrubOperatorBeadsEnv removes the operator's beads configuration from this
// process so config resolution sees only the isolated home. BEADS_TEST_* is
// the harness's own namespace and is preserved; TestMain sets those itself.
//
// Clearing by PREFIX rather than by a fixed name list is deliberate. A list
// naming only BEADS_DOLT_SHARED_SERVER left an exported BEADS_DIR,
// BEADS_DOLT_SERVER_HOST or BEADS_DOLT_SERVER_PORT reaching DefaultConfig,
// which reddened TestDefaultConfigReturnsZeroForStandalone,
// TestDefaultConfigPortFileTakesPrecedence and
// TestResolveServerMode_HostInferredExternal on any machine that had them set
// — a failure that reproduces only on the developer's own shell.
func scrubOperatorBeadsEnv() {
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, "BEADS_") || strings.HasPrefix(key, "BEADS_TEST_") {
			continue
		}
		_ = os.Unsetenv(key)
	}
}

// configureDoltIdentity gives the isolated home a dolt identity, mirroring
// configureDoltTestIdentity in lifecycle_integration_test.go, which this build
// tag cannot see. ensureDoltInit shells out to `dolt init` with the inherited
// environment, so the identity has to live under the redirected HOME.
//
// Best effort: when dolt is genuinely absent there is nothing to configure and
// the tests that need it still skip, which is the pre-existing behaviour.
func configureDoltIdentity(home string) {
	doltBin, err := exec.LookPath("dolt")
	if err != nil {
		return
	}
	for _, args := range [][]string{
		{"config", "--global", "--add", "user.name", "beads-test"},
		{"config", "--global", "--add", "user.email", "beads@test"},
	} {
		cmd := exec.Command(doltBin, args...)
		cmd.Env = append(os.Environ(), "HOME="+home, "DOLT_ROOT_PATH="+home)
		if out, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
			fmt.Fprintf(os.Stderr, "doltserver tests: dolt %v: %v\n%s", args, cmdErr, out)
			return
		}
	}
}
