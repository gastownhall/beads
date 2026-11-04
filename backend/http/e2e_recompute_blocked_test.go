//go:build cgo

package bdhttp_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestE2E_RecomputeBlockedOverHTTP is S25. On a remote backend
// `bd recompute-blocked` is a no-op that exits 0: the server maintains
// is_blocked on every write and the column lives in its database. --json still
// carries rows_corrected (gc parses it) and says the server maintains the flag.
func TestE2E_RecomputeBlockedOverHTTP(t *testing.T) {
	bd := connectE2EWorkspace(t, "worker")

	// A blocked edge exists, so there is state the server derived; the client
	// must not claim to have repaired or refused it.
	blocked := createE2EIssue(t, bd, "blocked")
	blocker := createE2EIssue(t, bd, "blocker")
	mustRun(t, bd, "dep", "add", blocked, blocker, "--json")

	r := mustRun(t, bd, "recompute-blocked", "--json")
	var body struct {
		RowsCorrected *int   `json:"rows_corrected"`
		MaintainedBy  string `json:"maintained_by"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.stdout)), &body); err != nil {
		t.Fatalf("parse recompute-blocked --json: %v\n%s", err, r.stdout)
	}
	if body.RowsCorrected == nil || *body.RowsCorrected != 0 || body.MaintainedBy != "server" {
		t.Fatalf("recompute-blocked --json = %s, want rows_corrected 0 and maintained_by server", r.stdout)
	}

	text := mustRun(t, bd, "recompute-blocked")
	if !strings.Contains(text.stdout, "maintained by the bd serve backend") {
		t.Errorf("recompute-blocked text does not say the server maintains the flag: %s", text.stdout)
	}

	// The ready set still reflects the edge: nothing was rewritten.
	ready := jsonIDSet(t, mustRun(t, bd, "ready", "--json").stdout)
	if ready[blocked] || !ready[blocker] {
		t.Errorf("bd ready after recompute-blocked = %v, want %s ready and %s blocked", ready, blocker, blocked)
	}
}
