//go:build cgo

package bdhttp_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestE2E_LinkOverHTTP runs `bd link` through the real `bd` binary against an
// in-process `bd serve`.
//
// bd link writes through the DependencyEditor role (one addDependencies
// request), so it is served over http for any type a workspace spells, well-
// known or not: the server takes any 1-32 character type. Its own checks —
// a non-empty type of at most 32 characters — refuse before anything is
// written, and an edge that already exists with another type reports the
// typed conflict.
func TestE2E_LinkOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)
	proxyAddr, counter := startCountingProxy(t, addr)
	f := newHTTPFixture(t, bin, proxyAddr)

	t.Run("custom types are served", func(t *testing.T) {
		for _, depType := range []string{"owned-by", "ships-in", "available-in", "excluded-from", "advances", "launches", "blocks"} {
			from := f.create("link source " + depType)
			to := f.create("link target " + depType)

			counter.reset()
			r := f.mustRun(nil, "link", from, to, "--type", depType, "--json")
			var out map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(r.stdout)), &out); err != nil {
				t.Fatalf("bd link --json: %v\n%s", err, r.stdout)
			}
			if out["status"] != "added" || out["issue_id"] != from || out["depends_on_id"] != to || out["type"] != depType {
				t.Errorf("bd link --type %s --json = %v, want added %s -> %s", depType, out, from, to)
			}
			adds := 0
			for _, p := range counter.snapshot() {
				if p == "POST /v0/beads/dependencies:add" {
					adds++
				}
			}
			if adds != 1 {
				t.Errorf("bd link --type %s sent %d addDependencies requests, want exactly one: %v", depType, adds, counter.snapshot())
			}

			list := f.mustRun(nil, "dep", "list", from, "--json").stdout
			if !depListHas(t, list, to, depType) {
				t.Errorf("bd dep list %s does not show the %s edge to %s: %s", from, depType, to, list)
			}
		}
	})

	t.Run("text output names both issues", func(t *testing.T) {
		from := f.create("text link source")
		to := f.create("text link target")
		r := f.mustRun(nil, "link", from, to, "--type", "owned-by")
		if !strings.Contains(r.stdout, "Linked:") || !strings.Contains(r.stdout, "text link source") ||
			!strings.Contains(r.stdout, "text link target") || !strings.Contains(r.stdout, "(owned-by)") {
			t.Errorf("bd link output = %q, want the Linked line with both titles and the type", r.stdout)
		}
	})

	t.Run("invalid types refuse before any write", func(t *testing.T) {
		from := f.create("invalid link source")
		to := f.create("invalid link target")
		for _, depType := range []string{"", strings.Repeat("x", 33)} {
			counter.reset()
			r := f.run(nil, "link", from, to, "--type", depType)
			if r.code == 0 {
				t.Fatalf("bd link --type %q exited 0: %s", depType, r.stdout)
			}
			if !strings.Contains(r.stderr, "invalid dependency type") {
				t.Errorf("bd link --type %q stderr = %s, want the invalid-type refusal", depType, r.stderr)
			}
			for _, p := range counter.snapshot() {
				if !strings.HasPrefix(p, "GET ") {
					t.Errorf("bd link --type %q wrote %s; the refusal must come before any write", depType, p)
				}
			}
		}
		if list := f.mustRun(nil, "dep", "list", from, "--json").stdout; depListHas(t, list, to, "") {
			t.Errorf("a refused link left an edge %s -> %s: %s", from, to, list)
		}
	})

	t.Run("an edge of another type is a typed conflict", func(t *testing.T) {
		from := f.create("conflict source")
		to := f.create("conflict target")
		f.mustRun(nil, "link", from, to, "--type", "owned-by")
		r := f.run(nil, "link", from, to, "--type", "ships-in")
		if r.code == 0 {
			t.Fatalf("bd link over an existing owned-by edge with ships-in exited 0: %s", r.stdout)
		}
		// The server answers the role's typed conflict as 409
		// dependency_exists, which the client surfaces verbatim.
		if !strings.Contains(r.stderr, "dependency_exists") || !strings.Contains(r.stderr, "different type") {
			t.Errorf("conflict stderr = %s, want the typed dependency_exists conflict", r.stderr)
		}
		list := f.mustRun(nil, "dep", "list", from, "--json").stdout
		if !depListHas(t, list, to, "owned-by") || depListHas(t, list, to, "ships-in") {
			t.Errorf("the conflicting link changed the edge: %s", list)
		}
		// Re-asserting the same edge is idempotent.
		f.mustRun(nil, "link", from, to, "--type", "owned-by")
	})
}

// depListHas reports whether `bd dep list --json` lists an edge to target of
// depType (any type when depType is empty).
func depListHas(t *testing.T, out, target, depType string) bool {
	t.Helper()
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "null" {
		return false
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		t.Fatalf("parse bd dep list --json: %v\n%s", err, out)
	}
	for _, row := range rows {
		if row["id"] != target && row["depends_on_id"] != target {
			continue
		}
		if depType == "" {
			return true
		}
		if row["dependency_type"] == depType || row["type"] == depType {
			return true
		}
	}
	return false
}
