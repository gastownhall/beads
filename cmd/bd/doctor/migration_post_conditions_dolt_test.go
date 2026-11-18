//go:build cgo

package doctor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/testutil"
)

// The two tests in this file need a real Dolt engine: a Dolt server for
// TestCheckMigrationPostConditions_RealDolt, the embedded engine for
// TestCheckMigrationPostConditions_Embedded. No CI lane runs them today.
// //cmd/bd/doctor:doctor_test has no dolt-server or embedded variant, and the
// ci, prcore and integration configs set BEADS_TEST_SKIP=dolt, so both skip
// there, like every other Dolt-backed test in this package. Until the target
// gains such a lane, run them by hand:
//
//	bazel test //cmd/bd/doctor:doctor_test --config=ci \
//	  --test_filter='^TestCheckMigrationPostConditions_(RealDolt|Embedded)$' \
//	  --test_env=BEADS_TEST_SKIP= --test_env=BEADS_TEST_DOLT_SERVER=local \
//	  --test_env=BEADS_TEST_REQUIRE_DOLT_CONTAINER=1 --test_env=BEADS_TEST_EMBEDDED_DOLT=1
//
// The gateway Fix and the could-not-check path are unit-tested in
// migration_post_conditions_test.go, which the ci lane does run.

// postConditionDriftColumns are one tracked and one ignored column whose
// default a recorded migration drops (0050 and ignored 0010).
var postConditionDriftColumns = []string{"dependencies.id", "wisp_events.id"}

func driftPostConditions(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	for _, q := range []string{
		"ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())",
		"CALL DOLT_COMMIT('-Am', 'test: drift dependencies.id default')",
		"ALTER TABLE wisp_events ALTER COLUMN id SET DEFAULT (UUID())",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// postConditionState is everything the check must leave alone: HEAD, the
// working set, both cursor tables and the drifted defaults.
func postConditionState(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	var parts []string
	for _, q := range []string{
		"SELECT CONCAT('head ', commit_hash) FROM dolt_log ORDER BY commit_order DESC LIMIT 1",
		"SELECT CONCAT('status ', table_name, ' ', staged, ' ', status) FROM dolt_status ORDER BY table_name, staged",
		"SELECT CONCAT('main ', version) FROM schema_migrations ORDER BY version",
		"SELECT CONCAT('ignored ', version) FROM ignored_schema_migrations ORDER BY version",
		`SELECT CONCAT(TABLE_NAME, '.', COLUMN_NAME, ' ', COALESCE(COLUMN_DEFAULT, '<none>'))
			FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE()
			AND COLUMN_NAME = 'id' AND TABLE_NAME IN ('dependencies', 'wisp_events') ORDER BY TABLE_NAME`,
	} {
		rows, err := conn.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatalf("scan %s: %v", q, err)
			}
			parts = append(parts, s)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return strings.Join(parts, "\n")
}

func assertPostConditionsHold(t *testing.T, got DoctorCheck) {
	t.Helper()
	if got.Status != StatusOK || !strings.Contains(got.Message, "All 8 recorded migration post-condition(s) hold") {
		t.Fatalf("healthy: check = %q (%s; %s), want OK with all 8 held", got.Status, got.Message, got.Detail)
	}
}

func assertPostConditionsViolated(t *testing.T, got DoctorCheck) {
	t.Helper()
	want := fmt.Sprintf("%d recorded migration post-condition(s) unmet: %s",
		len(postConditionDriftColumns), strings.Join(postConditionDriftColumns, ", "))
	if got.Status != StatusWarning || got.Message != want || !strings.Contains(got.Fix, "bd migrate schema") {
		t.Fatalf("drifted: check = %q (%s; fix %q), want warning %q with the bd migrate schema hint",
			got.Status, got.Message, got.Fix, want)
	}
	for _, c := range postConditionDriftColumns {
		if !strings.Contains(got.Detail, c+" has DEFAULT") {
			t.Fatalf("drifted: detail does not explain %s:\n%s", c, got.Detail)
		}
	}
}

// GH#5269, AC-7 (embedded mode): healthy reads OK, drift lists every violated
// recorded post-condition with the repair hint and changes nothing, and an
// unopenable database reports could-not-check.
func TestCheckMigrationPostConditions_Embedded(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "beads", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.Close()
	ss := &SharedStore{beadsDir: beadsDir}

	withDB := func(fn func(db *sql.DB)) {
		t.Helper()
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "beads", "")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		fn(db)
	}

	assertPostConditionsHold(t, CheckMigrationPostConditions(ss))

	var before string
	withDB(func(db *sql.DB) {
		driftPostConditions(t, ctx, db)
		before = postConditionState(t, ctx, db)
	})
	assertPostConditionsViolated(t, CheckMigrationPostConditions(ss))
	withDB(func(db *sql.DB) {
		if after := postConditionState(t, ctx, db); after != before {
			t.Fatalf("doctor changed the database:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})

	broken := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(broken, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "embeddeddolt"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := CheckMigrationPostConditions(&SharedStore{beadsDir: broken}); got.Status != StatusWarning ||
		!strings.Contains(got.Message, "Could not check") {
		t.Fatalf("unopenable: check = %q (%s), want a could-not-check warning", got.Status, got.Message)
	}
}

// GH#5269, AC-7 (server mode): the same contract through the server store.
func TestCheckMigrationPostConditions_RealDolt(t *testing.T) {
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}
	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available, skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	dbName := "postcond_" + hex.EncodeToString(buf)
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            t.TempDir(),
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        dbName,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		CreateIfMissing: true,
		MaxOpenConns:    1,
	})
	if err != nil {
		t.Fatalf("dolt.New: %v", err)
	}
	db := store.DB()
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		_, _ = db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()
	ss := &SharedStore{store: store}

	assertPostConditionsHold(t, CheckMigrationPostConditions(ss))

	driftPostConditions(t, ctx, db)
	before := postConditionState(t, ctx, db)
	assertPostConditionsViolated(t, CheckMigrationPostConditions(ss))
	if after := postConditionState(t, ctx, db); after != before {
		t.Fatalf("doctor changed the database:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
