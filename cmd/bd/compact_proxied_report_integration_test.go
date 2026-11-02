//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// bd compact --json on a proxied server reports the head it planned from, the
// head it left on main, and the tables whose data differs between the two. A
// quiet compaction changes no data, so the list is present and empty.
func TestProxiedServerCompactReportsPlannedHead(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "cph")
	for i := 1; i <= 3; i++ {
		bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Compact report %d", i), "--type", "task")
	}

	dry, err := bdProxiedRun(t, bd, p.dir, "compact", "--days", "0", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("compact --dry-run --json: %v\n%s", err, dry)
	}
	var preview struct {
		PlannedHead string `json:"planned_head"`
	}
	if err := json.Unmarshal(dry, &preview); err != nil {
		t.Fatalf("unmarshal dry run: %v\n%s", err, dry)
	}
	if preview.PlannedHead == "" {
		t.Fatalf("dry run reports no planned_head:\n%s", dry)
	}

	out, err := bdProxiedRun(t, bd, p.dir, "compact", "--days", "0", "--force", "--json")
	if err != nil {
		t.Fatalf("compact --force --json: %v\n%s", err, out)
	}
	var res struct {
		Success       bool      `json:"success"`
		PlannedHead   string    `json:"planned_head"`
		CompactedHead string    `json:"compacted_head"`
		Changed       *[]string `json:"tables_changed_by_compact"`
		ChangedError  string    `json:"tables_changed_error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal compact result: %v\n%s", err, out)
	}
	if !res.Success {
		t.Fatalf("compact did not report success:\n%s", out)
	}
	if res.PlannedHead != preview.PlannedHead {
		t.Fatalf("planned_head = %q, want the dry run's %q (no write in between)", res.PlannedHead, preview.PlannedHead)
	}
	if res.CompactedHead == "" || res.CompactedHead == res.PlannedHead {
		t.Fatalf("compacted_head = %q, want a new head distinct from planned_head %q:\n%s", res.CompactedHead, res.PlannedHead, out)
	}
	if res.ChangedError != "" {
		t.Fatalf("tables_changed_error = %q", res.ChangedError)
	}
	if res.Changed == nil || len(*res.Changed) != 0 {
		t.Fatalf("tables_changed_by_compact = %v, want a present, empty list:\n%s", res.Changed, out)
	}
}
