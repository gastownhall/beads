//go:build cgo

package doctor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/schema"
)

// seedGateDatabase creates a throwaway database on the shared test server and
// runs setup against it. Returns the database name; dropped on cleanup.
func seedGateDatabase(t *testing.T, port int, setup func(t *testing.T, db *sql.DB)) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	dbName := "gateassess_" + hex.EncodeToString(buf)

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Timeout: 10 * time.Second}.String()
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		_, _ = admin.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		_ = admin.Close()
	})
	// Single connection: USE and the statements that follow must share a session.
	admin.SetMaxOpenConns(1)
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE `%s`", dbName)); err != nil {
		t.Fatalf("create database: %v", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
		t.Fatalf("use database: %v", err)
	}
	setup(t, admin)
	return dbName
}

// seedMigrations returns a setup func that creates schema_migrations and
// records the given versions as applied.
func seedMigrations(versions ...int) func(t *testing.T, db *sql.DB) {
	return func(t *testing.T, db *sql.DB) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(),
			"CREATE TABLE schema_migrations (version INT NOT NULL PRIMARY KEY)"); err != nil {
			t.Fatalf("create schema_migrations: %v", err)
		}
		for _, v := range versions {
			if _, err := db.ExecContext(t.Context(), "INSERT INTO schema_migrations (version) VALUES (?)", v); err != nil {
				t.Fatalf("insert version %d: %v", v, err)
			}
		}
	}
}

// TestAssessSchemaFixGate_RealDolt exercises the function that decides ahead /
// pending / equal / undetermined against a real Dolt server with a seeded
// schema_migrations. Every other gate test builds a FixGate literal by hand, so
// without this the assessor itself — the headline guard of GH#4993 — is never
// run: `case dbVer > binary:` could be replaced by `case false:` and the suite
// stayed green.
func TestAssessSchemaFixGate_RealDolt(t *testing.T) {
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}
	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available, skipping")
	}
	binary := schema.LatestVersion()
	if binary < 2 {
		t.Fatalf("test needs at least 2 known migrations, binary knows %d", binary)
	}

	// The reachable-but-not-readable states share one expectation: fail closed.
	failClosed := func(t *testing.T, g FixGate) {
		t.Helper()
		if !g.DBReachable {
			t.Error("DBReachable = false, want true (the server answered)")
		}
		if g.Determined || g.AllowDBFix || g.RecommendFix {
			t.Errorf("Determined=%v AllowDBFix=%v RecommendFix=%v, want all false: unknown is not safe",
				g.Determined, g.AllowDBFix, g.RecommendFix)
		}
		if g.Ahead || g.Pending {
			t.Errorf("Ahead=%v Pending=%v, want neither when the version is unknown", g.Ahead, g.Pending)
		}
		if !strings.Contains(g.Reason, "could not be determined") {
			t.Errorf("Reason = %q, want it to say the version could not be determined", g.Reason)
		}
	}

	cases := []struct {
		name   string
		setup  func(t *testing.T, db *sql.DB)
		verify func(t *testing.T, g FixGate)
	}{
		{
			name:  "ahead",
			setup: seedMigrations(binary + 3),
			verify: func(t *testing.T, g FixGate) {
				if !g.Ahead || g.Pending {
					t.Errorf("Ahead=%v Pending=%v, want Ahead only", g.Ahead, g.Pending)
				}
				if g.AllowDBFix || g.RecommendFix {
					t.Errorf("AllowDBFix=%v RecommendFix=%v, want both false on a newer schema", g.AllowDBFix, g.RecommendFix)
				}
				if g.DBVersion != binary+3 || g.BinaryVersion != binary {
					t.Errorf("DBVersion=%d BinaryVersion=%d, want %d/%d", g.DBVersion, g.BinaryVersion, binary+3, binary)
				}
				if !strings.Contains(g.Reason, "3 migrations ahead") {
					t.Errorf("Reason = %q, want it to name 3 migrations ahead", g.Reason)
				}
			},
		},
		{
			name:  "behind (pending)",
			setup: seedMigrations(binary - 1),
			verify: func(t *testing.T, g FixGate) {
				if !g.Pending || g.Ahead {
					t.Errorf("Ahead=%v Pending=%v, want Pending only", g.Ahead, g.Pending)
				}
				if g.AllowDBFix || g.RecommendFix {
					t.Errorf("AllowDBFix=%v RecommendFix=%v, want both false while migrations are pending", g.AllowDBFix, g.RecommendFix)
				}
				if g.DBVersion != binary-1 {
					t.Errorf("DBVersion = %d, want %d", g.DBVersion, binary-1)
				}
				if !strings.Contains(g.Reason, "1 migration pending") {
					t.Errorf("Reason = %q, want it to name 1 migration pending", g.Reason)
				}
			},
		},
		{
			name:  "equal",
			setup: seedMigrations(binary-1, binary),
			verify: func(t *testing.T, g FixGate) {
				if g.Ahead || g.Pending {
					t.Errorf("Ahead=%v Pending=%v, want neither at parity", g.Ahead, g.Pending)
				}
				if !g.AllowDBFix || !g.RecommendFix || !g.Determined {
					t.Errorf("AllowDBFix=%v RecommendFix=%v Determined=%v, want all true at parity",
						g.AllowDBFix, g.RecommendFix, g.Determined)
				}
				if g.Reason != "" {
					t.Errorf("Reason = %q, want empty at parity", g.Reason)
				}
				if g.DBVersion != binary {
					t.Errorf("DBVersion = %d, want %d", g.DBVersion, binary)
				}
			},
		},
		{
			// Pins current behavior, not an endorsement: a version of 0 (empty
			// table) is treated as "could not be determined" and so fails
			// closed. Revisit together with the table-absent case below.
			name:   "empty schema_migrations",
			setup:  seedMigrations(),
			verify: failClosed,
		},
		{
			name:   "no schema_migrations table",
			setup:  func(*testing.T, *sql.DB) {},
			verify: failClosed,
		},
		{
			name: "schema_migrations unreadable",
			setup: func(t *testing.T, db *sql.DB) {
				if _, err := db.ExecContext(t.Context(), "CREATE TABLE schema_migrations (unrelated INT)"); err != nil {
					t.Fatalf("create malformed schema_migrations: %v", err)
				}
			},
			verify: failClosed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbName := seedGateDatabase(t, port, tc.setup)
			setGatePort(t, port)
			gate := AssessSchemaFixGate(writeGateWorkspace(t, dbName))

			// Invariants for every reachable assessment.
			if !gate.DBReachable {
				t.Fatalf("DBReachable = false; the seeded database should be reachable: %+v", gate)
			}
			if !gate.AllowFSFix {
				t.Error("AllowFSFix = false; filesystem repair must not depend on schema state")
			}
			if gate.BinaryVersion != binary {
				t.Errorf("BinaryVersion = %d, want %d", gate.BinaryVersion, binary)
			}
			tc.verify(t, gate)
		})
	}
}
