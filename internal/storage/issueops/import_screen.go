package issueops

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// ValidateIssueForInsertScreen reports whether an issue would pass the
// writer's own insert validation (PrepareIssueForInsert's normalization,
// metadata check and ValidateWithCustom, plus the label-length check
// persistLabels enforces), WITHOUT mutating the caller's issue: the row is
// validated on a shallow copy, so screening an import candidate cannot
// normalize its timestamps or stamp a content hash (GH#4492 design §2).
//
// The reason is returned bare — without PrepareIssueForInsert's
// "validation failed for issue %s: " prefix — because the import screen
// reports the row's line and id itself and would otherwise repeat the id.
// This is a screen, not a gate: the writer still runs the real
// PrepareIssueForInsert inside its transaction, because configuration can
// change between screening and the write.
func ValidateIssueForInsertScreen(issue *types.Issue, customStatuses, customTypes []string) error {
	cp := *issue
	if err := PrepareIssueForInsert(&cp, customStatuses, customTypes); err != nil {
		return stripIssuePrefix(err, issue.ID)
	}
	// The label-length refusal persistLabels applies at write time
	// (addLabelInTx) is deterministic in the row alone, so it is screenable
	// here; reuse the same rule, never a second one.
	for _, label := range cp.Labels {
		if err := types.CheckFieldLen("label", label); err != nil {
			return stripIssuePrefix(err, issue.ID)
		}
	}
	return nil
}

// stripIssuePrefix removes the row-identifying prefix the prepare/validate
// errors carry ("validation failed for issue %s: " / "metadata validation
// failed for issue %s: "), so a caller that names the row's line and id
// itself does not repeat the id inside the reason.
func stripIssuePrefix(err error, issueID string) error {
	msg := err.Error()
	for _, prefix := range []string{
		"validation failed for issue ",
		"metadata validation failed for issue ",
	} {
		if trimmed, ok := strings.CutPrefix(msg, prefix); ok {
			if rest, okID := strings.CutPrefix(trimmed, issueID); okID {
				if reason, okColon := strings.CutPrefix(rest, ": "); okColon {
					return fmt.Errorf("%s", reason)
				}
			}
		}
	}
	return err
}
