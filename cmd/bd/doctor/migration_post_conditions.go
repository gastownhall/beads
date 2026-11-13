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
// points at the explicit `bd migrate schema`, which repairs it. A check that
// cannot run says so instead of reading as healthy.
//
// Database access mirrors CheckMigrationContentSkew: the server-mode store
// when there is one, else the workspace's embedded database opened directly.
func CheckMigrationPostConditions(ss *SharedStore) DoctorCheck {
	ctx := context.Background()
	if store := ss.Store(); store != nil {
		return checkMigrationPostConditions(ctx, store.DB())
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
	return checkMigrationPostConditions(ctx, db), true
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

func checkMigrationPostConditions(ctx context.Context, db *sql.DB) DoctorCheck {
	// One session for every probe, so a failed statement cannot leave a later
	// read on a different pooled session's catalog.
	conn, err := db.Conn(ctx)
	if err != nil {
		return migrationPostConditionsCannotCheck(err)
	}
	defer func() { _ = conn.Close() }()

	check, err := schema.CheckRecordedPostConditions(ctx, conn)
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
	return DoctorCheck{
		Name:     migrationPostConditionsCheckName,
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d recorded migration post-condition(s) unmet: %s", len(names), strings.Join(names, ", ")),
		Detail:   detail,
		Fix:      "Run `bd migrate schema` to drop the leftover default(s); commit any uncommitted changes to those tables first.",
		Category: CategoryData,
	}
}
