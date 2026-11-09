//go:build cgo

// bd-fnvnuq: gate parity for test pollution (GH#5025). The "Test Pollution"
// validation gate and `bd doctor --check=pollution` must agree because they
// share one scorer (cmd/bd/doctor/pollution.go). The test seeds a database
// whose eligible hits, substantive work and ineligible rows (closed, epic,
// ephemeral) all look like pollution to a title-pattern query, then compares
// the detailed command's JSON count against the gate's count.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

func TestDoctorPollution_ValidateGateMatchesScorer(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	tmpDir := t.TempDir()
	beads := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beads, 0755); err != nil {
		t.Fatal(err)
	}
	store := newTestStoreIsolatedDB(t, filepath.Join(beads, "dolt"), "dpgt")
	now := time.Now()

	// The two hits the shared scorer must report: a generic-titled fixture
	// (1.4 high) and a mixed-case prefixed fixture (0.8 medium).
	seed := []*types.Issue{
		{ID: "dpgt-hit1", Title: "Test issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "dpgt-hit2", Title: "Test-fixture data load", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		// Substantive prefixed work: reported by a title-only query, never by
		// the scorer.
		{ID: "dpgt-substantive", Title: "test-runner hangs on CI", Description: "The test runner deadlocks when two suites contend on the lock.", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		// Ineligible rows with strong fixture titles.
		{ID: "dpgt-closed", Title: "test-closed leftover", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask, ClosedAt: &now},
		{ID: "dpgt-epic", Title: "test-quality architecture", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic},
		{ID: "dpgt-eph", Title: "debug-ephemeral leftover", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask, Ephemeral: true},
	}
	for _, issue := range seed {
		if err := store.CreateIssue(context.Background(), issue, "test"); err != nil {
			t.Fatalf("seeding issue %s: %v", issue.ID, err)
		}
	}

	// The detailed command's count, from the real bd binary's JSON output.
	bdBin := buildBDForInitTests(t)
	out, code := bdRunRaw(t, bdBin, tmpDir, doctorMaxRowsEnv(), "doctor", "--check=pollution", "--json")
	if code != 0 {
		t.Fatalf("bd doctor --check=pollution --json exited %d:\n%s", code, out)
	}
	var detailed struct {
		PollutedCount int `json:"polluted_count"`
		Issues        []struct {
			ID string `json:"id"`
		} `json:"issues"`
	}
	// The subprocess emits permission/config warnings before the JSON object,
	// so parse from the first line that opens the object.
	jsonStart := strings.Index(out, "{\n")
	if jsonStart < 0 {
		t.Fatalf("no JSON object in pollution output:\n%s", out)
	}
	if err := json.Unmarshal([]byte(out[jsonStart:]), &detailed); err != nil {
		t.Fatalf("parsing pollution JSON: %v\n%s", err, out)
	}

	// The validation gate's count, in-process on the same database.
	gate := doctor.CheckTestPollution(tmpDir)

	if detailed.PollutedCount != 2 {
		t.Errorf("detailed pollution count = %d, want 2 (shared scorer: eligible hits only)", detailed.PollutedCount)
	}
	reported := map[string]bool{}
	for _, issue := range detailed.Issues {
		reported[issue.ID] = true
	}
	for _, id := range []string{"dpgt-hit1", "dpgt-hit2"} {
		if !reported[id] {
			t.Errorf("detailed pollution output missing eligible hit %s:\n%s", id, out)
		}
	}
	for _, id := range []string{"dpgt-substantive", "dpgt-closed", "dpgt-epic", "dpgt-eph"} {
		if reported[id] {
			t.Errorf("detailed pollution output included ineligible or substantive row %s:\n%s", id, out)
		}
	}

	if gate.Status != statusWarning {
		t.Errorf("gate status = %q, want %q", gate.Status, statusWarning)
	}
	if !strings.Contains(gate.Message, "2 potential test issue(s)") {
		t.Errorf("gate message = %q, want it to count the same 2 hits as the detailed check", gate.Message)
	}
	if gate.Message == "N/A (query failed)" {
		t.Errorf("gate could not read the seeded database: %q", gate.Message)
	}
}
