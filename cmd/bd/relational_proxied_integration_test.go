//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProxiedServerLink(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("link_default_blocks", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "lk")
		a := bdProxiedCreate(t, bd, p.dir, "Link from")
		b := bdProxiedCreate(t, bd, p.dir, "Link to")

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "link", a.ID, b.ID)
		if err != nil {
			t.Fatalf("link failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, "Linked") {
			t.Errorf("expected 'Linked' output, got:\n%s", out)
		}

		db := openProxiedDB(t, p)
		assertProxiedDepExists(t, db, a.ID, b.ID)
	})

	t.Run("link_with_type", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "lt")
		a := bdProxiedCreate(t, bd, p.dir, "Rel from")
		b := bdProxiedCreate(t, bd, p.dir, "Rel to")

		if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "link", a.ID, b.ID, "--type", "related"); err != nil {
			t.Fatalf("link --type related failed: %v\nstderr:\n%s", err, stderr)
		}

		db := openProxiedDB(t, p)
		assertProxiedDepExistsWithType(t, db, a.ID, b.ID, "related")
	})

	t.Run("link_alias_canonicalizes_to_blocks", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "lb")
		a := bdProxiedCreate(t, bd, p.dir, "Alias from")

		// GH#5585: the documented aliases must store the canonical blocks
		// edge (the one ready/blocked gating matches), not the literal
		// spelling, and the confirmation must report what was stored.
		for _, alias := range []string{"blocked-by", "depends-on"} {
			target := bdProxiedCreate(t, bd, p.dir, "Alias target "+alias)
			out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "link", a.ID, target.ID, "--type", alias)
			if err != nil {
				t.Fatalf("link --type %s failed: %v\nstderr:\n%s", alias, err, stderr)
			}
			if !strings.Contains(out, "(blocks)") {
				t.Errorf("link --type %s reported the raw alias; want the canonical (blocks):\n%s", alias, out)
			}

			db := openProxiedDB(t, p)
			assertProxiedDepExistsWithType(t, db, a.ID, target.ID, "blocks")

			ready, _, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--json")
			if err != nil {
				t.Fatalf("ready --json: %v", err)
			}
			if proxiedIDSet(t, ready)[a.ID] {
				t.Errorf("link --type %s left %s ready; the canonical blocks edge must gate it", alias, a.ID)
			}
			blocked, _, err := bdProxiedRunBuffers(t, bd, p.dir, "blocked", "--json")
			if err != nil {
				t.Fatalf("blocked --json: %v", err)
			}
			if !proxiedIDSet(t, blocked)[a.ID] {
				t.Errorf("link --type %s did not list %s as blocked: %s", alias, a.ID, blocked)
			}
		}
	})

	t.Run("link_invalid_type_rejected", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "li")
		a := bdProxiedCreate(t, bd, p.dir, "Bad from")
		b := bdProxiedCreate(t, bd, p.dir, "Bad to")
		_, out, err := bdProxiedRunBuffers(t, bd, p.dir, "link", a.ID, b.ID, "--type", "")
		if err == nil {
			t.Errorf("expected invalid type to fail, got:\n%s", out)
		}
	})
}

func TestProxiedServerChildren(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	p := newSharedProxiedProject(t, bd, "ch")
	parent := bdProxiedCreate(t, bd, p.dir, "Parent", "--type", "epic")
	c1 := bdProxiedCreate(t, bd, p.dir, "Child 1", "--parent", parent.ID)
	c2 := bdProxiedCreate(t, bd, p.dir, "Child 2", "--parent", parent.ID)

	out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "children", parent.ID)
	if err != nil {
		t.Fatalf("children failed: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(out, c1.ID) || !strings.Contains(out, c2.ID) {
		t.Errorf("expected both children in output, got:\n%s", out)
	}
}

func TestProxiedServerGateList(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("db_wide_lists_gates", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ga")
		gate := bdProxiedCreate(t, bd, p.dir, "A gate", "--type", "gate")
		bdProxiedCreate(t, bd, p.dir, "Not a gate", "--type", "task")

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "list")
		if err != nil {
			t.Fatalf("gate list failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, gate.ID) {
			t.Errorf("expected gate %s in output, got:\n%s", gate.ID, out)
		}
	})

	t.Run("bead_scoped_lists_blocking_gates", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gb")
		work := bdProxiedCreate(t, bd, p.dir, "Blocked work")
		gate := bdProxiedCreate(t, bd, p.dir, "Blocking gate", "--type", "gate")
		unrelated := bdProxiedCreate(t, bd, p.dir, "Unrelated gate", "--type", "gate")

		if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "link", work.ID, gate.ID); err != nil {
			t.Fatalf("link work->gate failed: %v\nstderr:\n%s", err, stderr)
		}

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "list", work.ID)
		if err != nil {
			t.Fatalf("gate list <id> failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, gate.ID) {
			t.Errorf("expected blocking gate %s in bead-scoped output, got:\n%s", gate.ID, out)
		}
		if strings.Contains(out, unrelated.ID) {
			t.Errorf("bead-scoped list must not include unrelated gate %s:\n%s", unrelated.ID, out)
		}
	})
}

// proxiedIDSet parses a JSON listing of issues (`bd ready --json`, `bd
// blocked --json`, ...) into a set of issue ids: a bare array, or the object
// envelope whose "issues"/"data"/"items" member holds the rows.
func proxiedIDSet(t *testing.T, out string) map[string]bool {
	t.Helper()
	trimmed := []byte(strings.TrimSpace(out))
	set := make(map[string]bool)
	collect := func(rows []map[string]any) {
		for _, row := range rows {
			if id, ok := row["id"].(string); ok {
				set[id] = true
			}
		}
	}
	var rows []map[string]any
	if err := json.Unmarshal(trimmed, &rows); err == nil {
		collect(rows)
		return set
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		t.Fatalf("parse JSON issue listing: %v\n%s", err, out)
	}
	for _, key := range []string{"issues", "data", "items"} {
		if raw, ok := envelope[key]; ok {
			if err := json.Unmarshal(raw, &rows); err != nil {
				t.Fatalf("parse JSON listing member %q: %v\n%s", key, err, out)
			}
			collect(rows)
			return set
		}
	}
	t.Fatalf("JSON issue listing has no row array: %s", out)
	return set
}
