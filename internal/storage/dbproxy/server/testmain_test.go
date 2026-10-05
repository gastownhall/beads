package server_test

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// suiteRootPrefix is this suite's PinSuiteTempRoot pattern without its random
// tail. SweepDeadSuiteRoots globs for it, so the two must not drift. It is
// deliberately short: this package's unix-socket test builds a socket path
// under t.TempDir(), and sun_path is 108 bytes on linux / 104 on macOS.
const suiteRootPrefix = "beads-dbproxy-server-tests-"

// suiteTempRoot is the TestMain-owned temp directory that every t.TempDir()
// in this package lands under, so the sweeps have a root they can vouch for.
var suiteTempRoot string

// TestMain puts this package under the same suite-lifecycle contract as every
// other package that daemonizes a `dolt sql-server`.
//
// This suite starts real servers: newDoltServer (and the fixtures that build
// a DoltServer by hand) spawn `dolt sql-server` with cmd.Dir set to the
// rootDir, which is a t.TempDir(). Until now the package had no TestMain at
// all, which left two holes:
//
//   - A run killed by `go test -timeout`, CI cancel, or Ctrl-C skips every
//     t.Cleanup, so the server outlives the test binary. Its cwd survives too
//     (nothing deleted it), and this package claimed no suite root — so that
//     debris was reachable by neither arm of the sweep and simply accumulated.
//     Claiming a root here is what makes the NEXT run able to reap it.
//
//   - A server this suite leaks on a normal run has its cwd deleted by
//     t.TempDir's RemoveAll. A root-scoped post-run sweep detects that leak
//     and fails this package without consuming another live suite's evidence.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeDoltEnv); mode != "" {
		os.Exit(fakeDolt(mode, os.Args[1:]))
	}
	os.Exit(testMainInner(m))
}

// fakeDoltEnv, when set, makes this test binary act as a stand-in `dolt`
// (see fakeDolt) so tests can drive DoltServer through startup behaviors a
// real dolt cannot be made to show on demand. The internal tests set it and
// pass os.Args[0] as the dolt binary.
const fakeDoltEnv = "BEADS_TEST_FAKE_DOLT"

var fakeDoltPortRe = regexp.MustCompile(`(?m)^\s+port:\s*(\d+)`)

// fakeDolt answers the dolt invocations DoltServer.Start makes. For
// `sql-server --config <file>` it reads listener.port from the file and, by
// mode:
//   - "inuse": reports dolt's own port-in-use error and exits 1;
//   - "silent": listens but never logs the ready line;
//   - "ready": listens and logs the ready line.
func fakeDolt(mode string, args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "config":
		fmt.Println("fake")
		return 0
	case "init":
		return 0
	case "sql-server":
	default:
		return 2
	}
	var cfgPath string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--config" {
			cfgPath = args[i+1]
		}
	}
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	m := fakeDoltPortRe.FindSubmatch(body)
	if m == nil {
		fmt.Fprintln(os.Stderr, "no listener.port in", cfgPath)
		return 1
	}
	port := string(m[1])
	if mode == "inuse" {
		fmt.Fprintf(os.Stderr, "Port %s already in use.\n", port)
		return 1
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if mode == "ready" {
		fmt.Fprintln(os.Stderr, `level=info msg="Server ready. Accepting connections."`)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return 0
		}
		defer c.Close()
	}
}

func testMainInner(m *testing.M) int {
	// Clear out the roots of earlier runs of this suite whose process is
	// gone, before claiming one of our own. Roots with no owner marker, and
	// roots whose owner is still running (a parallel package under
	// scripts/test.sh, a second `go test`), are left untouched.
	doltserver.SweepDeadSuiteRoots(os.TempDir(), suiteRootPrefix)

	// Pin TMPDIR under a suite-owned root so every t.TempDir() — including
	// each server's rootDir, which is its working directory — is nested under
	// something the sweeps may vouch for.
	root, pinErr := testutil.PinSuiteTempRoot(suiteRootPrefix + "*")
	if pinErr != nil {
		fmt.Fprintf(os.Stderr, "FATAL: suite temp root: %v\n", pinErr)
		return 1
	}
	suiteTempRoot = root
	defer os.RemoveAll(root)

	// Claim the root for this process so the NEXT run can tell our debris
	// from a concurrent run's live tree.
	if err := doltserver.WriteSuiteOwnerMarker(root); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not claim suite temp root %s: %v\n", root, err)
	}

	code := m.Run()

	// Best-effort reap of any dolt sql-server still running under this run's
	// own root — the backstop for a test whose Cleanup did not get to run.
	swept := doltserver.SweepSuiteTestServers(root)
	return doltserver.ApplyLeakPolicy("internal/storage/dbproxy/server", code, swept)
}

// TestTempDirLandsUnderSuiteSweepRoot guards the pinning above: if t.TempDir()
// ever stops landing under suiteTempRoot, the post-run sweep silently loses
// its scope and leaked sql-servers become unreapable again.
func TestTempDirLandsUnderSuiteSweepRoot(t *testing.T) {
	if suiteTempRoot == "" {
		t.Fatal("TestMain did not pin suiteTempRoot; leaked dolt sql-server processes cannot be swept")
	}
	dir := t.TempDir()
	if !testutil.PathUnderSuiteRoot(dir, suiteTempRoot) {
		t.Fatalf("t.TempDir() %q is not under suiteTempRoot %q", dir, suiteTempRoot)
	}
}
