//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// P0 verification row (i) for the BDP bead-graph row-level fence
// (engdocs/BDP_GRAPH_CLI_AND_STORAGE_SPEC.md Part B4, ruling 13; the report is
// engdocs/BDP_P0_VERIFICATION_ROWS.md).
//
// Question: through the tree's EMBEDDED Dolt path — the in-process engine
// behind embeddeddolt.OpenSQL, not a sql-server — does a migration-style
// multi-statement SQL text create the session-gated BEGIN ... END triggers,
// do they fire on INSERT/UPDATE/DELETE, and does SET @bd_graph_role = 1 in
// the same session let a write through?
//
// The migration text is the throwaway file under
// internal/storage/schema/testdata/beadgraph_fence_spike/ (never part of the
// migration series). It is applied exactly the way production applies a
// migration on this leg: EmbeddedDoltStore.ApplySchemaMigrations opens the
// engine with OpenSQL, pins one *sql.Conn, and schema.execMigrationBody runs
// the whole file through a single ExecContext (the DSN carries
// multiStatements=true; the driver splits with the engine's own parser, see
// github.com/dolthub/driver/v2 conn.go prepareMultiStatement).
//
// Gate: BEADS_TEST_EMBEDDED_DOLT=1, like every other cgo embedded-dolt test in
// this package. Deterministic; no server, no network.

const beadGraphFenceSpikeFile = "9001_beadgraph_fence_spike.up.sql"

func requireEmbeddedForFenceSpike(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run the embedded bead-graph fence spike")
	}
}

// readBeadGraphFenceSpikeSQL reads the shared throwaway migration text. go test
// runs with the package directory as cwd, so the schema package's testdata is
// two directories over.
func readBeadGraphFenceSpikeSQL(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "schema", "testdata", "beadgraph_fence_spike", beadGraphFenceSpikeFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spike migration %s: %v", path, err)
	}
	return string(data)
}

func isOutOfRoleRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "out-of-role write refused")
}

