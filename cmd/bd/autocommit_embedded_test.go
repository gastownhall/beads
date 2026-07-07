//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func bdCommand(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	out, err := bdRunWithFlockRetry(t, bd, dir, args...)
	if err != nil {
		t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func assertEmbeddedHeadUnchanged(t *testing.T, beadsDir, database, before, command string) {
	t.Helper()
	after := embeddedCurrentCommit(t, beadsDir, database)
	if after != before {
		t.Fatalf("%s advanced HEAD in batch mode; before=%s after=%s", command, before, after)
	}
}

func assertEmbeddedHeadAdvanced(t *testing.T, beadsDir, database, before, command string) {
	t.Helper()
	after := embeddedCurrentCommit(t, beadsDir, database)
	if after == before {
		t.Fatalf("%s did not advance target HEAD; before=%s after=%s", command, before, after)
	}
}

func setupRoutedEmbeddedRepo(t *testing.T, bd string, sourcePrefix, targetPrefix string) (sourceDir, targetDir, targetBeadsDir string) {
	t.Helper()

	sourceDir, _, _ = bdInit(t, bd, "--prefix", sourcePrefix)
	targetDir = filepath.Join(sourceDir, "target-repo")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	runBDInit(t, bd, targetDir, "--prefix", targetPrefix)

	route := `{"prefix":"` + targetPrefix + `-","path":"target-repo"}` + "\n"
	if err := os.WriteFile(filepath.Join(sourceDir, ".beads", "routes.jsonl"), []byte(route), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	return sourceDir, targetDir, filepath.Join(targetDir, ".beads")
}

func TestEmbeddedDepAndLinkBatchAutoCommitDoesNotAdvanceHead(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt auto-commit tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("dep_add", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "da")
		from := bdCreate(t, bd, dir, "Dependent issue")
		to := bdCreate(t, bd, dir, "Blocking issue")
		before := embeddedCurrentCommit(t, beadsDir, "da")

		bdCommand(t, bd, dir, "--dolt-auto-commit", "batch", "dep", "add", from.ID, to.ID)

		assertEmbeddedHeadUnchanged(t, beadsDir, "da", before, "dep add")
	})

	t.Run("dep_blocks", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "db")
		blocker := bdCreate(t, bd, dir, "Blocking issue")
		blocked := bdCreate(t, bd, dir, "Blocked issue")
		before := embeddedCurrentCommit(t, beadsDir, "db")

		bdCommand(t, bd, dir, "--dolt-auto-commit", "batch", "dep", blocker.ID, "--blocks", blocked.ID)

		assertEmbeddedHeadUnchanged(t, beadsDir, "db", before, "dep --blocks")
	})

	t.Run("dep_remove", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dr")
		from := bdCreate(t, bd, dir, "Dependent issue")
		to := bdCreate(t, bd, dir, "Blocking issue")
		bdDep(t, bd, dir, "add", from.ID, to.ID)
		before := embeddedCurrentCommit(t, beadsDir, "dr")

		bdCommand(t, bd, dir, "--dolt-auto-commit", "batch", "dep", "remove", from.ID, to.ID)

		assertEmbeddedHeadUnchanged(t, beadsDir, "dr", before, "dep remove")
	})

	t.Run("link", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dl")
		from := bdCreate(t, bd, dir, "Dependent issue")
		to := bdCreate(t, bd, dir, "Blocking issue")
		before := embeddedCurrentCommit(t, beadsDir, "dl")

		bdCommand(t, bd, dir, "--dolt-auto-commit", "batch", "link", from.ID, to.ID)

		assertEmbeddedHeadUnchanged(t, beadsDir, "dl", before, "link")
	})
}

