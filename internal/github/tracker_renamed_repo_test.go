package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// The tests in this file cover a repository renamed or transferred from
// acme/old to acme/new. GitHub keeps answering the old owner/repo, but
// returns each issue under its new html_url. Since gastownhall/beads#7781
// scoped refs to the configured repository, a sync learns the repository's
// other names from GitHub (Tracker.ResolveTarget), so beads linked under
// either name keep syncing whichever name the configuration uses.

const (
	oldIssue42Ref = "https://github.com/acme/old/issues/42"
	newIssue42Ref = "https://github.com/acme/new/issues/42"
	newIssue43Ref = "https://github.com/acme/new/issues/43"

	renamedRepoID = 7000
)

// renameDay returns midnight UTC on 2026-10-{d}. The tests record last_sync
// on day 1, edit issues on GitHub on day 2 and edit beads on other days.
func renameDay(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }

type remoteIssue struct {
	title   string
	updated time.Time
}

// renamedRepoTransport serves a repository renamed from acme/old to acme/new
// the way GitHub does: both names answer with the repository acme/new, every
// issue carries its acme/new html_url, and the issue list honors since=, so a
// test sees exactly what an incremental pull would. It records every request,
// as ghFixtureTransport does, and answers a PATCH with the issue unchanged.
type renamedRepoTransport struct {
	ghFixtureTransport
	webHost string // host of the html_urls: github.com, or a GitHub Enterprise host
	issues  map[int]*remoteIssue
}

func newRenamedRepoTransport(webHost string, issues map[int]*remoteIssue) *renamedRepoTransport {
	return &renamedRepoTransport{webHost: webHost, issues: issues}
}

func (t *renamedRepoTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.requests = append(t.requests, req.Method+" "+req.URL.Path)
	t.mu.Unlock()
	path := strings.TrimPrefix(req.URL.Path, "/api/v3") // GitHub Enterprise's REST prefix
	for _, repo := range []string{"/repos/acme/old", "/repos/acme/new"} {
		if path == repo && req.Method == http.MethodGet {
			return t.respond(http.StatusOK, ghRepositoryJSON(renamedRepoID, "acme/new", "https://"+t.webHost+"/acme/new")), nil
		}
		list := repo + "/issues"
		if path == list && req.Method == http.MethodGet {
			return t.respond(http.StatusOK, t.list(req.URL.Query().Get("since"))), nil
		}
		n, err := strconv.Atoi(strings.TrimPrefix(path, list+"/"))
		if err == nil && t.issues[n] != nil && (req.Method == http.MethodGet || req.Method == http.MethodPatch) {
			return t.respond(http.StatusOK, t.issueJSON(n)), nil
		}
	}
	return t.respond(http.StatusNotFound, `{"message":"Not Found"}`), nil
}

// ghRepositoryJSON renders the REST repository object GitHub answers
// GET /repos/{owner}/{repo} with.
func ghRepositoryJSON(id int, fullName, htmlURL string) string {
	return fmt.Sprintf(`{"id":%d,"name":%s,"full_name":%s,"html_url":%s}`,
		id, strconv.Quote(fullName[strings.Index(fullName, "/")+1:]), strconv.Quote(fullName), strconv.Quote(htmlURL))
}

// list renders the issues updated at or after since (all of them without
// since=), as GitHub's issue list does.
func (t *renamedRepoTransport) list(since string) string {
	cut, _ := time.Parse(time.RFC3339, since)
	numbers := make([]int, 0, len(t.issues))
	for n := range t.issues {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)
	var items []string
	for _, n := range numbers {
		if !t.issues[n].updated.Before(cut) {
			items = append(items, t.issueJSON(n))
		}
	}
	return "[" + strings.Join(items, ",") + "]"
}

func (t *renamedRepoTransport) issueJSON(n int) string {
	iss := t.issues[n]
	return fmt.Sprintf(`{"id":%d,"number":%d,"title":%s,"body":%s,"state":"open","html_url":%s,`+
		`"created_at":"2026-10-01T00:00:00Z","updated_at":%s}`,
		9000+n, n, strconv.Quote(iss.title), strconv.Quote(iss.title+" body"),
		strconv.Quote(fmt.Sprintf("https://%s/acme/new/issues/%d", t.webHost, n)),
		strconv.Quote(iss.updated.Format(time.RFC3339)))
}

