package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/labelns"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// ExclusiveLabelsCheckName is the doctor check name for exclusive label
// namespace violations (bd-7u5ki).
const ExclusiveLabelsCheckName = "Exclusive Label Namespaces"

// maxExclusiveLabelDetailLines caps the per-issue lines in the check detail
// so a widespread violation doesn't flood doctor output.
const maxExclusiveLabelDetailLines = 10

// CheckExclusiveLabelNamespacesWithStore reports non-closed issues carrying
// more than one label in a namespace configured exclusive via
// labels.exclusive-prefixes. Write-path enforcement only guards new label
// additions, so violations written before the config was set (or merged in
// by import, which warns instead of failing) stay invisible to routing
// consumers until cleaned up — this check is how adopters find them.
func CheckExclusiveLabelNamespacesWithStore(ss *SharedStore) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return DoctorCheck{
			Name:    ExclusiveLabelsCheckName,
			Status:  StatusOK,
			Message: "No database yet",
		}
	}
	return checkExclusiveLabelNamespaces(context.Background(), store)
}

func checkExclusiveLabelNamespaces(ctx context.Context, store *dolt.DoltStore) DoctorCheck {
	raw, err := store.GetConfig(ctx, labelns.ConfigKey)
	if err != nil {
		return DoctorCheck{
			Name:    ExclusiveLabelsCheckName,
			Status:  StatusWarning,
			Message: "Unable to read " + labelns.ConfigKey,
			Detail:  err.Error(),
		}
	}
	prefixes := labelns.ParsePrefixes(raw)
	if len(prefixes) == 0 {
		return DoctorCheck{
			Name:    ExclusiveLabelsCheckName,
			Status:  StatusOK,
			Message: "No exclusive label namespaces configured (" + labelns.ConfigKey + ")",
		}
	}
	violations, err := issueops.FindExclusiveLabelViolations(ctx, store.UnderlyingDB(), prefixes)
	if err != nil {
		return DoctorCheck{
			Name:    ExclusiveLabelsCheckName,
			Status:  StatusWarning,
			Message: "Unable to scan labels for exclusive-namespace violations",
			Detail:  err.Error(),
		}
	}
	if len(violations) == 0 {
		return DoctorCheck{
			Name:    ExclusiveLabelsCheckName,
			Status:  StatusOK,
			Message: fmt.Sprintf("No violations in exclusive namespaces (%s)", strings.Join(prefixes, ", ")),
		}
	}
	lines := make([]string, 0, maxExclusiveLabelDetailLines+1)
	for i, v := range violations {
		if i == maxExclusiveLabelDetailLines {
			lines = append(lines, fmt.Sprintf("... and %d more", len(violations)-maxExclusiveLabelDetailLines))
			break
		}
		lines = append(lines, fmt.Sprintf("%s: %s", v.IssueID, strings.Join(v.Labels, ", ")))
	}
	return DoctorCheck{
		Name:    ExclusiveLabelsCheckName,
		Status:  StatusWarning,
		Message: fmt.Sprintf("%d issue(s) carry more than one label in an exclusive namespace — routing filters may silently exclude them", len(violations)),
		Detail:  strings.Join(lines, "\n"),
		Fix:     "Remove the extra label: bd label remove <id> <label>, or swap with: bd label add --replace <id> <label>",
	}
}
