package issueops

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/types"
)

// InProgressMoleculeIDs answers the molecules with a step in progress for
// agent (every agent when agent is empty), each once, in the order the
// listing first reached a step of it.
//
// The listing is Reader.List over every plane and class a step can live in —
// templates, gates, infra and ephemeral rows, the four knobs rather than
// IncludeAllTypes, because the v0 wire publishes each knob and refuses the
// union. A read the backend refuses is returned, never answered as "none".
func InProgressMoleculeIDs(ctx context.Context, reader Reader, r MoleculeReader, agent string) ([]string, error) {
	steps, err := listMoleculeCandidates(ctx, reader, types.StatusInProgress, agent)
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, nil
	}
	ids := make([]string, len(steps))
	for i, step := range steps {
		ids[i] = step.ID
	}
	roots, err := MoleculeRoots(ctx, r, ids)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if root := roots[id]; root != "" && !seen[root] {
			seen[root] = true
			out = append(out, root)
		}
	}
	return out, nil
}

// ActiveMoleculeIDs answers the molecules agent is working on, sorted: the
// ones with a step in progress or, when there are none, the ones its HOOKED
// work is attached to — a hooked epic itself, or an epic or template the
// hooked issue is blocked by.
func ActiveMoleculeIDs(ctx context.Context, reader Reader, r MoleculeReader, agent string) ([]string, error) {
	ids, err := InProgressMoleculeIDs(ctx, reader, r, agent)
	if err != nil {
		return nil, fmt.Errorf("finding molecules in progress: %w", err)
	}
	if len(ids) == 0 {
		ids, err = hookedMoleculeIDs(ctx, reader, r, agent)
		if err != nil {
			return nil, fmt.Errorf("finding hooked molecules: %w", err)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func hookedMoleculeIDs(ctx context.Context, reader Reader, r MoleculeReader, agent string) ([]string, error) {
	hooked, err := listMoleculeCandidates(ctx, reader, types.StatusHooked, agent)
	if err != nil || len(hooked) == 0 {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	var attached []string
	for _, issue := range hooked {
		// A hooked epic IS the molecule (a patrol wisp hooked without a
		// separate handoff bead).
		if issue.IssueType == types.TypeEpic {
			add(issue.ID)
			continue
		}
		attached = append(attached, issue.ID)
	}
	if len(attached) == 0 {
		return out, nil
	}
	edges, err := r.ReadEdges(ctx, EdgeReadRequest{IDs: attached, Types: []types.DependencyType{types.DepBlocks}})
	if err != nil {
		return nil, fmt.Errorf("reading the blockers of hooked work: %w", err)
	}
	var blockers []string
	for _, anchor := range edges.Anchors {
		for _, edge := range anchor.Edges {
			if edge != nil && edge.Type == types.DepBlocks {
				blockers = append(blockers, edge.DependsOnID)
			}
		}
	}
	candidates, err := getIssues(ctx, r, blockers)
	if err != nil {
		return nil, err
	}
	for _, id := range blockers {
		// An external or dangling blocker names nothing to load.
		if candidate := candidates[id]; candidate != nil &&
			(candidate.IssueType == types.TypeEpic || hasLabel(candidate, MoleculeTemplateLabel)) {
			add(id)
		}
	}
	return out, nil
}

func listMoleculeCandidates(ctx context.Context, reader Reader, status types.Status, agent string) ([]*Issue, error) {
	if reader == nil {
		return nil, fmt.Errorf("%w: listing molecule work needs a reader", ErrValidation)
	}
	unlimited := 0
	page, err := reader.List(ctx, ListRequest{
		Status:           string(status),
		Assignee:         agent,
		IncludeTemplates: true,
		IncludeGates:     true,
		IncludeInfra:     true,
		IncludeEphemeral: true,
		Limit:            &unlimited,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*Issue, 0, len(page.Items))
	for _, row := range page.Items {
		if row != nil && row.Issue != nil {
			out = append(out, row.Issue)
		}
	}
	return out, nil
}