// writes returns every request other than a GET.
func (t *renamedRepoTransport) writes() []string {
	var out []string
	for _, r := range t.requestLog() {
		if !strings.HasPrefix(r, http.MethodGet+" ") {
			out = append(out, r)
		}
	}
	return out
}

// renamedRepoEngine returns an engine syncing store with the repository the
// configuration names acme/{repo} on rt; baseURL "" keeps api.github.com.
func renamedRepoEngine(rt http.RoundTripper, baseURL, repo string, store *ghCrossRepoStore) (*tracker.Engine, *[]string, *[]string) {
	client := NewClient("token", "acme", repo).WithHTTPClient(&http.Client{Transport: rt})
	if baseURL != "" {
		client = client.WithBaseURL(baseURL)
	}
	engine := tracker.NewEngine(&Tracker{client: client, config: DefaultMappingConfig()}, store, "test")
	var messages, warnings []string
	engine.OnMessage = func(msg string) { messages = append(messages, msg) }
	engine.OnWarning = func(msg string) { warnings = append(warnings, msg) }
	return engine, &messages, &warnings
}

// linkedBead returns a bead linked to ref, last edited locally on day d.
func linkedBead(id, title, ref string, d int) *types.Issue {
	return &types.Issue{
		ID:          id,
		Title:       title,
		Description: title + " (local)",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		ExternalRef: &ref,
		UpdatedAt:   renameDay(d),
	}
}

func renamedRepoIssues() map[int]*remoteIssue {
	return map[int]*remoteIssue{
		42: {title: "Issue 42 (remote)", updated: renameDay(2)},
		43: {title: "Issue 43 (remote)", updated: renameDay(2)},
	}
}

// TestPull_RenamedRepository pins that a pull of a renamed repository takes
// each issue into the bead linked to it under the old URL, relinks that bead
// to the new URL, and imports the issues no bead links, whether the
// configuration still names the old repository or already names the new one,
// and without a warning: base matched such beads by bare issue number, and
// scoping refs to the configured repository must not break that.
func TestPull_RenamedRepository(t *testing.T) {
	ctx := context.Background()
	for _, configured := range []string{"old", "new"} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("bulk pull configured acme/%s dry-run=%v", configured, dryRun), func(t *testing.T) {
				store := newGHCrossRepoStore(linkedBead("bd-o42", "Issue 42", oldIssue42Ref, 1))
				engine, messages, warnings := renamedRepoEngine(newRenamedRepoTransport("github.com", renamedRepoIssues()), "", configured, store)
				result, err := engine.Sync(ctx, tracker.SyncOptions{Pull: true, DryRun: dryRun})
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}
				if result.PullStats.Created != 1 || result.PullStats.Updated != 1 || result.PullStats.Skipped != 0 {
					t.Errorf("pull: created=%d updated=%d skipped=%d, want created=1 (issue 43) updated=1 (issue 42 into bd-o42) skipped=0",
						result.PullStats.Created, result.PullStats.Updated, result.PullStats.Skipped)
				}
				if len(*warnings) != 0 {
					t.Errorf("warnings = %q, want none", *warnings)
				}
				if dryRun {
					want := []string{"[dry-run] Would update local issue: 42 - Issue 42 (remote)", "[dry-run] Would import: 43 - Issue 43 (remote)"}
					if b := store.issues["bd-o42"]; len(store.issues) != 1 || b.Title != "Issue 42" || !reflect.DeepEqual(*messages, want) {
						t.Errorf("dry run: store holds %d beads, bd-o42 title %q, printed %q; want bd-o42 alone, untouched, and %q",
							len(store.issues), b.Title, *messages, want)
					}
					return
				}
				if b := store.issues["bd-o42"]; b.Title != "Issue 42 (remote)" || ghDerefRef(b.ExternalRef) != newIssue42Ref {
					t.Errorf("bd-o42 = title %q external_ref %v, want issue 42's title and %s", b.Title, ghDerefRef(b.ExternalRef), newIssue42Ref)
				}
				if id := ghBeadIDByRef(store, newIssue43Ref); id == "" || len(store.issues) != 2 {
					t.Errorf("store holds %d beads, issue 43 on %q; want bd-o42 and a new bead linked to %s", len(store.issues), id, newIssue43Ref)
				}
			})
		}
	}

	t.Run("pull by issue number and by bead ID", func(t *testing.T) {
		for _, ids := range [][]string{{"42", "43"}, {"bd-o42", "43"}} {
			store := newGHCrossRepoStore(linkedBead("bd-o42", "Issue 42", oldIssue42Ref, 1))
			engine, _, warnings := renamedRepoEngine(newRenamedRepoTransport("github.com", renamedRepoIssues()), "", "old", store)
			result, err := engine.Sync(ctx, tracker.SyncOptions{Pull: true, IssueIDs: ids})
			if err != nil {
				t.Fatalf("Sync(%v): %v", ids, err)
			}
			if result.PullStats.Created != 1 || result.PullStats.Updated != 1 || result.PullStats.Skipped != 0 || len(store.issues) != 2 {
				t.Errorf("pull of %v: created=%d updated=%d skipped=%d beads=%d, want created=1 updated=1 skipped=0 beads=2",
					ids, result.PullStats.Created, result.PullStats.Updated, result.PullStats.Skipped, len(store.issues))
			}
			if b := store.issues["bd-o42"]; b.Title != "Issue 42 (remote)" {
				t.Errorf("pull of %v: bd-o42 title %q, want it updated from issue 42", ids, b.Title)
			}
			if len(*warnings) != 0 {
				t.Errorf("pull of %v warned %q, want nothing", ids, *warnings)
			}
		}
	})
}

