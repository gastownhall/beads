//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedMetadatalessAllowedFallbacks (GH#4381, AC-4) pins the cases the
// refusal must NOT change: a metadata-less workspace with an EMPTY embedded
// store keeps its existing guidance (nothing to be ambiguous with — the
// workspace stays bootstrap-eligible), and a metadata-less store holding
// exactly the database that would be opened is unambiguous. These are pinning
// tests — they pass at the base commit too; the guard must leave them green.
func TestEmbeddedMetadatalessAllowedFallbacks(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	// One configured fixture doubles as the configured control below.
	f := newMetadatalessFixture(t)

	t.Run("empty workspace keeps its existing guidance", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), f.jsonlData, 0o600); err != nil {
			t.Fatal(err)
		}

		// Zero databases means zero ambiguity: the write command keeps its
		// existing answer — no database, bootstrap via bd init — and must not
		// turn into the metadata-less refusal or create a sibling.
		out, err := runBDMetadataless(t, bd, dir, nil, "create", "--silent", "bootstrap probe")
		if err == nil {
			t.Fatalf("create against an empty metadata-less workspace unexpectedly succeeded:\n%s", out)
		}
		if strings.Contains(out, "no workspace metadata at") {
			t.Fatalf("an empty store must not hit the metadata-less refusal:\n%s", out)
		}
		if !strings.Contains(out, "bd init") {
			t.Errorf("the empty-workspace guidance must still name bd init:\n%s", out)
		}
		if _, statErr := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); !os.IsNotExist(statErr) {
			t.Fatalf("an embedded store appeared (stat error: %v)", statErr)
		}
	})

	t.Run("sole default database stays allowed", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "beads")
		removeWorkspaceMetadata(t, beadsDir)

		if out, err := runBDMetadataless(t, bd, dir, nil, "list"); err != nil {
			t.Fatalf("read against the sole default database failed: %v\n%s", err, out)
		}
		if out, err := runBDMetadataless(t, bd, dir, []string{"BEADS_SKIP_IDENTITY_CHECK=1"}, "create", "--silent", "still allowed"); err != nil {
			t.Fatalf("write against the sole default database failed: %v\n%s", err, out)
		}
		if got := listDatabaseDirs(t, filepath.Join(beadsDir, "embeddeddolt")); strings.Join(got, ",") != "beads" {
			t.Fatalf("embedded store holds %v, want exactly [beads]", got)
		}
	})

	t.Run("configured workspace keeps working", func(t *testing.T) {
		// f still has its metadata: the guard must not fire on it.
		issues := bdListJSON(t, bd, f.dir)
		if len(issues) != 1 || !strings.Contains(issues[0].Title, "existing fixture issue") {
			t.Fatalf("configured workspace list held %d issues, want the original 1", len(issues))
		}
	})
}
