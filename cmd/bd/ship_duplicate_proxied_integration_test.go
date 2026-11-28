//go:build cgo && unix

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Keep the process/proxy/commit boundary real: previews and refusals must not
// publish, and duplicate and supersede must add their edge and close the issue
// in one commit, with the embedded route's unchecked close. It runs in
// //cmd/bd:bd_managed_local_test (managed_local_shards.txt).
func TestManagedLocalProxiedShipDuplicateSupersede(t *testing.T) {
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
		if err == nil || !strings.Contains(out+stderr, "strict readonly is unavailable") {
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
		// The embedded route closes through store.CloseIssue, which checks no
		// assignee, pin, blocker or children; the proxied route must match it.
		assigned := bdProxiedCreate(t, bd, p.dir, "assigned report", "--assignee", "bob")
		before = history(t)
		if got := call(t, "--actor", "agent", "duplicate", assigned.ID, "--of", canonical.ID); got["status"] != "closed" {
			t.Fatalf("assigned duplicate: %v", got)
		}
		assertProxiedDepExistsWithType(t, db, assigned.ID, canonical.ID, "duplicates")
		if count(t, "SELECT COUNT(*) FROM issues WHERE id=? AND status='closed'", assigned.ID) != 1 || history(t) != before+1 {
			t.Fatal("assigned duplicate did not close in one commit")
		}
		open := bdProxiedCreate(t, bd, p.dir, "open report")
		readonly(t, "duplicate", open.ID, "--of", canonical.ID)
	})

	t.Run("supersede", func(t *testing.T) {
		old := bdProxiedCreate(t, bd, p.dir, "old design", "--id", "caproute-supersede-old")
		replacement := bdProxiedCreate(t, bd, p.dir, "new design", "--id", "caproute-supersede-new")
		before := history(t)
		refuse(t, "itself", "supersede", old.ID, "--with", old.ID)
		refuse(t, "failed to resolve", "supersede", old.ID, "--with", "caproute-absent")
		if history(t) != before {
			t.Fatal("invalid supersede wrote history")
		}
		got := call(t, "supersede", old.ID[:len(old.ID)-1], "--with", replacement.ID[:len(replacement.ID)-1])
		if got["superseded"] != old.ID || got["replacement"] != replacement.ID || got["status"] != "closed" {
			t.Fatalf("supersede result: %v", got)
		}
		assertProxiedDepExistsWithType(t, db, old.ID, replacement.ID, "supersedes")
		if count(t, "SELECT COUNT(*) FROM issues WHERE id=? AND status='closed' AND closed_at IS NOT NULL", old.ID) != 1 {
			t.Fatal("supersede did not persist complete closure")
		}
		if history(t) != before+1 {
			t.Fatal("supersede must publish one atomic commit")
		}
		assigned := bdProxiedCreate(t, bd, p.dir, "assigned design", "--assignee", "bob")
		before = history(t)
		if got := call(t, "--actor", "agent", "supersede", assigned.ID, "--with", replacement.ID); got["status"] != "closed" {
			t.Fatalf("assigned supersede: %v", got)
		}
		if count(t, "SELECT COUNT(*) FROM issues WHERE id=? AND status='closed'", assigned.ID) != 1 || history(t) != before+1 {
			t.Fatal("assigned supersede did not close in one commit")
		}
		open := bdProxiedCreate(t, bd, p.dir, "open design")
		readonly(t, "supersede", open.ID, "--with", replacement.ID)
	})
}
