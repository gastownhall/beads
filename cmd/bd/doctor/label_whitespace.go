package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// maxLabelExamples caps how many damaged labels the detail line names, so a
// database with thousands of them stays readable.
const maxLabelExamples = 5

// CheckLabelWhitespaceWithStore reports labels carrying whitespace damage
// (#5812): labels with leading/trailing whitespace, blank labels, and labels
// containing a space.
//
// bd normalizes labels on every filter path but historically did not on any
// write path, so `--labels 'a, b'` stored " b" — a label that can never match
// its own filter. Databases written by an older bd still carry that damage
// after the write paths are fixed, and nothing surfaces it: a filtered list
// that is silently short is indistinguishable from a complete one.
func CheckLabelWhitespaceWithStore(ss *SharedStore) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return DoctorCheck{
			Name:    "Label Whitespace",
			Status:  StatusOK,
			Message: "No database yet",
		}
	}
	return checkLabelWhitespaceWithStore(store)
}

func checkLabelWhitespaceWithStore(store *dolt.DoltStore) DoctorCheck {
	anomalies, err := fix.ScanLabelWhitespace(context.Background(), store.UnderlyingDB())
	if err != nil {
		return DoctorCheck{
			Name:    "Label Whitespace",
			Status:  StatusWarning,
			Message: "Unable to scan labels",
			Detail:  err.Error(),
		}
	}
	if len(anomalies) == 0 {
		return DoctorCheck{
			Name:    "Label Whitespace",
			Status:  StatusOK,
			Message: "No labels carry whitespace damage",
		}
	}

	var untrimmed, blank, internal int
	var examples []string
	for _, a := range anomalies {
		untrimmed += len(a.Untrimmed)
		blank += len(a.Blank)
		internal += len(a.Internal)
		for _, group := range [][]fix.LabelRow{a.Untrimmed, a.Blank, a.Internal} {
			for _, row := range group {
				if len(examples) < maxLabelExamples {
					examples = append(examples, fmt.Sprintf("%s: %q", row.IssueID, row.Label))
				}
			}
		}
	}

	var parts []string
	if untrimmed > 0 {
		parts = append(parts, fmt.Sprintf("%d with leading/trailing whitespace", untrimmed))
	}
	if blank > 0 {
		parts = append(parts, fmt.Sprintf("%d blank", blank))
	}
	if internal > 0 {
		parts = append(parts, fmt.Sprintf("%d containing a space", internal))
	}

	detail := strings.Join(examples, "; ")
	if total := untrimmed + blank + internal; total > len(examples) {
		detail += fmt.Sprintf(" (+%d more)", total-len(examples))
	}

	// Deliberately not auto-fixable. Trimming a damaged label can collide with a
	// correct label already on the same issue, and a label containing a space may
	// have been meant — bd cannot tell "good first issue" from a missed comma.
	// Both need a human.
	return DoctorCheck{
		Name:    "Label Whitespace",
		Status:  StatusWarning,
		Message: fmt.Sprintf("%s — these may not match the filters meant to find them", strings.Join(parts, ", ")),
		Detail:  detail,
		Fix:     "Repair with: bd update <id> --remove-label '<damaged>' --add-label <replacement>. A label containing a space is often a missed comma; bd now warns when one is written.",
	}
}
