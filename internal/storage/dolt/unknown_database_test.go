package dolt

import (
	"fmt"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

// A server-mode open that was not asked to create the database skips the
// no-database admin connection and lets the database-scoped ping decide
// whether the database is there. That makes this classifier the whole
// difference between "database not found" guidance and a raw driver error,
// so it has to recognise the refusal in every form it actually arrives in.
func TestIsUnknownDatabaseError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			// Dolt's wording. Text matching tuned to MySQL alone misses it.
			name: "dolt 1049 typed error",
			err:  &mysql.MySQLError{Number: erUnknownDatabase, Message: "database not found: beads"},
			want: true,
		},
		{
			// MySQL's wording for the same error number.
			name: "mysql 1049 typed error",
			err:  &mysql.MySQLError{Number: erUnknownDatabase, Message: "Unknown database 'beads'"},
			want: true,
		},
		{
			name: "typed 1049 wrapped by a caller",
			err: fmt.Errorf("failed to connect: %w",
				&mysql.MySQLError{Number: erUnknownDatabase, Message: "database not found: beads"}),
			want: true,
		},
		{
			// Fallback path: the typed value was lost, only text survives.
			name: "untyped dolt text",
			err:  fmt.Errorf("database not found: beads"),
			want: true,
		},
		{
			name: "untyped mysql text",
			err:  fmt.Errorf("Error 1049 (HY000): Unknown database 'beads'"),
			want: true,
		},
		{
			// Access denied must not be read as absence: the database may
			// well exist, and reporting it as missing sends the operator
			// down the recovery path in databaseNotFoundError for nothing.
			name: "access denied is not absence",
			err:  &mysql.MySQLError{Number: 1045, Message: "Access denied for user 'bd'"},
			want: false,
		},
		{
			name: "server unreachable is not absence",
			err:  fmt.Errorf("dial tcp 127.0.0.1:3307: connect: connection refused"),
			want: false,
		},
		{
			name: "unrelated table error is not absence",
			err:  &mysql.MySQLError{Number: 1146, Message: "table not found: issues"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnknownDatabaseError(tt.err); got != tt.want {
				t.Errorf("isUnknownDatabaseError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
