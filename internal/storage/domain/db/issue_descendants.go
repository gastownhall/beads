package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
)

type predBundle struct {
	// matchesCTE is a named non-recursive CTE ("<name> AS (SELECT id FROM
	// <table> WHERE ...)") that the walk branches reference via snippet.
	// Hoisting the filter subquery out of the recursive branches is load-
	// bearing: inlining `id IN (SELECT id FROM issues WHERE ...)` into three
	// or more branches trips a dolt 2.1.6 analyzer bug ("unable to find
	// field with index N in row of M columns") on the recursive LIKE-join
	// branch. The named CTE keeps the semantics (same per-level filter) and
	// dodges the analyzer.
	matchesCTE string
	snippet    string
	args       []any
}

func buildDescendantsPred(table, alias, cteName string, clauses []string, args []any) predBundle {
	if len(clauses) == 0 {
		return predBundle{}
	}
	return predBundle{
		matchesCTE: fmt.Sprintf("%s AS (SELECT id FROM %s WHERE %s)",
			cteName, table, strings.Join(clauses, " AND ")),
		snippet: fmt.Sprintf(" AND %s.id IN (SELECT id FROM %s)", alias, cteName),
		args:    args,
	}
}

func (r *issueSQLRepositoryImpl) GetDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	levelFilter := filter
	levelFilter.ParentID = nil
	levelFilter.Limit = 0
	levelFilter.Offset = 0

	issueWhereClauses, issueArgs, err := buildIssueFilterClauses("", levelFilter, issuesFilterTables)
	if err != nil {
		return nil, fmt.Errorf("descendants: issues filter: %w", err)
	}

	wispDepsExist, err := r.optionalTableExists(ctx, "wisp_dependencies")
	if err != nil {
		return nil, fmt.Errorf("descendants: wisp_dependencies probe: %w", err)
	}
	walkWisps := wispDepsExist && !filter.SkipWisps
	if walkWisps {
		empty, probeErr := r.wispsTableEmptyOrMissing(ctx)
		if probeErr != nil {
			return nil, fmt.Errorf("descendants: wisps table probe: %w", probeErr)
		}
		walkWisps = !empty
	}

	var wispWhereClauses []string
	var wispArgs []any
	if walkWisps {
		wispWhereClauses, wispArgs, err = buildIssueFilterClauses("", levelFilter, wispsFilterTables)
		if err != nil {
			return nil, fmt.Errorf("descendants: wisps filter: %w", err)
		}
	}

	issuePred := buildDescendantsPred("issues", "i", "issue_matches", issueWhereClauses, issueArgs)
	var wispPred predBundle
	if walkWisps {
		wispPred = buildDescendantsPred("wisps", "w", "wisp_matches", wispWhereClauses, wispArgs)
	}

	cte, allArgs := buildDescendantsCTE(rootID, walkWisps, issuePred, wispPred)

	rows, err := r.runner.QueryContext(ctx, cte, allArgs...)
	if err != nil {
		return nil, fmt.Errorf("descendants: query: %w", err)
	}
	page, err := scanIDSrcPage(rows, false)
	if err != nil {
		return nil, fmt.Errorf("descendants: %w", err)
	}

	issuesByID, err := r.fetchIssuesByIDs(ctx, page.issueIDs, issuesFilterTables, filter)
	if err != nil {
		return nil, fmt.Errorf("descendants: hydrate issues: %w", err)
	}

	var wispsByID map[string]*types.Issue
	if len(page.wispIDs) > 0 {
		wispsByID, err = r.fetchIssuesByIDs(ctx, page.wispIDs, wispsFilterTables, filter)
		if err != nil && !dberrors.IsTableNotExist(err) {
			return nil, fmt.Errorf("descendants: hydrate wisps: %w", err)
		}
	}

	return reassembleBySrc(page.ordered, issuesByID, wispsByID), nil
}

