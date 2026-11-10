package docsync

import "testing"

// TestIncompleteCommitRecoveryGuidance guards the runbook for a commit that
// leaves tracked changes in the working set or cannot verify that it did not
// (GH#5433): both typed errors and their fields, the two messages, what the
// check cannot see, and a nondestructive procedure that never replays the
// mutations or treats an export as a full snapshot. Each assertion is scoped
// to the section a reader lands in.
func TestIncompleteCommitRecoveryGuidance(t *testing.T) {
	runbook := readRepoDoc(t, "docs", "recovery", "incomplete-commits.md")
	section := func(heading string) string {
		t.Helper()
		body, ok := docSubtree(runbook, heading)
		if !ok {
			t.Fatalf("docs/recovery/incomplete-commits.md missing the %q section", heading)
		}
		return body
	}

	requirePhrases(t, "incomplete-commits.md ## Symptoms", section("## Symptoms"),
		"commit incomplete:",
		"a commit was created, but tracked changes remain in",
		"commit verification failed:",
		"the commit may have landed, but completeness could not be verified",
		"nothing to commit; working-set cleanliness could not be verified",
		"stops before contacting the remote and prints no success line",
	)
	requirePhrases(t, "incomplete-commits.md ## What the errors carry", section("## What the errors carry"),
		"*storage.UncommittedChangesError", "Tables", "IgnorePatterns", "Committed",
		"*storage.CommitVerificationError", "storage.ErrCommitVerificationFailed",
		"Outcome", "commit_succeeded", "nothing_to_commit", "Operation", "Cause",
		"neither error means the write failed",
	)
	requirePhrases(t, "incomplete-commits.md ## Diagnosis", section("## Diagnosis"),
		"only rows dolt_status shows count",
		"a new table an ignore pattern hides never appears there",
		"a hint, not proof",
		"server-mode storage does not run this check",
	)
	solution := section("## Solution")
	requirePhrases(t, "incomplete-commits.md ## Solution", solution,
		"back up the whole .beads directory",
		"do not change anything you have not confirmed",
		"bd dolt commit",
		"verify the history",
		"do not replay the mutations",
		"do not treat an export as a complete rescue snapshot",
	)
	forbidPhrases(t, "incomplete-commits.md ## Solution", solution,
		"rm -rf .beads", "bd init --force", "bd import ",
	)
}
