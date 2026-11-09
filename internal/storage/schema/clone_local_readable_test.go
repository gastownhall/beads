package schema

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// expectIgnoredCursor primes the ignored-lane cursor read: the existence
// probe, then MAX(version).
func expectIgnoredCursor(mock sqlmock.Sqlmock, exists bool, version any) {
	expectCursorProbe(mock, "ignored_schema_migrations", exists)
	if exists {
		expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", version)
	}
}

// allSentinelsHealthy stubs both sentinel seams present so a case can
// disbelieve or believe the cursor through the column sentinel alone.
func allSentinelsHealthy(t *testing.T) map[string]bool {
	t.Helper()
	present := allSentinelTables()
	stubSentinelTables(t, present, nil)
	stubSentinelColumns(t, map[string]bool{"leases.granted_node": true}, nil)
	return present
}

// expectLeaseColumns primes missingColumns' probe of the leases table: the
// table exists and SHOW COLUMNS lists granted only.
func expectLeaseColumns(mock sqlmock.Sqlmock, grantedNode bool) {
	expectCursorProbe(mock, "leases", true)
	rows := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
		AddRow("issue_id", "varchar(255)", "NO", "PRI", nil, "").
		AddRow("lease_expires_at", "bigint", "YES", "", nil, "").
		AddRow("heartbeat_at", "bigint", "YES", "", nil, "")
	if grantedNode {
		rows.AddRow("granted_node", "varchar(255)", "YES", "", nil, "")
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM leases")).WillReturnRows(rows)
}

// expectWispsColumns primes the probe of the wisps table: the table exists
// and SHOW COLUMNS lists the projected columns, with arrival_seq
// (ignored/0029) only when asked.
func expectWispsColumns(mock sqlmock.Sqlmock, arrivalSeq bool) {
	expectCursorProbe(mock, "wisps", true)
	rows := sqlmock.NewRows([]string{"Field", "Type", "Null", "Key", "Default", "Extra"}).
		AddRow("id", "varchar(255)", "NO", "PRI", nil, "").
		AddRow("row_lock", "bigint", "NO", "", "0", "").
		AddRow("storage_class", "varchar(16)", "YES", "", nil, "")
	if arrivalSeq {
		rows.AddRow("arrival_seq", "bigint", "YES", "UNI", nil, "")
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW COLUMNS FROM wisps")).WillReturnRows(rows)
}

// leaseOnly is the lease overlay alone, the projection most cases below
// probe. leaseAndWisps adds the optional wisps plane, as the server-mode
// open's projection list does.
var (
	leaseProjection = CloneLocalProjection{Table: "leases", Columns: []string{"lease_expires_at", "heartbeat_at", "granted_node"}}
	wispsProjection = CloneLocalProjection{Table: "wisps", Columns: []string{"id", "row_lock", "storage_class", "arrival_seq"}, Optional: true}
	leaseOnly       = []CloneLocalProjection{leaseProjection}
	leaseAndWisps   = []CloneLocalProjection{leaseProjection, wispsProjection}
)

// TestCheckCloneLocalReadableAtLatestIssuesNoProbe pins the check's cost on
// the healthy path every read-only open pays: the effective cursor is at this
// binary's latest, so no column probe runs at all, for either table. The
// sentinel probes readCursor always issues for a non-zero cursor are stubbed
// present; a table probe that fired here would surface as an unexpected query.
func TestCheckCloneLocalReadableAtLatestIssuesNoProbe(t *testing.T) {
	db, mock := newMockDB(t)
	allSentinelsHealthy(t)
	expectIgnoredCursor(mock, true, LatestIgnoredVersion())

	if err := CheckCloneLocalReadable(context.Background(), db, leaseAndWisps); err != nil {
		t.Fatalf("CheckCloneLocalReadable (at latest) = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestCheckCloneLocalReadableBehindWithColumnsStaysReadable is AC-5: a
// behind clone-local cursor whose lease columns are all present must keep
// opening — reads keep working through the window, exactly as
// CheckReadableDrift tolerates a behind main series with a complete issues
// projection. The main-series version read must not fire either: there is no
// refusal to render it for.
func TestCheckCloneLocalReadableBehindWithColumnsStaysReadable(t *testing.T) {
	db, mock := newMockDB(t)
	allSentinelsHealthy(t)
	expectIgnoredCursor(mock, true, LatestIgnoredVersion()-1)
	expectLeaseColumns(mock, true)

	if err := CheckCloneLocalReadable(context.Background(), db, leaseOnly); err != nil {
		t.Fatalf("CheckCloneLocalReadable (behind, columns present) = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestCheckCloneLocalReadableRefusesMissingColumn is the check's core
// refusal: a behind cursor whose leases table lacks a projected column
// returns *CloneLocalBehindError — never a raw read that would fail later on
// the unknown column — with the effective cursor, the missing column, the
// main-series posture, and the per-database remedy.
func TestCheckCloneLocalReadableRefusesMissingColumn(t *testing.T) {
	rawCursor := LatestIgnoredVersion() - 3
	// The raw cursor is contradicted by the missing sentinel column, so the
	// effective cursor is the sentinel's replayFloor, not the raw read.
	floor := ignoredSource.sentinelColumns[0].replayFloor
	if rawCursor <= floor {
		t.Fatalf("fixture: raw cursor %d must exceed the replay floor %d to prove the clamp", rawCursor, floor)
	}

	db, mock := newMockDB(t)
	stubSentinelTables(t, allSentinelTables(), nil)
	stubSentinelColumns(t, map[string]bool{"leases.granted_node": false}, nil)
	expectIgnoredCursor(mock, true, rawCursor)
	expectLeaseColumns(mock, false)
	// The main-series posture read for the refusal's message: current.
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())

	err := CheckCloneLocalReadable(context.Background(), db, leaseOnly)
	var e *CloneLocalBehindError
	if !errors.As(err, &e) {
		t.Fatalf("CheckCloneLocalReadable = %T (%v), want *CloneLocalBehindError", err, err)
	}
	if e.DBIgnoredVersion != floor {
		t.Errorf("DBIgnoredVersion = %d, want the sentinel replay floor %d, not the raw cursor %d", e.DBIgnoredVersion, floor, rawCursor)
	}
	if e.LatestIgnoredVersion != LatestIgnoredVersion() || e.MainVersion != LatestVersion() || e.LatestMainVersion != LatestVersion() {
		t.Errorf("CloneLocalBehindError versions = local %d/%d main %d/%d, want %d/%d and current main", e.DBIgnoredVersion, e.LatestIgnoredVersion, e.MainVersion, e.LatestMainVersion, LatestIgnoredVersion(), LatestVersion())
	}
	if len(e.MissingColumns) != 1 || e.MissingColumns[0] != "leases.granted_node" {
		t.Errorf("MissingColumns = %v, want [leases.granted_node]", e.MissingColumns)
	}
	if msg := err.Error(); !strings.Contains(msg, "granted_node") || !strings.Contains(msg, "bd migrate schema") {
		t.Errorf("refusal %q does not name the missing column and its remedy", msg)
	}
	if msg := err.Error(); !strings.Contains(msg, "never arrive with `bd dolt pull`") {
		t.Errorf("refusal %q does not explain why a pull cannot fix it", msg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestCheckCloneLocalReadableNamesMainBehindPosture pins that a refusal on a
// database whose MAIN series is also behind says so, rather than claiming the
// shared schema is current: that database still owes the designated-migrator
// procedure before the one local command applies.
func TestCheckCloneLocalReadableNamesMainBehindPosture(t *testing.T) {
	db, mock := newMockDB(t)
	stubSentinelTables(t, allSentinelTables(), nil)
	stubSentinelColumns(t, map[string]bool{"leases.granted_node": false}, nil)
	expectIgnoredCursor(mock, true, ignoredSource.sentinelColumns[0].replayFloor+2)
	expectLeaseColumns(mock, false)
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion()-2)

	err := CheckCloneLocalReadable(context.Background(), db, leaseOnly)
	var e *CloneLocalBehindError
	if !errors.As(err, &e) {
		t.Fatalf("CheckCloneLocalReadable = %T (%v), want *CloneLocalBehindError", err, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "designated-migrator/pull procedure") {
		t.Errorf("refusal on a main-behind database %q does not send the operator to the migrator procedure first", msg)
	}
	if strings.Contains(msg, "no further shared-schema migration consent") {
		t.Errorf("refusal on a main-behind database %q claims the shared schema is current", msg)
	}
}

// TestCheckCloneLocalReadableMissingLeaseTable covers the whole-table shape:
// a database materialized without the leases table at all reads as every
// column missing, and the refusal still names the per-database remedy.
func TestCheckCloneLocalReadableMissingLeaseTable(t *testing.T) {
	db, mock := newMockDB(t)
	allSentinelsHealthy(t)
	expectIgnoredCursor(mock, true, LatestIgnoredVersion()-1)
	expectCursorProbe(mock, "leases", false)
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())

	err := CheckCloneLocalReadable(context.Background(), db, leaseOnly)
	var e *CloneLocalBehindError
	if !errors.As(err, &e) {
		t.Fatalf("CheckCloneLocalReadable (no leases table) = %T (%v), want *CloneLocalBehindError", err, err)
	}
	if len(e.MissingColumns) != 3 {
		t.Errorf("MissingColumns = %v, want all three projected columns", e.MissingColumns)
	}
}

// TestCheckCloneLocalReadableRefusesMissingWispsColumn is the shape a v1.3.x
// consumer clone reaches after pull-then-upgrade: every v1.3.x release tops
// the clone-local series at 26, so the leases table is complete, but the
// wisps table lacks arrival_seq (ignored/0029), which the issue projection
// selects there (GetIssue's wisps fallback, ready work, search). The check
// must refuse it and name the column with its table, rather than let those
// reads fail on the raw unknown-column error.
func TestCheckCloneLocalReadableRefusesMissingWispsColumn(t *testing.T) {
	const v13xCursor = 26
	if v13xCursor >= LatestIgnoredVersion() {
		t.Fatalf("fixture: clone-local cursor %d must be behind the latest v%d", v13xCursor, LatestIgnoredVersion())
	}
	db, mock := newMockDB(t)
	allSentinelsHealthy(t)
	expectIgnoredCursor(mock, true, v13xCursor)
	expectLeaseColumns(mock, true)
	expectWispsColumns(mock, false)
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())

	err := CheckCloneLocalReadable(context.Background(), db, leaseAndWisps)
	var e *CloneLocalBehindError
	if !errors.As(err, &e) {
		t.Fatalf("CheckCloneLocalReadable (wisps lacks arrival_seq) = %T (%v), want *CloneLocalBehindError", err, err)
	}
	if e.DBIgnoredVersion != v13xCursor {
		t.Errorf("DBIgnoredVersion = %d, want %d", e.DBIgnoredVersion, v13xCursor)
	}
	if len(e.MissingColumns) != 1 || e.MissingColumns[0] != "wisps.arrival_seq" {
		t.Errorf("MissingColumns = %v, want [wisps.arrival_seq]", e.MissingColumns)
	}
	if msg := err.Error(); !strings.Contains(msg, "wisps.arrival_seq") || !strings.Contains(msg, "bd migrate schema") {
		t.Errorf("refusal %q does not name the missing wisps column and its remedy", msg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestCheckCloneLocalReadableNamesEveryTablesGaps pins that the refusal lists
// the missing columns of every projected table, each qualified by its table,
// in projection order, rather than stopping at the first table with a gap.
func TestCheckCloneLocalReadableNamesEveryTablesGaps(t *testing.T) {
	db, mock := newMockDB(t)
	stubSentinelTables(t, allSentinelTables(), nil)
	stubSentinelColumns(t, map[string]bool{"leases.granted_node": false}, nil)
	expectIgnoredCursor(mock, true, 13)
	expectLeaseColumns(mock, false)
	expectWispsColumns(mock, false)
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())

	err := CheckCloneLocalReadable(context.Background(), db, leaseAndWisps)
	var e *CloneLocalBehindError
	if !errors.As(err, &e) {
		t.Fatalf("CheckCloneLocalReadable (both tables behind) = %T (%v), want *CloneLocalBehindError", err, err)
	}
	if got, want := strings.Join(e.MissingColumns, ", "), "leases.granted_node, wisps.arrival_seq"; got != want {
		t.Errorf("MissingColumns = %q, want %q", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestCheckCloneLocalReadableAbsentOptionalTableRefusesNothing covers the
// optional table: a database without a wisps table has no wisps to read
// (sqlbuild.OptionalWispTable), so a behind cursor with complete leases stays
// readable, and no SHOW COLUMNS goes to the absent table (a failing one pins
// the pooled session's catalog, be-bv7x).
func TestCheckCloneLocalReadableAbsentOptionalTableRefusesNothing(t *testing.T) {
	db, mock := newMockDB(t)
	allSentinelsHealthy(t)
	expectIgnoredCursor(mock, true, LatestIgnoredVersion()-1)
	expectLeaseColumns(mock, true)
	expectCursorProbe(mock, "wisps", false)

	if err := CheckCloneLocalReadable(context.Background(), db, leaseAndWisps); err != nil {
		t.Fatalf("CheckCloneLocalReadable (no wisps table, leases complete) = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestCheckCloneLocalReadableAbsentCursor covers the cursor-less shape: no
// ignored_schema_migrations at all reads as version 0, which is behind, and a
// missing lease column still refuses.
func TestCheckCloneLocalReadableAbsentCursor(t *testing.T) {
	db, mock := newMockDB(t)
	expectIgnoredCursor(mock, false, 0)
	expectLeaseColumns(mock, false)
	expectCursorProbe(mock, "schema_migrations", true)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())

	var e *CloneLocalBehindError
	err := CheckCloneLocalReadable(context.Background(), db, leaseOnly)
	if !errors.As(err, &e) {
		t.Fatalf("CheckCloneLocalReadable (cursor absent) = %T (%v), want *CloneLocalBehindError", err, err)
	}
	if e.DBIgnoredVersion != 0 {
		t.Errorf("DBIgnoredVersion = %d, want 0 for an absent cursor", e.DBIgnoredVersion)
	}
}

// TestCheckCloneLocalReadableWrapsProbeErrors pins the propagation contract:
// a genuine probe failure surfaces as a wrapped error, never as a missing-
// schema refusal that would misdirect the operator.
func TestCheckCloneLocalReadableWrapsProbeErrors(t *testing.T) {
	db, mock := newMockDB(t)
	stubSentinelTables(t, allSentinelTables(), nil)
	probeErr := errors.New("dial timeout")
	stubSentinelColumns(t, nil, probeErr)
	expectIgnoredCursor(mock, true, LatestIgnoredVersion()-1)

	err := CheckCloneLocalReadable(context.Background(), db, leaseOnly)
	if err == nil {
		t.Fatalf("CheckCloneLocalReadable (probe error) = nil, want the wrapped probe failure")
	}
	var e *CloneLocalBehindError
	if errors.As(err, &e) {
		t.Fatalf("probe failure returned as *CloneLocalBehindError (%v), want a context error", err)
	}
	if !strings.Contains(err.Error(), "clone-local readable-schema check") {
		t.Errorf("probe failure %q lacks the check's context wrapper", err)
	}
}

// TestCheckCloneLocalReadableEscapeHatchDowngradesToWarning mirrors
// behindDriftError: BD_IGNORE_SCHEMA_SKEW=1 downgrades the refusal to a
// warning and lets the open proceed without widening what the hatch bypasses
// — the open still does not migrate.
func TestCheckCloneLocalReadableEscapeHatchDowngradesToWarning(t *testing.T) {
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
	db, mock := newMockDB(t)
	stubSentinelTables(t, allSentinelTables(), nil)
	stubSentinelColumns(t, map[string]bool{"leases.granted_node": false}, nil)
	expectIgnoredCursor(mock, true, LatestIgnoredVersion()-1)
	expectLeaseColumns(mock, false)

	if err := CheckCloneLocalReadable(context.Background(), db, leaseOnly); err != nil {
		t.Fatalf("CheckCloneLocalReadable (BD_IGNORE_SCHEMA_SKEW=1) = %v, want nil: the hatch reads anyway", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestCloneLocalBehindErrorGatewayWording pins the gateway posture: bd never
// migrates a gateway's schema, so the refusal must direct the operator to the
// server administrator and never prescribe `bd migrate schema`.
func TestCloneLocalBehindErrorGatewayWording(t *testing.T) {
	e := &CloneLocalBehindError{
		DBIgnoredVersion:     11,
		LatestIgnoredVersion: LatestIgnoredVersion(),
		MissingColumns:       []string{"leases.granted_node"},
		MainVersion:          LatestVersion(),
		LatestMainVersion:    LatestVersion(),
		Gateway:              true,
	}
	for name, msg := range map[string]string{"Error": e.Error(), "UserMessage": e.UserMessage(), "EscapeHint": e.EscapeHint()} {
		if strings.Contains(msg, "bd migrate schema") {
			t.Errorf("%s prescribes `bd migrate schema` for a gateway: %q", name, msg)
		}
		if !strings.Contains(msg, "server administrator") {
			t.Errorf("%s does not direct the operator to the server administrator: %q", name, msg)
		}
	}
	if IsCloneLocalBehindError(errors.Join(errors.New("outer"), e)) != true {
		t.Errorf("IsCloneLocalBehindError does not unwrap")
	}
}
