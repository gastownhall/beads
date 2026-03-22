package notion

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	itracker "github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

const defaultBatchPushWorkers = 8

type pushOutcome struct {
	created      *itracker.BatchPushItem
	updated      *itracker.BatchPushItem
	skipped      string
	warning      string
	err          *itracker.BatchPushError
	trackerIssue *itracker.TrackerIssue
}

type notionAPI interface {
	GetCurrentUser(ctx context.Context) (*User, error)
	RetrieveDataSource(ctx context.Context, dataSourceID string) (*DataSource, error)
	QueryDataSource(ctx context.Context, dataSourceID string) ([]Page, error)
	CreatePage(ctx context.Context, dataSourceID string, properties map[string]interface{}) (*Page, error)
	UpdatePage(ctx context.Context, pageID string, properties map[string]interface{}) (*Page, error)
	ArchivePage(ctx context.Context, pageID string, inTrash bool) (*Page, error)
}

var newNotionClient = func(token string) notionAPI {
	return NewClient(token)
}

func init() {
	itracker.Register("notion", func() itracker.IssueTracker {
		return &Tracker{}
	})
}

type Tracker struct {
	client       notionAPI
	store        storage.Storage
	config       *MappingConfig
	dataSourceID string
	viewURL      string
	authSource   AuthSource

	cacheMu         sync.RWMutex
	issueCache      []itracker.TrackerIssue
	remoteByPageID  map[string]itracker.TrackerIssue
	remoteByLocalID map[string]itracker.TrackerIssue
	lastQueried     int
	lastCandidates  int
}

func (t *Tracker) Name() string         { return "notion" }
func (t *Tracker) DisplayName() string  { return "Notion" }
func (t *Tracker) ConfigPrefix() string { return "notion" }

func (t *Tracker) Init(ctx context.Context, store storage.Storage) error {
	t.store = store
	t.dataSourceID = t.getConfig(ctx, "notion.data_source_id", "NOTION_DATA_SOURCE_ID")
	t.viewURL = t.getConfig(ctx, "notion.view_url", "NOTION_VIEW_URL")

	auth, err := ResolveAuth(ctx, store)
	if err != nil {
		return err
	}
	if auth == nil || strings.TrimSpace(auth.Token) == "" {
		return fmt.Errorf("Notion authentication is not configured (set notion.token or export NOTION_TOKEN)")
	}
	if t.dataSourceID == "" {
		return fmt.Errorf("Notion data source not configured (run 'bd notion init --parent <page-id>', 'bd notion connect --url <notion-url>', or set notion.data_source_id)")
	}
	t.authSource = auth.Source
	if t.client == nil {
		t.client = newNotionClient(auth.Token)
	}
	if t.config == nil {
		t.config = DefaultMappingConfig()
	}
	return nil
}

func (t *Tracker) Validate() error {
	if t.client == nil {
		return fmt.Errorf("Notion tracker not initialized")
	}
	_, err := t.client.RetrieveDataSource(context.Background(), t.dataSourceID)
	if err != nil {
		return fmt.Errorf("Notion validation failed: %w", err)
	}
	return nil
}

func (t *Tracker) Close() error { return nil }

func (t *Tracker) FetchIssues(ctx context.Context, opts itracker.FetchOptions) ([]itracker.TrackerIssue, error) {
	if err := t.ensureRemoteIndex(ctx); err != nil {
		return nil, err
	}
	localByExternalIdentifier, localByID, err := t.buildLocalPullIndexes(ctx)
	if err != nil {
		return nil, err
	}
	t.cacheMu.RLock()

	result := make([]itracker.TrackerIssue, 0, len(t.issueCache))
	for _, issue := range t.issueCache {
		candidate := cloneTrackerIssue(issue)
		if !matchesFetchState(&candidate, opts.State) {
			continue
		}
		if !matchesFetchSince(&candidate, opts.Since) && !shouldBackfillNotionIssue(&candidate, localByExternalIdentifier, localByID) {
			continue
		}
		result = append(result, candidate)
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
	}
	queried := len(t.issueCache)
	candidates := len(result)
	t.cacheMu.RUnlock()
	t.cacheMu.Lock()
	t.lastQueried = queried
	t.lastCandidates = candidates
	t.cacheMu.Unlock()
	return result, nil
}

func (t *Tracker) LastPullStats() (queried int, candidates int) {
	t.cacheMu.RLock()
	defer t.cacheMu.RUnlock()
	return t.lastQueried, t.lastCandidates
}

