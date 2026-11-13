package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/schema"
)

func TestCheckMigrationPostConditions_NoDatabase(t *testing.T) {
	got := CheckMigrationPostConditions(&SharedStore{})
	if got.Status != StatusOK || got.Message != "N/A (no database)" {
		t.Fatalf("check = %q (%s), want OK N/A", got.Status, got.Message)
	}
}

// A probe that fails says the check could not run; it never reads as healthy
// (GH#5269, AC-7).
func TestCheckMigrationPostConditions_CouldNotCheck(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(`.*`).WillReturnError(errors.New("catalog unavailable"))

	got := checkMigrationPostConditions(t.Context(), db, false)
	if got.Status != StatusWarning ||
		!strings.Contains(got.Message, "Could not check") ||
		!strings.Contains(got.Message, "catalog unavailable") ||
		!strings.Contains(got.Detail, "does not mean the schema is healthy") {
		t.Fatalf("check = %q (%s; %s), want a could-not-check warning", got.Status, got.Message, got.Detail)
	}
}

// An unmet post-condition points at `bd migrate schema`, except through a
// gateway: the gateway owns the schema, bd migrate schema does not repair it
// there, so the fix names the server administrator (GH#5269).
func TestMigrationPostConditionsResult_FixFollowsWhoOwnsTheSchema(t *testing.T) {
	check := schema.PostConditionCheck{
		Applicable: 8,
		Violations: []schema.PostConditionViolation{{
			Table: "dependencies", Column: "id", Default: "(uuid())",
			Sources: []string{"main 0050_dependencies_deterministic_id.up.sql"},
		}},
	}
	for _, tc := range []struct {
		name    string
		gateway bool
		want    string
		notWant string
	}{
		{name: "direct", want: "`bd migrate schema`", notWant: "administrator"},
		{name: "gateway", gateway: true, want: "server administrator", notWant: "bd migrate schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := migrationPostConditionsResult(check, nil, tc.gateway)
			if got.Status != StatusWarning || got.Message != "1 recorded migration post-condition(s) unmet: dependencies.id" {
				t.Fatalf("check = %q (%s), want a warning naming dependencies.id", got.Status, got.Message)
			}
			if !strings.Contains(got.Fix, tc.want) || strings.Contains(got.Fix, tc.notWant) {
				t.Fatalf("fix = %q, want it to name %q and not %q", got.Fix, tc.want, tc.notWant)
			}
		})
	}
}
