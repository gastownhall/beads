package fix

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// TestReadServerMetadataDatabasesQuotesIdentifiers pins the call sites, not
// just the escape helper. readServerMetadataDatabases is the catalog read
// behind both the authoritative reconcile and the `bd doctor --fix` fallback
// of the GH#5576 wrong-database repair. While it interpolated a SHOW DATABASES
// name raw, a backtick-bearing name broke out of the identifier and made the
// probe error, so a database was cataloged wrong — no fix, no error, no
// output — for exactly the names a repair needed to consider. Expectations
// are exact strings, so reverting to raw interpolation fails this test
// rather than silently regressing. Both query shapes — the issues probe and
// the _project_id probe — are pinned, and a legal hyphenated legacy name
// (GH#2142 named databases from raw prefixes) probes verbatim.
func TestReadServerMetadataDatabasesQuotesIdentifiers(t *testing.T) {
	tests := []struct {
		name      string
		dbName    string
		wantIssue string
		wantMeta  string
	}{
		{
			name:      "plain name is unchanged",
			dbName:    "beads_x",
			wantIssue: "SELECT COUNT(*) FROM `beads_x`.issues LIMIT 1",
			wantMeta:  "SELECT value FROM `beads_x`.metadata WHERE `key` = '_project_id' LIMIT 1",
		},
		{
			name:      "backtick cannot break out of the identifier",
			dbName:    "evil`; DROP TABLE x",
			wantIssue: "SELECT COUNT(*) FROM `evil``; DROP TABLE x`.issues LIMIT 1",
			wantMeta:  "SELECT value FROM `evil``; DROP TABLE x`.metadata WHERE `key` = '_project_id' LIMIT 1",
		},
		{
			name:      "legal hyphenated legacy name probes verbatim",
			dbName:    "my-project",
			wantIssue: "SELECT COUNT(*) FROM `my-project`.issues LIMIT 1",
			wantMeta:  "SELECT value FROM `my-project`.metadata WHERE `key` = '_project_id' LIMIT 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectQuery("SHOW DATABASES").
				WillReturnRows(sqlmock.NewRows([]string{"Database"}).AddRow(tt.dbName))
			mock.ExpectQuery(regexp.QuoteMeta(tt.wantIssue)).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			mock.ExpectQuery(regexp.QuoteMeta(tt.wantMeta)).
				WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("proj-1"))

			databases, err := readServerMetadataDatabases(context.Background(), db)
			if err != nil {
				t.Fatalf("readServerMetadataDatabases: %v", err)
			}
			if len(databases) != 1 {
				t.Fatalf("cataloged %d databases, want 1", len(databases))
			}
			if databases[0].Name != tt.dbName || !databases[0].HasSchema || databases[0].ProjectID != "proj-1" {
				t.Errorf("cataloged %+v, want name %q with schema and project_id proj-1", databases[0], tt.dbName)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

// newRecordingMock wraps sqlmock with a matcher that appends each actual query
// to a recorded list before delegating to the default regexp matcher. The
// recorded list pins the ordered set of queries that reached expectation
// matching; it does not capture queries sqlmock rejects before matching (an
// unexpected query surfaces as an error the probe's expected-error check
// classifies), so the assertions pair DeepEqual on the recording with
// ExpectationsWereMet, which together bound mid-sequence drift; a query issued
// after the last expectation is fulfilled is caught by neither. In ordered mode
// sqlmock's query() skips fulfilled expectations and returns "all expectations
// were already fulfilled" before it reaches the matcher, so a trailing surplus
// query is never recorded, and ExpectationsWereMet reports only unfulfilled
// expectations, never surplus calls. sqlmock v1.5.2 offers no clean trailing
// sentinel: an extra never-matching expectation would fail
// ExpectationsWereMet unconditionally.
func newRecordingMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *[]string) {
	t.Helper()
	var recorded []string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(
		func(expectedSQL, actualSQL string) error {
			recorded = append(recorded, actualSQL)
			return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
		},
	)))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock, &recorded
}

