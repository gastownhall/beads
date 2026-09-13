//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/migration"
)

// TestEmbeddedWorkspaceMigrationFreezeRefusesWrites drives the freeze end to
// end in a workspace that is NOT a Gas Town: no mayor/town.json anywhere above
// it, so findTownRoot returns "" and, before this change, migration.IsFrozen("")
// was false and the sentinel was inert no matter where it was placed.
//
// The A/B on one file is the whole test: the same command must be refused with
// the sentinel present and succeed with it gone. Asserting only the refusal
// would pass against a bd that refused `create` for any reason at all.
func TestEmbeddedWorkspaceMigrationFreezeRefusesWrites(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "fz")

	sentinel := filepath.Join(dir, ".beads", migration.FileName)
	if err := os.WriteFile(sentinel, []byte("kb\t2026-09-03T10:00:00Z\tschema bump"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	out, code := bdRunFailCode(t, bd, dir, "create", "blocked by the freeze", "--type", "task")
	if code != 1 {
		t.Errorf("bd create under a freeze exited %d, want 1", code)
	}
	// The operator and reason come out of the file, so a refusal that printed a
	// generic message would still fail here — the parse has to reach the caller.
	for _, want := range []string{"frozen for migration", "kb", "schema bump"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal must mention %q; got:\n%s", want, out)
		}
	}
	// Outside a town the remedy is the file, not `gt migrate thaw` — naming a
	// binary the user does not have is the failure mode this replaces.
	if !strings.Contains(out, migration.FileName) {
		t.Errorf("refusal must name the sentinel to remove; got:\n%s", out)
	}
	if strings.Contains(out, "gt migrate thaw") {
		t.Errorf("outside a town the remedy must not be a gt command; got:\n%s", out)
	}

	// Thaw: the same command must now work. Without this half, a bd that
	// refused every create would pass everything above.
	if err := os.Remove(sentinel); err != nil {
		t.Fatalf("remove sentinel: %v", err)
	}
	if issue := bdCreate(t, bd, dir, "allowed after thaw", "--type", "task"); issue == nil || issue.ID == "" {
		t.Fatal("bd create must succeed once the sentinel is removed")
	}
}
