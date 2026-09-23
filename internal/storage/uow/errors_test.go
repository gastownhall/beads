package uow

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

func TestIsAccessDeniedError(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "mysql 1044 db access denied", err: &mysql.MySQLError{Number: 1044, Message: "Access denied for user"}, want: true},
		{name: "mysql 1045 access denied", err: &mysql.MySQLError{Number: 1045, Message: "Access denied for user"}, want: true},
		// Dolt reports both refusals under the generic 1105, so only the text
		// classifies them.
		{name: "dolt 1105 access denied", err: &mysql.MySQLError{Number: 1105, Message: "Access denied for user 'scoped'@'%' to database 'team_b'"}, want: true},
		{name: "dolt 1105 command denied", err: &mysql.MySQLError{Number: 1105, Message: "command denied to user 'scoped'@'%'"}, want: true},
		{name: "dolt 1105 unrelated", err: &mysql.MySQLError{Number: 1105, Message: "unexpected internal error"}, want: false},
		{name: "not found is not a denial", err: &mysql.MySQLError{Number: 1049, Message: "database not found: team_b"}, want: false},
		{name: "plain error with the wording", err: errors.New("db: UseDatabase: Access denied for user"), want: true},
		{name: "plain unrelated error", err: errors.New("connection reset by peer"), want: false},
		{name: "wrapped", err: fmt.Errorf("db: UseDatabase: %w", &mysql.MySQLError{Number: 1044}), want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAccessDeniedError(tt.err); got != tt.want {
				t.Fatalf("isAccessDeniedError(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsDatabaseNotFoundError(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "mysql 1049", err: &mysql.MySQLError{Number: 1049, Message: "Unknown database 'team_b'"}, want: true},
		{name: "dolt text", err: errors.New("db: UseDatabase: database not found: team_b"), want: true},
		{name: "mysql text", err: errors.New("Error 1049 (42000): Unknown database 'team_b'"), want: true},
		// The load-bearing negative: a denial must never be reported as absence.
		{name: "access denied is not absence", err: &mysql.MySQLError{Number: 1105, Message: "Access denied for user 'scoped'@'%' to database 'team_b'"}, want: false},
		{name: "plain unrelated error", err: errors.New("connection reset by peer"), want: false},
		{name: "wrapped", err: fmt.Errorf("db: UseDatabase: %w", &mysql.MySQLError{Number: 1049}), want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDatabaseNotFoundError(tt.err); got != tt.want {
				t.Fatalf("isDatabaseNotFoundError(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

func TestClassifyUseDatabaseError(t *testing.T) {
	// The exact server errors observed from dolt 2.1.8 against a credential
	// granted only one database.
	denied := fmt.Errorf("db: UseDatabase: %w",
		&mysql.MySQLError{Number: 1105, Message: "Access denied for user 'scoped'@'%' to database 'team_b'"})
	missing := fmt.Errorf("db: UseDatabase: %w",
		&mysql.MySQLError{Number: 1049, Message: "database not found: team_b"})
	other := fmt.Errorf("db: UseDatabase: %w", errors.New("connection reset by peer"))

	t.Run("denied reports a denial, never absence", func(t *testing.T) {
		err := classifyUseDatabaseError(denied, "team_b", teamServerUseDatabaseRemedy)
		assertContainsAll(t, err.Error(),
			`access to database "team_b" was denied`,
			"not provisioned on the server",
			"has not been granted access",
			"ask the server administrator",
			// The wrapped driver error must survive for diagnosis.
			"Access denied for user 'scoped'",
		)
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("denial must not claim the database is absent: %v", err)
		}
		if !errors.Is(err, denied) {
			t.Fatalf("classifyUseDatabaseError() dropped the cause: %v", err)
		}
	})

	t.Run("missing reports absence with the caller's remedy", func(t *testing.T) {
		err := classifyUseDatabaseError(missing, "team_b", teamServerUseDatabaseRemedy)
		assertContainsAll(t, err.Error(),
			`database "team_b" does not exist on the server`,
			"ask the server administrator to create it",
			"database not found: team_b",
		)
	})

	t.Run("missing under preview keeps the preview remedy", func(t *testing.T) {
		err := classifyUseDatabaseError(missing, "team_b", previewUseDatabaseRemedy)
		assertContainsAll(t, err.Error(),
			`database "team_b" does not exist on the server`,
			"--dry-run",
			"without the preview flag first",
		)
	})

	t.Run("unclassified claims neither absence nor denial", func(t *testing.T) {
		err := classifyUseDatabaseError(other, "team_b", teamServerUseDatabaseRemedy)
		assertContainsAll(t, err.Error(),
			`could not switch to database "team_b"`,
			"connection reset by peer",
		)
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "was denied") {
			t.Fatalf("unclassified failure must not guess a cause: %v", err)
		}
	})

	// Vendor neutrality: the classified branches speak of "the server
	// administrator", never a product. Only the untouched unclassified remedy
	// still carries the historical product wording (tracked as a separate
	// naming sweep).
	t.Run("classified messages name no product", func(t *testing.T) {
		for _, err := range []error{
			classifyUseDatabaseError(denied, "team_b", teamServerUseDatabaseRemedy),
			classifyUseDatabaseError(missing, "team_b", teamServerUseDatabaseRemedy),
		} {
			for _, banned := range []string{"beads-team-server", "bts "} {
				if strings.Contains(err.Error(), banned) {
					t.Fatalf("message must not name a product (%q): %v", banned, err)
				}
			}
		}
	})
}

func assertContainsAll(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("message %q missing %q", got, want)
		}
	}
}
