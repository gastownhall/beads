package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// DetailBatchPolicy selects the existing Reader.Get behavior of a route. The
// stores select labels and comments by wisp existence; UOW selects them by the
// winning subject plane. Only direct-server selects one outgoing edge table
// and joins incoming sources to their own plane.
type DetailBatchPolicy int

const (
	DetailBatchEmbedded DetailBatchPolicy = iota
	DetailBatchDirectServer
	DetailBatchUOW
)

// ExecuteDetailBatch is the shared DBTX body for one read snapshot. All SQL is
// batched at queryBatchSize, including far-end hydration. Assembly below uses
// BuildIssueDetails over collected maps, so it cannot run per-ID SQL or swallow
// a component read error through BuildIssueDetails's best-effort methods.
func ExecuteDetailBatch(ctx context.Context, tx DBTX, request publicops.DetailBatchRequest, policy DetailBatchPolicy) (publicops.DetailBatchResult, error) {
	if err := publicops.ValidateDetailBatchRequest(request); err != nil {
		return publicops.DetailBatchResult{}, err
	}
	request.IDs = append([]string(nil), request.IDs...)
	if len(request.IDs) == 0 {
		return publicops.DetailBatchResult{Items: []publicops.DetailBatchItem{}}, nil
	}
	if policy != DetailBatchEmbedded && policy != DetailBatchDirectServer && policy != DetailBatchUOW {
		return publicops.DetailBatchResult{}, fmt.Errorf("detail batch: unknown route policy %d", policy)
	}
	ids := EdgeReadAnchors(request.IDs)
	subjects, subjectWisps, err := detailBatchSubjectsInTx(ctx, tx, ids)
	if err != nil {
		return publicops.DetailBatchResult{}, err
	}
	foundIDs := make([]string, 0, len(subjects))
	for _, id := range ids {
		if subjects[id] != nil {
			foundIDs = append(foundIDs, id)
		}
	}
	auxWisps := subjectWisps
	if policy != DetailBatchUOW {
		auxWisps, err = WispIDSetInTx(ctx, tx, foundIDs)
		if err != nil {
			return publicops.DetailBatchResult{}, err
		}
	}
	outgoing, err := detailBatchEdgesInTx(ctx, tx, foundIDs, auxWisps, policy, false)
	if err != nil {
		return publicops.DetailBatchResult{}, err
	}
	var incoming []detailBatchEdge
	var dependents map[string][]*types.IssueWithDependencyMetadata
	if request.IncludeDependents {
		if policy == DetailBatchDirectServer {
			dependents, err = detailBatchServerDependentsInTx(ctx, tx, foundIDs)
		} else {
			incoming, err = detailBatchEdgesInTx(ctx, tx, foundIDs, nil, policy, true)
		}
		if err != nil {
			return publicops.DetailBatchResult{}, err
		}
	}
	farIDs := make([]string, 0, len(outgoing)+len(incoming))
	for _, edges := range [][]detailBatchEdge{outgoing, incoming} {
		for _, edge := range edges {
			farIDs = append(farIDs, edge.farID)
		}
	}
	farIDs = EdgeReadAnchors(farIDs)
	// Far ends can be wisps even when every subject is durable. This set must
	// be scoped to the far-end IDs, not reused from the subject routing probe.
	farWisps, err := WispIDSetInTx(ctx, tx, farIDs)
	if err != nil {
		return publicops.DetailBatchResult{}, err
	}
	farIssues, err := GetIssuesByIDsInTx(ctx, tx, farIDs, farWisps)
	if err != nil {
		return publicops.DetailBatchResult{}, err
	}
	farMap := make(map[string]*types.Issue, len(farIssues))
	for _, issue := range farIssues {
		farMap[issue.ID] = issue
	}
	src := detailBatchSource{
		subjects: subjects, subjectWisps: subjectWisps,
		dependencies: detailBatchNeighbors(outgoing, farMap),
		dependents:   dependents,
		labels:       make(map[string][]string), commentCounts: make(map[string]int),
		comments: make(map[string][]*types.Comment),
	}
	if policy != DetailBatchDirectServer {
		src.dependents = detailBatchNeighbors(incoming, farMap)
	}
	src.dependencyCounts, err = tallyEdgesInTx(ctx, tx, foundIDs, publicops.EdgeCountRequest{Direction: publicops.EdgeDirectionOut})
	if err != nil {
		return publicops.DetailBatchResult{}, err
	}
	src.dependentCounts, err = tallyEdgesInTx(ctx, tx, foundIDs, publicops.EdgeCountRequest{Direction: publicops.EdgeDirectionIn})
	if err != nil {
		return publicops.DetailBatchResult{}, err
	}
	wispIDs, permIDs := partitionByWispSet(foundIDs, auxWisps)
	for _, plane := range []struct {
		labels, comments string
		ids              []string
	}{
		{"labels", "comments", permIDs},
		{"wisp_labels", "wisp_comments", wispIDs},
	} {
		labels, err := GetLabelsForIssuesFromTableInTx(ctx, tx, plane.labels, plane.ids)
		if err != nil {
			return publicops.DetailBatchResult{}, err
		}
		for id, values := range labels {
			src.labels[id] = values
		}
		if err := getCommentCountsForIDsInto(ctx, tx, plane.comments, plane.ids, src.commentCounts); err != nil {
			return publicops.DetailBatchResult{}, err
		}
		if request.IncludeComments {
			if err := getCommentsForIDsInto(ctx, tx, plane.comments, plane.ids, src.comments); err != nil {
				return publicops.DetailBatchResult{}, err
			}
		}
	}
	out := publicops.DetailBatchResult{Items: make([]publicops.DetailBatchItem, 0, len(request.IDs))}
	for _, id := range request.IDs {
		item := publicops.DetailBatchItem{ID: id}
		if issue := subjects[id]; issue != nil {
			_, isWisp := subjectWisps[id]
			item.Issue, err = workapi.BuildIssueDetails(ctx, src, issue, isWisp, workapi.DetailOptions{
				IncludeDependents: request.IncludeDependents,
				IncludeComments:   request.IncludeComments,
				BriefDeps:         request.BriefDeps,
			})
			if err != nil {
				return publicops.DetailBatchResult{}, err
			}
			item.Found = true
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// detailBatchSubjectsInTx is durable-first, unlike GetIssuesByIDsInTx's
// wisp-existence partition. An ID in both planes must retain the durable row.
func detailBatchSubjectsInTx(ctx context.Context, tx DBTX, ids []string) (map[string]*types.Issue, map[string]struct{}, error) {
	subjects := make(map[string]*types.Issue, len(ids))
	wisps := make(map[string]struct{})
	for _, table := range []string{"issues", "wisps"} {
		remaining := make([]string, 0, len(ids))
		for _, id := range ids {
			if subjects[id] == nil {
				remaining = append(remaining, id)
			}
		}
		for start := 0; start < len(remaining); start += queryBatchSize {
			batch := remaining[start:min(start+queryBatchSize, len(remaining))]
			placeholders, args := buildSQLInClause(batch)
			//nolint:gosec // G201: table is hardcoded and the IN clause contains only placeholders.
			rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s %s WHERE id IN (%s)",
				IssueSelectColumns, table, sqlbuild.LeaseJoin(table), placeholders), args...)
			if err != nil {
				// GetIssueInTx reads a missing optional wisps table as "row absent".
				if missingOptionalIssueTable(err, table) {
					break
				}
				return nil, nil, fmt.Errorf("detail batch subjects from %s: %w", table, err)
			}
			for rows.Next() {
				issue, err := ScanIssueFrom(rows)
				if err != nil {
					_ = rows.Close()
					return nil, nil, fmt.Errorf("detail batch subjects: scan: %w", err)
				}
				subjects[issue.ID] = issue
				if table == "wisps" {
					wisps[issue.ID] = struct{}{}
				}
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, nil, fmt.Errorf("detail batch subjects: rows: %w", err)
			}
		}
	}
	return subjects, wisps, nil
}

type detailBatchEdge struct {
	anchor, farID string
	depType       types.DependencyType
}

func detailBatchEdgesInTx(ctx context.Context, tx DBTX, ids []string, wispSet map[string]struct{}, policy DetailBatchPolicy, incoming bool) ([]detailBatchEdge, error) {
	permIDs, wispIDs := ids, ids
	if policy == DetailBatchDirectServer {
		wispIDs, permIDs = partitionByWispSet(ids, wispSet)
	}
	var edges []detailBatchEdge
	for _, plane := range []struct {
		table string
		ids   []string
	}{
		{"dependencies", permIDs},
		{"wisp_dependencies", wispIDs},
	} {
		for start := 0; start < len(plane.ids); start += queryBatchSize {
			batch := plane.ids[start:min(start+queryBatchSize, len(plane.ids))]
			placeholders, args := buildSQLInClause(batch)
			anchor, farID, order := "issue_id", DepTargetExpr, "issue_id, depends_on_external, id"
			if incoming {
				anchor, farID, order = DepTargetExpr, "issue_id", "id"
			}
			//nolint:gosec // G201: table/columns/order are hardcoded; the IN clause contains only placeholders.
			rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT %s, %s, type FROM %s WHERE %s IN (%s) ORDER BY %s",
				anchor, farID, plane.table, anchor, placeholders, order), args...)
			if err != nil {
				if optionalBlockedTable(plane.table) && isTableNotExistError(err) {
					break
				}
				return nil, fmt.Errorf("detail batch edges from %s: %w", plane.table, err)
			}
			for rows.Next() {
				var edge detailBatchEdge
				if err := rows.Scan(&edge.anchor, &edge.farID, &edge.depType); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("detail batch edges: scan: %w", err)
				}
				edges = append(edges, edge)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("detail batch edges: rows: %w", err)
			}
		}
	}
	return edges, nil
}

