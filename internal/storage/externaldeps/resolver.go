// Package externaldeps resolves dependencies whose target lives outside the
// source store: explicit cross-project capability references
// (external:<project>:<capability>) and bare cross-rig issue IDs (a target
// whose prefix names another rig, stored in depends_on_external).
package externaldeps

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

const externalPrefix = "external:"

func defaultProjectWarning(project ProjectName) {
	fmt.Fprintf(os.Stderr, "Warning: external project %q is unavailable; its capability dependencies remain blocking\n", project)
}

func defaultIssueWarning(id, reason string) {
	prefix := strings.TrimSuffix(types.ExtractPrefix(id), "-")
	fmt.Fprintf(os.Stderr,
		"Warning: cross-rig dependency target %s cannot be read (%s); it remains blocking. "+
			"Add a route for prefix %q to routes.jsonl, or remove the dependency with `bd dep remove`\n",
		id, reason, prefix)
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

// IssueResolver reads issues another workspace holds, by exact stored ID,
// for the bare cross-rig dependency targets the source store does not hold.
// It answers each ID it was asked about either in found or in unavailable
// (with a short human-readable reason: no route, the workspace refused, the
// row is missing, the read failed). A missing ID must not poison the other IDs
// that share its prefix. The returned error is reserved for an operation
// failure such as context cancellation; any other error leaves every asked ID
// unavailable rather than failing the caller.
//
// A resolver opens what it needs for one call and closes it before returning:
// the policy never caches its answers past one evaluation, so a later query
// observes a foreign close or reopen.
type IssueResolver func(ctx context.Context, ids []string) (found map[string]*types.Issue, unavailable map[string]string, err error)

// Option configures a Store.
type Option func(*Store)

// WithIssueResolver installs the cross-rig issue resolver bare cross-prefix
// blocking targets are looked up through after a miss in the source store.
// Without one, such a target the source store does not hold stays blocking.
func WithIssueResolver(resolve IssueResolver) Option {
	return func(s *Store) { s.resolveIssues = resolve }
}

// referenceKind distinguishes the two shapes of out-of-store target.
type referenceKind uint8

const (
	// kindCapability is an explicit external:<project>:<capability> ref.
	kindCapability referenceKind = iota + 1
	// kindForeignIssue is a bare issue ID whose prefix differs from its
	// source's: a cross-rig issue, stored in depends_on_external.
	kindForeignIssue
)

type reference struct {
	raw        string
	kind       referenceKind
	project    ProjectName
	capability CapabilityName
	valid      bool
}

func parseReference(raw string) reference {
	ref := reference{raw: raw, kind: kindCapability}
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

// isForeignIssueTarget reports whether target is a bare cross-rig issue ID of
// source: the same rule issueops.IsExternalDepTarget stores by, minus the
// external: shape. Classifying from the edge itself keeps the narrow
// GetExternalBlockingDependencyRecords read and the GetAllDependencyRecords
// fallback in agreement.
func isForeignIssueTarget(source, target string) bool {
	if target == "" || isExternalReference(target) {
		return false
	}
	return types.ExtractPrefix(source) != types.ExtractPrefix(target)
}

// outOfStoreReference classifies dep's target: an explicit capability
// reference, a bare cross-rig issue ID, or neither (ok false).
func outOfStoreReference(dep *types.Dependency) (reference, bool) {
	if dep == nil {
		return reference{}, false
	}
	if isExternalReference(dep.DependsOnID) {
		return parseReference(dep.DependsOnID), true
	}
	if isForeignIssueTarget(dep.IssueID, dep.DependsOnID) {
		return reference{raw: dep.DependsOnID, kind: kindForeignIssue, valid: true}, true
	}
	return reference{}, false
}

// enforcesForeignIssueType is the bare cross-rig blocking predicate. It is
// deliberately narrower than types.DependencyType.IsBlockingEdge, which also
// counts waits-for: a cross-rig waits-for has no child set this store could
// aggregate, so it stays informational.
func enforcesForeignIssueType(t types.DependencyType) bool {
	return t == types.DepBlocks || t == types.DepConditionalBlocks
}

// policyKinds names the reference kinds this decorator enforces itself. A
// remote server may already enforce one or both (serverPolicyKinds).
type policyKinds struct {
	capability bool
	foreign    bool
}

var allKinds = policyKinds{capability: true, foreign: true}

func (k policyKinds) none() bool { return !k.capability && !k.foreign }

func (k policyKinds) covers(kind referenceKind) bool {
	switch kind {
	case kindCapability:
		return k.capability
	case kindForeignIssue:
		return k.foreign
	default:
		return false
	}
}

// enforcedReference reports the out-of-store reference dep's edge type makes
// a blocker under kinds: capability refs for every blocking edge type, bare
// cross-rig IDs for blocks and conditional-blocks only.
func enforcedReference(dep *types.Dependency, kinds policyKinds) (reference, bool) {
	ref, ok := outOfStoreReference(dep)
	if !ok || !kinds.covers(ref.kind) {
		return reference{}, false
	}
	switch ref.kind {
	case kindCapability:
		return ref, dep.Type.IsBlockingEdge()
	case kindForeignIssue:
		return ref, enforcesForeignIssueType(dep.Type)
	}
	return reference{}, false
}

// resolution is one policy evaluation's answer for a set of references.
// Every ref starts unsatisfied; malformed refs and foreign-store failures
// therefore fail closed.
type resolution struct {
	satisfied map[string]bool
	// issues holds the observed row of every bare cross-rig target that was
	// read, locally or through the resolver.
	issues map[string]*types.Issue
	// unavailable maps every bare cross-rig target that could not be read to
	// the reason.
	unavailable map[string]string
}

// resolveReferences returns ref -> satisfied.
func (s *Store) resolveReferences(ctx context.Context, refs []reference) (map[string]bool, error) {
	res, err := s.resolve(ctx, refs)
	if err != nil {
		return nil, err
	}
	return res.satisfied, nil
}

func (s *Store) resolve(ctx context.Context, refs []reference) (resolution, error) {
	res := resolution{
		satisfied:   make(map[string]bool, len(refs)),
		issues:      make(map[string]*types.Issue),
		unavailable: make(map[string]string),
	}
	capabilities := make([]reference, 0, len(refs))
	foreign := make([]string, 0)
	for _, ref := range refs {
		res.satisfied[ref.raw] = false
		switch ref.kind {
		case kindForeignIssue:
			if !slices.Contains(foreign, ref.raw) {
				foreign = append(foreign, ref.raw)
			}
		default:
			capabilities = append(capabilities, ref)
		}
	}
	if err := s.resolveCapabilities(ctx, capabilities, res.satisfied); err != nil {
		return resolution{}, err
	}
	if err := s.resolveForeignIssues(ctx, foreign, &res); err != nil {
		return resolution{}, err
	}
	return res, nil
}

func (s *Store) resolveCapabilities(ctx context.Context, refs []reference, result map[string]bool) error {
	byProject := make(map[ProjectName]map[CapabilityName][]string)
	for _, ref := range refs {
		if !ref.valid {
			continue
		}
		if byProject[ref.project] == nil {
			byProject[ref.project] = make(map[CapabilityName][]string)
		}
		byProject[ref.project][ref.capability] = append(byProject[ref.project][ref.capability], ref.raw)
	}

	if s.locateProject == nil || s.openProject == nil {
		return nil
	}

	for project, capabilities := range byProject {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, ok := s.locateProject(project)
		if !ok {
			s.warnUnresolvedProject(project)
			continue
		}
		foreign, err := s.openProject(ctx, path)
		if err != nil || foreign == nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			s.warnUnresolvedProject(project)
			continue
		}

		for capability, rawRefs := range capabilities {
			issues, queryErr := foreign.GetIssuesByLabel(ctx, "provides:"+string(capability))
			if queryErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					_ = foreign.Close()
					return ctxErr
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

	return nil
}

// foreignIssueSatisfies is the direct-block rule for an observed target:
// closed or pinned releases the edge, every other status holds it.
func foreignIssueSatisfies(issue *types.Issue) bool {
	return issue != nil && (issue.Status == types.StatusClosed || issue.Status == types.StatusPinned)
}

// readForeignIssues reads each bare cross-rig target in the source store
// first (a differently prefixed row may live here), then the rest through the
// issue resolver. Every id lands in exactly one of the two maps: its observed
// row, or the reason it could not be read. It warns about nothing.
func (s *Store) readForeignIssues(ctx context.Context, ids []string) (map[string]*types.Issue, map[string]string, error) {
	issues := make(map[string]*types.Issue, len(ids))
	unavailable := make(map[string]string)
	if len(ids) == 0 {
		return issues, unavailable, nil
	}
	local, err := s.localIssues(ctx, ids)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		return nil, nil, fmt.Errorf("read cross-rig dependency targets in the source store: %w", err)
	}
	for _, issue := range local {
		if issue != nil && slices.Contains(ids, issue.ID) {
			issues[issue.ID] = issue
		}
	}
	remaining := make([]string, 0, len(ids))
	for _, id := range ids {
		if issues[id] == nil {
			remaining = append(remaining, id)
		}
	}
	if len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		var found map[string]*types.Issue
		reasons := map[string]string{}
		if s.resolveIssues == nil {
			for _, id := range remaining {
				reasons[id] = "no cross-rig routing is available here"
			}
		} else {
			var resolveErr error
			found, reasons, resolveErr = s.resolveIssues(ctx, remaining)
			if resolveErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, nil, ctxErr
				}
				found, reasons = nil, make(map[string]string, len(remaining))
				for _, id := range remaining {
					reasons[id] = resolveErr.Error()
				}
			}
		}
		for _, id := range remaining {
			if issue := found[id]; issue != nil {
				issues[id] = issue
				continue
			}
			reason := reasons[id]
			if reason == "" {
				reason = "not found"
			}
			unavailable[id] = reason
		}
	}
	return issues, unavailable, nil
}

