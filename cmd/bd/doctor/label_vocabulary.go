package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// maxLabelVocabularyExamples caps how many undefined labels or case-variant
// clusters the detail line names, matching maxLabelExamples's reasoning in
// label_whitespace.go for a workspace with many of either.
const maxLabelVocabularyExamples = 8

// CheckLabelVocabularyWithStore reports two independent things about the
// opt-in label vocabulary registry (bd label define/undefine/defined):
//
//   - undefined labels currently in use, when labels.vocabulary is warn or
//     enforce (advisory even under enforce -- a workspace may have turned
//     enforce on AFTER labels already existed; this surfaces the backlog,
//     it does not gate `bd doctor`'s overall pass/fail)
//   - case-variant clusters across ALL labels in use, regardless of mode
//     (e.g. Backend/backend), which the vocabulary registry cannot itself
//     prevent once such a pair already exists on issues
//
// Neither is auto-fixable: defining a label or reconciling which spelling of
// a case-variant cluster should win needs a human (`bd label define`, or
// `bd label rename` -- a separate capability).
func CheckLabelVocabularyWithStore(ss *SharedStore) DoctorCheck {
	store := ss.Store()
	if store == nil {
		return DoctorCheck{Name: "Label Vocabulary", Status: StatusOK, Message: "No database yet"}
	}
	return checkLabelVocabularyWithStore(context.Background(), store)
}

func checkLabelVocabularyWithStore(ctx context.Context, store *dolt.DoltStore) DoctorCheck {
	const name = "Label Vocabulary"

	mode, err := store.GetConfig(ctx, "labels.vocabulary")
	if err != nil {
		return DoctorCheck{Name: name, Status: StatusWarning, Message: "Unable to read labels.vocabulary", Detail: err.Error()}
	}
	defs, err := store.ListLabelDefinitions(ctx)
	if err != nil {
		return DoctorCheck{Name: name, Status: StatusWarning, Message: "Unable to list label definitions", Detail: err.Error()}
	}
	counts, err := labelCountsWithStore(ctx, store)
	if err != nil {
		return DoctorCheck{Name: name, Status: StatusWarning, Message: "Unable to count labels in use", Detail: err.Error()}
	}

	var parts, details []string

	if mode == "warn" || mode == "enforce" {
		if undefined := undefinedLabelsInUse(defs, counts); len(undefined) > 0 {
			parts = append(parts, fmt.Sprintf("%d undefined label(s) in use", len(undefined)))
			details = append(details, formatLabelVocabularyExamples(undefined, func(l string) string {
				return fmt.Sprintf("%s (%d issue(s))", l, counts[l])
			}))
		}
	}

	if clusters := caseVariantLabelClusters(counts); len(clusters) > 0 {
		parts = append(parts, fmt.Sprintf("%d case-variant cluster(s)", len(clusters)))
		clusterStrings := make([]string, len(clusters))
		for i, c := range clusters {
			clusterStrings[i] = strings.Join(c, "/")
		}
		details = append(details, formatLabelVocabularyExamples(clusterStrings, func(s string) string { return s }))
	}

	if len(parts) == 0 {
		return DoctorCheck{Name: name, Status: StatusOK, Message: "Vocabulary consistent"}
	}
	return DoctorCheck{
		Name:    name,
		Status:  StatusWarning,
		Message: strings.Join(parts, "; "),
		Detail:  strings.Join(details, "; "),
		Fix:     "Define missing labels with 'bd label define <label>'. Case-variant clusters have no dedicated reconciliation command yet -- pick one spelling and move each issue onto it with 'bd label remove <old>' / 'bd label add <new>'.",
	}
}

// formatLabelVocabularyExamples renders up to maxLabelVocabularyExamples
// items through format, appending a "(+N more)" tail when truncated.
func formatLabelVocabularyExamples(items []string, format func(string) string) string {
	examples := items
	truncated := false
	if len(examples) > maxLabelVocabularyExamples {
		examples = examples[:maxLabelVocabularyExamples]
		truncated = true
	}
	rendered := make([]string, len(examples))
	for i, item := range examples {
		rendered[i] = format(item)
	}
	detail := strings.Join(rendered, ", ")
	if truncated {
		detail += fmt.Sprintf(" (+%d more)", len(items)-len(examples))
	}
	return detail
}

// undefinedLabelsInUse returns, sorted, every label counted in counts that
// has no exact-spelling row in defs.
func undefinedLabelsInUse(defs []types.LabelDefinition, counts map[string]int) []string {
	defined := make(map[string]bool, len(defs))
	for _, d := range defs {
		defined[d.Label] = true
	}
	var undefined []string
	for label := range counts {
		if !defined[label] {
			undefined = append(undefined, label)
		}
	}
	sort.Strings(undefined)
	return undefined
}

// caseVariantLabelClusters groups every label in counts by its lowercase
// form and returns, sorted, the groups with more than one member -- labels
// that differ only in case, such as "Backend" and "backend".
func caseVariantLabelClusters(counts map[string]int) [][]string {
	byLower := make(map[string][]string)
	for label := range counts {
		lower := strings.ToLower(label)
		byLower[lower] = append(byLower[lower], label)
	}
	var lowers []string
	for lower, group := range byLower {
		if len(group) > 1 {
			lowers = append(lowers, lower)
		}
	}
	sort.Strings(lowers)
	clusters := make([][]string, 0, len(lowers))
	for _, lower := range lowers {
		group := byLower[lower]
		sort.Strings(group)
		clusters = append(clusters, group)
	}
	return clusters
}

// labelCountsWithStore mirrors cmd/bd's countLabelsAcrossIssues (package
// main, not importable here): a single SearchIssues over the whole
// workspace, tallying every label on every issue.
func labelCountsWithStore(ctx context.Context, store *dolt.DoltStore) (map[string]int, error) {
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, issue := range issues {
		for _, label := range issue.Labels {
			counts[label]++
		}
	}
	return counts, nil
}
