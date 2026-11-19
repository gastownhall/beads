// Package externaldeps resolves explicit cross-project capability dependencies.
package externaldeps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

const externalPrefix = "external:"

func defaultProjectWarning(project ProjectName) {
	fmt.Fprintf(os.Stderr, "Warning: external project %q is unavailable; its capability dependencies remain blocking\n", project)
}

// ProjectName is the configured name in external_projects.
type ProjectName string

// CapabilityName is the suffix matched by a provides:<capability> label.
type CapabilityName string

// ProjectLocator resolves a configured project name to its workspace root.
// The bool is false when the project is not configured or unavailable.
type ProjectLocator func(ProjectName) (string, bool)

// StoreOpener opens a project workspace as a read-only store.
type StoreOpener func(context.Context, string) (storage.DoltStorage, error)

// IssueRoute is where a routed cross-prefix issue lives. Key names the
// destination store, so issues routed to one store are read through one open
// of it; Open opens that store for reading, and the caller closes it.
type IssueRoute struct {
	Key  string
	Open func(context.Context) (storage.DoltStorage, error)
}

// IssueRouter finds the store that owns issueID, a bare issue ID whose prefix
// differs from the dependent issue's (GH#4647). It, or the route's Open,
// returns an error wrapping ErrNoIssueRoute when no route is configured for
// the ID or the route leads to a store this process cannot read issues from;
// any other error means a configured route failed.
type IssueRouter func(ctx context.Context, issueID string) (IssueRoute, error)

// ErrNoIssueRoute reports that a cross-prefix issue has no usable route.
var ErrNoIssueRoute = errors.New("no route for issue")

// Option configures a Store.
type Option func(*Store)

// WithIssueRouter makes the policy enforce bare cross-prefix blocks and
// conditional-blocks edges (GH#4647): such an edge's target is read from the
// local store or, when absent there, from the store router names, and the
// dependent stays blocked while that target is neither closed nor pinned.
// Without a router those edges are not evaluated here.
func WithIssueRouter(router IssueRouter) Option {
	return func(s *Store) { s.routeIssue = router }
}

func defaultRouteWarning(prefix string) {
	fmt.Fprintf(os.Stderr, "Warning: issues with prefix %q are unavailable through their route; dependencies blocked on them remain blocking\n", prefix)
}

type reference struct {
	raw        string
	project    ProjectName
	capability CapabilityName
	valid      bool
	// issue marks a bare cross-prefix issue ID rather than an external:
	// capability reference.
	issue bool
}

// policyReference classifies one stored edge of sourceID: an external:
// reference on a blocking edge, or — when an issue router is configured and
// the store's roles are local — a blocks / conditional-blocks edge to a bare
// issue ID whose prefix differs from the source's. Every other edge is left
// to the local store.
func (s *Store) policyReference(sourceID string, dep *types.Dependency) (reference, bool) {
	if dep == nil {
		return reference{}, false
	}
	if isExternalReference(dep.DependsOnID) {
		if !dep.Type.IsBlockingEdge() {
			return reference{}, false
		}
		return parseReference(dep.DependsOnID), true
	}
	if s.routeIssue == nil || (dep.Type != types.DepBlocks && dep.Type != types.DepConditionalBlocks) {
		return reference{}, false
	}
	targetPrefix := types.ExtractPrefix(dep.DependsOnID)
	if targetPrefix == "" || targetPrefix == types.ExtractPrefix(sourceID) || s.rolesAreRemote() {
		return reference{}, false
	}
	return reference{raw: dep.DependsOnID, issue: true}, true
}

func parseReference(raw string) reference {
	ref := reference{raw: raw}
	if !strings.HasPrefix(raw, externalPrefix) {
		return ref
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return ref
	}
	ref.project = ProjectName(parts[1])
	ref.capability = CapabilityName(parts[2])
	ref.valid = true
	return ref
}

func isExternalReference(raw string) bool {
	return strings.HasPrefix(raw, externalPrefix)
}