// TestSync_RenamedRepositoryKeepsRemoteEdits follows a rename through a
// configuration change. A sync under the old name takes the issue's remote
// edit into the bead linked under the old URL. Whatever bd github command
// runs next, and once the configuration names the new repository, no sync
// writes the bead's older content over that edit.
func TestSync_RenamedRepositoryKeepsRemoteEdits(t *testing.T) {
	ctx := context.Background()
	between := map[string]tracker.SyncOptions{
		"nothing":     {},
		"sync":        {},
		"pull bd-o42": {Pull: true, IssueIDs: []string{"bd-o42"}},
		"push bd-o42": {Push: true, IssueIDs: []string{"bd-o42"}},
	}
	for name, opts := range between {
		t.Run(name, func(t *testing.T) {
			rt := newRenamedRepoTransport("github.com", map[int]*remoteIssue{
				42: {title: "Issue 42 (remote edit)", updated: renameDay(2)},
			})
			store := newGHCrossRepoStore(linkedBead("bd-o42", "Issue 42", oldIssue42Ref, 0))
			store.local["github.last_sync"] = renameDay(1).Format(time.RFC3339)

			steps := []struct {
				repo string
				opts tracker.SyncOptions
				skip bool
			}{
				{repo: "old"},
				{repo: "old", opts: opts, skip: name == "nothing"},
				{repo: "new"},
			}
			for i, step := range steps {
				if step.skip {
					continue
				}
				engine, _, warnings := renamedRepoEngine(rt, "", step.repo, store)
				result, err := engine.Sync(ctx, step.opts)
				if err != nil {
					t.Fatalf("step %d Sync: %v", i, err)
				}
				if result.Stats.Conflicts != 0 || len(*warnings) != 0 {
					t.Errorf("step %d: conflicts=%d warnings=%q, want neither", i, result.Stats.Conflicts, *warnings)
				}
				if i == 0 && result.PullStats.Updated != 1 {
					t.Errorf("sync under acme/old updated %d beads, want issue 42's remote edit in bd-o42", result.PullStats.Updated)
				}
			}
			if w := rt.writes(); len(w) != 0 {
				t.Errorf("syncs wrote %q over issue 42's remote edit", w)
			}
			if b := store.issues["bd-o42"]; len(store.issues) != 1 || b.Title != "Issue 42 (remote edit)" || ghDerefRef(b.ExternalRef) != newIssue42Ref {
				t.Errorf("store holds %d beads, bd-o42 = title %q external_ref %v; want bd-o42 alone with the remote edit and %s",
					len(store.issues), b.Title, ghDerefRef(b.ExternalRef), newIssue42Ref)
			}
		})
	}
}

