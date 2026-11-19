//go:build cgo

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// bdDryRunVocabSet registers a database-only custom type in an embedded
// workspace: `bd config set types.custom` writes the store's config plane,
// so the vocabulary exists in the database and nowhere else.
func bdDryRunVocabSet(t *testing.T, bd, dir, types string) {
	t.Helper()
	cmd := exec.Command(bd, "config", "set", "types.custom", types)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd config set types.custom %s failed: %v\n%s", types, err, out)
	} else if strings.Contains(string(out), "not a recognized config key") {
		t.Fatalf("bd config set types.custom warned about an unrecognized key:\n%s", out)
	}
}

// TestEmbeddedCreateDryRunUsesDestinationVocabulary pins the dry-run
// preview's vocabulary rule (GH#5787): a preview validates the request
// against the DESTINATION's registered types, never the source workspace's.
// A database-only custom type is accepted where it is registered, and a type
// registered only at the source is refused for a foreign target — while the
// preview itself never changes stored state.
func TestEmbeddedCreateDryRunUsesDestinationVocabulary(t *testing.T) {
	bd := buildEmbeddedBD(t)

	run := func(dir string, args ...string) (string, error) {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		return stdout.String() + stderr.String(), err
	}

	t.Run("local preview accepts a database-only custom type", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dvl")
		bdDryRunVocabSet(t, bd, dir, "incident")

		out, err := run(dir, "create", "--dry-run", "--type", "incident", "--title", "PROBE")
		if err != nil {
			t.Fatalf("local dry-run refused a type registered in its own database; got:\n%s", out)
		}
		if !strings.Contains(out, "Would create issue") || !strings.Contains(out, "incident") {
			t.Fatalf("expected an incident preview; got:\n%s", out)
		}
		if snapshot := readEmbeddedStoreSnapshot(t, beadsDir, "dvl"); snapshot.issueCount != 0 {
			t.Fatalf("dry-run changed stored state; issue count = %d", snapshot.issueCount)
		}
	})

	t.Run("routed preview uses the destination vocabulary", func(t *testing.T) {
		targetDir, targetBeadsDir, _ := bdInit(t, bd, "--prefix", "dvt")
		bdDryRunVocabSet(t, bd, targetDir, "incident")
		callerDir, _, _ := bdInit(t, bd, "--prefix", "dvs")
		// The caller registers a type the target does not know.
		bdDryRunVocabSet(t, bd, callerDir, "opsnotes")

		out, err := run(callerDir, "create", "--dry-run", "--repo", targetDir, "--type", "incident", "--title", "PROBE")
		if err != nil {
			t.Fatalf("routed dry-run refused a type registered at the destination; got:\n%s", out)
		}
		if !strings.Contains(out, "Would create issue") || !strings.Contains(out, "incident") {
			t.Fatalf("expected the destination's incident preview; got:\n%s", out)
		}
		if snapshot := readEmbeddedStoreSnapshot(t, targetBeadsDir, "dvt"); snapshot.issueCount != 0 {
			t.Fatalf("routed dry-run changed the target's state; issue count = %d", snapshot.issueCount)
		}

		// A type registered only at the source is refused for the target:
		// the source's vocabulary must never vouch for a foreign destination.
		out, err = run(callerDir, "create", "--dry-run", "--repo", targetDir, "--type", "opsnotes", "--title", "PROBE")
		if err == nil {
			t.Fatalf("routed dry-run accepted a source-only type for a foreign target; got:\n%s", out)
		}
		if !strings.Contains(out, "invalid issue type") {
			t.Fatalf("routed dry-run should name the type the destination refuses; got:\n%s", out)
		}
		if strings.Contains(out, "Would create issue") {
			t.Fatalf("refused routed dry-run must emit no preview; got:\n%s", out)
		}
		if snapshot := readEmbeddedStoreSnapshot(t, targetBeadsDir, "dvt"); snapshot.issueCount != 0 {
			t.Fatalf("refused routed dry-run changed the target's state; issue count = %d", snapshot.issueCount)
		}
	})
}
