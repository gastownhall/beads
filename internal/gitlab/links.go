package gitlab

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

const (
	gitLabLinkBlocks      = "blocks"
	gitLabLinkIsBlockedBy = "is_blocked_by"
	gitLabLinkRelatesTo   = "relates_to"
)

// DependencyLink is a GitLab issue-link operation derived from a beads
// dependency. SourceIID and TargetIID are GitLab project-scoped issue/work-item
// IIDs in the direction expected by GitLab's issue-links API.
type DependencyLink struct {
	SourceIID      int
	TargetIID      int
	LinkType       string
	FromBeadsID    string
	ToBeadsID      string
	DependencyType types.DependencyType
}

// PushLinkOptions configures dependency link push behavior.
type PushLinkOptions struct {
	DryRun bool
	OnPlan func(DependencyLink)
}

// PushLinkResult summarizes a PushLinks pass. LicenseSkipped counts
// blocks/is_blocked_by links that GitLab rejected because the instance license
// lacks the issue-blocking feature (Premium/Ultimate). These are an expected,
// non-fatal degradation and are kept separate from Errors, which holds genuine
// (non-license) failures the caller should surface as real warnings.
type PushLinkResult struct {
	Created        int
	LicenseSkipped int
	Errors         []error
}

// EpicMilestoneOptions configures the dependency-pass epic milestone repair.
type EpicMilestoneOptions struct {
	DryRun bool
	OnPlan func(issueID string, issueIID int, milestoneID int)
}

// LinkResolver handles GitLab issue-link convergence.
type LinkResolver struct {
	Client *Client
}

// NewLinkResolver creates a GitLab dependency link resolver.
func NewLinkResolver(client *Client) *LinkResolver {
	return &LinkResolver{Client: client}
}

// IssueIIDFromRef extracts a GitLab issue/work-item IID from a local external
// ref. Milestone refs are intentionally rejected because epics map to
// milestones, not issue-link endpoints.
func IssueIIDFromRef(ref string) (int, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || milestoneIDPattern.MatchString(ref) {
		return 0, false
	}
	if matches := glShorthandPattern.FindStringSubmatch(ref); len(matches) >= 2 {
		iid, err := strconv.Atoi(matches[1])
		return iid, err == nil && iid > 0
	}
	if matches := issueIIDPattern.FindStringSubmatch(ref); len(matches) >= 2 {
		iid, err := strconv.Atoi(matches[1])
		return iid, err == nil && iid > 0
	}
	return 0, false
}

// parseGitLabURLRef splits a URL-shaped ref into its canonical host and
// lowercase path segments: scheme, query, and fragment are ignored; the host
// is lowercased with the default ports :80 and :443 dropped; repeated slashes
// collapse and a trailing slash is dropped; the path is lowercased because
// GitLab routes are case-insensitive. ok is false for refs without a scheme
// or host (the "gitlab:{iid}" shorthand is not a URL).
func parseGitLabURLRef(ref string) (host string, segments []string, ok bool) {
	rest := strings.TrimSpace(ref)
	scheme := strings.Index(rest, "://")
	if scheme < 0 {
		return "", nil, false
	}
	rest = rest[scheme+3:]
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	hostPart, pathPart, _ := strings.Cut(rest, "/")
	host = strings.ToLower(hostPart)
	host = strings.TrimSuffix(strings.TrimSuffix(host, ":80"), ":443")
	if host == "" {
		return "", nil, false
	}
	for _, seg := range strings.Split(pathPart, "/") {
		if seg == "" {
			continue
		}
		segments = append(segments, strings.ToLower(seg))
	}
	return host, segments, true
}

