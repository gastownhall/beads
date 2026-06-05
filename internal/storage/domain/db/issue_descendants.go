package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
)

type predBundle struct {
	snippet string
	args    []any
}

func buildDescendantsPred(table, alias string, clauses []string, args []any) predBundle {
	if len(clauses) == 0 {
		return predBundle{}
	}
	return predBundle{
		snippet: fmt.Sprintf(" AND %s.id IN (SELECT id FROM %s WHERE %s)",
			alias, table, strings.Join(clauses, " AND ")),
		args: args,
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

	issuePred := buildDescendantsPred("issues", "i", issueWhereClauses, issueArgs)
	var wispPred predBundle
	if walkWisps {
		wispPred = buildDescendantsPred("wisps", "w", wispWhereClauses, wispArgs)
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

func buildDescendantsCTE(rootID string, walkWisps bool, issuePred, wispPred predBundle) (string, []any) {
	var b strings.Builder
	var args []any
	b.WriteString("WITH RECURSIVE descendants AS (\n")

	fmt.Fprintf(&b, `    SELECT i.id, 'i' AS src
    FROM issues i
    JOIN dependencies d ON d.issue_id = i.id
    WHERE d.type = 'parent-child'
      AND COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id) = ?
      %s`, issuePred.snippet)
	args = append(args, rootID)
	args = append(args, issuePred.args...)

	if walkWisps {
		b.WriteString("\n    UNION ALL\n")
		fmt.Fprintf(&b, `    SELECT w.id, 'w' AS src
    FROM wisps w
    JOIN wisp_dependencies wd ON wd.issue_id = w.id
    WHERE wd.type = 'parent-child'
      AND COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id) = ?
      %s`, wispPred.snippet)
		args = append(args, rootID)
		args = append(args, wispPred.args...)
	}

	b.WriteString("\n    UNION ALL\n")

	fmt.Fprintf(&b, `    SELECT i.id, 'i' AS src
    FROM issues i
    JOIN dependencies d ON d.issue_id = i.id
    JOIN descendants p ON COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id) = p.id
    WHERE d.type = 'parent-child'
      %s`, issuePred.snippet)
	args = append(args, issuePred.args...)

	if walkWisps {
		b.WriteString("\n    UNION ALL\n")
		fmt.Fprintf(&b, `    SELECT w.id, 'w' AS src
    FROM wisps w
    JOIN wisp_dependencies wd ON wd.issue_id = w.id
    JOIN descendants p ON COALESCE(wd.depends_on_issue_id, wd.depends_on_wisp_id) = p.id
    WHERE wd.type = 'parent-child'
      %s`, wispPred.snippet)
		args = append(args, wispPred.args...)
	}

	b.WriteString("\n)\nSELECT id, src FROM descendants\n")
	return b.String(), args
}
