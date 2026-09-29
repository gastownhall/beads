//go:build cgo

package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The proxied-server mirror of TestEmbeddedCreateStorageClass. This is the
// transport the field report was filed against and the one Enterprise builds
// run: every `--storage-class` value was accepted and dropped, so an
// `ephemeral` request minted a DURABLE row and the durable-class-on-wisp-plane
// conflict the direct door rejects was silently collapsed.
//
// Every assertion reads the raw cell out of the server's own tables, because
// the JSON the command prints is the request's own struct on some routes and
// would agree with itself either way.
//
// CI note: this shard is gated on BEADS_TEST_PROXIED_SERVER=1 and runs mostly
// only on push-to-main, so it is meant to be run locally alongside any change
// to the create input path.
func TestStorageClassProxiedServer(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// storageClassCell pins the at-rest form (NULL vs literal) on whichever
	// plane the row was supposed to land on.
	storageClassCell := func(t *testing.T, db *sql.DB, table, id string) sql.NullString {
		t.Helper()
		var got sql.NullString
		//nolint:gosec // G202: table is a test-local literal, never caller input
		if err := db.QueryRowContext(context.Background(), "SELECT storage_class FROM "+table+" WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("query %s.storage_class for %s: %v", table, id, err)
		}
		return got
	}

	// Batch doors mint their own ids, so their rows are found by title. An
	// absent row is "" rather than a failure: the PLANE is what is under test.
	idByTitle := func(t *testing.T, db *sql.DB, table, title string) string {
		t.Helper()
		var id string
		//nolint:gosec // G202: table is a test-local literal, never caller input
		err := db.QueryRowContext(context.Background(), "SELECT id FROM "+table+" WHERE title = ?", title).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return ""
		}
		if err != nil {
			t.Fatalf("query %s by title %q: %v", table, title, err)
		}
		return id
	}

	setStorageClassConfig := func(t *testing.T, p proxiedProject, key, value string) {
		t.Helper()
		if out, err := bdProxiedRun(t, bd, p.dir, "config", "set", key, value); err != nil {
			t.Fatalf("config set %s: %v\n%s", key, err, out)
		}
	}

	// THE FIELD REPORT: `--storage-class unversioned` on this door left the cell
	// NULL, so the class could never be read back — and `bd update` has no
	// storage-class surface, which makes a create-time drop unrepairable.
	t.Run("explicit_unversioned_persists", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pu")
		issue := bdProxiedCreate(t, bd, p.dir, "Unversioned bead", "-d", "x", "--storage-class", "unversioned")
		if issue.StorageClass != types.StorageClassUnversioned {
			t.Errorf("JSON storage_class = %q, want unversioned", issue.StorageClass)
		}
		db := openProxiedDB(t, p)
		if cell := storageClassCell(t, db, "issues", issue.ID); !cell.Valid || cell.String != "unversioned" {
			t.Errorf("DB cell = %+v, want 'unversioned'", cell)
		}
	})

	t.Run("default_and_explicit_versioned_stay_null", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pv")
		db := openProxiedDB(t, p)

		plain := bdProxiedCreate(t, bd, p.dir, "Plain bead", "-d", "x")
		if cell := storageClassCell(t, db, "issues", plain.ID); cell.Valid {
			t.Errorf("plain create: DB cell = %q, want NULL", cell.String)
		}
		// C2.4: the explicit versioned spelling normalizes to the same NULL, so
		// a NULL cell here is correct rather than the bug.
		explicit := bdProxiedCreate(t, bd, p.dir, "Versioned bead", "-d", "x", "--storage-class", "versioned")
		if cell := storageClassCell(t, db, "issues", explicit.ID); cell.Valid {
			t.Errorf("explicit versioned: DB cell = %q, want NULL", cell.String)
		}
	})

	// WORSE THAN A NO-OP before the fix: this minted a durable versioned row for
	// a request that asked for ephemeral.
	t.Run("ephemeral_spelling_routes_to_wisp_plane", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pe")
		issue := bdProxiedCreate(t, bd, p.dir, "Ephemeral bead", "-d", "x", "--storage-class", "ephemeral")
		if !issue.Ephemeral {
			t.Errorf("--storage-class ephemeral should set the ephemeral flag")
		}
		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM issues WHERE id = ?", issue.ID).Scan(&count); err != nil {
			t.Fatalf("query issues: %v", err)
		}
		if count != 0 {
			t.Errorf("an ephemeral request minted a DURABLE row %s", issue.ID)
		}
		var ephemeral int
		if err := db.QueryRowContext(context.Background(), "SELECT ephemeral FROM wisps WHERE id = ?", issue.ID).Scan(&ephemeral); err != nil {
			t.Fatalf("query wisps: %v", err)
		}
		if ephemeral != 1 {
			t.Errorf("wisps.ephemeral = %d, want 1", ephemeral)
		}
		if cell := storageClassCell(t, db, "wisps", issue.ID); cell.Valid {
			t.Errorf("wisp row cell = %q, want NULL (wisp-plane rows derive their class)", cell.String)
		}
	})

	// Finding A of #5164, unfixed on this route until now: the direct door
	// rejects the contradiction, this one silently made the row a wisp.
	t.Run("durable_class_with_wisp_plane_is_rejected", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pf")
		db := openProxiedDB(t, p)
		for _, planeFlag := range []string{"--ephemeral", "--no-history"} {
			out := bdProxiedCreateFail(t, bd, p.dir, "Conflicting "+planeFlag, "-d", "x", planeFlag, "--storage-class", "versioned")
			// Byte-identical to the direct door's message.
			if !strings.Contains(out, "conflicts with --ephemeral/--no-history") {
				t.Errorf("%s: expected the wisp-plane conflict error, got:\n%s", planeFlag, out)
			}
			if id := idByTitle(t, db, "wisps", "Conflicting "+planeFlag); id != "" {
				t.Errorf("%s: a refused create left wisp %s behind", planeFlag, id)
			}
			if id := idByTitle(t, db, "issues", "Conflicting "+planeFlag); id != "" {
				t.Errorf("%s: a refused create left issue %s behind", planeFlag, id)
			}
		}
		out := bdProxiedCreateFail(t, bd, p.dir, "Doubly ephemeral", "-d", "x", "--no-history", "--storage-class", "ephemeral")
		if !strings.Contains(out, "--storage-class ephemeral and --no-history are mutually exclusive") {
			t.Errorf("expected the ephemeral/no-history exclusion, got:\n%s", out)
		}
	})

	// Release-audit finding #48: the per-type default was resolved only by the
	// direct door, so the same config.yaml produced different rows depending on
	// which transport the workspace was initialized with.
	t.Run("per_type_config_default", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pc")
		setStorageClassConfig(t, p, "storage-class.task", "unversioned")
		db := openProxiedDB(t, p)

		byDefault := bdProxiedCreate(t, bd, p.dir, "Defaulted bead", "-d", "x", "-t", "task")
		if cell := storageClassCell(t, db, "issues", byDefault.ID); !cell.Valid || cell.String != "unversioned" {
			t.Errorf("config default: DB cell = %+v, want 'unversioned'", cell)
		}
		// Flag over config (C1.3), and versioned normalizes back to NULL.
		forced := bdProxiedCreate(t, bd, p.dir, "Forced versioned", "-d", "x", "-t", "task", "--storage-class", "versioned")
		if cell := storageClassCell(t, db, "issues", forced.ID); cell.Valid {
			t.Errorf("explicit versioned over config default: DB cell = %q, want NULL", cell.String)
		}
		// Other types are untouched by the task default.
		bug := bdProxiedCreate(t, bd, p.dir, "A bug", "-d", "x", "-t", "bug")
		if cell := storageClassCell(t, db, "issues", bug.ID); cell.Valid {
			t.Errorf("other type: DB cell = %q, want NULL", cell.String)
		}
		// A config-derived durable class is not a caller contradiction, so it
		// YIELDS to the explicit plane instead of blocking the create.
		yielded := bdProxiedCreate(t, bd, p.dir, "Ephemeral over config", "-d", "x", "-t", "task", "--ephemeral")
		if !yielded.Ephemeral {
			t.Errorf("--ephemeral over config default should set the ephemeral flag")
		}
		if cell := storageClassCell(t, db, "wisps", yielded.ID); cell.Valid {
			t.Errorf("config-derived unversioned leaked to the wisp row: %q", cell.String)
		}
	})

	// buildMarkdownBatchRequest is ONE projection shared by both transports, so
	// this is the proxied half of the same fix the embedded suite covers.
	t.Run("markdown_batch", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pm")
		db := openProxiedDB(t, p)
		writeBatch := func(t *testing.T, name, body string) string {
			t.Helper()
			path := filepath.Join(p.dir, name)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write markdown plan: %v", err)
			}
			return path
		}

		unversioned := writeBatch(t, "unversioned.md", `## Proxied batch one

### Description
First.

## Proxied batch two

### Description
Second.
`)
		if out, err := bdProxiedRun(t, bd, p.dir, "create", "--file", unversioned, "--storage-class", "unversioned"); err != nil {
			t.Fatalf("create --file: %v\n%s", err, out)
		}
		for _, title := range []string{"Proxied batch one", "Proxied batch two"} {
			id := idByTitle(t, db, "issues", title)
			if id == "" {
				t.Fatalf("%s was not created", title)
			}
			if cell := storageClassCell(t, db, "issues", id); !cell.Valid || cell.String != "unversioned" {
				t.Errorf("%s (%s): DB cell = %+v, want 'unversioned'", title, id, cell)
			}
		}

		ephemeral := writeBatch(t, "ephemeral.md", `## Proxied batch wisp

### Description
Third.
`)
		if out, err := bdProxiedRun(t, bd, p.dir, "create", "--file", ephemeral, "--storage-class", "ephemeral"); err != nil {
			t.Fatalf("create --file --storage-class ephemeral: %v\n%s", err, out)
		}
		if id := idByTitle(t, db, "wisps", "Proxied batch wisp"); id == "" {
			t.Error("--file --storage-class ephemeral should land its rows in wisps")
		}

		conflicting := writeBatch(t, "conflict.md", `## Proxied batch refused

### Description
Fourth.
`)
		out, err := bdProxiedRun(t, bd, p.dir, "create", "--file", conflicting, "--ephemeral", "--storage-class", "versioned")
		if err == nil {
			t.Fatalf("durable class with --ephemeral should be refused, got:\n%s", out)
		}
		if !strings.Contains(string(out), "conflicts with --ephemeral/--no-history") {
			t.Errorf("expected the wisp-plane conflict error, got:\n%s", out)
		}
		if id := idByTitle(t, db, "issues", "Proxied batch refused"); id != "" {
			t.Errorf("a refused --file left %s behind", id)
		}
	})

	t.Run("markdown_batch_per_type_config_default", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pb")
		setStorageClassConfig(t, p, "storage-class.task", "unversioned")
		db := openProxiedDB(t, p)

		path := filepath.Join(p.dir, "defaults.md")
		if err := os.WriteFile(path, []byte(`## Proxied default task

### Description
Task body.

## Proxied default bug

### Type
bug

### Description
Bug body.
`), 0o600); err != nil {
			t.Fatalf("write markdown plan: %v", err)
		}
		if out, err := bdProxiedRun(t, bd, p.dir, "create", "--file", path); err != nil {
			t.Fatalf("create --file: %v\n%s", err, out)
		}
		taskID := idByTitle(t, db, "issues", "Proxied default task")
		if cell := storageClassCell(t, db, "issues", taskID); !cell.Valid || cell.String != "unversioned" {
			t.Errorf("task template: DB cell = %+v, want the storage-class.task default", cell)
		}
		bugID := idByTitle(t, db, "issues", "Proxied default bug")
		if cell := storageClassCell(t, db, "issues", bugID); cell.Valid {
			t.Errorf("bug template: DB cell = %q, want NULL (the task default must not spill)", cell.String)
		}
	})

	t.Run("graph_rejects_plan_wide_storage_class", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pg")
		path := filepath.Join(p.dir, "plan.json")
		if err := os.WriteFile(path, []byte(`{"nodes":[{"key":"a","title":"Proxied graph node","description":"n"}]}`), 0o600); err != nil {
			t.Fatalf("write graph plan: %v", err)
		}
		out := bdProxiedCreateFail(t, bd, p.dir, "--graph", path, "--storage-class", "unversioned")
		if !strings.Contains(out, "set storage_class per node in the plan instead") {
			t.Errorf("error should point at the per-node field, got:\n%s", out)
		}
		// The per-node field is the mechanism that works, on this transport too.
		perNode := filepath.Join(p.dir, "per-node.json")
		if err := os.WriteFile(perNode, []byte(`{"nodes":[{"key":"a","title":"Proxied per-node","description":"n","storage_class":"unversioned"}]}`), 0o600); err != nil {
			t.Fatalf("write graph plan: %v", err)
		}
		if out, err := bdProxiedRun(t, bd, p.dir, "create", "--graph", perNode); err != nil {
			t.Fatalf("create --graph with a per-node class: %v\n%s", err, out)
		}
		db := openProxiedDB(t, p)
		id := idByTitle(t, db, "issues", "Proxied per-node")
		if cell := storageClassCell(t, db, "issues", id); !cell.Valid || cell.String != "unversioned" {
			t.Errorf("per-node class: DB cell = %+v, want 'unversioned'", cell)
		}
	})

	// The dry-run preview builds the same issue the real create does, so it
	// inherits the fix — and a preview that showed a class the create would not
	// write would be worse than none.
	t.Run("dry_run_preview_shows_the_resolved_class", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pd")
		setStorageClassConfig(t, p, "storage-class.bug", "unversioned")

		preview := bdProxiedCreate(t, bd, p.dir, "Previewed bead", "-d", "x", "--dry-run", "--storage-class", "unversioned")
		if preview.StorageClass != types.StorageClassUnversioned {
			t.Errorf("dry-run preview storage_class = %q, want unversioned", preview.StorageClass)
		}
		fromConfig := bdProxiedCreate(t, bd, p.dir, "Previewed default", "-d", "x", "-t", "bug", "--dry-run")
		if fromConfig.StorageClass != types.StorageClassUnversioned {
			t.Errorf("dry-run preview of the config default = %q, want unversioned", fromConfig.StorageClass)
		}
		wisp := bdProxiedCreate(t, bd, p.dir, "Previewed wisp", "-d", "x", "--dry-run", "--storage-class", "ephemeral")
		if !wisp.Ephemeral || wisp.StorageClass != "" {
			t.Errorf("dry-run preview of the ephemeral spelling: ephemeral=%v class=%q", wisp.Ephemeral, wisp.StorageClass)
		}
	})
}