func TestEmbeddedDirectCommitPathsBatchAutoCommitDoesNotAdvanceHead(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt auto-commit tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("create", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dc")
		before := embeddedCurrentCommit(t, beadsDir, "dc")

		bdCommand(t, bd, dir, "--dolt-auto-commit", "batch", "create", "Deferred create")

		assertEmbeddedHeadUnchanged(t, beadsDir, "dc", before, "create")
	})

	t.Run("markdown_create", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dm")
		mdFile := filepath.Join(dir, "issues.md")
		mdContent := `## Markdown batch issue

### Priority
2

### Type
task

### Description
Created while Dolt auto-commit is in batch mode.
`
		if err := os.WriteFile(mdFile, []byte(mdContent), 0644); err != nil {
			t.Fatalf("write markdown fixture: %v", err)
		}
		before := embeddedCurrentCommit(t, beadsDir, "dm")

		bdCommand(t, bd, dir, "--dolt-auto-commit", "batch", "create", "-f", mdFile)

		assertEmbeddedHeadUnchanged(t, beadsDir, "dm", before, "markdown create")
	})

	t.Run("label_add", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dlb")
		issue := bdCreate(t, bd, dir, "Batch label target")
		before := embeddedCurrentCommit(t, beadsDir, "dlb")

		bdCommand(t, bd, dir, "--dolt-auto-commit", "batch", "label", "add", issue.ID, "batched")

		assertEmbeddedHeadUnchanged(t, beadsDir, "dlb", before, "label add")
		labels := showLabels(t, bd, dir, issue.ID)
		if !slices.Contains(labels, "batched") {
			t.Fatalf("labels = %v, want batched", labels)
		}
	})

	t.Run("delete", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dd")
		issue := bdCreate(t, bd, dir, "Batch delete target")
		before := embeddedCurrentCommit(t, beadsDir, "dd")

		bdCommand(t, bd, dir, "--dolt-auto-commit", "batch", "delete", issue.ID, "--force")

		assertEmbeddedHeadUnchanged(t, beadsDir, "dd", before, "delete")
	})
}

