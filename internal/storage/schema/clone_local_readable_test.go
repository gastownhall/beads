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

// TestCheckCloneLocalReadableAtLatestIssuesNoProbe pins the check's cost on
// the healthy path every read-only open pays: the effective cursor is at this
// binary's latest, so no lease-column probe runs at all. The sentinel probes
// readCursor always issues for a non-zero cursor are stubbed present; a
// lease probe that fired here would surface as an unmet expectation.
func TestCheckCloneLocalReadableAtLatestIssuesNoProbe(t *testing.T) {
	db, mock := newMockDB(t)
	allSentinelsHealthy(t)
	expectIgnoredCursor(mock, true, LatestIgnoredVersion())

	if err := CheckCloneLocalReadable(context.Background(), db, "leases", []string{"lease_expires_at", "heartbeat_at", "granted_node"}); err != nil {
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

	if err := CheckCloneLocalReadable(context.Background(), db, "leases", []string{"lease_expires_at", "heartbeat_at", "granted_node"}); err != nil {
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

	err := CheckCloneLocalReadable(context.Background(), db, "leases", []string{"lease_expires_at", "heartbeat_at", "granted_node"})
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
	if len(e.MissingColumns) != 1 || e.MissingColumns[0] != "granted_node" {
		t.Errorf("MissingColumns = %v, want [granted_node]", e.MissingColumns)
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

	err := CheckCloneLocalReadable(context.Background(), db, "leases", []string{"lease_expires_at", "heartbeat_at", "granted_node"})
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

	err := CheckCloneLocalReadable(context.Background(), db, "leases", []string{"lease_expires_at", "heartbeat_at", "granted_node"})
	var e *CloneLocalBehindError
	if !errors.As(err, &e) {
		t.Fatalf("CheckCloneLocalReadable (no leases table) = %T (%v), want *CloneLocalBehindError", err, err)
	}
	if len(e.MissingColumns) != 3 {
		t.Errorf("MissingColumns = %v, want all three projected columns", e.MissingColumns)
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
	err := CheckCloneLocalReadable(context.Background(), db, "leases", []string{"lease_expires_at", "heartbeat_at", "granted_node"})
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

	err := CheckCloneLocalReadable(context.Background(), db, "leases", []string{"lease_expires_at", "heartbeat_at", "granted_node"})
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

	if err := CheckCloneLocalReadable(context.Background(), db, "leases", []string{"lease_expires_at", "heartbeat_at", "granted_node"}); err != nil {
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
		MissingColumns:       []string{"granted_node"},
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
