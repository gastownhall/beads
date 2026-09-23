package uow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// dataBehindCurrent is a schema cursor at or above the convergence floor
// (schema.LastNonDeterministicMigration) and below this binary's latest, so the
// smart gate reaches the equal-version first-mover arm with migrations still
// pending — the only arm where the #6575 ancestry check runs.
const dataBehindCurrent = 65

// expectSmartGateDataBehindRoute mocks the smart router's reads for a clone
// that is LEVEL with the cached remote ref on schema and BEHIND it in data:
// the local hashes, the active branch, the remote ref's hashes (identical, so
// no skew and the same max version), and finally the ahead/behind commit
// counts the #6575 check reads through the injected adopter.
//
// The last query is the one that did not happen before this fix: with a nil
// adopter localDataBehind short-circuits, so the route ended at
// smartAutoMigrate and the refusal reported shared-store instead.
func expectSmartGateDataBehindRoute(mock sqlmock.Sqlmock, ahead, behind int) {
	hashes := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"version", "content_hash"}).
			AddRow(dataBehindCurrent-1, "hash-a").
			AddRow(dataBehindCurrent, "hash-b")
	}
	ref := "remotes/origin/main"

	mock.ExpectQuery(regexp.QuoteMeta("SELECT version, content_hash FROM schema_migrations")).
		WillReturnRows(hashes())
	mock.ExpectQuery(regexp.QuoteMeta("SELECT active_branch()")).
		WillReturnRows(sqlmock.NewRows([]string{"branch"}).AddRow("main"))
	mock.ExpectQuery(regexp.QuoteMeta(fmt.Sprintf("SHOW TABLES AS OF '%s' LIKE 'schema_migrations'", ref))).
		WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("schema_migrations"))
	mock.ExpectQuery(regexp.QuoteMeta(fmt.Sprintf("SHOW COLUMNS FROM schema_migrations AS OF '%s' LIKE 'content_hash'", ref))).
		WillReturnRows(sqlmock.NewRows([]string{"column"}).AddRow("content_hash"))
	mock.ExpectQuery(regexp.QuoteMeta(fmt.Sprintf("SELECT version, content_hash FROM schema_migrations AS OF '%s'", ref))).
		WillReturnRows(hashes())
	// versioncontrolops.LocalAheadBehind — reached only through a wired
	// AheadBehind callback.
	mock.ExpectQuery(`FROM dolt_log AS OF`).
		WillReturnRows(sqlmock.NewRows([]string{"ahead", "behind"}).AddRow(ahead, behind))
}

