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

// CheckMigrationPostConditions detects recorded migration marks whose
// dropped-default DDL never took effect — gastownhall/beads#5269's
// marks-say-v53 / DDL-says-v49 state. The store claims a version while the
// column default that version drops is still in place, and because the mark
// exists no later migration pass heals it. This is the read-only diagnosis;
// the repair is the explicit `bd migrate schema` (never run here, and no
// doctor --fix arm).
//
// The check is independent of remotes. In an embedded workspace SharedStore
// only ever holds a server-mode store (nil here), so — as with content skew
// — the check falls back to opening the embedded database directly.
func CheckMigrationPostConditions(ss *SharedStore) DoctorCheck {
	if store := ss.Store(); store != nil {
		return checkMigrationPostConditions(context.Background(), store.DB())
	}
	if check, ok := checkMigrationPostConditionsEmbedded(context.Background(), sharedStoreBeadsDir(ss)); ok {
		return check
	}
	return DoctorCheck{
		Name:     migrationPostConditionsCheckName,
		Status:   StatusOK,
		Message:  "N/A (no database)",
		Category: CategoryData,
	}
}

// checkMigrationPostConditionsEmbedded opens the workspace's embedded Dolt
// database (read-only diagnostic queries only; nothing is written) and runs
// the post-condition diagnosis on it. Returns ok=false when there is no
// embedded database to inspect.
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
		return DoctorCheck{
			Name:     migrationPostConditionsCheckName,
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Could not check migration post-conditions (open embedded database): %v", err),
			Detail:   "The post-condition check failed to run; this does not mean drift exists. Re-run `bd doctor` and report the error if it persists.",
			Category: CategoryData,
		}, true
	}
	defer func() { _ = cleanup() }()
	return checkMigrationPostConditions(ctx, db), true
}

// checkMigrationPostConditions reports every recorded dropped-default
// obligation whose default survived, with the `bd migrate schema` remedy.
// "Cannot check" is NOT "checked and holds": unexpected read failures warn
// instead of being swallowed as OK (the bd-6dnrw.27 lesson).
func checkMigrationPostConditions(ctx context.Context, db *sql.DB) DoctorCheck {
	violations, err := schema.DiagnoseDroppedDefaultDrift(ctx, db)
	if err != nil {
		return DoctorCheck{
			Name:     migrationPostConditionsCheckName,
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Could not check migration post-conditions (inspect recorded defaults): %v", err),
			Detail:   "The post-condition check failed to run; this does not mean drift exists. Re-run `bd doctor` and report the error if it persists.",
			Category: CategoryData,
		}
	}
	if len(violations) == 0 {
		return DoctorCheck{
			Name:     migrationPostConditionsCheckName,
			Status:   StatusOK,
			Message:  "Recorded migration post-conditions hold",
			Category: CategoryData,
		}
	}
	return DoctorCheck{
		Name:     migrationPostConditionsCheckName,
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d recorded migration post-condition(s) unmet: migration marks claim versions whose dropped default is still in place", len(violations)),
		Detail:   strings.Join(violations, "; "),
		Fix:      "Run `bd migrate schema` to re-drop the lingering defaults (GH#5269). Rows and migration marks are preserved; repaired tracked tables are committed separately, ignored-plane tables in place.",
		Category: CategoryData,
	}
}