func TestEmbeddedRoutedSiblingWritesCommitTargetHead(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt auto-commit tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("comment", func(t *testing.T) {
		sourceDir, targetDir, targetBeadsDir := setupRoutedEmbeddedRepo(t, bd, "sc", "tc")
		issue := bdCreate(t, bd, targetDir, "Routed comment target")
		before := embeddedCurrentCommit(t, targetBeadsDir, "tc")

		bdCommand(t, bd, sourceDir, "comment", issue.ID, "routed comment")

		assertEmbeddedHeadAdvanced(t, targetBeadsDir, "tc", before, "routed comment")
		targetStore := openStore(t, targetBeadsDir, "tc")
		comments, err := targetStore.GetIssueComments(t.Context(), issue.ID)
		if err != nil {
			t.Fatalf("GetIssueComments: %v", err)
		}
		found := false
		for _, c := range comments {
			if c.Text == "routed comment" {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("routed comment was not persisted in target store")
		}
	})

	t.Run("note", func(t *testing.T) {
		sourceDir, targetDir, targetBeadsDir := setupRoutedEmbeddedRepo(t, bd, "sn", "tn")
		issue := bdCreate(t, bd, targetDir, "Routed note target")
		before := embeddedCurrentCommit(t, targetBeadsDir, "tn")

		bdCommand(t, bd, sourceDir, "note", issue.ID, "routed note")

		assertEmbeddedHeadAdvanced(t, targetBeadsDir, "tn", before, "routed note")
		targetStore := openStore(t, targetBeadsDir, "tn")
		got, err := targetStore.GetIssue(t.Context(), issue.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if !strings.Contains(got.Notes, "routed note") {
			t.Fatalf("target notes = %q, want routed note", got.Notes)
		}
	})

	t.Run("reopen", func(t *testing.T) {
		sourceDir, targetDir, targetBeadsDir := setupRoutedEmbeddedRepo(t, bd, "sr", "tr")
		issue := bdCreate(t, bd, targetDir, "Routed reopen target")
		bdClose(t, bd, targetDir, issue.ID)
		before := embeddedCurrentCommit(t, targetBeadsDir, "tr")

		bdCommand(t, bd, sourceDir, "reopen", issue.ID, "--reason", "routed reopen")

		assertEmbeddedHeadAdvanced(t, targetBeadsDir, "tr", before, "routed reopen")
		targetStore := openStore(t, targetBeadsDir, "tr")
		got, err := targetStore.GetIssue(t.Context(), issue.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.Status != types.StatusOpen {
			t.Fatalf("target status = %s, want %s", got.Status, types.StatusOpen)
		}
	})
}

// TestEmbeddedRoutedMutatingSiblingWritesCommitTargetHead pins the rest of the
// prefix-routed mutating command surface to the same write-through contract as
// comment/note/reopen above. These commands (the assign/tag shorthands plus
// dep/delete/close) resolve a prefix-routed target through the write-intent
// router and must commit on the target head, not fail "store is read-only"
// (#4141). Each subtest creates the bead in the target rig, runs the command
// from the source rig so resolution must route, and asserts both that the
// target HEAD advanced and that the mutation persisted in the target store.
func TestEmbeddedRoutedMutatingSiblingWritesCommitTargetHead(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt auto-commit tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("assign", func(t *testing.T) {
		sourceDir, targetDir, targetBeadsDir := setupRoutedEmbeddedRepo(t, bd, "sas", "tas")
		issue := bdCreate(t, bd, targetDir, "Routed assign target")
		before := embeddedCurrentCommit(t, targetBeadsDir, "tas")

		bdCommand(t, bd, sourceDir, "assign", issue.ID, "alice")

		assertEmbeddedHeadAdvanced(t, targetBeadsDir, "tas", before, "routed assign")
		targetStore := openStore(t, targetBeadsDir, "tas")
		got, err := targetStore.GetIssue(t.Context(), issue.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.Assignee != "alice" {
			t.Fatalf("target assignee = %q, want alice", got.Assignee)
		}
	})

	t.Run("tag", func(t *testing.T) {
		sourceDir, targetDir, targetBeadsDir := setupRoutedEmbeddedRepo(t, bd, "stg", "ttg")
		issue := bdCreate(t, bd, targetDir, "Routed tag target")
		before := embeddedCurrentCommit(t, targetBeadsDir, "ttg")

		bdCommand(t, bd, sourceDir, "tag", issue.ID, "routed-label")

		assertEmbeddedHeadAdvanced(t, targetBeadsDir, "ttg", before, "routed tag")
		targetStore := openStore(t, targetBeadsDir, "ttg")
		labels, err := targetStore.GetLabelsForIssues(t.Context(), []string{issue.ID})
		if err != nil {
			t.Fatalf("GetLabelsForIssues: %v", err)
		}
		if !slices.Contains(labels[issue.ID], "routed-label") {
			t.Fatalf("target labels = %v, want routed-label", labels[issue.ID])
		}
	})

	t.Run("dep_add", func(t *testing.T) {
		sourceDir, targetDir, targetBeadsDir := setupRoutedEmbeddedRepo(t, bd, "sdp", "tdp")
		from := bdCreate(t, bd, targetDir, "Routed dep source")
		to := bdCreate(t, bd, targetDir, "Routed dep target")
		before := embeddedCurrentCommit(t, targetBeadsDir, "tdp")

		bdCommand(t, bd, sourceDir, "dep", "add", from.ID, to.ID)

		assertEmbeddedHeadAdvanced(t, targetBeadsDir, "tdp", before, "routed dep add")
		targetStore := openStore(t, targetBeadsDir, "tdp")
		deps, err := targetStore.GetDependencies(t.Context(), from.ID)
		if err != nil {
			t.Fatalf("GetDependencies: %v", err)
		}
		found := false
		for _, d := range deps {
			if d.ID == to.ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("routed dep add did not persist %s -> %s in target store (deps=%v)", from.ID, to.ID, deps)
		}
	})

	t.Run("delete", func(t *testing.T) {
		sourceDir, targetDir, targetBeadsDir := setupRoutedEmbeddedRepo(t, bd, "sdl", "tdl")
		issue := bdCreate(t, bd, targetDir, "Routed delete target")
		before := embeddedCurrentCommit(t, targetBeadsDir, "tdl")

		bdCommand(t, bd, sourceDir, "delete", issue.ID, "--force")

		assertEmbeddedHeadAdvanced(t, targetBeadsDir, "tdl", before, "routed delete")
		targetStore := openStore(t, targetBeadsDir, "tdl")
		if _, err := targetStore.GetIssue(t.Context(), issue.ID); err == nil {
			t.Fatalf("routed delete did not remove %s from target store", issue.ID)
		}
	})

	t.Run("close", func(t *testing.T) {
		sourceDir, targetDir, targetBeadsDir := setupRoutedEmbeddedRepo(t, bd, "scl", "tcl")
		issue := bdCreate(t, bd, targetDir, "Routed close target")
		before := embeddedCurrentCommit(t, targetBeadsDir, "tcl")

		bdCommand(t, bd, sourceDir, "close", issue.ID, "--reason", "routed close")

		assertEmbeddedHeadAdvanced(t, targetBeadsDir, "tcl", before, "routed close")
		targetStore := openStore(t, targetBeadsDir, "tcl")
		got, err := targetStore.GetIssue(t.Context(), issue.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if got.Status != types.StatusClosed {
			t.Fatalf("target status = %s, want %s", got.Status, types.StatusClosed)
		}
	})
}