func (t *Tracker) buildLocalPullIndexes(ctx context.Context) (map[string]struct{}, map[string]struct{}, error) {
	localByExternalIdentifier := map[string]struct{}{}
	localByID := map[string]struct{}{}
	if t.store == nil {
		return localByExternalIdentifier, localByID, nil
	}
	localIssues, err := t.store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil, nil, fmt.Errorf("searching local issues: %w", err)
	}
	for _, issue := range localIssues {
		if issue == nil {
			continue
		}
		if id := strings.TrimSpace(issue.ID); id != "" {
			localByID[id] = struct{}{}
		}
		if issue.ExternalRef == nil {
			continue
		}
		if identifier := ExtractNotionIdentifier(strings.TrimSpace(*issue.ExternalRef)); identifier != "" {
			localByExternalIdentifier[identifier] = struct{}{}
		}
	}
	return localByExternalIdentifier, localByID, nil
}

func (t *Tracker) FetchIssue(ctx context.Context, identifier string) (*itracker.TrackerIssue, error) {
	if err := t.ensureRemoteIndex(ctx); err != nil {
		return nil, err
	}
	want := ExtractNotionIdentifier(identifier)
	if want == "" {
		want = strings.TrimSpace(identifier)
	}

	t.cacheMu.RLock()
	defer t.cacheMu.RUnlock()

	if issue, ok := t.remoteByPageID[want]; ok {
		cloned := cloneTrackerIssue(issue)
		return &cloned, nil
	}
	if issue, ok := t.remoteByLocalID[want]; ok {
		cloned := cloneTrackerIssue(issue)
		return &cloned, nil
	}
	for _, candidate := range t.issueCache {
		if candidate.Identifier == want {
			cloned := cloneTrackerIssue(candidate)
			return &cloned, nil
		}
	}
	return nil, nil
}

func (t *Tracker) CreateIssue(ctx context.Context, issue *types.Issue) (*itracker.TrackerIssue, error) {
	pushIssue, err := PushIssueFromIssue(issue, t.config)
	if err != nil {
		return nil, err
	}
	page, err := t.client.CreatePage(ctx, t.dataSourceID, BuildPageProperties(pushIssue))
	if err != nil {
		return nil, err
	}
	trackerIssue, err := TrackerIssueFromPullIssue(PulledIssueFromPage(*page), t.config)
	if err != nil {
		return nil, err
	}
	t.upsertRemoteIssue(trackerIssue)
	return trackerIssue, nil
}

func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*itracker.TrackerIssue, error) {
	pageID := ExtractNotionIdentifier(externalID)
	if pageID == "" && issue != nil && issue.ExternalRef != nil {
		pageID = ExtractNotionIdentifier(*issue.ExternalRef)
	}
	if pageID == "" {
		return nil, fmt.Errorf("invalid Notion page ID %q", externalID)
	}
	pushIssue, err := PushIssueFromIssue(issue, t.config)
	if err != nil {
		return nil, err
	}
	page, err := t.client.UpdatePage(ctx, pageID, BuildPageProperties(pushIssue))
	if err != nil {
		return nil, err
	}
	trackerIssue, err := TrackerIssueFromPullIssue(PulledIssueFromPage(*page), t.config)
	if err != nil {
		return nil, err
	}
	t.upsertRemoteIssue(trackerIssue)
	return trackerIssue, nil
}

func (t *Tracker) BatchPush(ctx context.Context, issues []*types.Issue, forceIDs map[string]bool) (*itracker.BatchPushResult, error) {
	return t.executeBatchPush(ctx, issues, forceIDs, false)
}

func (t *Tracker) BatchPushDryRun(ctx context.Context, issues []*types.Issue, forceIDs map[string]bool) (*itracker.BatchPushResult, error) {
	return t.executeBatchPush(ctx, issues, forceIDs, true)
}

