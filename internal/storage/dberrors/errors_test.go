package dberrors

import (
	"errors"
	"fmt"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

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