// TestSync_RenamedRepositoryReportsConflicts pins that a bead edited on both
// sides since the last sync is a conflict whichever name of the repository
// its ref uses, so the configured resolution decides the outcome: with
// --prefer-github the bead takes the remote edit and nothing is written.
func TestSync_RenamedRepositoryReportsConflicts(t *testing.T) {
	for _, ref := range []string{oldIssue42Ref, newIssue42Ref} {
		for _, configured := range []string{"old", "new"} {
			t.Run(fmt.Sprintf("%s configured acme/%s", ref, configured), func(t *testing.T) {
				rt := newRenamedRepoTransport("github.com", map[int]*remoteIssue{
					42: {title: "Issue 42 (remote edit)", updated: renameDay(2)},
				})
				store := newGHCrossRepoStore(linkedBead("bd-42", "Issue 42 (local edit)", ref, 3))
				store.local["github.last_sync"] = renameDay(1).Format(time.RFC3339)
				engine, _, _ := renamedRepoEngine(rt, "", configured, store)
				result, err := engine.Sync(context.Background(), tracker.SyncOptions{ConflictResolution: tracker.ConflictExternal})
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}
				if result.Stats.Conflicts != 1 {
					t.Errorf("sync reported %d conflicts, want bd-42's", result.Stats.Conflicts)
				}
				if w := rt.writes(); len(w) != 0 {
					t.Errorf("sync preferring GitHub wrote %q", w)
				}
				if b := store.issues["bd-42"]; len(store.issues) != 1 || b.Title != "Issue 42 (remote edit)" {
					t.Errorf("store holds %d beads, bd-42 title %q; want bd-42 alone with the remote edit", len(store.issues), b.Title)
				}
			})
		}
	}
}

// TestPush_RenamedRepository pins that a push updates the issue of a bead
// edited locally, whichever name of the repository its ref and the
// configuration use, even when no issue changed on GitHub since the last sync
// and so no pull ever sees the new URLs.
func TestPush_RenamedRepository(t *testing.T) {
	for _, configured := range []string{"old", "new"} {
		t.Run("configured acme/"+configured, func(t *testing.T) {
			rt := newRenamedRepoTransport("github.com", map[int]*remoteIssue{
				42: {title: "Issue 42", updated: renameDay(1)},
			})
			store := newGHCrossRepoStore(linkedBead("bd-o42", "Issue 42 (local edit)", oldIssue42Ref, 3))
			store.local["github.last_sync"] = renameDay(2).Format(time.RFC3339)
			engine, _, warnings := renamedRepoEngine(rt, "", configured, store)
			result, err := engine.Sync(context.Background(), tracker.SyncOptions{Push: true})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			want := []string{http.MethodPatch + " /repos/acme/" + configured + "/issues/42"}
			if result.PushStats.Updated != 1 || !reflect.DeepEqual(rt.writes(), want) {
				t.Errorf("push: updated=%d writes=%q, want updated=1 and %q", result.PushStats.Updated, rt.writes(), want)
			}
			if len(*warnings) != 0 {
				t.Errorf("warnings = %q, want none", *warnings)
			}
		})
	}
}

