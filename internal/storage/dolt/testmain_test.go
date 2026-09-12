package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// testServerPort is the port of the shared test Dolt server (0 = not running).
// Set by TestMain before tests run, used implicitly via BEADS_DOLT_PORT env var
// which applyConfigDefaults reads when ServerPort is 0.
var testServerPort int

// testSharedDB is the name of the shared database for branch-per-test isolation.
var testSharedDB string

// testSharedConn is a raw *sql.DB for branch operations in the shared database.
var testSharedConn *sql.DB

// helperSubprocessSentinels are the env vars a parent test sets when it
// re-execs this test binary as a helper subprocess. Keep in sync with the
// TestHelper* entry points — TestHelperSubprocessSentinelsAreComplete fails the
// build if a new one is added without listing it here.
var helperSubprocessSentinels = []string{
	"BEADS_SCHEMA_INIT_HELPER",
	"BEADS_MULTISTORE_HELPER",
}

// isHelperSubprocess reports whether this process was re-exec'd as a helper
// subprocess rather than run as the suite itself.
func isHelperSubprocess() bool {
	for _, sentinel := range helperSubprocessSentinels {
		if os.Getenv(sentinel) == "1" {
			return true
		}
	}
	return false
}

func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	os.Setenv("BEADS_TEST_MODE", "1")
	// BEADS_TEST_PDEATHSIG=1 protects TestMultiProcessSchemaInit_DoltVerify
	// (initschema_multiprocess_test.go), which calls doltserver.Start
	// directly (in-process, no exec boundary) and this TestMain's own
	// process stays alive for the server's whole lifetime. See
	// internal/doltserver/procattr_linux.go for why this is a narrower,
	// separate flag from BEADS_TEST_MODE.
	os.Setenv("BEADS_TEST_PDEATHSIG", "1")

	// AD-01 (be-c5p): the test/bench harness opens a process-local dolt
	// sql-server (testcontainer or external port). The new database-name
	// firewall in dolt.New refuses test-named DBs unless this opt-in is set.
	os.Setenv("BEADS_TEST_SERVER", "1")
	if isHelperSubprocess() {
		// A helper subprocess is handed its server's port by the parent test
		// and never looks at this suite's own server. Starting one anyway
		// costs a Dolt container plus a full migration chain per subprocess:
		// TestMultiProcessSchemaInit spawns 8 at once, so the machine ran 9
		// Dolt servers to measure a lock race between 8 clients of the
		// parent's single server. Worse, every helper exits through os.Exit,
		// which skips the deferred TerminateDoltContainer below and leaks the
		// container. That load is what pushed the contended GET_LOCK past its
		// 5s wait in CI shard 3.
		//
		// It returns BEFORE the suite temp root below for the same reason:
		// eight concurrent helpers would each run the dead-root sweep and each
		// claim a root of its own, and os.Exit would abandon every one of
		// them. A helper needs neither: it starts no server, so it has no
		// orphans to sweep and nothing for a later run to reap.
		//
		// It DOES still need its own circuit-breaker directory. That state is
		// one file keyed host:port:db, read-modify-written, and the parent's
		// BEADS_TEST_CIRCUIT_DIR reaches every helper through the BEADS_
		// allowlist in integration.FilterEnv — so inheriting it would put all
		// eight of TestMultiProcessSchemaInit's helpers on ONE file at once,
		// against the same host:port:db. Each helper used to get a fresh
		// directory under a suite root of its own; this keeps that isolation
		// without the root, the sweep, or the marker.
		circuitDir, circuitErr := os.MkdirTemp("", "beads-dolt-helper-circuit-*")
		if circuitErr != nil {
			fmt.Fprintf(os.Stderr, "FATAL: failed to isolate helper circuit state: %v\n", circuitErr)
			return 1
		}
		// A helper commonly exits through os.Exit (see above), which skips
		// this defer. The directory holds a few bytes of circuit state and
		// never a server, and it carries no owner marker, so no sweep will
		// ever look at it: the occasional leftover is accepted rather than
		// given machinery of its own.
		defer os.RemoveAll(circuitDir)
		if err := os.Setenv(testCircuitBreakerDirEnv, circuitDir); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: failed to isolate helper circuit state: %v\n", err)
			return 1
		}
		defer os.Unsetenv(testCircuitBreakerDirEnv)
		return m.Run()
	}

	// Suite-owned root for the orphan-server sweep below. Must never be a
	// shared/global temp dir (see SweepOrphanedTestServers) — this one is
	// unique to this test run and removed when it exits.
	//
	// Before claiming one, clear out the roots of earlier runs of this suite
	// whose process is gone: a `go test -timeout` panic skips both the defer
	// below and the post-Run sweep, so those runs' servers outlive every
	// cleanup installed here (wy-j2zc8q). Unclaimed roots, and roots whose
	// owner is still running, are left untouched.
	doltserver.SweepDeadSuiteRoots(os.TempDir(), suiteRootPrefix)

	suiteTempRoot, tempRootErr := os.MkdirTemp("", suiteRootPrefix+"*")
	if tempRootErr != nil {
		fmt.Fprintf(os.Stderr, "FATAL: failed to create suite temp root: %v\n", tempRootErr)
		return 1
	} else {
		defer os.RemoveAll(suiteTempRoot)
		if err := doltserver.WriteSuiteOwnerMarker(suiteTempRoot); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not claim suite temp root %s: %v\n", suiteTempRoot, err)
		}
		if err := os.Setenv(testCircuitBreakerDirEnv, filepath.Join(suiteTempRoot, "circuit")); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: failed to isolate circuit state: %v\n", err)
			return 1
		}
		defer os.Unsetenv(testCircuitBreakerDirEnv)
	}
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: %v, skipping Dolt tests\n", err)
	} else {
		defer testutil.TerminateDoltContainer()
		testServerPort = testutil.DoltContainerPortInt()

		// Guard against the ambient-Dolt-port bug (be-33se): confirm
		// applyConfigDefaults actually resolves to this suite's own
		// container rather than a stray ambient BEADS_DOLT_SERVER_PORT.
		resolveCfg := &Config{ServerPort: 0}
		applyConfigDefaults(resolveCfg)
		if resolveCfg.ServerPort != testServerPort {
			fmt.Fprintf(os.Stderr, "FATAL: applyConfigDefaults resolved port %d, want this suite's container port %d (ambient BEADS_DOLT_SERVER_PORT redirect?)\n", resolveCfg.ServerPort, testServerPort)
			return 1
		}

		// Set up shared database for branch-per-test isolation
		testSharedDB = "dolt_pkg_shared"
		db, err := testutil.SetupSharedTestDB(testServerPort, testSharedDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared DB setup failed: %v\n", err)
			return 1
		}
		testSharedConn = db
		defer db.Close()

		// Create the schema by opening a store against the shared DB,
		// configuring it, and committing.
		if err := initSharedSchema(testServerPort); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: shared schema init failed: %v\n", err)
			return 1
		}
	}

	code := m.Run()

	// Best-effort reap of any dolt sql-server left running under this
	// suite's own temp root (e.g. a SIGKILLed run of the multiprocess
	// schema init tests, which call doltserver.Start directly) — see
	// gastownhall/beads mybd-q6cz.
	swept := doltserver.SweepOrphanedTestServers(suiteTempRoot)
	code = doltserver.ApplyLeakPolicy("internal/storage/dolt", code, swept)

	testServerPort = 0
	os.Unsetenv("BEADS_DOLT_PORT")
	os.Unsetenv("BEADS_TEST_MODE")
	os.Unsetenv("BEADS_TEST_PDEATHSIG")
	return code
}

// suiteRootPrefix is testMainInner's os.MkdirTemp pattern without its random
// tail. It is what SweepDeadSuiteRoots globs for, so the two must not drift.
const suiteRootPrefix = "beads-storage-dolt-tests-"

// initSharedSchema creates a store on the shared DB, sets config, and commits
// so that branches inherit the full schema.
func initSharedSchema(port int) error {
	ctx := context.Background()
	cfg := &Config{
		Path:            "/tmp/dolt-shared-init", // not used, just needs to be non-empty
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        testSharedDB,
		MaxOpenConns:    1,
		CreateIfMissing: true, // TestMain creates the shared database
	}
	store, err := New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("New: %w", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		return fmt.Errorf("SetConfig(issue_prefix): %w", err)
	}

	// Commit schema to main so branches get a clean snapshot
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		return fmt.Errorf("DOLT_ADD: %w", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('--allow-empty', '-m', 'test: init shared schema')"); err != nil {
		return fmt.Errorf("DOLT_COMMIT: %w", err)
	}
	if err := testutil.MaterializeLocalTableSchemasForBranchTests(ctx, store.db); err != nil {
		return fmt.Errorf("materialize local table schemas: %w", err)
	}

	return nil
}