// positiveIID reports whether s is an IID as GitLab spells it in URLs: all
// digits with no leading zero, so "0" is rejected just like the
// glShorthandPattern already rejects "gitlab:0".
func positiveIID(s string) bool {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// gitLabURLMatchKey returns the project-qualified fallback-dedup key for a
// URL-shaped GitLab ref: "gl://{host}/{project-path}#{iid}" for issue and
// work-item refs — both the current /-/ HTML spelling and the legacy one
// without the separator, since issues and work items in one project share an
// IID space, so a re-pull under the other spelling still matches — and
// "gl://{host}/{project-path}#milestone:{iid}" for milestone refs, whose
// path segment set is the whole project (a group milestone's is
// "groups/{group}"), keeping milestones namespaced away from issue IIDs. The
// full project path before the resource is the key's scope, so sibling
// projects such as group/b and group/b-other never collide. Anything else —
// other resources, URLs without a project path, non-URLs — returns "".
func gitLabURLMatchKey(ref string) string {
	host, segments, ok := parseGitLabURLRef(ref)
	if !ok || len(segments) < 3 {
		return ""
	}
	kind, iid := segments[len(segments)-2], segments[len(segments)-1]
	if !positiveIID(iid) {
		return ""
	}
	project := segments[:len(segments)-2]
	if len(project) == 0 {
		return ""
	}
	sep := false
	if project[len(project)-1] == "-" {
		project = project[:len(project)-1]
		sep = true
	}
	if len(project) == 0 {
		return ""
	}
	switch kind {
	case "issues", "work_items":
		// Current /-/ spelling or legacy without the separator.
	case "milestones":
		// Milestones only exist in the /-/ spelling (milestoneIDPattern).
		if !sep {
			return ""
		}
		return fmt.Sprintf("gl://%s/%s#milestone:%s", host, strings.Join(project, "/"), iid)
	default:
		return ""
	}
	return fmt.Sprintf("gl://%s/%s#%s", host, strings.Join(project, "/"), iid)
}

// LinkFromBeadsDependency converts one local dependency record to the GitLab
// issue-link direction. For a beads blocks edge A -> B, GitLab stores the
// inverse API relation: B --blocks--> A.
func LinkFromBeadsDependency(issue *types.Issue, dep *types.IssueWithDependencyMetadata) (DependencyLink, bool) {
	if issue == nil || dep == nil || issue.ExternalRef == nil || dep.ExternalRef == nil {
		return DependencyLink{}, false
	}
	issueIID, ok := IssueIIDFromRef(*issue.ExternalRef)
	if !ok {
		return DependencyLink{}, false
	}
	depIID, ok := IssueIIDFromRef(*dep.ExternalRef)
	if !ok || issueIID == depIID {
		return DependencyLink{}, false
	}

	link := DependencyLink{
		FromBeadsID:    issue.ID,
		ToBeadsID:      dep.ID,
		DependencyType: dep.DependencyType,
	}
	switch dep.DependencyType {
	case types.DepBlocks:
		link.SourceIID = depIID
		link.TargetIID = issueIID
		link.LinkType = gitLabLinkBlocks
	case types.DepRelated, types.DepRelatesTo:
		link.SourceIID, link.TargetIID = orderedIIDs(issueIID, depIID)
		link.LinkType = gitLabLinkRelatesTo
	default:
		return DependencyLink{}, false
	}
	return link, true
}

// DeduplicateLinks removes duplicate desired GitLab links, including reciprocal
// local related/relates-to edges that map to the same unordered GitLab pair.
func DeduplicateLinks(links []DependencyLink) []DependencyLink {
	if len(links) == 0 {
		return nil
	}
	result := make([]DependencyLink, 0, len(links))
	seen := make(map[gitLabLinkKey]struct{}, len(links))
	for _, link := range links {
		key := link.key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, link)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SourceIID != result[j].SourceIID {
			return result[i].SourceIID < result[j].SourceIID
		}
		if result[i].TargetIID != result[j].TargetIID {
			return result[i].TargetIID < result[j].TargetIID
		}
		return result[i].LinkType < result[j].LinkType
	})
	return result
}

