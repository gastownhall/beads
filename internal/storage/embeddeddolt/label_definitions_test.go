//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestListLabelDefinitionsPreVocabularySchema pins the pre-0070 read path:
// a database whose schema predates the label_definitions table (a read-only
// open skips migrations, and an embedded open can stay on the previous
// schema) has, by construction, zero definitions. Listing must report that
// empty registry, not fail - bd export reads it unconditionally.
func TestListLabelDefinitionsPreVocabularySchema(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "ldpre")
	ctx := t.Context()
	te.exec(t, ctx, "DROP TABLE label_definitions")

	defs, err := te.store.ListLabelDefinitions(ctx)
	if err != nil {
		t.Fatalf("ListLabelDefinitions on a schema without label_definitions: %v", err)
	}
	if len(defs) != 0 {
		t.Fatalf("expected no definitions, got %v", defs)
	}
}

// TestRenameLabelMovesDefinition pins that bd label rename carries the
// vocabulary registry along with the issue labels, in the same transaction,
// so the registry never keeps a definition for a label no issue can carry
// any more while the renamed label goes undefined.
func TestRenameLabelMovesDefinition(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	definitions := func(t *testing.T, te *testEnv) map[string]string {
		t.Helper()
		defs, err := te.store.ListLabelDefinitions(t.Context())
		if err != nil {
			t.Fatalf("ListLabelDefinitions: %v", err)
		}
		out := make(map[string]string, len(defs))
		for _, d := range defs {
			desc := ""
			if d.Description != nil {
				desc = *d.Description
			}
			out[d.Label] = desc
		}
		return out
	}
	folded := func(t *testing.T, te *testEnv, label string) string {
		t.Helper()
		var f string
		te.queryScalar(t, t.Context(), "SELECT label_folded FROM label_definitions WHERE label = ?", []any{label}, &f)
		return f
	}
	carry := func(t *testing.T, te *testEnv, id, label string) {
		t.Helper()
		ctx := t.Context()
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
		if err := te.store.AddLabel(ctx, id, label, "tester"); err != nil {
			t.Fatalf("AddLabel(%s): %v", id, err)
		}
	}

	t.Run("old defined, new not", func(t *testing.T) {
		te := newTestEnv(t, "ldr")
		ctx := t.Context()
		carry(t, te, "ldr-a", "backend")
		if err := te.store.DefineLabel(ctx, "backend", "server side", "tester"); err != nil {
			t.Fatalf("DefineLabel: %v", err)
		}

		if _, _, _, err := te.store.RenameLabel(ctx, "backend", "Server", "tester"); err != nil {
			t.Fatalf("RenameLabel: %v", err)
		}
		got := definitions(t, te)
		if _, ok := got["backend"]; ok {
			t.Fatalf("stale definition for the old label survived the rename: %v", got)
		}
		if desc, ok := got["Server"]; !ok || desc != "server side" {
			t.Fatalf("definition not renamed with its fields kept: %v", got)
		}
		if f := folded(t, te, "Server"); f != "server" {
			t.Fatalf("label_folded = %q, want %q", f, "server")
		}
	})

	t.Run("both defined merges into new", func(t *testing.T) {
		te := newTestEnv(t, "ldm")
		ctx := t.Context()
		carry(t, te, "ldm-a", "wip")
		if err := te.store.DefineLabel(ctx, "wip", "old meaning", "tester"); err != nil {
			t.Fatalf("DefineLabel(wip): %v", err)
		}
		if err := te.store.DefineLabel(ctx, "in-progress", "new meaning", "tester"); err != nil {
			t.Fatalf("DefineLabel(in-progress): %v", err)
		}

		if _, _, _, err := te.store.RenameLabel(ctx, "wip", "in-progress", "tester"); err != nil {
			t.Fatalf("RenameLabel: %v", err)
		}
		got := definitions(t, te)
		if len(got) != 1 || got["in-progress"] != "new meaning" {
			t.Fatalf("expected only in-progress with its own definition, got %v", got)
		}
	})

	t.Run("case-only rename updates spelling", func(t *testing.T) {
		te := newTestEnv(t, "ldc")
		ctx := t.Context()
		carry(t, te, "ldc-a", "backend")
		if err := te.store.DefineLabel(ctx, "backend", "server side", "tester"); err != nil {
			t.Fatalf("DefineLabel: %v", err)
		}

		if _, _, _, err := te.store.RenameLabel(ctx, "backend", "Backend", "tester"); err != nil {
			t.Fatalf("RenameLabel: %v", err)
		}
		got := definitions(t, te)
		if len(got) != 1 || got["Backend"] != "server side" {
			t.Fatalf("expected the definition respelled to Backend, got %v", got)
		}
	})

	t.Run("definition-only rename", func(t *testing.T) {
		te := newTestEnv(t, "ldo")
		ctx := t.Context()
		if err := te.store.DefineLabel(ctx, "frontend", "ui", "tester"); err != nil {
			t.Fatalf("DefineLabel: %v", err)
		}

		renamed, _, _, err := te.store.RenameLabel(ctx, "frontend", "client", "tester")
		if err != nil {
			t.Fatalf("RenameLabel: %v", err)
		}
		if renamed != 0 {
			t.Fatalf("renamed = %d, want 0 (no issue carries the label)", renamed)
		}
		got := definitions(t, te)
		if len(got) != 1 || got["client"] != "ui" {
			t.Fatalf("expected the definition renamed to client, got %v", got)
		}
	})
}