// readServerMetadataDatabases interpolates cataloged database names into
// backtick-quoted queries. Two properties must hold together:
//
//  1. Every candidate is probed with backtick-doubling (the complete escape
//     for a backtick-quoted identifier) on both query shapes, so a hostile
//     name on a shared server stays inert inside the quoting — the probe
//     merely errors with a real missing-table MySQL error and the candidate
//     is cataloged without schema or identity.
//  2. Legal names that are not unquoted-charset-safe still probe verbatim on
//     both query shapes. Legacy databases were named from raw prefixes before
//     GH#2142 (`my-project`-style, hyphens included) and this catalog read is
//     the doctor's mechanism for finding them.
func TestReadServerMetadataDatabasesEscapesHostileNames(t *testing.T) {
	db, mock, recorded := newRecordingMock(t)

	databases := sqlmock.NewRows([]string{"Database"}).
		AddRow("x`; DROP TABLE issues; --").
		AddRow("my-project")
	mock.ExpectQuery("SHOW DATABASES").WillReturnRows(databases)

	escapedHostile := "x`; DROP TABLE issues; --"
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `" + strings.ReplaceAll(escapedHostile, "`", "``") + "`.issues LIMIT 1")).
		WillReturnError(&mysql.MySQLError{Number: 1146, Message: "table not found: issues"})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM `" + strings.ReplaceAll(escapedHostile, "`", "``") + "`.metadata WHERE `key` = '_project_id' LIMIT 1")).
		WillReturnError(&mysql.MySQLError{Number: 1146, Message: "table not found: metadata"})

	legacyHyphen := "my-project"
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `" + legacyHyphen + "`.issues LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(3))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM `" + legacyHyphen + "`.metadata WHERE `key` = '_project_id' LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("legacy-proj"))

	got, err := readServerMetadataDatabases(context.Background(), db)
	if err != nil {
		t.Fatalf("readServerMetadataDatabases: %v", err)
	}
	want := []serverDatabaseMetadata{
		{Name: "x`; DROP TABLE issues; --", HasSchema: false, ProjectID: ""},
		{Name: "my-project", HasSchema: true, ProjectID: "legacy-proj"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog = %+v, want %+v", got, want)
	}

	wantQueries := []string{
		"SHOW DATABASES",
		"SELECT COUNT(*) FROM `x``; DROP TABLE issues; --`.issues LIMIT 1",
		"SELECT value FROM `x``; DROP TABLE issues; --`.metadata WHERE `key` = '_project_id' LIMIT 1",
		"SELECT COUNT(*) FROM `my-project`.issues LIMIT 1",
		"SELECT value FROM `my-project`.metadata WHERE `key` = '_project_id' LIMIT 1",
	}
	if !reflect.DeepEqual(*recorded, wantQueries) {
		t.Fatalf("executed queries = %q, want %q", *recorded, wantQueries)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected queries: %v", err)
	}
}

// Real MySQL missing-table errors are classified as expected probe errors;
// anything else — a dropped connection mid-probe, an unknown server error —
// propagates, on both query shapes, as does a SHOW DATABASES failure.
func TestReadServerMetadataDatabasesPropagatesProbeErrors(t *testing.T) {
	t.Run("show databases fails", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery("SHOW DATABASES").WillReturnError(errors.New("connection refused"))

		if _, err := readServerMetadataDatabases(context.Background(), db); err == nil ||
			!strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("readServerMetadataDatabases = %v, want the SHOW DATABASES error", err)
		}
	})

	t.Run("schema probe error propagates", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery("SHOW DATABASES").
			WillReturnRows(sqlmock.NewRows([]string{"Database"}).AddRow("some_db"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `some_db`.issues LIMIT 1")).
			WillReturnError(errors.New("connection reset"))

		_, err = readServerMetadataDatabases(context.Background(), db)
		if err == nil || !strings.Contains(err.Error(), "probing database \"some_db\" for schema") {
			t.Fatalf("readServerMetadataDatabases = %v, want the schema probe error to propagate", err)
		}
	})

	t.Run("project_id probe error propagates", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery("SHOW DATABASES").
			WillReturnRows(sqlmock.NewRows([]string{"Database"}).AddRow("some_db"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `some_db`.issues LIMIT 1")).
			WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(1))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM `some_db`.metadata WHERE `key` = '_project_id' LIMIT 1")).
			WillReturnError(errors.New("connection reset"))

		_, err = readServerMetadataDatabases(context.Background(), db)
		if err == nil || !strings.Contains(err.Error(), "probing database \"some_db\" for project_id") {
			t.Fatalf("readServerMetadataDatabases = %v, want the project_id probe error to propagate", err)
		}
	})
}

