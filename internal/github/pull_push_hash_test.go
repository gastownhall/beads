package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/tracker/conformance"
	"github.com/steveyegge/beads/internal/types"
)

// oneIssueServer is a GitHub REST double for the calls a sync makes on a
// repository with one issue: the issue list, a GET of the issue, and a PATCH
// that writes the fields it sends and dates the issue, as GitHub does.
type oneIssueServer struct {
	mu         sync.Mutex
	issue      Issue
	patched    [][]string
	unexpected []string
}

func (s *oneIssueServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body interface{}
	switch r.Method + " " + r.URL.Path {
	case "GET /repos/o/r/issues":
		body = []Issue{s.issue}
	case "GET /repos/o/r/issues/1":
		body = s.issue
	case "PATCH /repos/o/r/issues/1":
		var fields struct {
			Title  *string  `json:"title"`
			Body   *string  `json:"body"`
			State  *string  `json:"state"`
			Labels []string `json:"labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.patched = append(s.patched, fields.Labels)
		if fields.Title != nil {
			s.issue.Title = *fields.Title
		}
		if fields.Body != nil {
			s.issue.Body = *fields.Body
		}
		if fields.State != nil {
			s.issue.State = *fields.State
		}
		s.issue.Labels = labelsNamed(fields.Labels...)
		updatedAt := time.Now().UTC().Truncate(time.Second)
		s.issue.UpdatedAt = &updatedAt
		body = s.issue
	default:
		s.unexpected = append(s.unexpected, r.Method+" "+r.URL.Path)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (s *oneIssueServer) editLabels(updatedAt time.Time, names ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issue.Labels = labelsNamed(names...)
	s.issue.UpdatedAt = &updatedAt
}

func (s *oneIssueServer) editIssue(updatedAt time.Time, title, body string, labels ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issue.Title, s.issue.Body = title, body
	s.issue.Labels = labelsNamed(labels...)
	s.issue.UpdatedAt = &updatedAt
}

func labelsNamed(names ...string) []Label {
	labels := make([]Label, 0, len(names))
	for _, name := range names {
		labels = append(labels, Label{Name: name})
	}
	return labels
}

// newOneIssueSync wires the sync engine, with GitHub's mapping and the push
// hooks of syncPushHooks, to server and to a store holding local. It returns
// the store and a function that runs one sync.
func newOneIssueSync(t *testing.T, server *oneIssueServer, local *types.Issue) (*conformance.Store, func(tracker.SyncOptions)) {
	t.Helper()
	srv := httptest.NewServer(server)
	t.Cleanup(srv.Close)

	// The client keeps GitHub's base URL, so the tracker reads the github.com
	// ref as its repository's issue (RefScope); the transport sends the
	// requests to the double.
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.URL.Scheme = "http"
		req.URL.Host = srv.Listener.Addr().String()
		req.Host = req.URL.Host
		return http.DefaultTransport.RoundTrip(req)
	})
	client := NewClient("token", "o", "r").WithHTTPClient(&http.Client{Transport: transport})
	gt := &Tracker{client: client, config: DefaultMappingConfig()}
	store := conformance.NewStore()
	store.Config["issue_prefix"] = "bd"
	store.Issues[local.ID] = local
	engine := tracker.NewEngine(gt, store, "test")
	engine.PushHooks = syncPushHooks(gt)
	return store, func(opts tracker.SyncOptions) {
		t.Helper()
		if _, err := engine.Sync(context.Background(), opts); err != nil {
			t.Fatalf("Sync(%+v): %v", opts, err)
		}
	}
}

// syncPushHooks mirrors the push hooks cmd/bd's buildGitHubPushHooks gives
// the engine of `bd github sync`, `push` and `pull`.
func syncPushHooks(gt *Tracker) *tracker.PushHooks {
	config := gt.MappingConfig()
	pushFieldsEqual := func(local *types.Issue, remote *tracker.TrackerIssue) bool {
		gh, ok := remote.Raw.(*Issue)
		return ok && PushFieldsEqual(local, gh, config)
	}
	return &tracker.PushHooks{
		ContentEqual:    pushFieldsEqual,
		PushFieldsEqual: pushFieldsEqual,
		ContentHash:     func(local *types.Issue) string { return PushContentHash(local, config) },
		TargetScope:     gt.PushTargetScope,
	}
}

// TestPullMirrorsRemovedLabelsSoThePushIsANoOp drives the sync engine with
// GitHub's mapping and push hash against a REST double. GitHub drops labels
// the local issue still has, or changes labels the pull maps to the fields the
// local issue already has. The pull must leave the issue so that the next
// push of it makes no PATCH: one would put the removed labels back, or replace
// a priority label bd cannot map.
func TestPullMirrorsRemovedLabelsSoThePushIsANoOp(t *testing.T) {
	for _, test := range []struct {
		name         string
		plainLabels  []string // on both sides at the first push
		remoteLabels []string // on GitHub after its edit
		pullOnly     bool     // `bd github pull <id>`, else `bd github sync`
	}{
		{name: "sync/last_plain_label_removed", plainLabels: []string{"needs-triage"}, remoteLabels: []string{"type::task", "priority::medium"}},
		{name: "sync/only_an_unmapped_priority_left", plainLabels: []string{"needs-triage"}, remoteLabels: []string{"priority::urgent"}},
		{name: "pull/last_plain_label_removed", plainLabels: []string{"needs-triage"}, remoteLabels: []string{"type::task", "priority::medium"}, pullOnly: true},
		{name: "pull/only_an_unmapped_priority_left", plainLabels: []string{"needs-triage"}, remoteLabels: []string{"priority::urgent"}, pullOnly: true},
		// The pull finds these equal to the local issue and writes nothing.
		{name: "pull/equal_with_an_unmapped_priority", remoteLabels: []string{"type::task", "priority::urgent"}, pullOnly: true},
		{name: "pull/equal_with_every_label_removed", pullOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := "https://github.com/o/r/issues/1"
			pushed := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
			server := &oneIssueServer{issue: Issue{
				ID: 1001, Number: 1, Title: "Fix it", Body: "Details", State: "open", HTMLURL: ref, UpdatedAt: &pushed,
				Labels: labelsNamed(append([]string{"type::task", "priority::medium"}, test.plainLabels...)...),
			}}
			store, run := newOneIssueSync(t, server, &types.Issue{
				ID: "bd-1", Title: "Fix it", Description: "Details", Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask, Labels: test.plainLabels,
				ExternalRef: &ref, UpdatedAt: pushed,
			})

			// The issue matches GitHub, so this push only records its hash.
			run(tracker.SyncOptions{Push: true})

			// GitHub's edit comes after the push's last_sync, as a real edit
			// does: the pull of `bd github sync` leaves a linked issue alone
			// for a row dated before its fetch window.
			server.editLabels(time.Now().UTC().Add(time.Hour), test.remoteLabels...)
			if test.pullOnly {
				run(tracker.SyncOptions{Pull: true, IssueIDs: []string{"bd-1"}})
			} else {
				run(tracker.SyncOptions{Pull: true, Push: true})
			}
			if got := store.Snapshot().Issues["bd-1"].Labels; len(got) != 0 {
				t.Fatalf("local labels after the pull = %q, want none", got)
			}

			run(tracker.SyncOptions{Push: true})
			server.mu.Lock()
			defer server.mu.Unlock()
			if len(server.patched) != 0 || len(server.unexpected) != 0 {
				t.Fatalf("GitHub got PATCHes setting labels %q and unexpected requests %q, want neither",
					server.patched, server.unexpected)
			}
		})
	}
}

// TestSyncPushesALabelAddedSinceThePush drives the sync engine with GitHub's
// mapping and push hash against the REST double, whose PATCH dates the issue
// as GitHub's does. bd pushes an edit, then a label is added locally (bd label
// add moves no updated_at), and the pull of the next sync fetches the row of
// that push again through its overlap window. The pull must keep the label so
// that a push sends it: it used to delete the label and record the issue
// without it as pushed, which lost the label on both sides.
func TestSyncPushesALabelAddedSinceThePush(t *testing.T) {
	for _, test := range []struct {
		name         string
		plainLabels  []string // on both sides before the label is added
		pullThenPush bool     // `bd github pull <id>`, then `bd github push`; else `bd github sync`
	}{
		{name: "sync/no_plain_label_on_github"},
		{name: "sync/beside_a_plain_label", plainLabels: []string{"needs-triage"}},
		{name: "pull_then_push/no_plain_label_on_github", pullThenPush: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := "https://github.com/o/r/issues/1"
			created := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
			server := &oneIssueServer{issue: Issue{
				ID: 1001, Number: 1, Title: "Fix it", Body: "Details", State: "open", HTMLURL: ref, UpdatedAt: &created,
				Labels: labelsNamed(append([]string{"type::task", "priority::medium"}, test.plainLabels...)...),
			}}
			// The local issue was raised to P1, which the first push sends.
			store, run := newOneIssueSync(t, server, &types.Issue{
				ID: "bd-1", Title: "Fix it", Description: "Details", Status: types.StatusOpen,
				Priority: 1, IssueType: types.TypeTask, Labels: append([]string(nil), test.plainLabels...),
				ExternalRef: &ref, UpdatedAt: created,
			})
			run(tracker.SyncOptions{Push: true})

			// bd label add bd-1 needs-review
			want := append(append([]string(nil), test.plainLabels...), "needs-review")
			store.Issues["bd-1"].Labels = append([]string(nil), want...)
			if test.pullThenPush {
				run(tracker.SyncOptions{Pull: true, IssueIDs: []string{"bd-1"}})
				if got := store.Snapshot().Issues["bd-1"].Labels; !labelSetsEqual(got, want) {
					t.Fatalf("local labels after the pull = %q, want %q", got, want)
				}
				run(tracker.SyncOptions{Push: true})
			} else {
				run(tracker.SyncOptions{Pull: true, Push: true})
			}

			if got := store.Snapshot().Issues["bd-1"].Labels; !labelSetsEqual(got, want) {
				t.Fatalf("local labels = %q, want %q", got, want)
			}
			wantRemote := append([]string{"type::task", "priority::high"}, want...)
			server.mu.Lock()
			remote, patches, unexpected := server.issue.LabelNames(), len(server.patched), server.unexpected
			server.mu.Unlock()
			if !labelSetsEqual(remote, wantRemote) || patches != 2 || len(unexpected) != 0 {
				t.Fatalf("GitHub labels = %q after %d PATCHes and unexpected requests %q, want %q after 2 and none",
					remote, patches, unexpected, wantRemote)
			}

			// The push recorded what it sent, so the next one sends nothing.
			run(tracker.SyncOptions{Push: true})
			server.mu.Lock()
			defer server.mu.Unlock()
			if len(server.patched) != 2 {
				t.Fatalf("GitHub got %d PATCHes, want the 2 before", len(server.patched))
			}
		})
	}
}

// TestSyncMergesALocalLabelEditWithAGitHubEdit drives the sync engine with
// GitHub's mapping and push hash against the REST double. After bd's push, a
// label is added locally (bd label add moves no updated_at) while the title
// and body are edited on GitHub. The pull takes GitHub's title and body. While
// GitHub's plain labels are still those of the push, it keeps the local label
// so that the push sends it: it used to delete the label and record the issue
// without it as pushed, which lost the label on both sides. When GitHub
// changed its plain labels too, GitHub's labels win and nothing is pushed.
func TestSyncMergesALocalLabelEditWithAGitHubEdit(t *testing.T) {
	for _, test := range []struct {
		name         string
		plainLabels  []string // on both sides at the first push
		githubLabels []string // GitHub's plain labels after its edit
		want         []string // plain labels on both sides after the sync
		patches      int      // PATCHes in all, the first push's included
	}{
		{name: "no_plain_label_on_github", want: []string{"needs-review"}, patches: 2},
		{name: "beside_a_plain_label", plainLabels: []string{"needs-triage"}, githubLabels: []string{"needs-triage"},
			want: []string{"needs-triage", "needs-review"}, patches: 2},
		{name: "github_removed_its_plain_label", plainLabels: []string{"needs-triage"}, patches: 1},
		{name: "github_added_a_plain_label", githubLabels: []string{"blocked"}, want: []string{"blocked"}, patches: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := "https://github.com/o/r/issues/1"
			created := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
			server := &oneIssueServer{issue: Issue{
				ID: 1001, Number: 1, Title: "Fix it", Body: "Details", State: "open", HTMLURL: ref, UpdatedAt: &created,
				Labels: labelsNamed(append([]string{"type::task", "priority::medium"}, test.plainLabels...)...),
			}}
			// The local issue was raised to P1, which the first push sends.
			store, run := newOneIssueSync(t, server, &types.Issue{
				ID: "bd-1", Title: "Fix it", Description: "Details", Status: types.StatusOpen,
				Priority: 1, IssueType: types.TypeTask, Labels: append([]string(nil), test.plainLabels...),
				ExternalRef: &ref, UpdatedAt: created,
			})
			run(tracker.SyncOptions{Push: true})

			// bd label add bd-1 needs-review, while GitHub's title and body
			// are edited after the push's last_sync.
			store.Issues["bd-1"].Labels = append(append([]string(nil), test.plainLabels...), "needs-review")
			server.editIssue(time.Now().UTC().Add(time.Hour), "Fix it properly", "More details",
				append([]string{"type::task", "priority::high"}, test.githubLabels...)...)
			run(tracker.SyncOptions{Pull: true, Push: true})

			local := store.Snapshot().Issues["bd-1"]
			if local.Title != "Fix it properly" || local.Description != "More details" || !labelSetsEqual(local.Labels, test.want) {
				t.Fatalf("local title=%q body=%q labels=%q, want GitHub's title and body and labels %q",
					local.Title, local.Description, local.Labels, test.want)
			}
			wantRemote := append([]string{"type::task", "priority::high"}, test.want...)
			server.mu.Lock()
			remote, patches, unexpected := server.issue, len(server.patched), server.unexpected
			server.mu.Unlock()
			if remote.Title != "Fix it properly" || remote.Body != "More details" || !labelSetsEqual(remote.LabelNames(), wantRemote) ||
				patches != test.patches || len(unexpected) != 0 {
				t.Fatalf("GitHub title=%q body=%q labels=%q after %d PATCHes and unexpected requests %q, want its own title and body and %q after %d and none",
					remote.Title, remote.Body, remote.LabelNames(), patches, unexpected, wantRemote, test.patches)
			}

			// The push recorded what it sent, so the next one sends nothing.
			run(tracker.SyncOptions{Push: true})
			server.mu.Lock()
			defer server.mu.Unlock()
			if len(server.patched) != test.patches {
				t.Fatalf("GitHub got %d PATCHes, want the %d before", len(server.patched), test.patches)
			}
		})
	}
}
