package conformance

import "testing"

// RunAudit runs the exhaustive "strange behavior" cases derived from a systematic audit
// of the Dolt reference implementation (the audit_*.go files). Every case was validated
// against the embedded-Dolt oracle, so a failure here on a SQL backend is a genuine
// divergence from the reference, not a bad test.
func RunAudit(t *testing.T, f Factory) {
	t.Helper()
	t.Run("issue-lifecycle", func(t *testing.T) { RunAudit_issue_lifecycle(t, f) })
	t.Run("dependencies-readiness", func(t *testing.T) { RunAudit_dependencies_readiness(t, f) })
	t.Run("search-counts-stats", func(t *testing.T) { RunAudit_search_counts_stats(t, f) })
	t.Run("labels-comments-events", func(t *testing.T) { RunAudit_labels_comments_events(t, f) })
	t.Run("config-metadata-slots-repomtime", func(t *testing.T) { RunAudit_config_metadata_slots_repomtime(t, f) })
	t.Run("molecule-wisp-batch-iter", func(t *testing.T) { RunAudit_molecule_wisp_batch_iter(t, f) })
}
