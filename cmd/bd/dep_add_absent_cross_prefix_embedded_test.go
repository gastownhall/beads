//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEmbeddedDepAddAbsentCrossPrefixTargetStaysExternal pins GH#8299's
// retention half: a bare cross-prefix target that no route resolved is still
// stored under depends_on_external — with its unverified warning and
// paste-ready removal hint — and does not block the source. Presence-based
// classification must not tighten this into a refusal; only a target THIS
// database holds changes column.
func TestEmbeddedDepAddAbsentCrossPrefixTargetStaysExternal(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "b")

	source := bdCreate(t, bd, dir, "Source with an absent cross-prefix target", "--type", "task")

	cmd := exec.Command(bd, "dep", "add", source.ID, "xy-404", "--type", "blocks")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd dep add %s xy-404 failed: %v\nstdout:\n%s\nstderr:\n%s", source.ID, err, stdout.String(), stderr.String())
	}

	// The warning names the target and hands over the removal command that
	// deletes exactly the stored row (stderr, so stdout stays byte-stable).
	for _, want := range []string{
		"xy-404 was not found",
		"stored unverified",
		"bd dep remove " + source.ID + " xy-404",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("dep add stderr missing %q:\n%s", want, stderr.String())
		}
	}

	// The edge is stored external: counted, but not enumerable, and disclosed
	// as unresolvable (be-lpi).
	details := bdShowDetails(t, bd, dir, source.ID)
	if depCount, ok := details["dependency_count"].(float64); !ok || depCount != 1 {
		t.Fatalf("dependency_count = %v, want 1 — the external edge must still be counted", details["dependency_count"])
	}
	if unresolvable, ok := details["unresolvable_dependencies"].(float64); !ok || unresolvable != 1 {
		t.Fatalf("unresolvable_dependencies = %v, want 1", details["unresolvable_dependencies"])
	}

	// An absent target has no status to derive from: the source stays ready.
	if ids := readyIDSet(t, bd, dir); !ids[source.ID] {
		t.Fatalf("%s is not ready with only an absent cross-prefix target", source.ID)
	}
}