// GH#4931: a shared sql-server often hosts databases the beads user cannot
// read. Access-denied peers are cataloged without schema or identity, never
// an error, on both query shapes.
func TestReadServerMetadataDatabasesToleratesUnreadablePeers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SHOW DATABASES").
		WillReturnRows(sqlmock.NewRows([]string{"Database"}).AddRow("secret_db"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM `secret_db`.issues LIMIT 1")).
		WillReturnError(&mysql.MySQLError{Number: 1142, Message: "SELECT command denied to user"})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM `secret_db`.metadata WHERE `key` = '_project_id' LIMIT 1")).
		WillReturnError(&mysql.MySQLError{Number: 1142, Message: "SELECT command denied to user"})

	got, err := readServerMetadataDatabases(context.Background(), db)
	if err != nil {
		t.Fatalf("readServerMetadataDatabases: %v", err)
	}
	want := []serverDatabaseMetadata{{Name: "secret_db", HasSchema: false, ProjectID: ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog = %+v, want %+v", got, want)
	}
}

// selectSkip want: a policy skip is a *fix.SkipError naming the candidates
// sorted and the manual remedy.
func selectSkipWant(t *testing.T, err error, wantCandidates []string) {
	t.Helper()
	var skip *SkipError
	if !errors.As(err, &skip) {
		t.Fatalf("error = %v, want a *fix.SkipError", err)
	}
	msg := err.Error()
	for _, name := range wantCandidates {
		if !strings.Contains(msg, name) {
			t.Errorf("skip does not name candidate %q:\n%s", name, msg)
		}
	}
	if !strings.Contains(msg, "bd dolt set database <name>") {
		t.Errorf("skip does not give the manual remedy:\n%s", msg)
	}
}

// TestSelectFallbackDoltDatabase_SharedServerDeclinesWithoutAnchor pins the
// GH#5576 core: with no local project_id in shared-server mode, no eligible
// candidate is ever adopted — a sole schema-matching database can belong to a
// different workspace, exactly the adoption the authoritative reconcile
// refuses.
func TestSelectFallbackDoltDatabase_SharedServerDeclinesWithoutAnchor(t *testing.T) {
	for _, tt := range []struct {
		name      string
		databases []serverDatabaseMetadata
		want      []string
	}{
		{
			name: "sole schema candidate",
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: false},
				{Name: "foreign_db", HasSchema: true, ProjectID: "other-project"},
			},
			want: []string{"foreign_db"},
		},
		{
			name: "several schema candidates, listed sorted",
			databases: []serverDatabaseMetadata{
				{Name: "zeta_db", HasSchema: true, ProjectID: "zeta"},
				{Name: "alpha_db", HasSchema: true, ProjectID: "alpha"},
			},
			want: []string{"alpha_db, zeta_db"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := selectFallbackDoltDatabase("", configfile.DefaultDoltDatabase, true, tt.databases)
			if selected != nil {
				t.Fatalf("selected %+v in shared-server mode without a project_id", selected)
			}
			selectSkipWant(t, err, tt.want)
		})
	}
}

// A unique project_id match is a positive control: it selects in either
// shared or non-shared mode, with the known local ID preserved by the caller.
func TestSelectFallbackDoltDatabase_UniqueProjectIDMatchSelects(t *testing.T) {
	for _, shared := range []bool{false, true} {
		databases := []serverDatabaseMetadata{
			{Name: configfile.DefaultDoltDatabase, HasSchema: false},
			{Name: "wrong_db", HasSchema: true, ProjectID: ""},
			{Name: "canonical_db", HasSchema: true, ProjectID: "proj-123"},
		}
		selected, err := selectFallbackDoltDatabase("proj-123", configfile.DefaultDoltDatabase, shared, databases)
		if err != nil {
			t.Fatalf("shared=%v: %v", shared, err)
		}
		if selected == nil || selected.Name != "canonical_db" {
			t.Fatalf("shared=%v: selected %+v, want canonical_db", shared, selected)
		}
	}
}

