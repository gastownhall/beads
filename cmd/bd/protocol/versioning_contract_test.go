// versioning_contract_test.go — protocol v0 §V (versioning and migration), at
// the CLI level.
//
// V2 (designated migrator). An implementation MUST NOT auto-migrate a database
// that has a replication remote configured. Independent migration of two
// replicas is the canonical schema-fork generator (GH#4259): both clones apply
// the same pending migration in place, the histories diverge, and `bd dolt pull`
// can no longer merge them. The gate refuses the silent in-place migration and
// makes the operator choose migrate-as-the-designated-migrator
// (BD_ALLOW_REMOTE_MIGRATE=1) or adopt-the-remote.
//
// V3 (version skew). An older implementation opening a NEWER database MUST
// refuse by default with a clear version statement, not half-work — a stale
// binary querying a migrated schema fails with cryptic "column X could not be
// found" errors deep in unrelated commands. An explicit override
// (BD_IGNORE_SCHEMA_SKEW=1) MAY downgrade the refusal to a warning; accepting
// partial failure is the documented cost, never the default.
//
// Both clauses are properties of the DATABASE, so each test stages its
// precondition with a store fixture (writing schema_migrations behind bd's
// back — proposal §14 explicitly allows this) and then asserts the behavior
// through the frozen CLI surface.
package protocol

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestProtocol_V3_SchemaSkewRefusal pins §V3: a database ahead of the binary is
// refused, the refusal states both versions, and --json carries a structured
// skew block.
func TestProtocol_V3_SchemaSkewRefusal(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	// Stage a database one migration AHEAD of this binary: from bd's point of
	// view it is now the stale implementation.
	ahead := stageDatabaseAheadOfBinary(t, w)

	out, code := w.runExpectError("list")
	if code == 0 {
		t.Fatalf("bd list against a newer database exited 0 — §V3 requires a refusal\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "schema version mismatch") {
		t.Errorf("refusal does not state the skew (§V3):\n%s", out)
	}
	// "not half-work": the refusal must name both versions so the operator can
	// see which side is stale.
	if !strings.Contains(out, "v"+strconv.Itoa(ahead)) || !strings.Contains(out, "v"+strconv.Itoa(schema.LatestVersion())) {
		t.Errorf("refusal does not name both DB (v%d) and binary (v%d) versions (§V3):\n%s",
			ahead, schema.LatestVersion(), out)
	}

	jsonOut, _ := w.runExpectError("list", "--json")
	obj := requireJSONError(t, jsonOut, "schema-skew --json")
	skew, ok := obj["schema_skew"].(map[string]any)
	if !ok {
		t.Fatalf("--json refusal carries no schema_skew block (§V3/§E5):\n%s", jsonOut)
	}
	if got, want := skew["current_version"], float64(ahead); got != want {
		t.Errorf("schema_skew.current_version = %v, want %v", got, want)
	}
	if got, want := skew["required_version"], float64(schema.LatestVersion()); got != want {
		t.Errorf("schema_skew.required_version = %v, want %v", got, want)
	}
}

// TestProtocol_V3_SkewOverrideDowngradesToWarning pins the second half of §V3:
// the explicit override downgrades the refusal to a warning, and the command
// runs. The override is the escape hatch the refusal itself advertises; if it
// stopped working, the refusal would be a dead end.
func TestProtocol_V3_SkewOverrideDowngradesToWarning(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("Readable under skew")

	stageDatabaseAheadOfBinary(t, w)

	// Default: refused.
	if _, code := w.runExpectError("list"); code == 0 {
		t.Fatal("skewed database was not refused by default (§V3)")
	}

	// Override: proceeds, and warns that it is doing so.
	out := w.runEnv([]string{"BD_IGNORE_SCHEMA_SKEW=1"}, "list", "--json")
	if !strings.Contains(out, id) {
		t.Errorf("BD_IGNORE_SCHEMA_SKEW=1 did not let the read through (§V3):\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "warning") {
		t.Errorf("override proceeded silently — §V3 requires the downgraded refusal to still warn:\n%s", out)
	}
}

// TestProtocol_V2_RemoteMigrateGate pins §V2: a behind, remote-backed database
// is NOT auto-migrated. The gate must fire on the write path with a message
// that names the pending migrations and the two ways out.
func TestProtocol_V2_RemoteMigrateGate(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	stageBehindRemoteBackedDB(t, w)

	// BD_SMART_GATE=0 pins the blunt gate: the smart router (GH#4516) can
	// auto-resolve provably-safe cases, which is a bd refinement, not the v0
	// clause. The clause is "MUST NOT auto-migrate a remote-backed database" —
	// that is what this asserts.
	out, code := w.runEnvExpectError([]string{"BD_SMART_GATE=0", "BD_ALLOW_REMOTE_MIGRATE=0"},
		"create", "Should not get through the gate")
	if code == 0 {
		t.Fatalf("a behind, remote-backed database was auto-migrated — §V2 requires a refusal\n%s", out)
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "migrat") {
		t.Errorf("gate refusal does not mention migration (§V2):\n%s", out)
	}
	if !strings.Contains(lower, "remote") {
		t.Errorf("gate refusal does not name the remote as the reason (§V2):\n%s", out)
	}
}

// TestProtocol_V2_DesignatedMigratorOverride pins the other half of §V2: the
// operator who has decided to be the single designated migrator gets through
// with BD_ALLOW_REMOTE_MIGRATE=1, and the database is migrated (the write
// lands). A gate with no deliberate way through would just be a wall.
func TestProtocol_V2_DesignatedMigratorOverride(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	stageBehindRemoteBackedDB(t, w)

	out := w.runEnv([]string{"BD_SMART_GATE=0", "BD_ALLOW_REMOTE_MIGRATE=1"},
		"create", "Designated migrator writes")
	if strings.TrimSpace(out) == "" {
		t.Fatal("bd create produced no output under BD_ALLOW_REMOTE_MIGRATE=1")
	}

	// The migration was applied, so the gate no longer fires: a plain command
	// (no override) now succeeds against the same remote-backed database.
	w.runEnv([]string{"BD_SMART_GATE=0", "BD_ALLOW_REMOTE_MIGRATE=0"}, "list")
}

// stageDatabaseAheadOfBinary records a migration this binary does not know
// about, so the next bd command opens a database from the future. Returns that
// version.
func stageDatabaseAheadOfBinary(t *testing.T, w *workspace) int {
	t.Helper()
	ahead := schema.LatestVersion() + 1
	w.storeExec(t, fmt.Sprintf(
		"INSERT INTO schema_migrations (version, content_hash) VALUES (%d, '%s')",
		ahead, strings.Repeat("f", 64)))
	return ahead
}

// stageBehindRemoteBackedDB makes the workspace's database look like a clone
// that upgraded its binary but not its schema: one migration pending, and a
// Dolt remote configured. Both halves are required — the gate exists to stop
// exactly that combination, and neither alone is a fork risk.
func stageBehindRemoteBackedDB(t *testing.T, w *workspace) {
	t.Helper()
	w.storeExec(t, fmt.Sprintf("DELETE FROM schema_migrations WHERE version = %d", schema.LatestVersion()))
	// The remote is never contacted: the gate reads dolt_remotes to learn that
	// this database replicates somewhere, which is all the clause turns on.
	w.storeExec(t, fmt.Sprintf("CALL DOLT_REMOTE('add', 'origin', 'file://%s')", t.TempDir()))
}