func (t *Tracker) executeBatchPush(ctx context.Context, issues []*types.Issue, forceIDs map[string]bool, dryRun bool) (*itracker.BatchPushResult, error) {
	if err := t.ensureRemoteIndex(ctx); err != nil {
		return nil, err
	}
	result := &itracker.BatchPushResult{}
	if len(issues) == 0 {
		return result, nil
	}

	workerCount := defaultBatchPushWorkers
	if len(issues) < workerCount {
		workerCount = len(issues)
	}
	if workerCount < 1 {
		workerCount = 1
	}

	jobs := make(chan *types.Issue)
	outcomes := make(chan pushOutcome, len(issues))
	var wg sync.WaitGroup

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for issue := range jobs {
				outcomes <- t.pushOne(ctx, issue, forceIDs[issue.ID], dryRun)
			}
		}()
	}

	for _, issue := range issues {
		if issue == nil {
			continue
		}
		jobs <- issue
	}
	close(jobs)
	wg.Wait()
	close(outcomes)

	for outcome := range outcomes {
		if outcome.created != nil {
			result.Created = append(result.Created, *outcome.created)
		}
		if outcome.updated != nil {
			result.Updated = append(result.Updated, *outcome.updated)
		}
		if strings.TrimSpace(outcome.skipped) != "" {
			result.Skipped = append(result.Skipped, outcome.skipped)
		}
		if strings.TrimSpace(outcome.warning) != "" {
			result.Warnings = append(result.Warnings, outcome.warning)
		}
		if outcome.err != nil {
			result.Errors = append(result.Errors, *outcome.err)
		}
		if outcome.trackerIssue != nil {
			t.upsertRemoteIssue(outcome.trackerIssue)
		}
	}
	return result, nil
}

func (t *Tracker) pushOne(ctx context.Context, issue *types.Issue, force, dryRun bool) pushOutcome {
	if issue == nil {
		return pushOutcome{}
	}
	pushIssue, err := PushIssueFromIssue(issue, t.config)
	if err != nil {
		return pushOutcome{err: &itracker.BatchPushError{LocalID: issue.ID, Message: err.Error()}}
	}

	extRef := derefStr(issue.ExternalRef)
	pageID := ExtractNotionIdentifier(extRef)
	remote, hasRemote := t.lookupRemoteByPageID(pageID)
	if !hasRemote {
		remote, hasRemote = t.lookupRemoteByLocalID(issue.ID)
	}
	if !hasRemote && strings.TrimSpace(extRef) != "" && pageID != "" {
		return pushOutcome{
			skipped: issue.ID,
			warning: fmt.Sprintf("Skipped %s: Notion external_ref points outside the current target; clear external_ref to recreate it in this data source", issue.ID),
		}
	}
	create := !hasRemote

	if !create && !force {
		if trackerIssueEqual(issue, remote) {
			return pushOutcome{skipped: issue.ID}
		}
		if !remote.UpdatedAt.Before(issue.UpdatedAt) {
			return pushOutcome{skipped: issue.ID}
		}
	}

	if create {
		if dryRun {
			return pushOutcome{
				created: &itracker.BatchPushItem{LocalID: issue.ID},
			}
		}
		page, err := t.client.CreatePage(ctx, t.dataSourceID, BuildPageProperties(pushIssue))
		if err != nil {
			return pushOutcome{err: &itracker.BatchPushError{LocalID: issue.ID, Message: err.Error()}}
		}
		trackerIssue, err := TrackerIssueFromPullIssue(PulledIssueFromPage(*page), t.config)
		if err != nil {
			return pushOutcome{err: &itracker.BatchPushError{LocalID: issue.ID, Message: err.Error()}}
		}
		ref := firstNonEmpty(t.BuildExternalRef(trackerIssue), trackerIssue.URL)
		return pushOutcome{
			created:      &itracker.BatchPushItem{LocalID: issue.ID, ExternalRef: ref},
			trackerIssue: trackerIssue,
		}
	}

	if dryRun {
		ref := firstNonEmpty(t.BuildExternalRef(remote), remote.URL)
		return pushOutcome{
			updated: &itracker.BatchPushItem{LocalID: issue.ID, ExternalRef: ref},
		}
	}

	page, err := t.client.UpdatePage(ctx, remote.ID, BuildPageProperties(pushIssue))
	if err != nil {
		return pushOutcome{err: &itracker.BatchPushError{LocalID: issue.ID, Message: err.Error()}}
	}
	trackerIssue, err := TrackerIssueFromPullIssue(PulledIssueFromPage(*page), t.config)
	if err != nil {
		return pushOutcome{err: &itracker.BatchPushError{LocalID: issue.ID, Message: err.Error()}}
	}
	ref := firstNonEmpty(t.BuildExternalRef(trackerIssue), trackerIssue.URL)
	return pushOutcome{
		updated:      &itracker.BatchPushItem{LocalID: issue.ID, ExternalRef: ref},
		trackerIssue: trackerIssue,
	}
}