// buildDescendantsCTE walks parent-child edges AND the dotted-ID fallback the
// classic ParentID filter applies (issueops/filters.go): a row named
// <node>.<suffix> with no parent-child edge at all is a child of <node>.
// Rows carry a via marker: 'e' for edge-found, 'd' for dotted-found. Dotted
// recursion only fires from 'e' rows — a dotted node's own dotted
// descendants share its prefix and were already matched by whichever LIKE
// found it, so re-expanding them would only multiply duplicate rows.
// Filter predicates are hoisted into named matches CTEs (see predBundle) to
// dodge a dolt 2.1.6 analyzer bug.
func buildDescendantsCTE(rootID string, walkWisps bool, issuePred, wispPred predBundle) (string, []any) {
	var b strings.Builder
	var args []any
	b.WriteString("WITH RECURSIVE ")
	if issuePred.matchesCTE != "" {
		b.WriteString(issuePred.matchesCTE)
		b.WriteString(",\n")
		args = append(args, issuePred.args...)
	}
	if walkWisps && wispPred.matchesCTE != "" {
		b.WriteString(wispPred.matchesCTE)
		b.WriteString(",\n")
		args = append(args, wispPred.args...)
	}
	b.WriteString("descendants AS (\n")

	fmt.Fprintf(&b, `    SELECT i.id, 'i' AS src, 'e' AS via
    FROM issues i
    JOIN dependencies d ON d.issue_id = i.id
    WHERE d.type = 'parent-child'
      AND COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id) = ?
      %s`, issuePred.snippet)
	args = append(args, rootID)

	b.WriteString("\n    UNION ALL\n")
	fmt.Fprintf(&b, `    SELECT i.id, 'i' AS src, 'd' AS via
    FROM issues i
    WHERE i.id LIKE CONCAT(?, '.%%')
      AND i.id NOT IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child')
      %s`, issuePred.snippet)
	args = append(args, rootID)

	if walkWisps {
		b.WriteString("\n    UNION ALL\n")
		fmt.Fprintf(&b, `    SELECT w.id, 'w' AS src, 'e' AS via
    FROM wisps w
    JOIN wisp_dependencies wd ON wd.issue_id = w.id
    WHERE wd.type = 'parent-child'
      AND COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id) = ?
      %s`, wispPred.snippet)
		args = append(args, rootID)

		b.WriteString("\n    UNION ALL\n")
		fmt.Fprintf(&b, `    SELECT w.id, 'w' AS src, 'd' AS via
    FROM wisps w
    WHERE w.id LIKE CONCAT(?, '.%%')
      AND w.id NOT IN (SELECT issue_id FROM wisp_dependencies WHERE type = 'parent-child')
      %s`, wispPred.snippet)
		args = append(args, rootID)
	}

	b.WriteString("\n    UNION ALL\n")

	fmt.Fprintf(&b, `    SELECT i.id, 'i' AS src, 'e' AS via
    FROM issues i
    JOIN dependencies d ON d.issue_id = i.id
    JOIN descendants p ON COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id) = p.id
    WHERE d.type = 'parent-child'
      %s`, issuePred.snippet)

	b.WriteString("\n    UNION ALL\n")
	fmt.Fprintf(&b, `    SELECT i.id, 'i' AS src, 'd' AS via
    FROM issues i
    JOIN descendants p ON i.id LIKE CONCAT(p.id, '.%%')
    WHERE p.via = 'e'
      AND i.id NOT IN (SELECT issue_id FROM dependencies WHERE type = 'parent-child')
      %s`, issuePred.snippet)

	if walkWisps {
		b.WriteString("\n    UNION ALL\n")
		fmt.Fprintf(&b, `    SELECT w.id, 'w' AS src, 'e' AS via
    FROM wisps w
    JOIN wisp_dependencies wd ON wd.issue_id = w.id
    JOIN descendants p ON COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id) = p.id
    WHERE wd.type = 'parent-child'
      %s`, wispPred.snippet)

		b.WriteString("\n    UNION ALL\n")
		fmt.Fprintf(&b, `    SELECT w.id, 'w' AS src, 'd' AS via
    FROM wisps w
    JOIN descendants p ON w.id LIKE CONCAT(p.id, '.%%')
    WHERE p.via = 'e'
      AND w.id NOT IN (SELECT issue_id FROM wisp_dependencies WHERE type = 'parent-child')
      %s`, wispPred.snippet)
	}

	b.WriteString("\n)\nSELECT id, src FROM descendants\n")
	return b.String(), args
}