// PushLinks creates missing GitLab issue links for the desired dependency set.
// It is additive: stale remote links are left untouched. blocks/is_blocked_by
// links rejected for lack of a GitLab license are counted in
// PushLinkResult.LicenseSkipped (an expected, non-fatal degradation) rather than
// reported as genuine errors.
func (r *LinkResolver) PushLinks(ctx context.Context, desired []DependencyLink, opts PushLinkOptions) PushLinkResult {
	if r == nil || r.Client == nil {
		return PushLinkResult{Errors: []error{fmt.Errorf("GitLab link resolver has no client")}}
	}

	desired = DeduplicateLinks(desired)
	if len(desired) == 0 {
		return PushLinkResult{}
	}

	currentBySource := make(map[int]map[gitLabLinkKey]struct{})
	var result PushLinkResult

	for _, link := range desired {
		current, ok := currentBySource[link.SourceIID]
		if !ok {
			links, err := r.Client.GetIssueLinks(ctx, link.SourceIID)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("fetch GitLab links for #%d: %w", link.SourceIID, err))
				continue
			}
			current = currentLinkSet(link.SourceIID, links)
			currentBySource[link.SourceIID] = current
		}

		if _, exists := current[link.key()]; exists {
			continue
		}

		if opts.DryRun {
			if opts.OnPlan != nil {
				opts.OnPlan(link)
			}
			result.Created++
			continue
		}

		if _, err := r.Client.CreateIssueLink(ctx, link.SourceIID, link.TargetIID, link.LinkType); err != nil {
			if isGitLabLicenseError(err) {
				// blocks/is_blocked_by needs GitLab Premium/Ultimate; this
				// instance's license lacks it. Expected, non-fatal: skip and
				// let the caller emit one curated message.
				result.LicenseSkipped++
				continue
			}
			result.Errors = append(result.Errors, fmt.Errorf("create GitLab link #%d %s #%d: %w", link.SourceIID, link.LinkType, link.TargetIID, err))
			continue
		}
		current[link.key()] = struct{}{}
		result.Created++
	}

	return result
}

// isGitLabLicenseError reports whether err is GitLab's 403 rejection of a
// blocks/is_blocked_by link on an instance whose license lacks the
// issue-blocking feature ("Blocked issues not available for current license").
// It is deliberately specific so genuine failures are not misclassified as the
// expected license limitation.
func isGitLabLicenseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "not available for current license") ||
		strings.Contains(msg, "blocked issues not available") {
		return true
	}
	// Fallback: a 403 that explicitly mentions licensing.
	return strings.Contains(msg, "status 403") && strings.Contains(msg, "license")
}

// PushEpicMilestones ensures non-epic GitLab issues under synced epic beads
// carry the epic's GitLab milestone. This runs during the dependency push pass
// so milestone hierarchy stays correct even when issue content was unchanged
// and the main push loop skipped the issue update.
func (t *Tracker) PushEpicMilestones(ctx context.Context, issues []*types.Issue, opts EpicMilestoneOptions) (int, []error) {
	if t == nil || t.client == nil || t.store == nil {
		return 0, nil
	}

	seen := make(map[string]struct{}, len(issues))
	updated := 0
	var errs []error
	for _, issue := range issues {
		if issue == nil || issue.IssueType == types.TypeEpic {
			continue
		}
		if _, ok := seen[issue.ID]; ok {
			continue
		}
		seen[issue.ID] = struct{}{}
		if issue.ExternalRef == nil {
			continue
		}
		iid, ok := IssueIIDFromRef(*issue.ExternalRef)
		if !ok {
			continue
		}

		milestoneID := t.findParentEpicMilestone(ctx, issue.ID)
		if milestoneID == 0 {
			continue
		}

		current, err := t.client.FetchIssueByIID(ctx, iid)
		if err != nil {
			errs = append(errs, fmt.Errorf("fetch GitLab issue #%d for milestone sync: %w", iid, err))
			continue
		}
		if current != nil && current.Milestone != nil && current.Milestone.ID == milestoneID {
			continue
		}

		if opts.DryRun {
			if opts.OnPlan != nil {
				opts.OnPlan(issue.ID, iid, milestoneID)
			}
			updated++
			continue
		}

		if _, err := t.client.UpdateIssue(ctx, iid, map[string]interface{}{"milestone_id": milestoneID}); err != nil {
			errs = append(errs, fmt.Errorf("set GitLab milestone for %s (#%d): %w", issue.ID, iid, err))
			continue
		}
		updated++
	}
	return updated, errs
}

// issueLinksToDependencies converts GitLab issue links to normalized beads
// dependencies. Both "B blocks A" and "A is_blocked_by B" become A -> B blocks.
func issueLinksToDependencies(sourceIID int, links []IssueLink, _ *MappingConfig) []DependencyInfo {
	var deps []DependencyInfo
	for _, link := range links {
		dep, ok := dependencyFromIssueLink(sourceIID, link)
		if ok {
			deps = append(deps, dep)
		}
	}
	return deps
}