// TestSync_TransferredIssue pins what a sync does with an issue transferred
// out of the configured repository: GitHub follows the transfer, so fetching
// the bead's issue returns it under its new repository's URL. The pull skips
// it and the push and the conflict check leave it alone, with a warning, so
// the bead's content is never written over an issue of another repository.
func TestSync_TransferredIssue(t *testing.T) {
	const (
		repoIssue42Ref     = "https://github.com/acme/repo/issues/42"
		transferredRef     = "https://github.com/acme/other/issues/7"
		configuredRepoName = "the configured GitHub repository acme/repo"
	)
	newTransport := func() *ghFixtureTransport {
		rt := &ghFixtureTransport{}
		rt.route(http.MethodGet, "/repos/acme/repo", ghRepositoryJSON(7100, "acme/repo", "https://github.com/acme/repo"))
		rt.route(http.MethodGet, "/repos/acme/repo/issues", "[]")
		rt.route(http.MethodGet, "/repos/acme/repo/issues/42", ghIssueJSON(9007, 7, "Issue 7 (remote edit)", "Moved body", transferredRef))
		// Served, so a push that wrote the bead would show up as a request.
		rt.route(http.MethodPatch, "/repos/acme/repo/issues/42", ghIssueJSON(9007, 7, "Issue 7 (remote edit)", "Moved body", transferredRef))
		return rt
	}
	assertUntouched := func(t *testing.T, rt *ghFixtureTransport, store *ghCrossRepoStore) {
		t.Helper()
		if rt.seen(http.MethodPatch, "/repos/acme/repo/issues/42") {
			t.Errorf("sync wrote the transferred issue: %q", rt.requestLog())
		}
		if b := store.issues["bd-r42"]; len(store.issues) != 1 || b.Title != "Issue 42" || ghDerefRef(b.ExternalRef) != repoIssue42Ref {
			t.Errorf("store holds %d beads, bd-r42 = title %q external_ref %v; want bd-r42 alone and untouched",
				len(store.issues), b.Title, ghDerefRef(b.ExternalRef))
		}
	}

	t.Run("pull by bead ID", func(t *testing.T) {
		rt := newTransport()
		store := newGHCrossRepoStore(linkedBead("bd-r42", "Issue 42", repoIssue42Ref, 1))
		engine, _, warnings := renamedRepoEngine(rt, "", "repo", store)
		result, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true, IssueIDs: []string{"bd-r42"}})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Skipped != 1 {
			t.Errorf("pull: skipped=%d, want the transferred issue skipped", result.PullStats.Skipped)
		}
		want := []string{"Skipped GitHub issue " + transferredRef + ": its URL is outside " + configuredRepoName}
		if !reflect.DeepEqual(*warnings, want) {
			t.Errorf("warnings = %q, want %q", *warnings, want)
		}
		assertUntouched(t, rt, store)
	})

	t.Run("sync with both sides edited", func(t *testing.T) {
		rt := newTransport()
		store := newGHCrossRepoStore(linkedBead("bd-r42", "Issue 42", repoIssue42Ref, 3))
		store.local["github.last_sync"] = "2026-10-01T00:00:00Z"
		engine, _, warnings := renamedRepoEngine(rt, "", "repo", store)
		result, err := engine.Sync(context.Background(), tracker.SyncOptions{})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.Stats.Conflicts != 0 {
			t.Errorf("sync reported %d conflicts for a bead whose issue left the repository", result.Stats.Conflicts)
		}
		want := "Skipped pushing bd-r42: GitHub serves its issue as " + transferredRef + ", outside " + configuredRepoName
		found := false
		for _, w := range *warnings {
			found = found || w == want
		}
		if !found {
			t.Errorf("warnings = %q, want %q", *warnings, want)
		}
		assertUntouched(t, rt, store)
	})
}

// TestPull_RenamedRepositoryOnEnterpriseHost runs the rename on a GitHub
// Enterprise host whose name does not contain "github.com": the tracker must
// still recognize the issues' URLs as GitHub's and learn the new name, or
// the pull would import a duplicate of the bead linked under the old URL.
func TestPull_RenamedRepositoryOnEnterpriseHost(t *testing.T) {
	const (
		oldRef = "https://ghe.example.test/acme/old/issues/42"
		newRef = "https://ghe.example.test/acme/new/issues/42"
	)
	store := newGHCrossRepoStore(linkedBead("bd-o42", "Issue 42", oldRef, 1))
	engine, _, warnings := renamedRepoEngine(newRenamedRepoTransport("ghe.example.test", renamedRepoIssues()),
		"https://ghe.example.test/api/v3", "old", store)
	result, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Created != 1 || result.PullStats.Updated != 1 || len(store.issues) != 2 {
		t.Errorf("pull: created=%d updated=%d beads=%d, want created=1 (issue 43) updated=1 (bd-o42) beads=2",
			result.PullStats.Created, result.PullStats.Updated, len(store.issues))
	}
	if b := store.issues["bd-o42"]; b.Title != "Issue 42 (remote)" || ghDerefRef(b.ExternalRef) != newRef {
		t.Errorf("bd-o42 = title %q external_ref %v, want issue 42's title and %s", b.Title, ghDerefRef(b.ExternalRef), newRef)
	}
	if len(*warnings) != 0 {
		t.Errorf("warnings = %q, want none", *warnings)
	}
}