func (t *Tracker) FieldMapper() itracker.FieldMapper {
	return NewFieldMapper(t.config)
}

func (t *Tracker) IsExternalRef(ref string) bool {
	return IsNotionExternalRef(ref)
}

func (t *Tracker) ExtractIdentifier(ref string) string {
	return ExtractNotionIdentifier(ref)
}

func (t *Tracker) BuildExternalRef(issue *itracker.TrackerIssue) string {
	if issue == nil {
		return ""
	}
	if canonical, ok := CanonicalizeNotionPageURL(issue.URL); ok {
		return canonical
	}
	if canonical, ok := CanonicalizeNotionPageURL(issue.ID); ok {
		return canonical
	}
	if canonical, ok := CanonicalizeNotionPageURL(issue.Identifier); ok {
		return canonical
	}
	return ""
}

func (t *Tracker) ensureRemoteIndex(ctx context.Context) error {
	t.cacheMu.RLock()
	ready := t.issueCache != nil && t.remoteByPageID != nil && t.remoteByLocalID != nil
	t.cacheMu.RUnlock()
	if ready {
		return nil
	}

	pages, err := t.client.QueryDataSource(ctx, t.dataSourceID)
	if err != nil {
		return err
	}
	cache := make([]itracker.TrackerIssue, 0, len(pages))
	byPageID := make(map[string]itracker.TrackerIssue, len(pages))
	byLocalID := make(map[string]itracker.TrackerIssue, len(pages))
	for _, page := range pages {
		if page.InTrash || page.Archived {
			continue
		}
		pulled := PulledIssueFromPage(page)
		trackerIssue, err := TrackerIssueFromPullIssue(pulled, t.config)
		if err != nil {
			return err
		}
		cache = append(cache, *trackerIssue)
		if id := strings.TrimSpace(trackerIssue.ID); id != "" {
			byPageID[id] = *trackerIssue
		}
		if identifier := strings.TrimSpace(pulled.ID); identifier != "" {
			byLocalID[identifier] = *trackerIssue
		}
	}

	t.cacheMu.Lock()
	t.issueCache = cache
	t.remoteByPageID = byPageID
	t.remoteByLocalID = byLocalID
	t.cacheMu.Unlock()
	return nil
}

func (t *Tracker) lookupRemoteByPageID(pageID string) (*itracker.TrackerIssue, bool) {
	if strings.TrimSpace(pageID) == "" {
		return nil, false
	}
	t.cacheMu.RLock()
	defer t.cacheMu.RUnlock()
	issue, ok := t.remoteByPageID[strings.TrimSpace(pageID)]
	if !ok {
		return nil, false
	}
	cloned := cloneTrackerIssue(issue)
	return &cloned, true
}

func (t *Tracker) lookupRemoteByLocalID(localID string) (*itracker.TrackerIssue, bool) {
	if strings.TrimSpace(localID) == "" {
		return nil, false
	}
	t.cacheMu.RLock()
	defer t.cacheMu.RUnlock()
	issue, ok := t.remoteByLocalID[strings.TrimSpace(localID)]
	if !ok {
		return nil, false
	}
	cloned := cloneTrackerIssue(issue)
	return &cloned, true
}

func (t *Tracker) upsertRemoteIssue(issue *itracker.TrackerIssue) {
	if issue == nil {
		return
	}
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()

	replaced := false
	for i := range t.issueCache {
		if sameTrackerIssue(t.issueCache[i], *issue) {
			t.issueCache[i] = cloneTrackerIssue(*issue)
			replaced = true
			break
		}
	}
	if !replaced {
		t.issueCache = append(t.issueCache, cloneTrackerIssue(*issue))
	}
	if t.remoteByPageID == nil {
		t.remoteByPageID = make(map[string]itracker.TrackerIssue)
	}
	if t.remoteByLocalID == nil {
		t.remoteByLocalID = make(map[string]itracker.TrackerIssue)
	}
	if id := strings.TrimSpace(issue.ID); id != "" {
		t.remoteByPageID[id] = cloneTrackerIssue(*issue)
	}
	if identifier := strings.TrimSpace(ExtractNotionIdentifier(issue.URL)); identifier != "" {
		t.remoteByPageID[identifier] = cloneTrackerIssue(*issue)
	}
	if raw, ok := issue.Raw.(*PulledIssue); ok && raw != nil && strings.TrimSpace(raw.ID) != "" {
		t.remoteByLocalID[strings.TrimSpace(raw.ID)] = cloneTrackerIssue(*issue)
	}
}