// Duplicate project_id matches are a hard error, never a skip and never a
// choice: the reconcile's ambiguous-match refusal, with sorted names.
func TestSelectFallbackDoltDatabase_DuplicateProjectIDMatchesHardError(t *testing.T) {
	databases := []serverDatabaseMetadata{
		{Name: "beta_match", HasSchema: true, ProjectID: "proj-123"},
		{Name: "alpha_match", HasSchema: true, ProjectID: "proj-123"},
	}
	selected, err := selectFallbackDoltDatabase("proj-123", configfile.DefaultDoltDatabase, false, databases)
	if selected != nil {
		t.Fatalf("selected %+v on duplicate matches", selected)
	}
	if err == nil {
		t.Fatal("want a hard error on duplicate matches")
	}
	var skip *SkipError
	if errors.As(err, &skip) {
		t.Fatalf("duplicate match error = %v, want a hard error, not a skip", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "multiple server databases match project_id proj-123") {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(msg, "alpha_match, beta_match") {
		t.Errorf("error does not list the matches sorted:\n%s", msg)
	}
}

// A current database with schema asserting a conflicting project_id refuses
// the exact match, exactly as the authoritative reconcile does. This also
// covers a mode-gated reconcile that did not run.
func TestSelectFallbackDoltDatabase_ConflictingCurrentDatabaseHardError(t *testing.T) {
	databases := []serverDatabaseMetadata{
		{Name: "current_db", HasSchema: true, ProjectID: "server-authoritative-id"},
		{Name: "canonical_db", HasSchema: true, ProjectID: "proj-123"},
	}
	selected, err := selectFallbackDoltDatabase("proj-123", "current_db", false, databases)
	if selected != nil {
		t.Fatalf("selected %+v despite conflicting current identity", selected)
	}
	if err == nil || !strings.Contains(err.Error(), "conflicting project identity") {
		t.Fatalf("error = %v, want the conflict refusal", err)
	}
}

// With nothing eligible — an empty catalog, or only databases the fallback
// must not adopt — there is no applicable repair: nil/nil, no skip.
func TestSelectFallbackDoltDatabase_NoEligibleCandidatesNoRepair(t *testing.T) {
	for _, tt := range []struct {
		name      string
		databases []serverDatabaseMetadata
	}{
		{"empty catalog", nil},
		{
			name: "only ineligible databases",
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: false},
				{Name: "empty_schema_db", HasSchema: false, ProjectID: "proj-123"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := selectFallbackDoltDatabase("", configfile.DefaultDoltDatabase, false, tt.databases)
			if selected != nil || err != nil {
				t.Fatalf("selected %+v, err %v, want nil/nil", selected, err)
			}
		})
	}
}

// The eligibility filter: the global issue database (its sentinel project_id),
// test-fixture prefixes, schema-less databases and system databases are never
// adoptable; the literal "beads" name is not excluded — a workspace's real
// database can legitimately be named beads, and the effective current database
// is a separate selector input.
func TestSelectFallbackDoltDatabase_EligibilityFilters(t *testing.T) {
	for _, tt := range []struct {
		name      string
		localID   string
		databases []serverDatabaseMetadata
		wantName  string
		wantSkip  bool
	}{
		{
			name:    "global sentinel database is excluded from identity-free recovery",
			localID: "",
			databases: []serverDatabaseMetadata{
				{Name: "global_db", HasSchema: true, ProjectID: doltserver.GlobalProjectID},
			},
			wantSkip: false, // nothing eligible: nil/nil
		},
		{
			name:    "global sentinel is excluded before exact-match selection",
			localID: doltserver.GlobalProjectID,
			databases: []serverDatabaseMetadata{
				{Name: "global_db", HasSchema: true, ProjectID: doltserver.GlobalProjectID},
			},
			wantSkip: false, // nothing eligible: nil/nil — the sentinel is never a match
		},
		{
			name:    "test-fixture prefixes are excluded",
			localID: "proj-123",
			databases: []serverDatabaseMetadata{
				{Name: "testdb_fixture", HasSchema: true, ProjectID: "proj-123"},
				{Name: "doctest_fixture", HasSchema: true, ProjectID: "proj-123"},
				{Name: "doctortest_fixture", HasSchema: true, ProjectID: "proj-123"},
			},
			wantSkip: false, // nothing eligible: nil/nil
		},
		{
			name:    "schema-less and system databases are excluded",
			localID: "proj-123",
			databases: []serverDatabaseMetadata{
				{Name: "information_schema", HasSchema: true, ProjectID: "proj-123"},
				{Name: "mysql", HasSchema: true, ProjectID: "proj-123"},
				{Name: "empty_db", HasSchema: false, ProjectID: "proj-123"},
				{Name: "real_db", HasSchema: true, ProjectID: "proj-123"},
			},
			wantName: "real_db",
		},
		{
			name:    "the literal beads name stays adoptable",
			localID: "proj-123",
			databases: []serverDatabaseMetadata{
				{Name: "other_current", HasSchema: false},
				{Name: configfile.DefaultDoltDatabase, HasSchema: true, ProjectID: "proj-123"},
			},
			wantName: configfile.DefaultDoltDatabase,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := selectFallbackDoltDatabase(tt.localID, "other_current", true, tt.databases)
			switch {
			case tt.wantSkip:
				if selected != nil {
					t.Fatalf("selected %+v, want a skip", selected)
				}
				selectSkipWant(t, err, nil)
			case tt.wantName != "":
				if err != nil {
					t.Fatalf("error %v, want selection of %q", err, tt.wantName)
				}
				if selected == nil || selected.Name != tt.wantName {
					t.Fatalf("selected %+v, want %q", selected, tt.wantName)
				}
			default:
				if selected != nil || err != nil {
					t.Fatalf("selected %+v, err %v, want nil/nil", selected, err)
				}
			}
		})
	}
}