// resolveReferences returns ref -> satisfied. Every capability ref starts
// unsatisfied; malformed refs and foreign-store failures therefore fail
// closed. Cross-prefix issue refs are resolved by resolveIssueReferences.
func (s *Store) resolveReferences(ctx context.Context, refs []reference) (map[string]bool, error) {
	result := make(map[string]bool, len(refs))
	byProject := make(map[ProjectName]map[CapabilityName][]string)
	var issueIDs []string
	for _, ref := range refs {
		if ref.issue {
			if _, seen := result[ref.raw]; !seen {
				issueIDs = append(issueIDs, ref.raw)
			}
			result[ref.raw] = false
			continue
		}
		result[ref.raw] = false
		if !ref.valid {
			continue
		}
		if byProject[ref.project] == nil {
			byProject[ref.project] = make(map[CapabilityName][]string)
		}
		byProject[ref.project][ref.capability] = append(byProject[ref.project][ref.capability], ref.raw)
	}

	if len(issueIDs) > 0 {
		if err := s.resolveIssueReferences(ctx, issueIDs, result); err != nil {
			return nil, err
		}
	}

	if s.locateProject == nil || s.openProject == nil {
		return result, nil
	}

	for project, capabilities := range byProject {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, ok := s.locateProject(project)
		if !ok {
			s.warnUnresolvedProject(project)
			continue
		}
		foreign, err := s.openProject(ctx, path)
		if err != nil || foreign == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			s.warnUnresolvedProject(project)
			continue
		}

		for capability, rawRefs := range capabilities {
			issues, queryErr := foreign.GetIssuesByLabel(ctx, "provides:"+string(capability))
			if queryErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					_ = foreign.Close()
					return nil, ctxErr
				}
				s.warnUnresolvedProject(project)
				continue
			}
			satisfied := false
			for _, issue := range issues {
				if issue != nil && issue.Status == types.StatusClosed {
					satisfied = true
					break
				}
			}
			if satisfied {
				for _, raw := range rawRefs {
					result[raw] = true
				}
			}
		}
		_ = foreign.Close()
	}

	return result, nil
}

// issueSatisfies reports whether a blocker in this status no longer blocks,
// the rule the local blocked computation applies to blocks edges.
func issueSatisfies(status types.Status) bool {
	return status == types.StatusClosed || status == types.StatusPinned
}

// resolveIssueReferences sets result[id] for each cross-prefix issue ID
// (GH#4647). The local store is read first, in one batch, since one database
// can hold several prefixes. Each ID it lacks is routed; the IDs routed to one
// store are read there in one batch, opening that store once.
//
//   - a target found closed or pinned is satisfied, any other status blocks;
//   - a target absent from the store that owns its prefix does not block;
//   - an ID with no usable route (ErrNoIssueRoute) does not block, silently;
//   - a configured route that fails (lookup, open, or read) blocks, with one
//     warning per prefix for the life of this policy.
//
// A failed local read and a canceled context are returned as errors.
func (s *Store) resolveIssueReferences(ctx context.Context, ids []string, result map[string]bool) error {
	local, err := s.inner.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("read cross-prefix blockers: %w", err)
	}
	found := make(map[string]bool, len(local))
	for _, issue := range local {
		if issue != nil {
			found[issue.ID] = true
			result[issue.ID] = issueSatisfies(issue.Status)
		}
	}

	type routeGroup struct {
		route IssueRoute
		ids   []string
	}
	groups := make(map[string]*routeGroup)
	var order []string
	for _, id := range ids {
		if found[id] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		route, routeErr := s.routeIssue(ctx, id)
		if routeErr != nil {
			if errors.Is(routeErr, ErrNoIssueRoute) {
				result[id] = true
				continue
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			s.warnUnavailableRoute(types.ExtractPrefix(id))
			continue
		}
		group := groups[route.Key]
		if group == nil {
			group = &routeGroup{route: route}
			groups[route.Key] = group
			order = append(order, route.Key)
		}
		group.ids = append(group.ids, id)
	}

	for _, key := range order {
		group := groups[key]
		satisfied, readErr := s.readRoutedIssues(ctx, group.route, group.ids)
		if readErr == nil {
			for id, ok := range satisfied {
				result[id] = ok
			}
			continue
		}
		if errors.Is(readErr, ErrNoIssueRoute) {
			for _, id := range group.ids {
				result[id] = true
			}
			continue
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		for _, id := range group.ids {
			s.warnUnavailableRoute(types.ExtractPrefix(id))
		}
	}
	return nil
}

// readRoutedIssues opens route's store, reads ids there in one batch and
// closes it, answering id -> satisfied; an ID the store lacks is satisfied.
func (s *Store) readRoutedIssues(ctx context.Context, route IssueRoute, ids []string) (map[string]bool, error) {
	if route.Open == nil {
		return nil, fmt.Errorf("route %q has no opener", route.Key)
	}
	foreign, err := route.Open(ctx)
	if err != nil {
		return nil, err
	}
	if foreign == nil {
		return nil, fmt.Errorf("route %q opened no store", route.Key)
	}
	defer func() { _ = foreign.Close() }()
	issues, err := foreign.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	satisfied := make(map[string]bool, len(ids))
	for _, id := range ids {
		satisfied[id] = true
	}
	for _, issue := range issues {
		if issue != nil {
			if _, asked := satisfied[issue.ID]; asked {
				satisfied[issue.ID] = issueSatisfies(issue.Status)
			}
		}
	}
	return satisfied, nil
}

func (s *Store) warnUnavailableRoute(prefix string) {
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	if _, warned := s.warnedRoutes[prefix]; warned {
		return
	}
	s.warnedRoutes[prefix] = struct{}{}
	if s.warnRoute != nil {
		s.warnRoute(prefix)
	}
}
