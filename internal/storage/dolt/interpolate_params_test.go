package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	gomysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// TestInterpolateParams_RoundTripParity writes and reads back the same edge-case
// values via two sql.DB handles — one with InterpolateParams=true, one with
// =false — against the same Dolt database, asserting byte-for-byte parity on
// the read path.
//
// This is the reviewer's BLOCKER 3 closer for the Commit 2 DSN flag flip
// (see docs/design/remote-latency-perf-design.md §5).
//
// Cases exercise the three places where client-side interpolation is most
// likely to differ from server-side PREPARE:
//   - JSON column with escaped Unicode and nested objects
//   - DATETIME column (schema is second-precision; sub-second
//     fractional values are truncated identically on both paths)
//   - String column with SQL metacharacters (quotes, backslashes, newlines)
//
// Intentionally no BLOB case — bd has no BLOB in parameterized write paths
// beyond federation_peers.password_encrypted, which is already covered by
// existing federation tests.
func TestInterpolateParams_RoundTripParity(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	if testServerPort == 0 {
		t.Skip("no Dolt test server available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create a fresh database for this test so we don't clobber the shared
	// schema. We apply the full migration set so the real `issues` table
	// (with JSON metadata and DATETIME created_at) is present.
	dbName := uniqueTestDBName(t)
	adminDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root"}.String()
	admin, err := sql.Open("mysql", adminDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+dbName+"`")
	})

	// Apply migrations via a throwaway handle using the default (post-Commit 2)
	// DSN. That DSN already has InterpolateParams=true, but that's irrelevant
	// for DDL — migrations are idempotent and schema-only.
	schemaDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName}.String()
	schemaDB, err := sql.Open("mysql", schemaDSN)
	if err != nil {
		t.Fatalf("open schema connection: %v", err)
	}
	if err := initSchemaOnDB(ctx, schemaDB); err != nil {
		schemaDB.Close()
		t.Fatalf("initSchemaOnDB: %v", err)
	}
	schemaDB.Close()

	// Build two handles: one explicitly InterpolateParams=false (pre-Commit 2
	// behavior: binary PREPARE/EXECUTE protocol), and one explicitly =true
	// (post-Commit 2 behavior: client-side interpolation). Keep every other
	// flag identical to the production DSN so we isolate the variable.
	buildCfg := func(interpolate bool) *gomysql.Config {
		return &gomysql.Config{
			User:                 "root",
			Net:                  "tcp",
			Addr:                 fmt.Sprintf("127.0.0.1:%d", testServerPort),
			DBName:               dbName,
			ParseTime:            true,
			MultiStatements:      true,
			InterpolateParams:    interpolate,
			Timeout:              5 * time.Second,
			AllowNativePasswords: true,
			TLSConfig:            "false",
		}
	}

	dbOff, err := sql.Open("mysql", buildCfg(false).FormatDSN())
	if err != nil {
		t.Fatalf("open InterpolateParams=false handle: %v", err)
	}
	defer dbOff.Close()
	dbOff.SetMaxOpenConns(1)

	dbOn, err := sql.Open("mysql", buildCfg(true).FormatDSN())
	if err != nil {
		t.Fatalf("open InterpolateParams=true handle: %v", err)
	}
	defer dbOn.Close()
	dbOn.SetMaxOpenConns(1)

	cases := []struct {
		name      string
		issueID   string
		metadata  string    // JSON column
		createdAt time.Time // DATETIME column (schema is second-precision)
		title     string    // VARCHAR with SQL metacharacters
	}{
		{
			name:      "json_null_nested",
			issueID:   "t-json",
			metadata:  `{"a":null,"emoji":"\u00e9","nested":{"k":[1,2,3]}}`,
			createdAt: time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC),
			title:     "plain",
		},
		{
			name:      "datetime_fractional",
			issueID:   "t-dt",
			metadata:  `{}`,
			createdAt: time.Date(2026, 4, 18, 12, 34, 56, 123456789, time.UTC),
			title:     "plain",
		},
		{
			name:      "string_metachars",
			issueID:   "t-str",
			metadata:  `{}`,
			createdAt: time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC),
			title:     `O'Brien` + "\n" + `"quote" \\`,
		},
	}

	type roundTripResult struct {
		id        string
		title     string
		metadata  string
		createdAt time.Time
	}

	roundTrip := func(t *testing.T, db *sql.DB, idSuffix string, tc struct {
		name      string
		issueID   string
		metadata  string
		createdAt time.Time
		title     string
	}) roundTripResult {
		t.Helper()
		id := tc.issueID + "-" + idSuffix
		_, err := db.ExecContext(ctx,
			`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, metadata, created_at)
			 VALUES (?, ?, '', '', '', '', ?, ?)`,
			id, tc.title, tc.metadata, tc.createdAt,
		)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}

		var got roundTripResult
		err = db.QueryRowContext(ctx,
			`SELECT id, title, metadata, created_at FROM issues WHERE id = ?`,
			id,
		).Scan(&got.id, &got.title, &got.metadata, &got.createdAt)
		if err != nil {
			t.Fatalf("select %s: %v", id, err)
		}
		return got
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			offResult := roundTrip(t, dbOff, "off", tc)
			onResult := roundTrip(t, dbOn, "on", tc)

			// Strip the per-handle ID suffix before comparing — everything else
			// (title, metadata JSON canonical form, created_at after DATETIME
			// truncation) must match byte-for-byte across the two paths.
			offResult.id = ""
			onResult.id = ""

			if !reflect.DeepEqual(offResult, onResult) {
				t.Fatalf("InterpolateParams changed round-trip result:\n  off: %+v\n  on:  %+v", offResult, onResult)
			}
		})
	}
}