// A known local project_id with no eligible candidate carrying it declines,
// in either mode — including when a sole candidate has no identity at all:
// absence of identity is not a match (downstream identity verification
// tolerates an empty remote identity, so adopting on one is not safe).
func TestSelectFallbackDoltDatabase_KnownProjectIDWithoutMatchDeclines(t *testing.T) {
	for _, tt := range []struct {
		name      string
		shared    bool
		databases []serverDatabaseMetadata
	}{
		{
			name:   "sole candidate without identity, non-shared",
			shared: false,
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: false},
				{Name: "legacy_db", HasSchema: true, ProjectID: ""},
			},
		},
		{
			name:   "sole candidate without identity, shared",
			shared: true,
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: false},
				{Name: "legacy_db", HasSchema: true, ProjectID: ""},
			},
		},
		{
			name:   "sole candidate with a foreign identity",
			shared: false,
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: false},
				{Name: "foreign_db", HasSchema: true, ProjectID: "other-project"},
			},
		},
		{
			name:   "several candidates, none carrying the local id, listed sorted",
			shared: false,
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: false},
				{Name: "zeta_db", HasSchema: true, ProjectID: "zeta"},
				{Name: "alpha_db", HasSchema: true, ProjectID: "alpha"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := selectFallbackDoltDatabase("proj-123", configfile.DefaultDoltDatabase, tt.shared, tt.databases)
			if selected != nil {
				t.Fatalf("selected %+v, want a skip", selected)
			}
			selectSkipWant(t, err, []string{"proj-123"})
			if !strings.Contains(err.Error(), "no server database with Beads schema carries project_id") {
				t.Errorf("skip does not say why selection is unsafe:\n%s", err.Error())
			}
			for _, db := range tt.databases[1:] {
				if !strings.Contains(err.Error(), db.Name) {
					t.Errorf("skip does not name candidate %q:\n%s", db.Name, err.Error())
				}
			}
		})
	}
}

// With no local project_id on a non-shared server and several eligible
// candidates, selection never goes by enumeration order: the result is the
// same skip — same sorted candidate list — whatever order SHOW DATABASES
// returned.
func TestSelectFallbackDoltDatabase_MultipleCandidatesWithoutIdentityDeclinePermutationInvariant(t *testing.T) {
	orders := [][]serverDatabaseMetadata{
		{
			{Name: configfile.DefaultDoltDatabase, HasSchema: false},
			{Name: "zeta_db", HasSchema: true, ProjectID: "zeta"},
			{Name: "alpha_db", HasSchema: true, ProjectID: "alpha"},
		},
		{
			{Name: "alpha_db", HasSchema: true, ProjectID: "alpha"},
			{Name: configfile.DefaultDoltDatabase, HasSchema: false},
			{Name: "zeta_db", HasSchema: true, ProjectID: "zeta"},
		},
	}
	var first string
	for i, databases := range orders {
		selected, err := selectFallbackDoltDatabase("", configfile.DefaultDoltDatabase, false, databases)
		if selected != nil {
			t.Fatalf("order %d: selected %+v by enumeration order", i, selected)
		}
		selectSkipWant(t, err, []string{"alpha_db, zeta_db"})
		if !strings.Contains(err.Error(), "multiple server databases have Beads schema") {
			t.Errorf("order %d: skip does not say why selection is unsafe:\n%s", i, err.Error())
		}
		if i == 0 {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("order changed the skip:\nfirst: %s\nthen:  %s", first, err.Error())
		}
	}
}

