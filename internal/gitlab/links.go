package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
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

// matchKeyIssuePathPattern and matchKeyMilestonePathPattern split a
// canonical GitLab URL path into its project path and IID. Issues and work
// items share a project's IID space, and GitLab still serves the legacy
// issue path without "/-/". A group milestone's project path is
// "groups/{group}".
var (
	matchKeyIssuePathPattern     = regexp.MustCompile(`^/(.+?)(?:/-)?/(?:issues|work_items)/([1-9]\d*)$`)
	matchKeyMilestonePathPattern = regexp.MustCompile(`^/(.+?)/-/milestones/([1-9]\d*)$`)
)

// matchKeyFromURL returns the project-qualified pull match key for a GitLab
// issue, work-item or milestone URL, or "" when ref is not one. IIDs are only
// unique within a project, so the key carries the host and full project path:
// gl://{host}/{project}#{iid} for issues and work items and
// gl://{host}/{project}#milestone:{iid} for milestones.
func matchKeyFromURL(ref string) string {
	host, path, ok := canonicalGitLabURL(ref)
	if !ok {
		return ""
	}
	if m := matchKeyMilestonePathPattern.FindStringSubmatch(path); m != nil && validMatchKeyProject(m[1]) {
		return "gl://" + host + "/" + m[1] + "#" + gitLabMilestoneIdentifierPrefix + m[2]
	}
	if m := matchKeyIssuePathPattern.FindStringSubmatch(path); m != nil && validMatchKeyProject(m[1]) {
		return "gl://" + host + "/" + m[1] + "#" + m[2]
	}
	return ""
}

// canonicalGitLabURL reduces a GitLab URL to the parts that identify a
// resource: the lowercased host without a default port, and the lowercased
// path without repeated or trailing slashes (GitLab routes are
// case-insensitive). Scheme, query and fragment are ignored.
func canonicalGitLabURL(raw string) (host, path string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "", "", false
	}
	host = strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		host += ":" + port
	}
	path = strings.ToLower(u.Path)
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	return host, strings.TrimSuffix(path, "/"), true
}