func dependencyFromIssueLink(currentIID int, link IssueLink) (DependencyInfo, bool) {
	sourceIID, targetIID, ok := issueLinkIIDs(currentIID, link)
	if !ok || (currentIID != sourceIID && currentIID != targetIID) {
		return DependencyInfo{}, false
	}

	switch link.LinkType {
	case gitLabLinkBlocks:
		return DependencyInfo{
			FromGitLabIID: targetIID,
			ToGitLabIID:   sourceIID,
			Type:          string(types.DepBlocks),
		}, true
	case gitLabLinkIsBlockedBy:
		return DependencyInfo{
			FromGitLabIID: sourceIID,
			ToGitLabIID:   targetIID,
			Type:          string(types.DepBlocks),
		}, true
	case gitLabLinkRelatesTo:
		otherIID := targetIID
		if currentIID == targetIID {
			otherIID = sourceIID
		}
		return DependencyInfo{
			FromGitLabIID: currentIID,
			ToGitLabIID:   otherIID,
			Type:          string(types.DepRelated),
		}, true
	default:
		return DependencyInfo{}, false
	}
}

type gitLabLinkKey struct {
	SourceIID int
	TargetIID int
	LinkType  string
}

func (l DependencyLink) key() gitLabLinkKey {
	sourceIID, targetIID := l.SourceIID, l.TargetIID
	if l.LinkType == gitLabLinkRelatesTo {
		sourceIID, targetIID = orderedIIDs(sourceIID, targetIID)
	}
	return gitLabLinkKey{SourceIID: sourceIID, TargetIID: targetIID, LinkType: l.LinkType}
}

func currentLinkSet(sourceIID int, links []IssueLink) map[gitLabLinkKey]struct{} {
	result := make(map[gitLabLinkKey]struct{}, len(links))
	for _, link := range links {
		for _, key := range currentIssueLinkKeys(sourceIID, link) {
			result[key] = struct{}{}
		}
	}
	return result
}

func currentIssueLinkKeys(currentIID int, link IssueLink) []gitLabLinkKey {
	sourceIID, targetIID, ok := issueLinkIIDs(currentIID, link)
	if !ok {
		return nil
	}
	switch link.LinkType {
	case gitLabLinkBlocks:
		return []gitLabLinkKey{{SourceIID: sourceIID, TargetIID: targetIID, LinkType: gitLabLinkBlocks}}
	case gitLabLinkIsBlockedBy:
		return []gitLabLinkKey{{SourceIID: targetIID, TargetIID: sourceIID, LinkType: gitLabLinkBlocks}}
	case gitLabLinkRelatesTo:
		sourceIID, targetIID = orderedIIDs(sourceIID, targetIID)
		return []gitLabLinkKey{{SourceIID: sourceIID, TargetIID: targetIID, LinkType: gitLabLinkRelatesTo}}
	default:
		return nil
	}
}

func issueLinkIIDs(currentIID int, link IssueLink) (int, int, bool) {
	if link.SourceIssue != nil && link.TargetIssue != nil {
		sourceIID := link.SourceIssue.IID
		targetIID := link.TargetIssue.IID
		if sourceIID <= 0 || targetIID <= 0 || sourceIID == targetIID {
			return 0, 0, false
		}
		return sourceIID, targetIID, true
	}

	if currentIID <= 0 || link.IID <= 0 || currentIID == link.IID {
		return 0, 0, false
	}
	return currentIID, link.IID, true
}

func orderedIIDs(a, b int) (int, int) {
	if a <= b {
		return a, b
	}
	return b, a
}

func trackerDependencyFromGitLab(dep DependencyInfo) tracker.DependencyInfo {
	return tracker.DependencyInfo{
		FromExternalID: fmt.Sprintf("%d", dep.FromGitLabIID),
		ToExternalID:   fmt.Sprintf("%d", dep.ToGitLabIID),
		Type:           dep.Type,
		Source:         tracker.DependencySourceRelation,
	}
}
