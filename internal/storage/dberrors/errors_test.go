package dberrors

import (
	"errors"
	"fmt"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

func TestIsNoUsableDatabase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil"},
		{name: "1105 no database selected", err: &mysql.MySQLError{Number: 1105, Message: "no database selected"}, want: true},
		{name: "1105 any case", err: &mysql.MySQLError{Number: 1105, Message: "No Database Selected"}, want: true},
		{name: "1049 unknown database", err: &mysql.MySQLError{Number: 1049, Message: "Unknown database 'beads'"}, want: true},
		{name: "1049 dolt wording", err: &mysql.MySQLError{Number: 1049, Message: "database not found: beads"}, want: true},
		{name: "wrapped", err: fmt.Errorf("read: %w", &mysql.MySQLError{Number: 1049}), want: true},
		{name: "text no database selected", err: errors.New("no database selected"), want: true},
		{name: "text database not found", err: errors.New("database not found: beads"), want: true},
		{name: "text error 1049", err: errors.New("Error 1049 (42000): Unknown database 'beads'"), want: true},
		{name: "1046 no database selected", err: &mysql.MySQLError{Number: 1046, Message: "No database selected"}, want: true},
		{name: "text error 1046", err: errors.New("Error 1046 (3D000): No database selected"), want: true},

		// 1105 is Dolt's generic error code: only the one message counts.
		{name: "1105 generic", err: &mysql.MySQLError{Number: 1105, Message: "lock wait timeout exceeded"}},
		{name: "1105 names a database another way", err: &mysql.MySQLError{Number: 1105, Message: "failed to read database catalog"}},
		{name: "table missing is not this", err: &mysql.MySQLError{Number: 1146, Message: "table not found: leases"}},
		{name: "access denied is not this", err: &mysql.MySQLError{Number: 1044, Message: "Access denied to database 'beads'"}},
		{name: "text generic 1105", err: errors.New("Error 1105 (HY000): connection reset")},

		// A true answer reads as "no schema cursor", so a phrase quoted inside
		// another failure must not count.
		{name: "1105 quoting the phrase", err: &mysql.MySQLError{Number: 1105, Message: "query failed: no database selected for the subquery"}},
		{name: "text access denied quoting a database name", err: errors.New("Access denied for user 'reader' to database 'unknown database'")},
		{name: "text error 1044 quoting the phrase", err: errors.New("Error 1044 (42000): Access denied to database 'database not found'")},
		{name: "text transport error quoting the phrase", err: errors.New("read tcp: connection reset while handling: database not found: beads")},
		{name: "text bare 1049 number", err: errors.New("query log line mentioned error 1049")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsNoUsableDatabase(tt.err); got != tt.want {
				t.Fatalf("IsNoUsableDatabase(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestMissingTableName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantName string
		wantOK   bool
	}{
		{name: "nil", err: nil},
		{
			name:     "go-mysql-server wording",
			err:      &mysql.MySQLError{Number: 1146, Message: "table not found: leases"},
			wantName: "leases",
			wantOK:   true,
		},
		{
			name:     "mysql wording strips the schema qualifier",
			err:      &mysql.MySQLError{Number: 1146, Message: "Table 'beads.leases' doesn't exist"},
			wantName: "leases",
			wantOK:   true,
		},
		{
			name:     "backticked",
			err:      errors.New("Error 1146 (42S02): Table `wisps` does not exist"),
			wantName: "wisps",
			wantOK:   true,
		},
		{
			name:     "wrapped",
			err:      fmt.Errorf("get issue: %w", &mysql.MySQLError{Number: 1146, Message: "table not found: wisp_labels"}),
			wantName: "wisp_labels",
			wantOK:   true,
		},
		{
			// A different failure must never be read as a missing table, or the
			// narrowed tolerance would be as blind as the blanket one.
			name: "unrelated mysql error",
			err:  &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"},
		},
		{
			name: "connection failure",
			err:  errors.New("dial tcp 127.0.0.1:3306: connect: connection refused"),
		},
		{
			name: "missing column is not a missing table",
			err:  &mysql.MySQLError{Number: 1054, Message: "Unknown column 'leases.granted_node' in 'field list'"},
		},
		{
			// The IsTableNotExist gate is what makes this a tightening rather
			// than a widening: prose that merely reads like a missing table,
			// without the classification, must stay untolerated. Drop the gate
			// and this case starts returning ("leases", true).
			name: "table-not-exist wording alone is not a classification",
			err:  errors.New("table not found: leases"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := MissingTableName(tc.err)
			if ok != tc.wantOK || got != tc.wantName {
				t.Fatalf("MissingTableName(%v) = (%q, %v), want (%q, %v)", tc.err, got, ok, tc.wantName, tc.wantOK)
			}
		})
	}
}

func TestIsMissingTable(t *testing.T) {
	t.Parallel()

	leasesGone := &mysql.MySQLError{Number: 1146, Message: "table not found: leases"}

	if !IsMissingTable(leasesGone, "leases") {
		t.Error("IsMissingTable(leases error, \"leases\") = false, want true")
	}
	// The whole point of the helper: the table the caller is willing to do
	// without is not the table that actually went missing.
	if IsMissingTable(leasesGone, "wisps") {
		t.Error("IsMissingTable(leases error, \"wisps\") = true, want false")
	}
	if IsMissingTable(nil, "leases") {
		t.Error("IsMissingTable(nil, \"leases\") = true, want false")
	}
	if !IsMissingTable(&mysql.MySQLError{Number: 1146, Message: "Table 'beads.WISPS' doesn't exist"}, "wisps") {
		t.Error("IsMissingTable should match table names case-insensitively")
	}
}