// The retained GH#2160 recovery: on a non-shared server, with no local
// project_id, a sole eligible candidate is selected — with or without an
// identity of its own. The caller saves the name and backfills an absent
// local project_id only when the candidate supplies one.
func TestSelectFallbackDoltDatabase_AdoptsSoleCandidateOnOwnServer(t *testing.T) {
	for _, tt := range []struct {
		name      string
		databases []serverDatabaseMetadata
		wantID    string
	}{
		{
			name: "with identity, for the caller to backfill",
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: false},
				{Name: "my-project", HasSchema: true, ProjectID: "legacy-proj"},
			},
			wantID: "legacy-proj",
		},
		{
			name: "without identity",
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: false},
				{Name: "legacy_db", HasSchema: true, ProjectID: ""},
			},
			wantID: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := selectFallbackDoltDatabase("", configfile.DefaultDoltDatabase, false, tt.databases)
			if err != nil {
				t.Fatalf("selectFallbackDoltDatabase: %v", err)
			}
			if selected == nil || selected.Name != "my-project" && selected.Name != "legacy_db" {
				t.Fatalf("selected %+v, want the sole eligible candidate", selected)
			}
			if selected.ProjectID != tt.wantID {
				t.Fatalf("selected identity %q, want %q", selected.ProjectID, tt.wantID)
			}
		})
	}
}

// A current database that already has Beads schema is never moved away from:
// no rewrite, whatever the alternatives, whatever the mode — the selector is
// handed the effective current name (configfile GetDoltDatabase, which honors
// BEADS_DOLT_SERVER_DATABASE), so an environment-selected current database is
// protected the same way.
func TestSelectFallbackDoltDatabase_KeepsCurrentSchemaDatabase(t *testing.T) {
	for _, tt := range []struct {
		name        string
		localID     string
		current     string
		shared      bool
		databases   []serverDatabaseMetadata
		wantErrText string
	}{
		{
			name:    "other schema database present, no local id",
			current: configfile.DefaultDoltDatabase,
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: true, ProjectID: ""},
				{Name: "other_db", HasSchema: true, ProjectID: ""},
			},
		},
		{
			name:    "current database selected by the environment",
			current: "env_db",
			databases: []serverDatabaseMetadata{
				{Name: "env_db", HasSchema: true, ProjectID: ""},
				{Name: "other_db", HasSchema: true, ProjectID: ""},
			},
		},
		{
			name:    "shared-server decline never moves a schema-holding current database",
			current: configfile.DefaultDoltDatabase,
			shared:  true,
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: true, ProjectID: ""},
				{Name: "foreign_db", HasSchema: true, ProjectID: "other"},
			},
		},
		{
			name:    "no local id carried by any candidate",
			localID: "proj-123",
			current: configfile.DefaultDoltDatabase,
			databases: []serverDatabaseMetadata{
				{Name: configfile.DefaultDoltDatabase, HasSchema: true, ProjectID: ""},
				{Name: "other_db", HasSchema: true, ProjectID: "someone-else"},
			},
		},
		{
			// The no-op is not permission to overlook an identity
			// contradiction: the conflict refusal comes first.
			name:    "conflicting current identity still refuses",
			localID: "proj-123",
			current: "current_db",
			databases: []serverDatabaseMetadata{
				{Name: "current_db", HasSchema: true, ProjectID: "server-authoritative-id"},
				{Name: "canonical_db", HasSchema: true, ProjectID: "proj-123"},
			},
			wantErrText: "conflicting project identity",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selected, err := selectFallbackDoltDatabase(tt.localID, tt.current, tt.shared, tt.databases)
			if tt.wantErrText != "" {
				if selected != nil || err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
					t.Fatalf("selected %+v, err %v, want the %q refusal", selected, err, tt.wantErrText)
				}
				return
			}
			if selected != nil || err != nil {
				t.Fatalf("selected %+v, err %v, want nil/nil (current database already has schema)", selected, err)
			}
		})
	}
}

// Server-supplied names in a policy skip are spelled by remedy.PrintedValue:
// a name carrying control characters cannot redraw the diagnostic.
func TestSelectFallbackDoltDatabase_EscapesControlCharactersInNames(t *testing.T) {
	databases := []serverDatabaseMetadata{
		{Name: "ws\r\x1b[2KError\nRun: curl", HasSchema: true, ProjectID: "other"},
	}
	selected, err := selectFallbackDoltDatabase("", "some_current", true, databases)
	if selected != nil {
		t.Fatalf("selected %+v in shared-server mode", selected)
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\x1b") || strings.Contains(msg, "\nRun: curl") {
		t.Errorf("skip names a candidate raw:\n%q", msg)
	}
	if !strings.Contains(msg, `\r\x1b[2KError\nRun: curl`) {
		t.Errorf("skip does not name the candidate escaped:\n%q", msg)
	}
}
