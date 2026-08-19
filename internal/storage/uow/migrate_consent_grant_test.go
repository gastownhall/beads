package uow

import (
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestMain grants package-wide migration consent: these tests exercise the
// provider/bootstrap machinery that runs BELOW the consent gate
// (schema/migrate_consent.go), exactly as production does once an operator
// has consented.
func TestMain(m *testing.M) {
	schema.SetLocalMigrateConsent(true)
	os.Exit(m.Run())
}
