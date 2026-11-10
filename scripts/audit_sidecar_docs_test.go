package scripts_test

import (
	"strings"
	"testing"
)

// TestAuditSidecarConfigDocsMentionKinds: GH#6146 — the configuration
// reference's audit.enabled row must describe what enabling the sidecar
// writes: both automatic entry kinds and what each carries, the best-effort
// caveat and the advisory old value, that `bd create` and `bd comment` write
// nothing, and the events journal as the replay trail, while keeping the
// per-issue history pointer.
func TestAuditSidecarConfigDocsMentionKinds(t *testing.T) {
	content := readPolicyFile(t, sourceRepoRoot(t), "docs/reference/configuration.md")
	var row string
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "`audit.enabled`") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatal("docs/reference/configuration.md has no audit.enabled row")
	}
	for _, want := range []string{
		"field_change",
		"claimed",
		"best-effort",
		"`bd create`",
		"`bd comment`",
		"advisory under concurrency",
		"bd config set events-journal true",
		"bd events tail",
		"bd history <id> --events",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("the audit.enabled row is missing %q:\n%s", want, row)
		}
	}
}
