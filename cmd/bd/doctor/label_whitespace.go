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

// CheckLabelWhitespaceWithStore reports labels no filter can match (#5812):
// labels with leading/trailing whitespace, and blank labels.
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

	var untrimmed, blank int
	var examples []string
	for _, a := range anomalies {
		untrimmed += len(a.Untrimmed)
		blank += len(a.Blank)
		for _, group := range [][]fix.LabelRow{a.Untrimmed, a.Blank} {
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

	detail := strings.Join(examples, "; ")
	if total := untrimmed + blank; total > len(examples) {
		detail += fmt.Sprintf(" (+%d more)", total-len(examples))
	}

	// Deliberately not auto-fixable: trimming a damaged label can collide with a
	// correct label already on the same issue, which needs a human decision.
	return DoctorCheck{
		Name:    "Label Whitespace",
		Status:  StatusWarning,
		Message: fmt.Sprintf("%s — a label bd cannot filter makes a filtered list silently short", strings.Join(parts, ", ")),
		Detail:  detail,
		Fix:     "Repair with: bd update <id> --remove-label '<damaged>' --add-label '<trimmed>'",
	}
}
