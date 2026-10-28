package dolt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestDoltNew_RequireReadableSchema covers server mode's refusal of a schema
// too old for this binary's issue reads. A read-only open never migrates, so
// after a bd upgrade it is routinely one migration behind; where that
// migration added a column the shared projection selects, every list, show and
// ready would fail on the unknown column. An open that sets
// RequireReadableSchema refuses such a database with SchemaBehindError, while a
// behind database that still has every projected column keeps opening, so
// reads keep working through the upgrade window (bd-578h9.5, #5920).
//
// One CREATE/DROP DATABASE cycle covers the matrix, as in
// TestDoltNew_SharedStoreMigrateGate_NoRemote, and no step opens it writable:
// a writable open would migrate it back to the latest schema.
func TestDoltNew_RequireReadableSchema(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)
	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	openWith := func(cfg *Config) error {
		cfg.Path = tmpDir
		cfg.CommitterName = "test"
		cfg.CommitterEmail = "test@example.com"
		cfg.Database = dbName
		s, err := New(ctx, cfg)
		if err == nil {
			s.Close()
		}
		return err
	}
	behind := schema.LatestVersion() - 1

	// Behind by the cursor alone: the schema change itself stays applied, the
	// state a co-resident client sees while its operator decides to migrate.
	if _, err := store.db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	t.Run("behind with every projected column opens", func(t *testing.T) {
		if err := openWith(&Config{ReadOnly: true, RequireReadableSchema: true}); err != nil {
			t.Fatalf("New (read-only, cursor behind, columns present) = %v, want success: reads keep working on the old schema", err)
		}
	})

	// Behind for real: 0073's down takes arrival_seq off both planes, the
	// shape of a database the previous release migrated.
	runMigrationSQL(t, ctx, store, "../schema/migrations/0073_add_arrival_seq.down.sql")

	t.Run("read-only open refuses a schema its reads cannot query", func(t *testing.T) {
		err := openWith(&Config{ReadOnly: true, RequireReadableSchema: true})
		var behindErr *schema.SchemaBehindError
		if !errors.As(err, &behindErr) {
			t.Fatalf("New (read-only, missing arrival_seq) = %T (%v), want *schema.SchemaBehindError, not an open that fails each read later", err, err)
		}
		if behindErr.DBVersion != behind || behindErr.BinaryVersion != schema.LatestVersion() || behindErr.Gateway {
			t.Errorf("SchemaBehindError = %+v, want v%d behind v%d, not a gateway's", *behindErr, behind, schema.LatestVersion())
		}
		if !strings.Contains(err.Error(), "bd migrate schema") {
			t.Errorf("refusal %q does not name `bd migrate schema`", err)
		}
	})

	t.Run("an open that does not ask still opens", func(t *testing.T) {
		// bd doctor's opens: they must reach a behind database to report it.
		if err := openWith(&Config{ReadOnly: true}); err != nil {
			t.Fatalf("New (read-only, no RequireReadableSchema) = %v, want success", err)
		}
	})

	t.Run("gateway open names the server administrator", func(t *testing.T) {
		// A gateway never migrates, writable or not, and bd cannot migrate
		// the schema it owns.
		err := openWith(&Config{Gateway: true, RequireReadableSchema: true})
		var behindErr *schema.SchemaBehindError
		if !errors.As(err, &behindErr) || !behindErr.Gateway {
			t.Fatalf("New (gateway, missing arrival_seq) = %T (%v), want a gateway *schema.SchemaBehindError", err, err)
		}
		if strings.Contains(err.Error(), "bd migrate schema") {
			t.Errorf("gateway refusal %q prescribes `bd migrate schema`", err)
		}
	})

	t.Run("the escape hatch reads anyway", func(t *testing.T) {
		t.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
		if err := openWith(&Config{ReadOnly: true, RequireReadableSchema: true}); err != nil {
			t.Fatalf("New (read-only, BD_IGNORE_SCHEMA_SKEW=1) = %v, want success", err)
		}
	})
}
