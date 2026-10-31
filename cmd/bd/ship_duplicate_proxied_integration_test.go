//go:build cgo && unix

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Keep the process/proxy/commit boundary real: previews must not publish and a
// rejected duplicate closure must roll its new dependency back.
func TestProxiedShipAndDuplicate(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "caproute", 5*time.Minute)
	db := openProxiedDB(t, p)
	call := func(t *testing.T, args ...string) map[string]any {
		t.Helper()
		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, append(args, "--json")...)
		if err != nil {
			t.Fatalf("%v: %v\n%s%s", args, err, out, stderr)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("%v JSON: %v: %s%s", args, err, out, stderr)
		}
		return result
	}
	refuse := func(t *testing.T, want string, args ...string) {
		t.Helper()
		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, append(args, "--json")...)
		if err == nil || !strings.Contains(out+stderr, want) {
			t.Fatalf("%v: want refusal %q, got %v: %s%s", args, want, err, out, stderr)
		}
	}
	count := func(t *testing.T, query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	history := func(t *testing.T) int { return count(t, "SELECT COUNT(*) FROM dolt_log") }
	readonly := func(t *testing.T, args ...string) {
		t.Helper()
		before := history(t)
		out, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"BD_READONLY=1"}, args...)
		if err == nil || !strings.Contains(out+stderr, "read-only") {
			t.Fatalf("readonly accepted %v: %v %s%s", args, err, out, stderr)
		}
		if history(t) != before {
			t.Fatal("readonly refusal published history")
		}
	}

	t.Run("ship", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "API capability", "--labels", "export:api")
		before := history(t)
		refuse(t, "no issue found", "ship", "missing")
		refuse(t, "is not closed", "ship", "api")
		preview := call(t, "ship", "api", "--force", "--dry-run")
		if preview["status"] != "dry_run" || preview["issue_id"] != issue.ID || preview["would_add"] != "provides:api" {
			t.Fatalf("forced preview: %v", preview)
		}
		if history(t) != before || count(t, "SELECT COUNT(*) FROM labels WHERE label='provides:api'") != 0 {
			t.Fatal("preview/refusal wrote capability or history")
		}
		if _, err := bdProxiedRun(t, bd, p.dir, "close", issue.ID); err != nil {
			t.Fatal(err)
		}
		before = history(t)
		call(t, "ship", "api", "--dry-run")
		if history(t) != before {
			t.Fatal("closed preview published history")
		}
		shipped := call(t, "ship", "api")
		if shipped["status"] != "shipped" || shipped["label"] != "provides:api" || shipped["issue_id"] != issue.ID {
			t.Fatalf("ship result: %v", shipped)
		}
		if count(t, "SELECT COUNT(*) FROM labels WHERE issue_id=? AND label='provides:api'", issue.ID) != 1 {
			t.Fatal("ship did not persist label")
		}
		before = history(t)
		if got := call(t, "ship", "api"); got["status"] != "already_shipped" {
			t.Fatalf("repeat ship: %v", got)
		}
		if history(t) != before {
			t.Fatal("already shipped minted history")
		}
		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "ship", "api")
		if err != nil || !strings.Contains(out, "already shipped") || stderr != "" {
			t.Fatalf("text output: %v %s%s", err, out, stderr)
		}
		forced := bdProxiedCreate(t, bd, p.dir, "forced capability", "--labels", "export:forced")
		call(t, "ship", "forced", "--force")
		if count(t, "SELECT COUNT(*) FROM issues WHERE id=? AND status='open'", forced.ID) != 1 {
			t.Fatal("forced ship changed lifecycle")
		}
		bdProxiedCreate(t, bd, p.dir, "ambiguous capability", "--labels", "export:api")
		before = history(t)
		refuse(t, "only one issue", "ship", "api", "--force")
		if history(t) != before {
			t.Fatal("ambiguous ship wrote history")
		}
		readonly(t, "ship", "forced", "--force")
	})

	t.Run("duplicate", func(t *testing.T) {
		original := bdProxiedCreate(t, bd, p.dir, "duplicate report", "--id", "caproute-duplicate-source")
		canonical := bdProxiedCreate(t, bd, p.dir, "canonical report", "--id", "caproute-canonical")
		before := history(t)
		refuse(t, "itself", "duplicate", original.ID, "--of", original.ID)
		refuse(t, "failed to resolve", "duplicate", original.ID, "--of", "caproute-absent")
		if history(t) != before {
			t.Fatal("invalid duplicate wrote history")
		}
		got := call(t, "duplicate", original.ID[:len(original.ID)-1], "--of", canonical.ID[:len(canonical.ID)-1])
		if got["duplicate"] != original.ID || got["canonical"] != canonical.ID || got["status"] != "closed" {
			t.Fatalf("duplicate result: %v", got)
		}
		assertProxiedDepExistsWithType(t, db, original.ID, canonical.ID, "duplicates")
		if count(t, "SELECT COUNT(*) FROM issues WHERE id=? AND status='closed' AND closed_at IS NOT NULL", original.ID) != 1 {
			t.Fatal("duplicate did not persist complete closure")
		}
		if history(t) != before+1 {
			t.Fatal("duplicate must publish one atomic commit")
		}
		protected := bdProxiedCreate(t, bd, p.dir, "protected task")
		gate := call(t, "gate", "create", "--blocks", protected.ID, "--type", "human")
		if _, err := bdProxiedRun(t, bd, p.dir, "config", "set", "gates.human.resolvers", `["owner"]`); err != nil {
			t.Fatal(err)
		}
		before = history(t)
		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "--actor", "agent", "duplicate", gate["id"].(string), "--of", canonical.ID)
		if err == nil {
			t.Fatalf("protected closure accepted: %s%s", out, stderr)
		}
		if count(t, "SELECT COUNT(*) FROM dependencies WHERE issue_id=? AND type='duplicates'", gate["id"]) != 0 ||
			count(t, "SELECT COUNT(*) FROM issues WHERE id=? AND status='closed'", gate["id"]) != 0 || history(t) != before {
			t.Fatal("refused duplicate leaked an edge, closure or history")
		}
		readonly(t, "duplicate", protected.ID, "--of", canonical.ID)
	})
}