func detailBatchNeighbors(edges []detailBatchEdge, farIssues map[string]*types.Issue) map[string][]*types.IssueWithDependencyMetadata {
	out := make(map[string][]*types.IssueWithDependencyMetadata)
	for _, edge := range edges {
		if issue := farIssues[edge.farID]; issue != nil {
			out[edge.anchor] = append(out[edge.anchor], &types.IssueWithDependencyMetadata{
				Issue: *issue, DependencyType: edge.depType,
			})
		}
	}
	return out
}

// The server iterator joins each source to its own plane and orders by source
// creation time. Only the fields BuildIssueDetails keeps in its shallow
// dependents are selected; the trailing columns pin the observed tie order.
func detailBatchServerDependentsInTx(ctx context.Context, tx DBTX, ids []string) (map[string][]*types.IssueWithDependencyMetadata, error) {
	out := make(map[string][]*types.IssueWithDependencyMetadata)
	for start := 0; start < len(ids); start += queryBatchSize {
		batch := ids[start:min(start+queryBatchSize, len(ids))]
		placeholders, args := buildSQLInClause(batch)
		//nolint:gosec // G201: expressions and the placeholder-only IN clause are generated from fixed columns.
		query := fmt.Sprintf(`
			SELECT i.id AS subject_id, i.title, i.status, i.priority, i.issue_type, i.close_reason,
			       d.type, %s AS anchor, i.created_at, 0 AS plane,
			       d.depends_on_wisp_id, d.id AS edge_id
			FROM issues i JOIN dependencies d ON d.issue_id = i.id
			WHERE %s
			UNION ALL
			SELECT w.id AS subject_id, w.title, w.status, w.priority, w.issue_type, w.close_reason,
			       d.type, %s AS anchor, w.created_at, 1 AS plane,
			       d.depends_on_wisp_id, d.id AS edge_id
			FROM wisps w JOIN wisp_dependencies d ON d.issue_id = w.id
			WHERE %s
			ORDER BY created_at, plane, subject_id, depends_on_wisp_id, edge_id
		`, depTargetExpr("d"), depTargetIn("d", placeholders), depTargetExpr("d"), depTargetIn("d", placeholders))
		args = append(args, args...)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("detail batch server dependents: %w", err)
		}
		for rows.Next() {
			var item types.IssueWithDependencyMetadata
			var anchor, edgeID string
			var closeReason, createdAt, wispTarget sql.NullString
			var plane int
			if err := rows.Scan(&item.ID, &item.Title, &item.Status, &item.Priority, &item.IssueType, &closeReason,
				&item.DependencyType, &anchor, &createdAt, &plane, &wispTarget, &edgeID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("detail batch server dependents: scan: %w", err)
			}
			item.CloseReason = closeReason.String
			out[anchor] = append(out[anchor], &item)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("detail batch server dependents: rows: %w", err)
		}
	}
	return out, nil
}

