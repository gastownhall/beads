package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

func GetReadyWorkWithCountsInTx(ctx context.Context, tx *sql.Tx, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	wispDepsExist, err := optionalTableExistsInTx(ctx, tx, "wisp_dependencies")
	if err != nil {
		return nil, fmt.Errorf("get ready work with counts: wisp dependency probe: %w", err)
	}

	issuePreds, err := buildReadyWorkPredicates(ctx, tx, filter, IssuesFilterTables)
	if err != nil {
		return nil, err
	}
	out, err := runSearchQueryInTx(ctx, tx, IssuesFilterTables, issuePreds.whereSQL, issuePreds.orderBySQL, issuePreds.limitSQL, issuePreds.args, wispDepsExist, false, filter.BriefBodies)
	if err != nil {
		return nil, err
	}

	empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
	if probeErr != nil {
		return nil, fmt.Errorf("get ready work with counts: wisp probe: %w", probeErr)
	}
	if empty {
		return out, nil
	}
	if !wispDepsExist {
		return out, nil
	}

	wispPreds, err := buildReadyWorkPredicates(ctx, tx, filter, WispsFilterTables)
	if err != nil {
		return nil, err
	}
	wisps, err := runSearchQueryInTx(ctx, tx, WispsFilterTables, wispPreds.whereSQL, wispPreds.orderBySQL, wispPreds.limitSQL, wispPreds.args, true, false, filter.BriefBodies)
	if err != nil {
		if isTableNotExistError(err) {
			return out, nil
		}
		return nil, err
	}
	if len(wisps) == 0 {
		return out, nil
	}

	seen := make(map[string]struct{}, len(out))
	for _, iwc := range out {
		if iwc != nil && iwc.Issue != nil {
			seen[iwc.Issue.ID] = struct{}{}
		}
	}
	for _, w := range wisps {
		if w == nil || w.Issue == nil {
			continue
		}
		if _, dup := seen[w.Issue.ID]; dup {
			return nil, fmt.Errorf("get ready work with counts: id %q exists in both issues and wisps", w.Issue.ID)
		}
		out = append(out, w)
	}
	sortIssuesWithCountsByPolicy(out, filter.SortPolicy)
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func sortIssuesWithCountsByPolicy(items []*types.IssueWithCounts, policy types.SortPolicy) {
	if len(items) <= 1 {
		return
	}
	issues := make([]*types.Issue, 0, len(items))
	for _, item := range items {
		if item == nil || item.Issue == nil {
			continue
		}
		issues = append(issues, item.Issue)
	}
	if len(issues) != len(items) {
		return
	}
	sortReadyIssues(issues, policy)
	byID := make(map[string]int, len(issues))
	for i, iss := range issues {
		byID[iss.ID] = i
	}
	sorted := make([]*types.IssueWithCounts, len(items))
	for _, item := range items {
		sorted[byID[item.Issue.ID]] = item
	}
	copy(items, sorted)
}

func aliasIssueColumns(cols string) string {
	raw := strings.ReplaceAll(cols, "\n", " ")
	raw = strings.ReplaceAll(raw, "\t", " ")
	parts := strings.Split(raw, ",")
	for i, p := range parts {
		parts[i] = "i." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

var readyWorkIssueColumns = aliasIssueColumns(IssueSelectColumns)

// briefReadyWorkIssueColumns is the i.-aliased brief projection (body columns
// dropped) used by the work-probe path when WorkFilter.BriefBodies is set (be-yvci).
var briefReadyWorkIssueColumns = aliasIssueColumns(IssueSelectColumnsBrief)

const readyWorkDepJSONObject = `JSON_OBJECT(
	'issue_id', issue_id,
	'depends_on_id', COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external),
	'type', type,
	'created_at', DATE_FORMAT(created_at, '%Y-%m-%dT%H:%i:%sZ'),
	'created_by', created_by,
	'metadata', CAST(metadata AS CHAR),
	'thread_id', thread_id
)`

func scanReadyWorkRowWithCounts(rows *sql.Rows) (*types.IssueWithCounts, error) {
	return scanReadyWorkRowWithScanner(rows, ScanIssueFrom)
}

// scanReadyWorkBriefRowWithCounts scans a row from the brief work-probe projection
// (briefReadyWorkIssueColumns): the same composite dep/label/count extras, but the
// issue columns are the body-stripped brief set (be-yvci). Sharing
// scanReadyWorkRowWithScanner guarantees the trailing extras are appended in the
// identical order for both projections.
func scanReadyWorkBriefRowWithCounts(rows *sql.Rows) (*types.IssueWithCounts, error) {
	return scanReadyWorkRowWithScanner(rows, ScanIssueBriefFrom)
}

func scanReadyWorkRowWithScanner(rows *sql.Rows, scanIssue func(IssueScanner) (*types.Issue, error)) (*types.IssueWithCounts, error) {
	var labelsJSON, depsJSON sql.NullString
	var parentID sql.NullString
	var depCount, rdepCount, commentCount sql.NullInt64

	composite := &compositeReadyRow{
		row: rows,
		extra: []any{
			&labelsJSON,
			&depCount,
			&rdepCount,
			&commentCount,
			&parentID,
			&depsJSON,
		},
	}
	issue, err := scanIssue(composite)
	if err != nil {
		return nil, fmt.Errorf("scan issue with counts: %w", err)
	}

	if labelsJSON.Valid && labelsJSON.String != "" {
		var labels []string
		if err := json.Unmarshal([]byte(labelsJSON.String), &labels); err != nil {
			return nil, fmt.Errorf("scan issue with counts: parse labels_json: %w", err)
		}
		sort.Strings(labels)
		issue.Labels = labels
	}

	if depsJSON.Valid && depsJSON.String != "" {
		var deps []*types.Dependency
		if err := json.Unmarshal([]byte(depsJSON.String), &deps); err != nil {
			return nil, fmt.Errorf("scan issue with counts: parse deps_json: %w", err)
		}
		issue.Dependencies = deps
	}

	iwc := &types.IssueWithCounts{
		Issue:           issue,
		DependencyCount: int(depCount.Int64),
		DependentCount:  int(rdepCount.Int64),
		CommentCount:    int(commentCount.Int64),
	}
	if parentID.Valid {
		s := parentID.String
		iwc.Parent = &s
	}
	return iwc, nil
}

type compositeReadyRow struct {
	row   *sql.Rows
	extra []any
}

func (c *compositeReadyRow) Scan(dest ...any) error {
	combined := make([]any, 0, len(dest)+len(c.extra))
	combined = append(combined, dest...)
	combined = append(combined, c.extra...)
	return c.row.Scan(combined...)
}