func (t *Tracker) getConfig(ctx context.Context, key, envVar string) string {
	if t.store != nil {
		if value, err := t.store.GetConfig(ctx, key); err == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if envVar != "" {
		return strings.TrimSpace(os.Getenv(envVar))
	}
	return ""
}

func trackerIssueEqual(local *types.Issue, remote *itracker.TrackerIssue) bool {
	if local == nil || remote == nil {
		return false
	}
	if strings.TrimSpace(local.Title) != strings.TrimSpace(remote.Title) {
		return false
	}
	if strings.TrimSpace(local.Description) != strings.TrimSpace(remote.Description) {
		return false
	}
	if local.Priority != remote.Priority {
		return false
	}
	if state, ok := remote.State.(types.Status); !ok || state != local.Status {
		return false
	}
	if issueType, ok := remote.Type.(types.IssueType); !ok || issueType != local.IssueType {
		return false
	}
	if strings.TrimSpace(local.Assignee) != strings.TrimSpace(remote.Assignee) {
		return false
	}
	return equalStringSets(local.Labels, remote.Labels)
}

func equalStringSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := normalizeStringSlice(left)
	rightCopy := normalizeStringSlice(right)
	for i := range leftCopy {
		if leftCopy[i] != rightCopy[i] {
			return false
		}
	}
	return true
}

func normalizeStringSlice(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameTrackerIssue(left, right itracker.TrackerIssue) bool {
	leftIDs := []string{
		ExtractNotionIdentifier(left.ID),
		ExtractNotionIdentifier(left.Identifier),
		ExtractNotionIdentifier(left.URL),
	}
	rightIDs := []string{
		ExtractNotionIdentifier(right.ID),
		ExtractNotionIdentifier(right.Identifier),
		ExtractNotionIdentifier(right.URL),
	}
	for _, leftID := range leftIDs {
		if leftID == "" {
			continue
		}
		for _, rightID := range rightIDs {
			if rightID != "" && leftID == rightID {
				return true
			}
		}
	}
	return false
}

func cloneTrackerIssue(issue itracker.TrackerIssue) itracker.TrackerIssue {
	cloned := issue
	if issue.Labels != nil {
		cloned.Labels = append([]string(nil), issue.Labels...)
	}
	return cloned
}

func matchesFetchOptions(issue *itracker.TrackerIssue, opts itracker.FetchOptions) bool {
	return matchesFetchState(issue, opts.State) && matchesFetchSince(issue, opts.Since)
}

func matchesFetchSince(issue *itracker.TrackerIssue, since *time.Time) bool {
	if issue == nil {
		return false
	}
	if since != nil && !issue.UpdatedAt.IsZero() {
		// Notion page timestamps are minute-precision. Revisit the boundary minute
		// so edits made later in the same minute as last_sync are not lost.
		cutoff := since.UTC().Truncate(time.Minute)
		if issue.UpdatedAt.Before(cutoff) {
			return false
		}
	}
	return true
}

func matchesFetchState(issue *itracker.TrackerIssue, stateFilter string) bool {
	if issue == nil {
		return false
	}
	switch strings.TrimSpace(strings.ToLower(stateFilter)) {
	case "", "all":
		return true
	case "open":
		status, _ := issue.State.(types.Status)
		return status != types.StatusClosed
	case "closed":
		status, _ := issue.State.(types.Status)
		return status == types.StatusClosed
	default:
		return true
	}
}

func shouldBackfillNotionIssue(issue *itracker.TrackerIssue, localByExternalIdentifier, localByID map[string]struct{}) bool {
	if issue == nil {
		return false
	}
	for _, ref := range []string{issue.URL, issue.ID, issue.Identifier} {
		if identifier := ExtractNotionIdentifier(ref); identifier != "" {
			if _, ok := localByExternalIdentifier[identifier]; ok {
				return false
			}
		}
	}
	raw, ok := issue.Raw.(*PulledIssue)
	if !ok || raw == nil {
		return false
	}
	localID := strings.TrimSpace(raw.ID)
	if localID == "" {
		return false
	}
	_, ok = localByID[localID]
	return !ok
}

func derefStr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
