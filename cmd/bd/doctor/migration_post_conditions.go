package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

const migrationPostConditionsCheckName = "Migration Post-Conditions"

// CheckMigrationPostConditions reports recorded migrations whose dropped
// column default is still in place (GH#5269): the cursor claims e.g. 0050
// applied while dependencies.id still mints random ids. It only reads — it
// creates no cursor table and changes no schema, cursor or history — and
// points at the explicit `bd migrate schema`, which repairs it. Through a
// gateway, which owns the schema and which bd never changes, it points at the
// server administrator instead. A check that cannot run says so instead of
// reading as healthy.
//
// Database access mirrors CheckMigrationContentSkew: the server-mode store
// when there is one, else the workspace's embedded database opened directly.
func CheckMigrationPostConditions(ss *SharedStore) DoctorCheck {
	ctx := context.Background()
	if store := ss.Store(); store != nil {
		return checkMigrationPostConditions(ctx, store.DB(), store.IsGateway())
	}
	if check, ok := checkMigrationPostConditionsEmbedded(ctx, sharedStoreBeadsDir(ss)); ok {
		return check
	}
	return DoctorCheck{
		Name:     migrationPostConditionsCheckName,
		Status:   StatusOK,
		Message:  "N/A (no database)",
		Category: CategoryData,
	}
}

func checkMigrationPostConditionsEmbedded(ctx context.Context, beadsDir string) (DoctorCheck, bool) {
	if beadsDir == "" {
		return DoctorCheck{}, false
	}
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if _, err := os.Stat(dataDir); err != nil {
		return DoctorCheck{}, false
	}
	database := configfile.DefaultDoltDatabase
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "")
	if err != nil {
		return migrationPostConditionsCannotCheck(fmt.Errorf("open embedded database: %w", err)), true
	}
	defer func() { _ = cleanup() }()
	return checkMigrationPostConditions(ctx, db, false), true
}

func migrationPostConditionsCannotCheck(err error) DoctorCheck {
	return DoctorCheck{
		Name:     migrationPostConditionsCheckName,
		Status:   StatusWarning,
		Message:  fmt.Sprintf("Could not check recorded migration post-conditions: %v", err),
		Detail:   "The check failed to run; this does not mean the schema is healthy. Re-run `bd doctor` and report the error if it persists.",
		Category: CategoryData,
	}
}

func checkMigrationPostConditions(ctx context.Context, db *sql.DB, gateway bool) DoctorCheck {
	// One session for every probe, so a failed statement cannot leave a later
	// read on a different pooled session's catalog.
	conn, err := db.Conn(ctx)
	if err != nil {
		return migrationPostConditionsCannotCheck(err)
	}
	defer func() { _ = conn.Close() }()

	check, err := schema.CheckRecordedPostConditions(ctx, conn)
	return migrationPostConditionsResult(check, err, gateway)
}

// Fixes for an unmet post-condition: the explicit repair, or, through a
// gateway, the server administrator (bd migrate schema does not repair there).
const (
	migrationPostConditionsFix = "Run `bd migrate schema` to drop the leftover default(s); " +
		"commit any uncommitted changes to those tables first."
	migrationPostConditionsGatewayFix = "This database is served by a gateway server, which owns its schema, " +
		"so bd does not repair it: ask the server administrator to drop the leftover default(s) " +
		"(ALTER TABLE <table> ALTER COLUMN <column> DROP DEFAULT) and commit."
)

// migrationPostConditionsResult is the check for what
// schema.CheckRecordedPostConditions saw (check, err) on a database that a
// gateway serves or not.
func migrationPostConditionsResult(check schema.PostConditionCheck, err error, gateway bool) DoctorCheck {
	if len(check.Violations) == 0 {
		if err != nil {
			return migrationPostConditionsCannotCheck(err)
		}
		return DoctorCheck{
			Name:     migrationPostConditionsCheckName,
			Status:   StatusOK,
			Message:  fmt.Sprintf("All %d recorded migration post-condition(s) hold", check.Applicable),
			Category: CategoryData,
		}
	}

	names := make([]string, len(check.Violations))
	details := make([]string, len(check.Violations))
	for i, v := range check.Violations {
		names[i] = v.Name()
		details[i] = v.String()
	}
	detail := strings.Join(details, "\n") +
		"\nInserts that omit id still get a random id, so clones mint different ids for the same row (GH#5269)."
	if err != nil {
		detail += fmt.Sprintf("\nSome conditions could not be checked: %v", err)
	}
	fix := migrationPostConditionsFix
	if gateway {
		fix = migrationPostConditionsGatewayFix
	}
	return DoctorCheck{
		Name:     migrationPostConditionsCheckName,
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d recorded migration post-condition(s) unmet: %s", len(names), strings.Join(names, ", ")),
		Detail:   detail,
		Fix:      fix,
		Category: CategoryData,
	}
}