// TestInitSchemaProxiedDataBehind covers the proxied/`bd serve` open path's
// half of gastownhall/beads#6575.
//
// The store-open gate on this path passed a nil adopter, so localDataBehind
// had no ancestry fact to read and the route could never emit smartDataBehind.
// A proxied clone that was level on schema and behind in data therefore
// reported the blunt shared-store refusal — whose body is the designated-
// migrator recipe, `bd migrate --force` then `bd dolt push`. In this state that
// recipe IS the #6368 wedge the stop exists to prevent, so the refusal was safe
// (the shared arm suppresses the auto-migrate either way) while the guidance it
// printed was not.
func TestInitSchemaProxiedDataBehind(t *testing.T) {
	// newProxiedProvider returns a provider over a fresh mock with the two
	// escape hatches pinned off, so the gate is reached on its own merits.
	newProxiedProvider := func(t *testing.T) (*doltSQLProvider, sqlmock.Sqlmock) {
		t.Helper()
		t.Setenv(schema.AllowRemoteMigrateEnv, "0")
		t.Setenv(schema.SmartGateEnv, "1")
		t.Cleanup(func() {
			schema.SetSharedMigrateConsent(false)
			schema.SetForceAllowRemoteMigrate(false)
		})
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("create sql mock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return &doltSQLProvider{
			defaultBranch:     defaultBranch,
			db:                db,
			serverEndpoint:    "tcp:127.0.0.1:3306",
			proxiedServerMode: true,
		}, mock
	}

	t.Run("routes to the data-behind stop instead of the blunt shared-store one", func(t *testing.T) {
		p, mock := newProxiedProvider(t)

		lockName := expectBehindDatabaseThroughPreparation(mock, "beads", dataBehindCurrent)
		expectSharedGateProbe(mock, dataBehindCurrent, 1)
		expectSmartGateDataBehindRoute(mock, 0, 2)
		// Nothing between the route and the lock release: the refusal must land
		// before MigrateUp issues its first statement.
		expectLockRelease(mock, lockName)

		err := p.initSchema(context.Background(), "beads")
		var gateErr *schema.RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("initSchema() error = %T (%v), want *schema.RemoteMigrateGateError", err, err)
		}
		if !gateErr.IsDataBehind() {
			t.Fatalf("FallbackReason = %q, Decision = %q — want the #6575 data-behind stop",
				gateErr.FallbackReason, gateErr.Decision)
		}
		if !gateErr.Shared {
			t.Error("Shared = false; this provider only ever serves a shared database")
		}
		if !gateErr.Proxied {
			t.Error("Proxied = false; every refusal from this provider is a proxied-server-mode refusal")
		}

		// The finding, stated as an assertion: the body an operator reads must
		// not hand them the migrate-in-place recipe while they are still behind.
		body := gateErr.UserMessage()
		for _, forbidden := range []string{"bd migrate --force\n", "designated migrator"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("proxied data-behind body still prescribes %q:\n%s", forbidden, body)
			}
		}
		if !strings.Contains(body, schema.DataBehindRemedyCommand) {
			t.Errorf("body must name the pull:\n%s", body)
		}
		// ...and because `bd dolt pull` is refused at the proxied front door,
		// the body has to say where it can actually be run.
		for _, want := range []string{"proxied-server mode", "proxy.dolt_pull.unsupported", "server host"} {
			if !strings.Contains(body, want) {
				t.Errorf("proxied body missing %q:\n%s", want, body)
			}
		}
		// The env hatch is the one consent surface the proxy does NOT refuse,
		// so a warning that names only `bd migrate --force` leaves the wedge
		// reachable on exactly this topology.
		if !strings.Contains(body, schema.AllowRemoteMigrateEnv+"=1") {
			t.Errorf("proxied body must warn about the env hatch too:\n%s", body)
		}

		opts := gateErr.Options()
		if len(opts) != 2 || opts[0].ID != "pull-first-on-server-host" {
			t.Fatalf("Options() = %+v, want the host-qualified pull plus the consent step", opts)
		}
		for _, o := range opts {
			for _, c := range o.Commands {
				if c == "bd migrate --force" || c == "bd bootstrap" {
					t.Errorf("option %q offers %q, measured not to work in this state", o.ID, c)
				}
			}
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	// The counterfactual for the topology flag. This same provider is the one
	// `bd serve` builds for a SERVER-mode workspace, where `bd dolt pull` is
	// not refused — telling that operator to go find another machine would be
	// the same class of wrong guidance in the other direction.
	t.Run("a server-mode open keeps the runnable-here remedy", func(t *testing.T) {
		p, mock := newProxiedProvider(t)
		p.proxiedServerMode = false

		lockName := expectBehindDatabaseThroughPreparation(mock, "beads", dataBehindCurrent)
		expectSharedGateProbe(mock, dataBehindCurrent, 1)
		expectSmartGateDataBehindRoute(mock, 0, 2)
		expectLockRelease(mock, lockName)

		err := p.initSchema(context.Background(), "beads")
		var gateErr *schema.RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("initSchema() error = %T (%v), want *schema.RemoteMigrateGateError", err, err)
		}
		if !gateErr.IsDataBehind() {
			t.Fatalf("FallbackReason = %q — the stop itself is topology-independent", gateErr.FallbackReason)
		}
		if gateErr.Proxied {
			t.Error("Proxied = true for a server-mode open")
		}
		if body := gateErr.UserMessage(); strings.Contains(body, "server host") {
			t.Errorf("server-mode body must keep the runnable-here pull:\n%s", body)
		}
		if id := gateErr.Options()[0].ID; id != "pull-first" {
			t.Errorf("server-mode pull option id = %q, want %q", id, "pull-first")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("a level clone is unaffected", func(t *testing.T) {
		// The counterfactual for the wiring: same route, behind == 0. Reading
		// the ancestry must not turn a genuine first-mover into a data-behind
		// stop — it stays the shared-store refusal it was before this fix.
		p, mock := newProxiedProvider(t)

		lockName := expectBehindDatabaseThroughPreparation(mock, "beads", dataBehindCurrent)
		expectSharedGateProbe(mock, dataBehindCurrent, 1)
		expectSmartGateDataBehindRoute(mock, 0, 0)
		expectLockRelease(mock, lockName)

		err := p.initSchema(context.Background(), "beads")
		var gateErr *schema.RemoteMigrateGateError
		if !errors.As(err, &gateErr) {
			t.Fatalf("initSchema() error = %T (%v), want *schema.RemoteMigrateGateError", err, err)
		}
		if gateErr.IsDataBehind() {
			t.Error("a level clone must not reach the data-behind stop")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("read-only open prints the pull-first block, not the consent template", func(t *testing.T) {
		// readThroughRefusedMigration's template names the shared consent verb
		// for every refusal. For this stop that verb is doubly wrong: the
		// remedy is the pull, and a remote-backed shared store never reads the
		// bare verb's consent at all (schema.SharedConsentCommandForced).
		p, mock := newProxiedProvider(t)
		p.readOnly = true

		lockName := expectBehindDatabaseThroughPreparation(mock, "beads", dataBehindCurrent)
		expectSharedGateProbe(mock, dataBehindCurrent, 1)
		expectSmartGateDataBehindRoute(mock, 1, 2) // diverged shape
		expectLockRelease(mock, lockName)
		mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
			WillReturnResult(sqlmock.NewResult(0, 0))

		var initErr error
		stderr := captureStderr(t, func() {
			initErr = p.initSchema(context.Background(), "beads")
		})
		if initErr != nil {
			t.Fatalf("initSchema() error = %v, want nil — reads keep working on the old schema", initErr)
		}
		for _, want := range []string{
			"Read-only command", "without migrating",
			schema.DataBehindRemedyCommand, "MERGES", "server host",
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("read-through warning missing %q:\n%s", want, stderr)
			}
		}
		// Two-sided: the template's "run 'bd migrate schema'" line must be gone
		// for this stop. Scrub the forms that DO work first — every forced form
		// contains the bare verb as a substring.
		scrubbed := strings.ReplaceAll(stderr, schema.SharedConsentCommandForcedGlobal, "")
		scrubbed = strings.ReplaceAll(scrubbed, schema.SharedConsentCommandForced, "")
		if strings.Contains(scrubbed, schema.SharedConsentCommand) {
			t.Errorf("read-through still prescribes the bare consent verb:\n%s", stderr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}

// TestDataBehindAdopterWiring pins the shape of the injected adopter, not just
// its effect. Only AheadBehind is wired: it is the complete #6575 predicate,
// while leaving IsStrictAncestor/WorkingSetClean nil keeps routeAdoptFastForward
// returning false, so the remote-AHEAD arm on this path keeps the plain adopt
// directive it had before. FastForward must stay nil — it is a WRITE that would
// promote the schema for every co-resident client of this server.
func TestDataBehindAdopterWiring(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		a := dataBehindAdopter(readOnly)
		if a.AheadBehind == nil {
			t.Fatal("AheadBehind must be wired; it is the whole point of the injection")
		}
		if a.FastForward != nil {
			t.Error("FastForward must stay nil: a fast-forward here promotes the schema for every co-resident client")
		}
		if a.IsStrictAncestor != nil || a.WorkingSetClean != nil {
			t.Error("IsStrictAncestor/WorkingSetClean must stay nil so the remote-ahead arm is unchanged")
		}
		if a.ReadOnly != readOnly {
			t.Errorf("ReadOnly = %v, want %v", a.ReadOnly, readOnly)
		}
	}
}