// TestPush_UpdatesLinkedIssueOnEnterpriseHost pins that a push on a GitHub
// Enterprise host whose name does not contain "github.com" updates the issue
// a bead is linked to, rather than take the bead for unlinked, create a
// second issue and relink the bead to it.
func TestPush_UpdatesLinkedIssueOnEnterpriseHost(t *testing.T) {
	const ref = "https://ghe.example.test/acme/repo-b/issues/7"
	rt := &ghFixtureTransport{}
	rt.route(http.MethodGet, "/api/v3/repos/acme/repo-b/issues/7", ghIssueJSON(9007, 7, "Repo B issue 7 (remote)", "Repo B body", ref))
	rt.route(http.MethodPatch, "/api/v3/repos/acme/repo-b/issues/7", ghIssueJSON(9007, 7, "Repo B issue 7", "Repo B body", ref))
	rt.route(http.MethodPost, "/api/v3/repos/acme/repo-b/issues",
		ghIssueJSON(9099, 99, "Repo B issue 7", "Repo B body", "https://ghe.example.test/acme/repo-b/issues/99"))
	store := newGHCrossRepoStore(newRepoBead("bd-b7", "Repo B issue 7", ref))
	client := NewClient("token", "acme", "repo-b").WithHTTPClient(&http.Client{Transport: rt}).WithBaseURL("https://ghe.example.test/api/v3")

	result, err := tracker.NewEngine(&Tracker{client: client, config: DefaultMappingConfig()}, store, "test").
		Sync(context.Background(), tracker.SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PushStats.Created != 0 || result.PushStats.Updated != 1 {
		t.Errorf("push: created=%d updated=%d, want created=0 updated=1", result.PushStats.Created, result.PushStats.Updated)
	}
	if !rt.seen(http.MethodPatch, "/api/v3/repos/acme/repo-b/issues/7") || rt.seen(http.MethodPost, "/api/v3/repos/acme/repo-b/issues") {
		t.Errorf("push requests %q, want a PATCH of issue 7 and no POST", rt.requestLog())
	}
	if b := store.issues["bd-b7"]; ghDerefRef(b.ExternalRef) != ref {
		t.Errorf("bd-b7 external_ref = %v, want %s", ghDerefRef(b.ExternalRef), ref)
	}
}

// failingTransport answers the paths in fail with a transport error and
// everything else through ghFixtureTransport.
type failingTransport struct {
	ghFixtureTransport
	fail map[string]bool
}

func (t *failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.fail[req.URL.Path] {
		t.mu.Lock()
		t.requests = append(t.requests, req.Method+" "+req.URL.Path)
		t.mu.Unlock()
		return nil, errors.New("connection reset")
	}
	return t.ghFixtureTransport.RoundTrip(req)
}

// TestResolveTargetLearnsTheRepositorysNames pins which names ResolveTarget
// lets ExtractIdentifier address: every owner/repo GitHub answers with the
// configured repository's ID, never another repository's, so the #7781 fix
// holds; and a failure to learn them stops the sync.
func TestResolveTargetLearnsTheRepositorysNames(t *testing.T) {
	const (
		repoAIssue7Ref = "https://github.com/acme/repo-a/issues/7"
		goneIssue7Ref  = "https://github.com/acme/gone/issues/7"
	)
	newTracker := func(rt http.RoundTripper, baseURL, repo string) *Tracker {
		client := NewClient("token", "acme", repo).WithHTTPClient(&http.Client{Transport: rt})
		if baseURL != "" {
			client = client.WithBaseURL(baseURL)
		}
		return &Tracker{client: client, config: DefaultMappingConfig()}
	}
	addressed := func(t *testing.T, gt *Tracker, want map[string]string) {
		t.Helper()
		for ref, id := range want {
			if got := gt.ExtractIdentifier(ref); got != id {
				t.Errorf("ExtractIdentifier(%q) = %q, want %q", ref, got, id)
			}
		}
	}

	t.Run("names of a renamed repository", func(t *testing.T) {
		rt := &ghFixtureTransport{}
		for _, name := range []string{"old", "new"} {
			rt.route(http.MethodGet, "/repos/acme/"+name, ghRepositoryJSON(7000, "acme/new", "https://github.com/acme/new"))
		}
		rt.route(http.MethodGet, "/repos/acme/repo-a", ghRepositoryJSON(7001, "acme/repo-a", "https://github.com/acme/repo-a"))
		for _, configured := range []string{"old", "new"} {
			gt := newTracker(rt, "", configured)
			refs := []string{oldIssue42Ref, "HTTPS://GitHub.com/Acme/Old/issues/43", repoAIssue7Ref, goneIssue7Ref, "github:5", "https://gitlab.com/acme/x/-/issues/1"}
			if err := gt.ResolveTarget(context.Background(), refs); err != nil {
				t.Fatalf("configured acme/%s: ResolveTarget: %v", configured, err)
			}
			addressed(t, gt, map[string]string{
				oldIssue42Ref: "42", newIssue42Ref: "42", "https://api.github.com/repos/acme/new/issues/42": "42",
				repoAIssue7Ref: "", goneIssue7Ref: "", "https://github.com/acme/newer/issues/42": "",
			})
		}
		probes := map[string]int{}
		for _, r := range rt.requestLog() {
			probes[r]++
		}
		if probes["GET /repos/acme/old"] != 2 || probes["GET /repos/acme/repo-a"] != 2 || probes["GET /repos/acme/gone"] != 2 {
			t.Errorf("requests %q, want each name asked once per ResolveTarget", rt.requestLog())
		}
	})

	t.Run("a repository proxied on another host", func(t *testing.T) {
		rt := &ghFixtureTransport{}
		rt.route(http.MethodGet, "/repos/acme/repo", ghRepositoryJSON(7100, "acme/repo", "https://github.com/acme/repo"))
		gt := newTracker(rt, "https://ghproxy.example.test", "repo")
		if err := gt.ResolveTarget(context.Background(), nil); err != nil {
			t.Fatalf("ResolveTarget: %v", err)
		}
		addressed(t, gt, map[string]string{
			"https://github.com/acme/repo/issues/42": "42", "https://ghproxy.example.test/acme/repo/issues/42": "42",
			"https://github.com/acme/other/issues/42": "",
		})
	})

	t.Run("configured repository not found", func(t *testing.T) {
		rt := &ghFixtureTransport{}
		gt := newTracker(rt, "", "repo-b")
		if err := gt.ResolveTarget(context.Background(), []string{repoAIssue7Ref}); err != nil {
			t.Fatalf("ResolveTarget: %v", err)
		}
		if rt.seen(http.MethodGet, "/repos/acme/repo-a") {
			t.Errorf("requests %q: asked about acme/repo-a with no repository ID to compare", rt.requestLog())
		}
		addressed(t, gt, map[string]string{repoAIssue7Ref: "", repoBIssue7Ref: "7"})
	})

	t.Run("a request that fails stops the sync", func(t *testing.T) {
		for _, path := range []string{"/repos/acme/repo-b", "/repos/acme/repo-a"} {
			rt := &failingTransport{fail: map[string]bool{path: true}}
			rt.route(http.MethodGet, "/repos/acme/repo-b", ghRepositoryJSON(7200, "acme/repo-b", "https://github.com/acme/repo-b"))
			rt.route(http.MethodGet, "/repos/acme/repo-b/issues", "[]")
			gt := newTracker(rt, "", "repo-b")
			gt.client.Retry.MaxRetries = 0
			store := newGHCrossRepoStore(newRepoBead("bd-a7", "Repo A issue 7", repoAIssue7Ref))
			_, err := tracker.NewEngine(gt, store, "test").Sync(context.Background(), tracker.SyncOptions{Pull: true})
			if err == nil || !strings.Contains(err.Error(), "resolving") || !strings.Contains(err.Error(), "connection reset") {
				t.Errorf("%s failing: Sync error = %v, want the resolution failure", path, err)
			}
			if rt.seen(http.MethodGet, "/repos/acme/repo-b/issues") {
				t.Errorf("%s failing: the sync went on to list issues: %q", path, rt.requestLog())
			}
		}
	})

	t.Run("names GitHub refuses or no repository can have", func(t *testing.T) {
		rt := &ghFixtureTransport{}
		rt.route(http.MethodGet, "/repos/acme/repo-b", ghRepositoryJSON(7200, "acme/repo-b", "https://github.com/acme/repo-b"))
		gt := newTracker(rt, "", "repo-b")
		refs := []string{repoAIssue7Ref, "https://github.com/../etc/issues/1", "https://github.com/acme/re%3Fpo/issues/1"}
		if err := gt.ResolveTarget(context.Background(), refs); err != nil {
			t.Fatalf("ResolveTarget: %v", err)
		}
		want := []string{"GET /repos/acme/repo-b", "GET /repos/acme/repo-a"}
		if got := rt.requestLog(); !reflect.DeepEqual(got, want) {
			t.Errorf("requests %q, want %q: a name that cannot be a repository is not asked about", got, want)
		}
		addressed(t, gt, map[string]string{repoAIssue7Ref: "", repoBIssue7Ref: "7"})
	})
}