// detailBatchSource holds only successful reads. Its methods do no SQL: errors
// are handled before assembly, not hidden by the assembler's best-effort fields.
type detailBatchSource struct {
	subjects                          map[string]*types.Issue
	subjectWisps                      map[string]struct{}
	labels                            map[string][]string
	dependencies, dependents          map[string][]*types.IssueWithDependencyMetadata
	dependencyCounts, dependentCounts map[string]int64
	commentCounts                     map[string]int
	comments                          map[string][]*types.Comment
}

var _ workapi.DetailSource = detailBatchSource{}

func (s detailBatchSource) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if _, isWisp := s.subjectWisps[id]; !isWisp && s.subjects[id] != nil {
		return s.subjects[id], nil
	}
	return nil, storage.ErrNotFound
}

func (s detailBatchSource) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	if _, isWisp := s.subjectWisps[id]; isWisp && s.subjects[id] != nil {
		return s.subjects[id], nil
	}
	return nil, storage.ErrNotFound
}

func (s detailBatchSource) Labels(_ context.Context, id string, _ bool) ([]string, error) {
	return s.labels[id], nil
}

func (s detailBatchSource) Dependencies(_ context.Context, id string, _ bool) ([]*types.IssueWithDependencyMetadata, error) {
	return s.dependencies[id], nil
}

func (s detailBatchSource) CountDependencies(_ context.Context, id string, _ bool) (int64, error) {
	return s.dependencyCounts[id], nil
}

func (s detailBatchSource) CountDependents(_ context.Context, id string, _ bool) (int64, error) {
	return s.dependentCounts[id], nil
}

func (s detailBatchSource) CountComments(_ context.Context, id string, _ bool) (int64, error) {
	return int64(s.commentCounts[id]), nil
}

func (s detailBatchSource) IterDependents(_ context.Context, id string, _ bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return storage.NewSliceIter(s.dependents[id]), nil
}

func (s detailBatchSource) IterComments(_ context.Context, id string, _ bool) (storage.Iter[types.Comment], error) {
	return storage.NewSliceIter(s.comments[id]), nil
}