func TestSpikeBeadGraphFenceEmbeddedLeg(t *testing.T) {
	requireEmbeddedForFenceSpike(t)
	ctx := t.Context()
	spikeSQL := readBeadGraphFenceSpikeSQL(t)

	dataDir := filepath.Join(t.TempDir(), ".beads", "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// initSchema shape: bootstrap engine, CREATE DATABASE, close.
	boot, bootCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("OpenSQL boot: %v", err)
	}
	if _, err := boot.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS spike"); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	if err := bootCleanup(); err != nil {
		t.Fatalf("close boot engine: %v", err)
	}

	// ApplySchemaMigrations shape: OpenSQL(database, branch) + one pinned conn.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "spike", "main")
	if err != nil {
		t.Fatalf("OpenSQL spike: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin conn: %v", err)
	}

	t.Run("migration-style multi-statement Exec creates the triggers", func(t *testing.T) {
		// execMigrationBody shape: the whole file in ONE ExecContext.
		if _, err := conn.ExecContext(ctx, spikeSQL); err != nil {
			t.Fatalf("ROW (i) FAIL — the embedded engine refused the migration text: %v", err)
		}
		var triggers int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_schemas WHERE type = 'trigger' AND name LIKE 'graph_beads_spike_%'").Scan(&triggers); err != nil {
			t.Fatalf("count dolt_schemas triggers: %v", err)
		}
		if triggers != 3 {
			t.Fatalf("dolt_schemas trigger rows = %d, want 3", triggers)
		}
		// The DROP TRIGGER IF EXISTS + CREATE TRIGGER pair is resumable: a
		// second pass of the same text must succeed and leave exactly three.
		if _, err := conn.ExecContext(ctx, spikeSQL); err != nil {
			t.Fatalf("second pass of the migration text (resumability): %v", err)
		}
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_schemas WHERE type = 'trigger' AND name LIKE 'graph_beads_spike_%'").Scan(&triggers); err != nil {
			t.Fatalf("count dolt_schemas triggers after second pass: %v", err)
		}
		if triggers != 3 {
			t.Fatalf("dolt_schemas trigger rows after second pass = %d, want 3", triggers)
		}
	})

	t.Run("triggers fire on INSERT, UPDATE and DELETE without the role variable", func(t *testing.T) {
		_, err := conn.ExecContext(ctx,
			"INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES ('b/raw', 'r1', 'a', 1)")
		if !isOutOfRoleRefusal(err) {
			t.Fatalf("raw INSERT: err = %v, want the out-of-role refusal", err)
		}
		t.Logf("embedded INSERT refusal: (%T) %v", err, err)
		// Seed one row in role so UPDATE/DELETE have something to refuse.
		if _, err := conn.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
			t.Fatalf("SET role: %v", err)
		}
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES ('b/seed', 'r1', 'a', 1)"); err != nil {
			t.Fatalf("in-role seed INSERT: %v", err)
		}
		if _, err := conn.ExecContext(ctx, "SET @bd_graph_role = NULL"); err != nil {
			t.Fatalf("clear role: %v", err)
		}
		_, err = conn.ExecContext(ctx, "UPDATE graph_beads_spike SET revision = 'r2' WHERE path = 'b/seed'")
		if !isOutOfRoleRefusal(err) {
			t.Fatalf("raw UPDATE: err = %v, want the out-of-role refusal", err)
		}
		_, err = conn.ExecContext(ctx, "DELETE FROM graph_beads_spike WHERE path = 'b/seed'")
		if !isOutOfRoleRefusal(err) {
			t.Fatalf("raw DELETE: err = %v, want the out-of-role refusal", err)
		}
		var revision string
		if err := conn.QueryRowContext(ctx, "SELECT revision FROM graph_beads_spike WHERE path = 'b/seed'").Scan(&revision); err != nil {
			t.Fatalf("read seed row: %v", err)
		}
		if revision != "r1" {
			t.Fatalf("seed row revision = %q after refused UPDATE/DELETE, want r1", revision)
		}
	})

	t.Run("SET @bd_graph_role = 1 in the same session lets INSERT, UPDATE and DELETE through", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
			t.Fatalf("SET role: %v", err)
		}
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES ('b/inrole', 'r1', 'a', 1)"); err != nil {
			t.Fatalf("in-role INSERT: %v", err)
		}
		if _, err := conn.ExecContext(ctx, "UPDATE graph_beads_spike SET revision = 'r2' WHERE path = 'b/inrole'"); err != nil {
			t.Fatalf("in-role UPDATE: %v", err)
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM graph_beads_spike WHERE path = 'b/inrole'"); err != nil {
			t.Fatalf("in-role DELETE: %v", err)
		}
		if _, err := conn.ExecContext(ctx, "SET @bd_graph_role = NULL"); err != nil {
			t.Fatalf("clear role: %v", err)
		}
		_, err := conn.ExecContext(ctx,
			"INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES ('b/raw2', 'r1', 'a', 1)")
		if !isOutOfRoleRefusal(err) {
			t.Fatalf("INSERT after clearing the role: err = %v, want the out-of-role refusal", err)
		}
	})

	t.Run("dolt_schemas is a dirty table the runner can stage and commit", func(t *testing.T) {
		// commitMigrationStep shape: dolt_status lists the tables the step
		// dirtied, then DOLT_ADD('-f', table) each and DOLT_COMMIT.
		rows, err := conn.QueryContext(ctx, "SELECT table_name FROM dolt_status")
		if err != nil {
			t.Fatalf("dolt_status: %v", err)
		}
		dirty := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				t.Fatalf("scan dolt_status: %v", err)
			}
			dirty[name] = true
		}
		rows.Close()
		if !dirty["dolt_schemas"] || !dirty["graph_beads_spike"] {
			t.Fatalf("dolt_status = %v, want both dolt_schemas and graph_beads_spike dirty", dirty)
		}
		for _, table := range []string{"dolt_schemas", "graph_beads_spike"} {
			if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('-f', ?)", table); err != nil {
				t.Fatalf("DOLT_ADD -f %s: %v", table, err)
			}
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'spike: apply migration 9001_beadgraph_fence_spike.up.sql')"); err != nil {
			t.Fatalf("DOLT_COMMIT: %v", err)
		}
		var committed int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_schemas AS OF 'HEAD' WHERE type = 'trigger' AND name LIKE 'graph_beads_spike_%'").Scan(&committed); err != nil {
			t.Fatalf("count committed triggers: %v", err)
		}
		if committed != 3 {
			t.Fatalf("triggers at HEAD = %d, want 3", committed)
		}
		var left int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&left); err != nil {
			t.Fatalf("dolt_status after commit: %v", err)
		}
		if left != 0 {
			t.Fatalf("dolt_status has %d rows after the commit, want a clean working set", left)
		}
	})

	if err := conn.Close(); err != nil {
		t.Fatalf("close pinned conn: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("close engine: %v", err)
	}

	t.Run("withConn shape: a fresh engine, a *sql.Tx per call", func(t *testing.T) {
		// EmbeddedDoltStore.withConn opens OpenSQL per call, BeginTx, runs the
		// body, commits or rolls back, and closes the engine handle. The fence
		// must be visible to that fresh engine instance (it was committed
		// above) and the variable must gate inside the transaction.
		db2, cleanup2, err := embeddeddolt.OpenSQL(ctx, dataDir, "spike", "main")
		if err != nil {
			t.Fatalf("OpenSQL (fresh): %v", err)
		}
		defer func() { _ = cleanup2() }()

		// Out of role: refused inside the transaction.
		tx, err := db2.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		_, err = tx.ExecContext(ctx,
			"INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES ('b/tx-raw', 'r1', 'a', 1)")
		if !isOutOfRoleRefusal(err) {
			_ = tx.Rollback()
			t.Fatalf("out-of-role INSERT inside *sql.Tx: err = %v, want the out-of-role refusal", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}

		// In role: set inside the transaction, write, clear, commit.
		tx, err = db2.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		if _, err := tx.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
			t.Fatalf("SET role in tx: %v", err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES ('b/tx-inrole', 'r1', 'a', 1)"); err != nil {
			t.Fatalf("in-role INSERT inside *sql.Tx: %v", err)
		}
		if _, err := tx.ExecContext(ctx, "SET @bd_graph_role = NULL"); err != nil {
			t.Fatalf("clear role in tx: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		var n int
		if err := db2.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_beads_spike WHERE path = 'b/tx-inrole'").Scan(&n); err != nil {
			t.Fatalf("count committed row: %v", err)
		}
		if n != 1 {
			t.Fatalf("committed in-role row count = %d, want 1", n)
		}
	})

	t.Run("pool hygiene on the embedded driver: a released connection is never reused, so an uncleared variable cannot leak", func(t *testing.T) {
		// Row (ii) is a sql-server question, but the embedded leg's pool is
		// worth pinning too: github.com/dolthub/driver/v2 conn.go ResetSession
		// returns driver.ErrBadConn on purpose ("do not try to reuse
		// connections ... throw the session away and get a new one"), so
		// database/sql opens a fresh session for every checkout after a
		// release. A commit that forgets to clear @bd_graph_role therefore
		// cannot unfence the next statement here — unlike the sql-server
		// driver, whose ResetSession only checks liveness. (Production also
		// closes the whole engine handle per store call in withConn.)
		db3, cleanup3, err := embeddeddolt.OpenSQL(ctx, dataDir, "spike", "main")
		if err != nil {
			t.Fatalf("OpenSQL (pool): %v", err)
		}
		defer func() { _ = cleanup3() }()
		db3.SetMaxOpenConns(1)

		tx, err := db3.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		if _, err := tx.ExecContext(ctx, "SET @bd_graph_role = 1"); err != nil {
			t.Fatalf("SET role: %v", err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES ('b/pool', 'r1', 'a', 1)"); err != nil {
			t.Fatalf("in-role INSERT: %v", err)
		}
		if err := tx.Commit(); err != nil { // deliberately no clear
			t.Fatalf("commit: %v", err)
		}
		var role sql.NullInt64
		if err := db3.QueryRowContext(ctx, "SELECT @bd_graph_role").Scan(&role); err != nil {
			t.Fatalf("read role on the pool: %v", err)
		}
		_, err = db3.ExecContext(ctx,
			"INSERT INTO graph_beads_spike (path, revision, last_authority_id, last_epoch) VALUES ('b/pool-leak', 'r1', 'a', 1)")
		t.Logf("embedded one-connection pool after an unclearing commit: @bd_graph_role valid=%v; raw INSERT err = %v", role.Valid, err)
		if role.Valid {
			t.Fatalf("@bd_graph_role = %d survived a pool release on the embedded driver; its ResetSession must have started reusing sessions", role.Int64)
		}
		if !isOutOfRoleRefusal(err) {
			t.Fatalf("raw INSERT on the next embedded checkout: err = %v, want the out-of-role refusal (fresh session, no leaked variable)", err)
		}
		if errors.Is(err, nil) {
			t.Fatal("unreachable")
		}
	})
}
