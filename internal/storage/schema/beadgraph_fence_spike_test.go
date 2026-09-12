package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
)

// P0 verification row (iii) for the BDP bead-graph row-level fence
// (engdocs/BDP_GRAPH_CLI_AND_STORAGE_SPEC.md Part B4, ruling 13; the report is
// engdocs/BDP_P0_VERIFICATION_ROWS.md).
//
// Question: do the tree's migration hygiene checks and its content-skew
// comparison tolerate a migration that carries the trigger DDL and the
// dolt_schemas rows it creates — including the case where one clone has the
// triggers and another does not?
//
// The throwaway migration lives under testdata/beadgraph_fence_spike/ and is
// embedded here as its own migrationSource, so the REAL runner
// (runMigrations → execMigrationBody → content-hash record →
// commitMigrationStep) applies it without anything being added to the
// migration series. A variant without the trigger block
// (testdata/beadgraph_fence_spike_variant/) plays the divergent clone.
//
// Gates: the real-Dolt tests need a dolt binary (testutil.RequireDoltBinary,
// which honours BEADS_TEST_SKIP=dolt and fails rather than skips under
// GITHUB_ACTIONS), so the tree's Dolt lane runs them like every other
// real-Dolt test; the hygiene-script test needs only bash >= 4 and git and
// runs by default.

//go:embed testdata/beadgraph_fence_spike/*.up.sql
var beadGraphFenceSpikeFS embed.FS

//go:embed testdata/beadgraph_fence_spike_variant/*.up.sql
var beadGraphFenceSpikeVariantFS embed.FS

const (
	beadGraphSpikeFile    = "9001_beadgraph_fence_spike.up.sql"
	beadGraphSpikeVersion = 9001
	beadGraphSpikeInsert  = "INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES (?, 'r1', 'a', 1)"
	countSpikeTriggers    = "SELECT COUNT(*) FROM dolt_schemas WHERE type = 'trigger' AND name LIKE 'graph_beads_spike_%'"
)

var (
	beadGraphFenceSpikeSource = migrationSource{
		files:       beadGraphFenceSpikeFS,
		dir:         "testdata/beadgraph_fence_spike",
		cursorTable: mainSource.cursorTable,
	}
	beadGraphFenceSpikeVariantSource = migrationSource{
		files:       beadGraphFenceSpikeVariantFS,
		dir:         "testdata/beadgraph_fence_spike_variant",
		cursorTable: mainSource.cursorTable,
	}
)

func requireBeadGraphSpike(t *testing.T) {
	t.Helper()
	testutil.RequireDoltBinary(t)
}

func beadGraphSpikeSQL(t *testing.T) string {
	t.Helper()
	data, err := beadGraphFenceSpikeFS.ReadFile(beadGraphFenceSpikeSource.dir + "/" + beadGraphSpikeFile)
	if err != nil {
		t.Fatalf("read embedded spike migration: %v", err)
	}
	return string(data)
}

func beadGraphSpikeVariantSQL(t *testing.T) string {
	t.Helper()
	data, err := beadGraphFenceSpikeVariantFS.ReadFile(beadGraphFenceSpikeVariantSource.dir + "/" + beadGraphSpikeFile)
	if err != nil {
		t.Fatalf("read embedded variant migration: %v", err)
	}
	return string(data)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// startBeadGraphSpikeServer starts a scratch dolt sql-server through the tree's
// own launcher (internal/storage/dbproxy/server) under t.TempDir(), with
// DOLT_ROOT_PATH isolated so `dolt config --global` never touches ~/.dolt.
func startBeadGraphSpikeServer(t *testing.T) (host string, port int) {
	t.Helper()
	bin, err := exec.LookPath("dolt")
	if err != nil {
		t.Skipf("dolt not on PATH: %v", err)
	}
	t.Setenv("DOLT_ROOT_PATH", filepath.Join(t.TempDir(), "dolt-root"))
	rootDir := filepath.Join(t.TempDir(), "spike_srv")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for a free port: %v", err)
	}
	port = ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := fmt.Sprintf("log_level: warning\nlistener:\n  host: 127.0.0.1\n  port: %d\n", port)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write server config: %v", err)
	}
	srv, err := server.NewDoltServer(bin, rootDir, cfgPath, filepath.Join(t.TempDir(), "dolt-server.log"), 0, "")
	if err != nil {
		t.Fatalf("NewDoltServer: %v", err)
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := srv.Start(startCtx); err != nil {
		t.Fatalf("start scratch dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if err := srv.Stop(stopCtx); err != nil {
			t.Logf("stop scratch dolt sql-server: %v", err)
		}
	})
	return "127.0.0.1", port
}