// validMatchKeyProject rejects project paths with an empty or "-" segment,
// which no GitLab project has.
func validMatchKeyProject(project string) bool {
	for _, seg := range strings.Split(project, "/") {
		if seg == "" || seg == "-" {
			return false
		}
	}
	return true
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

// ProjectScope decides which beads stand for issues of the configured project
// (gitlab.project_id, or gitlab.default_project_id in group mode), the one
// project PushLinks lists and creates links in, addressing issues by IID. A
// group pull stores dependencies between other projects' issues: pushed by
// IID, such a dependency would link the configured project's unrelated issues
// that have those IIDs.
type ProjectScope struct {
	tracker *Tracker
	project *Project // the configured project as GitLab reported it
	err     error    // why GitLab did not report it
	fetched bool
}

// ProjectScope returns the scope of the configured project. It asks GitLab
// for the project at most once, and only for what the configuration does not
// say: the project's path when it is configured by numeric ID, or its ID when
// it is configured by path.
func (t *Tracker) ProjectScope() *ProjectScope {
	return &ProjectScope{tracker: t}
}

// Contains reports whether issue's external_ref names an issue of the
// configured project: the "gitlab:{iid}" shorthand, which names that
// project's issue; a ref whose bead holds an identity (LocalIdentityKey) in
// that project, which a project rename leaves alone; or, for a bead without
// one, a URL in that project's path. A ref the scope cannot place in it, as
// when GitLab did not report the project, is outside it, and so is every ref
// when no project is configured (group mode without
// gitlab.default_project_id).
func (s *ProjectScope) Contains(ctx context.Context, issue *types.Issue) bool {
	if issue == nil || issue.ExternalRef == nil || s.tracker.client == nil || s.tracker.client.ProjectID == "" {
		return false
	}
	ref := strings.TrimSpace(*issue.ExternalRef)
	if glShorthandPattern.MatchString(ref) {
		return true
	}
	project, iid, ok := strings.Cut(matchKeyFromURL(ref), "#")
	if !ok || strings.HasPrefix(iid, gitLabMilestoneIdentifierPrefix) {
		return false
	}
	if m := sourceSystemPattern.FindStringSubmatch(s.tracker.LocalIdentityKey(issue)); m != nil {
		id := s.projectID(ctx)
		return id > 0 && m[1] == strconv.Itoa(id)
	}
	key := s.projectKey(ctx)
	return key != "" && project == key
}

// Err returns the error that kept GitLab from reporting the configured
// project when Contains asked for it, or nil.
func (s *ProjectScope) Err() error {
	return s.err
}

// projectID returns the configured project's ID, or 0 when it is unknown.
func (s *ProjectScope) projectID(ctx context.Context) int {
	if id, err := strconv.Atoi(s.tracker.client.ProjectID); err == nil && id > 0 {
		return id
	}
	if p := s.fetch(ctx); p != nil {
		return p.ID
	}
	return 0
}

// projectKey returns the configured project keyed as MatchKey keys a ref's
// project, "gl://{host}/{path}", or "" when it is unknown.
func (s *ProjectScope) projectKey(ctx context.Context) string {
	if key := s.tracker.configuredMatchKeyProject(); key != "" {
		return key
	}
	p := s.fetch(ctx)
	if p == nil {
		return ""
	}
	host, path, ok := canonicalGitLabURL(p.WebURL)
	if !ok || !validMatchKeyProject(strings.TrimPrefix(path, "/")) {
		return ""
	}
	return "gl://" + host + path
}

// fetch returns the configured project as GitLab reports it, asking once.
func (s *ProjectScope) fetch(ctx context.Context) *Project {
	if !s.fetched {
		s.fetched = true
		s.project, s.err = s.tracker.client.GetProject(ctx)
	}
	return s.project
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
// listed is the issue the links were listed for.
func issueLinksToDependencies(listed *Issue, links []IssueLink, _ *MappingConfig) []DependencyInfo {
	current := issueEndpoint(listed)
	var deps []DependencyInfo
	for _, link := range links {
		dep, ok := dependencyFromIssueLink(current, link)
		if ok {
			deps = append(deps, dep)
		}
	}
	return deps
}

func dependencyFromIssueLink(current issueLinkEndpoint, link IssueLink) (DependencyInfo, bool) {
	source, target, ok := issueLinkEndpoints(current, link)
	if !ok {
		return DependencyInfo{}, false
	}
	isSource, isTarget := current.sameIssue(source), current.sameIssue(target)
	if !isSource && !isTarget {
		return DependencyInfo{}, false
	}

	switch link.LinkType {
	case gitLabLinkBlocks:
		return newDependencyInfo(target, source, types.DepBlocks), true
	case gitLabLinkIsBlockedBy:
		return newDependencyInfo(source, target, types.DepBlocks), true
	case gitLabLinkRelatesTo:
		if isTarget {
			return newDependencyInfo(target, source, types.DepRelated), true
		}
		return newDependencyInfo(source, target, types.DepRelated), true
	default:
		return DependencyInfo{}, false
	}
}

func newDependencyInfo(from, to issueLinkEndpoint, depType types.DependencyType) DependencyInfo {
	return DependencyInfo{
		FromGitLabIID: from.iid,
		ToGitLabIID:   to.iid,
		FromProjectID: from.projectID,
		ToProjectID:   to.projectID,
		FromRef:       from.ref,
		ToRef:         to.ref,
		Type:          string(depType),
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
	source, target, ok := issueLinkEndpoints(issueLinkEndpoint{iid: currentIID}, link)
	return source.iid, target.iid, ok
}

// issueLinkEndpoint is one issue of a GitLab issue link: its project-scoped
// IID and, when GitLab sent them, its project ID and its web_url, which names
// its project's current path.
type issueLinkEndpoint struct {
	iid       int
	projectID int
	ref       string
}

// issueEndpoint returns gl as an issue link endpoint.
func issueEndpoint(gl *Issue) issueLinkEndpoint {
	return issueLinkEndpoint{iid: gl.IID, projectID: gl.ProjectID, ref: gl.WebURL}
}

// sameIssue reports whether e and o are one issue: the same IID, in the same
// project when both refs name one. Without a ref the IID is taken to be in the
// project the links were listed in.
func (e issueLinkEndpoint) sameIssue(o issueLinkEndpoint) bool {
	if e.iid != o.iid {
		return false
	}
	a, b := matchKeyFromURL(e.ref), matchKeyFromURL(o.ref)
	return a == "" || b == "" || a == b
}

// issueLinkEndpoints returns the source and target of a link listed for
// current: a single-link response names both, while a list entry is the
// linked issue, the target of a link from current. ok is false for a
// malformed link or one whose two ends are the same issue; a link between two
// projects' issues with the same IID is kept.
func issueLinkEndpoints(current issueLinkEndpoint, link IssueLink) (source, target issueLinkEndpoint, ok bool) {
	if link.SourceIssue != nil && link.TargetIssue != nil {
		source, target = issueEndpoint(link.SourceIssue), issueEndpoint(link.TargetIssue)
	} else {
		source, target = current, issueLinkEndpoint{iid: link.IID, projectID: link.ProjectID, ref: link.WebURL}
	}
	if source.iid <= 0 || target.iid <= 0 || source.sameIssue(target) {
		return issueLinkEndpoint{}, issueLinkEndpoint{}, false
	}
	return source, target, true
}

func orderedIIDs(a, b int) (int, int) {
	if a <= b {
		return a, b
	}
	return b, a
}

// trackerDependencyFromGitLab names each endpoint by its ref and by its
// identity key, which still finds the bead of an issue whose project was
// renamed since the bead was last pulled.
func trackerDependencyFromGitLab(dep DependencyInfo) tracker.DependencyInfo {
	return tracker.DependencyInfo{
		FromExternalID:  dependencyEndpointRef(dep.FromGitLabIID, dep.FromRef),
		ToExternalID:    dependencyEndpointRef(dep.ToGitLabIID, dep.ToRef),
		FromIdentityKey: gitLabIdentityKey(dep.FromProjectID, dep.FromGitLabIID),
		ToIdentityKey:   gitLabIdentityKey(dep.ToProjectID, dep.ToGitLabIID),
		Type:            dep.Type,
		Source:          tracker.DependencySourceRelation,
	}
}

// dependencyEndpointRef names a link endpoint for the engine's dependency
// resolver, which matches a ref exactly or by MatchKey: by its web_url, which
// carries its project, or, when GitLab sent none, by the "gitlab:{iid}"
// shorthand, which MatchKey keys as the configured project's issue. A bare IID
// would match that IID's bead in any project.
func dependencyEndpointRef(iid int, ref string) string {
	if ref = strings.TrimSpace(ref); ref != "" {
		return ref
	}
	return fmt.Sprintf("gitlab:%d", iid)
}
