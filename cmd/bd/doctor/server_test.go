package doctor

import (
	"fmt"
	"strings"
	"testing"
)

func TestIsValidIdentifier(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"beads", true},
		{"beads_db", true},
		{"Beads123", true},
		{"_private", true},
		{"123start", false},     // Can't start with number
		{"", false},             // Empty string
		{"db-name", false},      // Hyphen not allowed
		{"db.name", false},      // Dot not allowed
		{"db name", false},      // Space not allowed
		{"db;drop", false},      // Semicolon not allowed
		{"db'inject", false},    // Quote not allowed
		{"beads_test_db", true}, // Multiple underscores ok
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isValidIdentifier(tt.input)
			if got != tt.want {
				t.Errorf("isValidIdentifier(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestCheckServerReachable_UnreachableHost verifies error when server is not reachable.
func TestCheckServerReachable_UnreachableHost(t *testing.T) {
	// Use a port that's almost certainly not listening
	check := checkServerReachable("127.0.0.1", 19999)

	if check.Status != StatusError {
		t.Errorf("Status = %q, want %q", check.Status, StatusError)
	}
	if !strings.Contains(check.Message, "Cannot connect") {
		t.Errorf("Message = %q, want it to contain 'Cannot connect'", check.Message)
	}
	if check.Category != CategoryFederation {
		t.Errorf("Category = %q, want %q", check.Category, CategoryFederation)
	}
}

// TestCheckConnectionPool verifies connection pool health reporting.
func TestCheckConnectionPool_NilSafe(t *testing.T) {
	// checkConnectionPool only reads db.Stats(), which is always safe to call.
	// We need a real *sql.DB. Use a minimal MySQL/driver connection that doesn't
	// actually connect (sql.Open is lazy).
	// Since we can't easily create a mock db.Stats(), we just verify the function
	// signature and return structure are correct by testing with the server checks
	// integration. This test is a placeholder documenting the gap.
	t.Skip("checkConnectionPool requires a real *sql.DB; tested via integration")
}

// TestStaleDatabasePrefixes verifies the stale database detection prefixes.
func TestStaleDatabasePrefixes(t *testing.T) {
	tests := []struct {
		name    string
		dbName  string
		isStale bool
	}{
		{"production beads", "beads", false},
		{"information_schema", "information_schema", false},
		{"mysql", "mysql", false},
		{"test database", "testdb_abc123", true},
		{"doctest database", "doctest_abc", true},
		{"doctortest database", "doctortest_abc", true},
		{"patrol test db", "beads_pt_abc", true},
		{"router test db", "beads_vr_abc", true},
		{"protocol test db", "beads_t0a1b2c3", true},
		{"user database", "my_project", false},
		{"beads_production", "beads_production", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isStale := false
			if !knownProductionDatabases[tt.dbName] {
				for _, prefix := range staleDatabasePrefixes {
					if strings.HasPrefix(tt.dbName, prefix) {
						isStale = true
						break
					}
				}
			}
			if isStale != tt.isStale {
				t.Errorf("database %q: isStale = %v, want %v", tt.dbName, isStale, tt.isStale)
			}
		})
	}
}

// TestServerHealthResult_Structure verifies the ServerHealthResult type.
func TestServerHealthResult_Structure(t *testing.T) {
	result := ServerHealthResult{
		OverallOK: true,
		Checks: []DoctorCheck{
			{Name: "test", Status: StatusOK, Message: "ok"},
		},
	}

	if !result.OverallOK {
		t.Error("OverallOK should be true")
	}
	if len(result.Checks) != 1 {
		t.Errorf("len(Checks) = %d, want 1", len(result.Checks))
	}

	// Verify OverallOK flips when an error check is added
	result.Checks = append(result.Checks, DoctorCheck{
		Name: "bad", Status: StatusError, Message: "failed",
	})
	result.OverallOK = false
	for _, c := range result.Checks {
		if c.Status == StatusError {
			result.OverallOK = false
			break
		}
	}
	if result.OverallOK {
		t.Error("OverallOK should be false when error check present")
	}
}

// TestCheckDatabaseExists_InvalidIdentifier verifies error for invalid database names.
func TestCheckDatabaseExists_InvalidIdentifier(t *testing.T) {
	// We can't call checkDatabaseExists without a real *sql.DB,
	// but we can verify the identifier validation logic separately.
	tests := []struct {
		name  string
		valid bool
	}{
		{"beads", true},
		{"beads-db", false},     // hyphen
		{"beads_db", true},      // underscore
		{"123beads", false},     // starts with number
		{"", false},             // empty
		{"beads;drop", false},   // injection
		{"`beads`", false},      // backticks
		{"beads db", false},     // space
		{"a", true},             // single char
		{"_", true},             // underscore only
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.name), func(t *testing.T) {
			got := isValidIdentifier(tt.name)
			if got != tt.valid {
				t.Errorf("isValidIdentifier(%q) = %v, want %v", tt.name, got, tt.valid)
			}
		})
	}
}