// openBeadGraphSpikeDB opens a one-connection pool over the CLI server leg's
// DSN builder (doltutil.ServerDSN: parseTime, multiStatements=true,
// interpolateParams) and pings it until the engine answers.
func openBeadGraphSpikeDB(t *testing.T, host string, port int, database string) *sql.DB {
	t.Helper()
	dsn := doltutil.ServerDSN{Host: host, Port: port, User: "root", Database: database}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	deadline := time.Now().Add(30 * time.Second)
	for {
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := db.PingContext(pingCtx)
		cancel()
		if err == nil {
			return db
		}
		if time.Now().After(deadline) {
			t.Fatalf("ping %s: %v", dsn, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func createBeadGraphSpikeDatabase(t *testing.T, host string, port int, name string) {
	t.Helper()
	admin := openBeadGraphSpikeDB(t, host, port, "")
	if _, err := admin.ExecContext(context.Background(), "CREATE DATABASE IF NOT EXISTS "+name); err != nil {
		t.Fatalf("CREATE DATABASE %s: %v", name, err)
	}
}

func pinBeadGraphSpikeConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func scalarInt(t *testing.T, db DBConn, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func requireFencedSpike(t *testing.T, err error, what string) {
	t.Helper()
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1644 {
		t.Fatalf("%s: err = %v, want MySQL errno 1644 (the SIGNAL from the fence trigger)", what, err)
	}
}

// TestSpikeBeadGraphFenceRunnerAppliesTriggerDDL drives the production runner
// over the throwaway migration: execMigrationBody's single Exec creates the
// three triggers, the content hash of the file is recorded, and the per-step
// commit stages dolt_schemas along with the table and the cursor.
func TestSpikeBeadGraphFenceRunnerAppliesTriggerDDL(t *testing.T) {
	requireBeadGraphSpike(t)
	host, port := startBeadGraphSpikeServer(t)
	ctx := context.Background()
	createBeadGraphSpikeDatabase(t, host, port, "spike_runner")
	db := openBeadGraphSpikeDB(t, host, port, "spike_runner")
	conn := pinBeadGraphSpikeConn(t, db)

	// migrate() bootstraps the cursor table before runMigrations.
	if _, err := conn.ExecContext(ctx, beadGraphFenceSpikeSource.bootstrapSQL()); err != nil {
		t.Fatalf("bootstrap cursor table: %v", err)
	}
	applied, err := runMigrations(ctx, conn, beadGraphFenceSpikeSource, 0, 0, true)
	if err != nil {
		t.Fatalf("runMigrations over the trigger-carrying migration: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}

	hashes, err := ReadMigrationContentHashes(ctx, conn, "")
	if err != nil {
		t.Fatalf("ReadMigrationContentHashes: %v", err)
	}
	if want := sha256Hex(beadGraphSpikeSQL(t)); hashes[beadGraphSpikeVersion] != want {
		t.Fatalf("content_hash for %d = %q, want sha256 of the file %q", beadGraphSpikeVersion, hashes[beadGraphSpikeVersion], want)
	}
	if n := scalarInt(t, conn, countSpikeTriggers); n != 3 {
		t.Fatalf("dolt_schemas trigger rows = %d, want 3", n)
	}
	if n := scalarInt(t, conn, "SELECT COUNT(*) FROM dolt_schemas AS OF 'HEAD' WHERE type = 'trigger' AND name LIKE 'graph_beads_spike_%'"); n != 3 {
		t.Fatalf("dolt_schemas trigger rows AS OF HEAD = %d, want 3 (commitMigrationStep must have staged dolt_schemas)", n)
	}
	dirty, err := dirtyTables(ctx, conn, true)
	if err != nil {
		t.Fatalf("dirtyTables: %v", err)
	}
	if len(dirty) != 0 {
		t.Fatalf("working set after the per-step commit is dirty: %v, want clean", dirty)
	}
	if n := scalarInt(t, conn, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", "schema: apply migration "+beadGraphSpikeFile); n != 1 {
		t.Fatalf("per-step commit for %s not found in dolt_log (count %d)", beadGraphSpikeFile, n)
	}

	// The fence works over the tree's DSN: refused without the variable,
	// allowed with it, refused again after the clear.
	_, err = conn.ExecContext(ctx, beadGraphSpikeInsert, "b/raw")
	requireFencedSpike(t, err, "raw INSERT")
	if _, err := conn.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
		t.Fatalf("SET role: %v", err)
	}
	for _, stmt := range []string{
		"INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES ('b/inrole', 'r1', 'a', 1)",
		"UPDATE graph_beads_spike SET revision = 'r2' WHERE path = 'b/inrole'",
		"DELETE FROM graph_beads_spike WHERE path = 'b/inrole'",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("in-role %q: %v", stmt, err)
		}
	}
	// Leave one row behind in role so the BEFORE UPDATE/DELETE triggers have a
	// row to fire on (a row trigger never fires on a zero-row statement).
	if _, err := conn.ExecContext(ctx, beadGraphSpikeInsert, "b/seed"); err != nil {
		t.Fatalf("in-role seed INSERT: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "SET @bd_graph_role = NULL"); err != nil {
		t.Fatalf("clear role: %v", err)
	}
	_, err = conn.ExecContext(ctx, "UPDATE graph_beads_spike SET revision = 'r3' WHERE path = 'b/seed'")
	requireFencedSpike(t, err, "raw UPDATE")
	_, err = conn.ExecContext(ctx, "DELETE FROM graph_beads_spike WHERE path = 'b/seed'")
	requireFencedSpike(t, err, "raw DELETE")
	var revision string
	if err := conn.QueryRowContext(ctx, "SELECT revision FROM graph_beads_spike WHERE path = 'b/seed'").Scan(&revision); err != nil || revision != "r1" {
		t.Fatalf("seed row after the refused UPDATE/DELETE: revision=%q err=%v, want r1 untouched", revision, err)
	}

	// Resumable: the DROP TRIGGER IF EXISTS + CREATE TRIGGER pair re-applies
	// cleanly through execMigrationBody and leaves exactly three triggers.
	if err := execMigrationBody(ctx, conn, beadGraphSpikeSQL(t)); err != nil {
		t.Fatalf("re-applying the migration text: %v", err)
	}
	if n := scalarInt(t, conn, countSpikeTriggers); n != 3 {
		t.Fatalf("dolt_schemas trigger rows after re-apply = %d, want 3", n)
	}
}

// TestSpikeBeadGraphFenceDirtyDoltSchemasGuards exercises MigrateUp's
// dirty-table machinery against dolt_schemas rows: dolt_status reports the
// table, dolt_diff signs it, the pending-migration text scan cannot see
// trigger DDL as a touch of dolt_schemas, the post-pass signature check does,
// and stageSchemaTables stages it on the fresh path.
func TestSpikeBeadGraphFenceDirtyDoltSchemasGuards(t *testing.T) {
	requireBeadGraphSpike(t)
	host, port := startBeadGraphSpikeServer(t)
	ctx := context.Background()
	createBeadGraphSpikeDatabase(t, host, port, "spike_guards")
	db := openBeadGraphSpikeDB(t, host, port, "spike_guards")
	conn := pinBeadGraphSpikeConn(t, db)

	spikeSQL := beadGraphSpikeSQL(t)
	if !migrationSQLTouchesTable(spikeSQL, "graph_beads_spike") {
		t.Fatal("migrationSQLTouchesTable must see the CREATE TABLE in the spike migration")
	}
	if migrationSQLTouchesTable(spikeSQL, "dolt_schemas") {
		t.Fatal("migrationSQLTouchesTable unexpectedly treats trigger DDL as a dolt_schemas touch")
	}
	t.Log("finding: CREATE/DROP TRIGGER is invisible to migrationSQLTouchesTable, so a pre-existing dirty dolt_schemas is not refused up front by pendingMigrationDirtyTables")

	// Baseline: the table without triggers, committed.
	if _, err := conn.ExecContext(ctx, beadGraphFenceSpikeSource.bootstrapSQL()); err != nil {
		t.Fatalf("bootstrap cursor table: %v", err)
	}
	if _, err := conn.ExecContext(ctx, beadGraphSpikeVariantSQL(t)); err != nil {
		t.Fatalf("apply the trigger-less variant: %v", err)
	}
	if err := DrainCall(ctx, conn, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("DOLT_ADD: %v", err)
	}
	if err := DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', 'spike: baseline table')"); err != nil {
		t.Fatalf("DOLT_COMMIT: %v", err)
	}

	// Pre-existing dirt on dolt_schemas: an uncommitted trigger (a view would
	// dirty the same table).
	if _, err := conn.ExecContext(ctx,
		"CREATE TRIGGER pre_existing_dirt BEFORE INSERT ON graph_beads_spike FOR EACH ROW SET NEW.revision = NEW.revision"); err != nil {
		t.Fatalf("create pre-existing dirt trigger: %v", err)
	}
	dirty, err := dirtyTables(ctx, conn, true)
	if err != nil {
		t.Fatalf("dirtyTables: %v", err)
	}
	if _, ok := dirty["dolt_schemas"]; !ok {
		t.Fatalf("dirtyTables = %v, want dolt_schemas reported dirty by dolt_status", dirty)
	}
	before, err := dirtyTableSignatures(ctx, conn, dirty)
	if err != nil {
		t.Fatalf("dirtyTableSignatures over dolt_schemas (dolt_diff HEAD..WORKING): %v", err)
	}
	if before["dolt_schemas"] == "" {
		t.Fatal("empty signature for dolt_schemas")
	}
	touched, err := beadGraphFenceSpikeSource.pendingMigrationDirtyTables(ctx, conn, dirty)
	if err != nil {
		t.Fatalf("pendingMigrationDirtyTables: %v", err)
	}
	if len(touched) != 0 {
		t.Fatalf("pendingMigrationDirtyTables = %v, want none (the guard does not see trigger DDL)", touched)
	}

	// The pass runs (the guard let it) and the migration's own trigger DDL
	// changes the already-dirty dolt_schemas: the post-pass signature check is
	// what catches it — as a hard error at the END of MigrateUp.
	if err := execMigrationBody(ctx, conn, spikeSQL); err != nil {
		t.Fatalf("execMigrationBody: %v", err)
	}
	changed, err := changedDirtyTableSignatures(ctx, conn, before)
	if err != nil {
		t.Fatalf("changedDirtyTableSignatures: %v", err)
	}
	if len(changed) != 1 || changed[0] != "dolt_schemas" {
		t.Fatalf("changedDirtyTableSignatures = %v, want [dolt_schemas]", changed)
	}
	t.Logf("finding: with a pre-existing dirty dolt_schemas, MigrateUp fails after the pass with %q rather than refusing up front", "pre-existing dirty tables changed during schema migration: dolt_schemas")

	// The fresh-clone path: nothing was dirty before, stageSchemaTables stages
	// dolt_schemas with everything else and the terminal commit lands it.
	if _, err := conn.ExecContext(ctx, "DROP TRIGGER pre_existing_dirt"); err != nil {
		t.Fatalf("drop pre-existing dirt trigger: %v", err)
	}
	staged, err := stageSchemaTables(ctx, conn, map[string]dirtyTableState{})
	if err != nil {
		t.Fatalf("stageSchemaTables: %v", err)
	}
	if !staged {
		t.Fatal("stageSchemaTables staged nothing")
	}
	var stagedSchemas int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'dolt_schemas' AND staged = 1").Scan(&stagedSchemas); err != nil {
		t.Fatalf("dolt_status staged dolt_schemas: %v", err)
	}
	if stagedSchemas != 1 {
		t.Fatalf("dolt_schemas staged rows = %d, want 1 (DOLT_ADD -f dolt_schemas)", stagedSchemas)
	}
	if err := DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', 'schema: apply migrations')"); err != nil {
		t.Fatalf("terminal DOLT_COMMIT: %v", err)
	}
	if n := scalarInt(t, conn, "SELECT COUNT(*) FROM dolt_schemas AS OF 'HEAD' WHERE type = 'trigger' AND name LIKE 'graph_beads_spike_%'"); n != 3 {
		t.Fatalf("triggers at HEAD after the terminal commit = %d, want 3", n)
	}
	dirty, err = dirtyTables(ctx, conn, true)
	if err != nil {
		t.Fatalf("dirtyTables after commit: %v", err)
	}
	if len(dirty) != 0 {
		t.Fatalf("working set dirty after the terminal commit: %v", dirty)
	}
}

// TestSpikeBeadGraphFenceContentSkewAcrossClones runs content_skew.go's
// comparison the way bd doctor runs it (local HEAD vs AS OF
// remotes/origin/main) across a push/clone that replicates the triggers, and
// against a clone that applied different content for the same version.
func TestSpikeBeadGraphFenceContentSkewAcrossClones(t *testing.T) {
	requireBeadGraphSpike(t)
	host, port := startBeadGraphSpikeServer(t)
	ctx := context.Background()
	createBeadGraphSpikeDatabase(t, host, port, "spike_a")
	createBeadGraphSpikeDatabase(t, host, port, "spike_c")

	// Clone A: the real runner applies the trigger-carrying migration.
	dbA := openBeadGraphSpikeDB(t, host, port, "spike_a")
	connA := pinBeadGraphSpikeConn(t, dbA)
	if _, err := connA.ExecContext(ctx, beadGraphFenceSpikeSource.bootstrapSQL()); err != nil {
		t.Fatalf("bootstrap A: %v", err)
	}
	if _, err := runMigrations(ctx, connA, beadGraphFenceSpikeSource, 0, 0, true); err != nil {
		t.Fatalf("runMigrations on A: %v", err)
	}
	// Clone C: the real runner applies the VARIANT — same version, no triggers.
	dbC := openBeadGraphSpikeDB(t, host, port, "spike_c")
	connC := pinBeadGraphSpikeConn(t, dbC)
	if _, err := connC.ExecContext(ctx, beadGraphFenceSpikeVariantSource.bootstrapSQL()); err != nil {
		t.Fatalf("bootstrap C: %v", err)
	}
	if _, err := runMigrations(ctx, connC, beadGraphFenceSpikeVariantSource, 0, 0, true); err != nil {
		t.Fatalf("runMigrations on C: %v", err)
	}
	hashesA, err := ReadMigrationContentHashes(ctx, connA, "")
	if err != nil {
		t.Fatalf("hashes A: %v", err)
	}
	hashesC, err := ReadMigrationContentHashes(ctx, connC, "")
	if err != nil {
		t.Fatalf("hashes C: %v", err)
	}
	if skew := ContentHashSkew(hashesA, hashesC); len(skew) != 1 || skew[0] != beadGraphSpikeVersion {
		t.Fatalf("ContentHashSkew(A, C) = %v, want [%d] (same version, different content)", skew, beadGraphSpikeVersion)
	}
	t.Logf("divergent content for version %d is reported as skew: A=%s C=%s", beadGraphSpikeVersion, hashesA[beadGraphSpikeVersion][:12], hashesC[beadGraphSpikeVersion][:12])

	// A pushes to a file remote; B is a server-side clone of it.
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	remoteURL := "file://" + filepath.ToSlash(remoteDir)
	if err := DrainCall(ctx, connA, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL); err != nil {
		t.Fatalf("DOLT_REMOTE add: %v", err)
	}
	if err := DrainCall(ctx, connA, "CALL DOLT_PUSH('origin', 'main')"); err != nil {
		t.Fatalf("DOLT_PUSH: %v", err)
	}
	admin := openBeadGraphSpikeDB(t, host, port, "")
	if err := DrainCall(ctx, admin, "CALL DOLT_CLONE(?, 'spike_b')", remoteURL); err != nil {
		t.Fatalf("DOLT_CLONE: %v", err)
	}
	dbB := openBeadGraphSpikeDB(t, host, port, "spike_b")
	connB := pinBeadGraphSpikeConn(t, dbB)
	if n := scalarInt(t, connB, countSpikeTriggers); n != 3 {
		t.Fatalf("clone B trigger rows = %d, want 3 (triggers replicate through dolt_schemas)", n)
	}
	_, err = connB.ExecContext(ctx, beadGraphSpikeInsert, "b/raw-on-clone")
	requireFencedSpike(t, err, "raw INSERT on the clone")

	localB, err := ReadMigrationContentHashes(ctx, connB, "")
	if err != nil {
		t.Fatalf("hashes B: %v", err)
	}
	remoteB, err := ReadMigrationContentHashes(ctx, connB, "remotes/origin/main")
	if err != nil {
		t.Fatalf("hashes B AS OF remotes/origin/main: %v", err)
	}
	if skew := ContentHashSkew(localB, remoteB); len(skew) != 0 {
		t.Fatalf("ContentHashSkew(B local, B remote) = %v, want none", skew)
	}
	if localB[beadGraphSpikeVersion] != hashesA[beadGraphSpikeVersion] {
		t.Fatalf("clone B hash %q != A hash %q", localB[beadGraphSpikeVersion], hashesA[beadGraphSpikeVersion])
	}

	// B drops its triggers out of band and commits: the migration content
	// hashes are unchanged, so the doctor's skew check sees nothing.
	for _, trig := range []string{"graph_beads_spike_bi", "graph_beads_spike_bu", "graph_beads_spike_bd"} {
		if _, err := connB.ExecContext(ctx, "DROP TRIGGER "+trig); err != nil {
			t.Fatalf("DROP TRIGGER %s on B: %v", trig, err)
		}
	}
	if err := DrainCall(ctx, connB, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("DOLT_ADD on B: %v", err)
	}
	if err := DrainCall(ctx, connB, "CALL DOLT_COMMIT('-m', 'out of band: drop the fence')"); err != nil {
		t.Fatalf("DOLT_COMMIT on B: %v", err)
	}
	if _, err := connB.ExecContext(ctx, beadGraphSpikeInsert, "b/raw-after-drop"); err != nil {
		t.Fatalf("raw INSERT after dropping the triggers should pass: %v", err)
	}
	localB2, err := ReadMigrationContentHashes(ctx, connB, "")
	if err != nil {
		t.Fatalf("hashes B after drop: %v", err)
	}
	if skew := ContentHashSkew(localB2, remoteB); len(skew) != 0 {
		t.Fatalf("ContentHashSkew after the out-of-band drop = %v, want none (content skew does not cover dolt_schemas)", skew)
	}
	localTriggers := scalarInt(t, connB, "SELECT COUNT(*) FROM dolt_schemas WHERE type = 'trigger'")
	remoteTriggers := scalarInt(t, connB, "SELECT COUNT(*) FROM dolt_schemas AS OF 'remotes/origin/main' WHERE type = 'trigger'")
	if localTriggers != 0 || remoteTriggers != 3 {
		t.Fatalf("dolt_schemas triggers local=%d remote=%d, want 0 and 3", localTriggers, remoteTriggers)
	}
	t.Logf("finding: a clone that dropped the fence out of band shows no content skew (hashes equal); only dolt_schemas itself (local %d vs remote %d trigger rows) reveals it — dolt_schemas is not one of the eight tables ruling 14's fetch-inspect reads", localTriggers, remoteTriggers)
}

// TestSpikeBeadGraphFenceHygieneScript runs scripts/check-migration-hygiene.sh
// (checks A–E) in a scratch git repository that mirrors the migration tree,
// with the trigger-carrying spike file added as a new main-plane migration, and
// with negative controls proving each check still bites. Needs bash >= 4
// (check E uses associative arrays) and git; no Dolt.
func TestSpikeBeadGraphFenceHygieneScript(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "check-migration-hygiene.sh"))
	if err != nil {
		t.Fatalf("abs script path: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skipf("hygiene script not found at %s: %v", script, err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not on PATH: %v", err)
	}
	out, err := exec.Command(bash, "-c", `echo "${BASH_VERSINFO[0]}"`).Output()
	if err != nil {
		t.Skipf("probe bash version: %v", err)
	}
	if major, _ := strconv.Atoi(strings.TrimSpace(string(out))); major < 4 {
		t.Skipf("bash %s is too old for the hygiene script's associative arrays (need >= 4)", strings.TrimSpace(string(out)))
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}

	repo := t.TempDir()
	migDir := filepath.Join(repo, "internal", "storage", "schema", "migrations")
	if err := os.MkdirAll(filepath.Dir(migDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.CopyFS(migDir, os.DirFS("migrations")); err != nil {
		t.Fatalf("copy migrations tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	scriptBytes, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "scripts", "check-migration-hygiene.sh"), scriptBytes, 0o755); err != nil {
		t.Fatalf("write script copy: %v", err)
	}
	gitEnv := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(repo, ".no-gitconfig"), "HOME="+repo)
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-c", "user.name=spike", "-c", "user.email=spike@localhost", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		cmd.Env = gitEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-q", "-b", "main")
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "base: the shipped migration tree")
	base := runGit("rev-parse", "HEAD")

	runHygiene := func() (int, string) {
		t.Helper()
		cmd := exec.Command(bash, "scripts/check-migration-hygiene.sh")
		cmd.Dir = repo
		cmd.Env = append(gitEnv, "BASE_SHA="+base)
		out, err := cmd.CombinedOutput()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("run hygiene script: %v\n%s", err, out)
		}
		return code, string(out)
	}
	writeMigration := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(migDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	reset := func() {
		t.Helper()
		runGit("checkout", "--", ".")
		runGit("clean", "-fdq")
	}
	spikeSQL := beadGraphSpikeSQL(t)

	t.Run("the trigger-carrying migration passes checks A-E", func(t *testing.T) {
		defer reset()
		writeMigration(beadGraphSpikeFile, spikeSQL)
		code, out := runHygiene()
		if code != 0 || !strings.Contains(out, "Migration hygiene OK.") {
			t.Fatalf("hygiene exit %d, want 0 with OK:\n%s", code, out)
		}
		if strings.Contains(out, "WARN") {
			t.Fatalf("a check was skipped (no usable base ref?):\n%s", out)
		}
	})

	t.Run("control: NOW() inside the trigger body trips check B", func(t *testing.T) {
		defer reset()
		writeMigration(beadGraphSpikeFile, strings.Replace(spikeSQL,
			"SET MESSAGE_TEXT = 'graph_beads_spike: out-of-role write refused'",
			"SET MESSAGE_TEXT = CONCAT('refused at ', NOW())", 1))
		code, out := runHygiene()
		if code == 0 || !strings.Contains(out, "FAIL (nondeterministic SQL)") {
			t.Fatalf("hygiene exit %d, want check B failure:\n%s", code, out)
		}
	})

	t.Run("control: editing a shipped file trips check C", func(t *testing.T) {
		defer reset()
		shipped := filepath.Join(migDir, "0001_create_issues.up.sql")
		f, err := os.OpenFile(shipped, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open shipped migration: %v", err)
		}
		if _, err := f.WriteString("\n-- spike edit\n"); err != nil {
			t.Fatalf("append: %v", err)
		}
		_ = f.Close()
		code, out := runHygiene()
		if code == 0 || !strings.Contains(out, "FAIL (frozen migrations)") {
			t.Fatalf("hygiene exit %d, want check C failure:\n%s", code, out)
		}
	})

	t.Run("control: main-plane DDL on the clone-local leases table without an ignored twin trips check D", func(t *testing.T) {
		defer reset()
		writeMigration("9002_spike_leases_column.up.sql", "ALTER TABLE leases ADD COLUMN spike_col INT;\n")
		code, out := runHygiene()
		if code == 0 || !strings.Contains(out, "FAIL (ignored twin missing)") {
			t.Fatalf("hygiene exit %d, want check D failure:\n%s", code, out)
		}
	})

	t.Run("finding: the B4 lease table is not in check D's clone-local list, so a twin-less main-plane CREATE passes today", func(t *testing.T) {
		defer reset()
		writeMigration("9003_beadgraph_authority_lease_spike.up.sql",
			"CREATE TABLE IF NOT EXISTS graph_authority_lease (id TINYINT NOT NULL, fence CHAR(32) NOT NULL, PRIMARY KEY (id));\n")
		code, out := runHygiene()
		if code != 0 {
			t.Fatalf("hygiene exit %d for a twin-less graph_authority_lease migration; the script's clone-local list must have learned it:\n%s", code, out)
		}
		t.Log("finding: scripts/check-migration-hygiene.sh's ignored_tables list (and schema.go's doltIgnorePatterns) do not know graph_authority_lease; P1 must add it or check D cannot enforce the B4 twin")
	})
}

// TestSpikeBeadGraphFenceCLIBundleRoute shows the two ways a migration text
// reaches Dolt disagree on trigger bodies: the runner's single multi-statement
// Exec over the MySQL protocol takes the frozen text as-is, while
// AllMigrationsSQL's `dolt sql -f` route splits on the body's inner
// semicolons unless the text is DELIMITER-wrapped — and the wrapped text is
// refused over the protocol. One file cannot serve both routes; the bundle
// needs a cliCompatibleMigrationSQL rendition.
func TestSpikeBeadGraphFenceCLIBundleRoute(t *testing.T) {
	requireBeadGraphSpike(t)
	t.Setenv("DOLT_ROOT_PATH", filepath.Join(t.TempDir(), "dolt-root"))
	spikeSQL := beadGraphSpikeSQL(t)
	if hits := preparedALTERTableStatements(spikeSQL); len(hits) != 0 {
		t.Fatalf("preparedALTERTableStatements flagged the trigger migration: %+v", hits)
	}
	if got := cliCompatibleMigrationSQL(beadGraphSpikeFile, spikeSQL); got != spikeSQL {
		t.Fatal("cliCompatibleMigrationSQL substituted an unknown migration; expected the frozen text unchanged")
	}

	dir := filepath.Join(t.TempDir(), "cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runDoltCommand(t, dir, "init", "--name", "spike", "--email", "spike@localhost")

	// 1. The frozen text through `dolt sql -f` (the AllMigrationsSQL route).
	file := filepath.Join(t.TempDir(), "spike.sql")
	if err := os.WriteFile(file, []byte(spikeSQL), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("dolt", "sql", "-f", file)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("`dolt sql -f` accepted the BEGIN ... END trigger body as-is; the CLI splitter must have changed:\n%s", out)
	}
	t.Logf("`dolt sql -f` on the frozen text: %v\n%s", err, out)
	if !strings.Contains(string(out), "syntax error") {
		t.Fatalf("expected a parse error from the CLI splitter, got:\n%s", out)
	}

	// 2. A DELIMITER-wrapped rendition loads through the CLI.
	wrapped := delimiterWrapBeadGraphSpike(spikeSQL)
	runDoltSQL(t, dir, wrapped)
	rows := queryDoltCSV(t, dir, "SELECT COUNT(*) AS c FROM dolt_schemas WHERE type = 'trigger'")
	if len(rows) != 1 || rows[0]["c"] != "3" {
		t.Fatalf("triggers after the DELIMITER-wrapped `dolt sql -f` = %v, want 3", rows)
	}

	// 3. The DELIMITER-wrapped rendition over the MySQL protocol (the runner's
	// route) is refused: DELIMITER is a client-side command.
	host, port := startBeadGraphSpikeServer(t)
	createBeadGraphSpikeDatabase(t, host, port, "spike_cli")
	db := openBeadGraphSpikeDB(t, host, port, "spike_cli")
	if _, err := db.ExecContext(context.Background(), wrapped); err == nil {
		t.Fatal("the sql-server accepted DELIMITER over the protocol; one text could serve both routes after all")
	} else {
		t.Logf("DELIMITER-wrapped text over the protocol: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), spikeSQL); err != nil {
		t.Fatalf("the frozen text over the protocol (execMigrationBody's route): %v", err)
	}
	if n := scalarInt(t, db, countSpikeTriggers); n != 3 {
		t.Fatalf("triggers over the protocol = %d, want 3", n)
	}
}

// delimiterWrapBeadGraphSpike rewrites the spike migration for the `dolt sql
// -f` route: every top-level statement is terminated by // instead of ; so the
// trigger bodies' inner semicolons are not statement boundaries.
func delimiterWrapBeadGraphSpike(sqlText string) string {
	var b strings.Builder
	b.WriteString("DELIMITER //\n")
	depth := 0
	for _, line := range strings.Split(sqlText, "\n") {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		switch {
		case upper == "BEGIN":
			depth++
			b.WriteString(line + "\n")
		case upper == "END;":
			depth--
			b.WriteString(strings.TrimSuffix(line, ";") + "//\n")
		case depth == 0 && strings.HasSuffix(trimmed, ";") && !strings.HasPrefix(trimmed, "--"):
			b.WriteString(strings.TrimSuffix(line, ";") + "//\n")
		default:
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("DELIMITER ;\n")
	return b.String()
}