// resolveForeignIssues is readForeignIssues for a policy evaluation: an
// unreadable target stays unsatisfied, with a warning.
func (s *Store) resolveForeignIssues(ctx context.Context, ids []string, res *resolution) error {
	if len(ids) == 0 {
		return nil
	}
	issues, unavailable, err := s.readForeignIssues(ctx, ids)
	if err != nil {
		return err
	}
	for id, issue := range issues {
		res.issues[id] = issue
	}
	for id, reason := range unavailable {
		res.unavailable[id] = reason
	}
	for _, id := range ids {
		res.satisfied[id] = foreignIssueSatisfies(res.issues[id])
	}
	unread := make([]string, 0, len(res.unavailable))
	for id := range res.unavailable {
		unread = append(unread, id)
	}
	sort.Strings(unread)
	for _, id := range unread {
		s.warnUnresolvedIssue(id, res.unavailable[id])
	}
	return nil
}

// localIssues reads ids in the source store: through the bound lookup on the
// unit-of-work path, through the served BatchGetter over a remote store, and
// through GetIssuesByIDs otherwise.
func (s *Store) localIssues(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if s.localLookup != nil {
		return s.localLookup(ctx, ids)
	}
	if s.inner == nil {
		return nil, nil
	}
	if s.rolesAreRemote() {
		return s.remoteIssuesByIDs(ctx, ids)
	}
	return s.inner.GetIssuesByIDs(ctx, ids)
}
