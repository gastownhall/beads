package conformance

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func TestRunWithEngineAndUOWFixture(t *testing.T) {
	Run(t, func(_ *testing.T, f *Fixture) Setup {
		store := f.StoreFactory.Open().(*Store)
		store.Config["test.project"] = "PROJ"
		ref := "https://tracker.test/EXT-1"
		store.Issues["bd-1"] = &types.Issue{ID: "bd-1", Title: "local", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2, Labels: []string{"old"}, ExternalRef: &ref, UpdatedAt: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)}
		f.HTTP.Enqueue(Response{Status: http.StatusOK, Body: `[{"id":"EXT-1","identifier":"EXT-1","url":"https://tracker.test/EXT-1","title":"remote","updated_at":"2026-09-03T01:00:00Z","labels":[" bug ","","bug"]},{"id":"EXT-2","identifier":"EXT-2","url":"https://tracker.test/EXT-2","title":"dependent","updated_at":"2026-09-03T01:00:00Z"}]`})
		remote := &mockTracker{client: f.HTTP.Client()}
		return Setup{
			Engine:   tracker.NewEngine(remote, store, "conformance"),
			Store:    store,
			Snapshot: func(context.Context) (Snapshot, error) { return store.Snapshot(), nil },
			SeedExternalRefPlanes: func(context.Context) (string, string, error) {
				ref := "https://tracker.test/EXT-1"
				store.Wisps["aaa-wisp-bd-1"] = &types.Issue{ID: "aaa-wisp-bd-1", Title: "pushed wisp", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2, Ephemeral: true, ExternalRef: &ref}
				otherRef := ref + "-wisp-only"
				store.Wisps["wisp-only"] = &types.Issue{ID: "wisp-only", ExternalRef: &otherRef}
				return "bd-1", "wisp-only", nil
			},
			DependencyExists: func(context.Context) (bool, error) { return store.HasDependency("bd-2", "bd-1"), nil },
			Expected:         Expected{ExternalRef: ref, ConfigKey: "test.project", MetadataKey: "test.last_sync"},
			Refusal: func(context.Context) (*tracker.SyncResult, error) {
				return nil, &storage.ErrUnsupported{Op: "proxy-only operation", Backend: "conformance"}
			},
			APIOnly: func(ctx context.Context, _ func() tracker.Store) error {
				f.HTTP.Enqueue(Response{Status: http.StatusOK, Body: `{"teams":[]}`})
				_, err := f.HTTP.Client().Get("https://tracker.test/teams")
				return err
			},
		}
	})
}

func TestHTTPDoubleRecordsBodyAndTransportError(t *testing.T) {
	d := NewHTTPDouble()
	d.Enqueue(Response{Status: http.StatusCreated, Body: `{"id":"1"}`})
	resp, err := d.Client().Post("https://tracker.test/issues", "application/json", strings.NewReader(`{"title":"x"}`))
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("response=(%v,%v)", resp, err)
	}
	d.Enqueue(Response{Err: context.DeadlineExceeded})
	if _, err := d.Client().Get("https://tracker.test/issues"); err == nil {
		t.Fatal("transport error was swallowed")
	}
	requests := d.Requests()
	if len(requests) != 2 || string(requests[0].Body) != `{"title":"x"}` {
		t.Fatalf("requests=%+v", requests)
	}
	if _, err := d.Client().Get("https://tracker.test/underflow"); err == nil {
		t.Fatal("response queue underflow succeeded")
	}

	d.Enqueue(Response{Status: http.StatusOK})
	req, err := http.NewRequest(http.MethodGet, "https://tracker.test/copied", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	req.URL.Path = "/mutated"
	if got := d.Requests()[3].URL.Path; got != "/copied" {
		t.Fatalf("recorded URL aliases request: %q", got)
	}
}

type mockTracker struct{ client *http.Client }

func (*mockTracker) Name() string                              { return "test" }
func (*mockTracker) DisplayName() string                       { return "Test" }
func (*mockTracker) ConfigPrefix() string                      { return "test" }
func (*mockTracker) Init(context.Context, tracker.Store) error { return nil }
func (*mockTracker) Validate() error                           { return nil }
func (*mockTracker) Close() error                              { return nil }
func (t *mockTracker) FetchIssues(ctx context.Context, _ tracker.FetchOptions) ([]tracker.TrackerIssue, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://tracker.test/issues", nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var issues []tracker.TrackerIssue
	err = json.NewDecoder(resp.Body).Decode(&issues)
	return issues, err
}
func (*mockTracker) FetchIssue(context.Context, string) (*tracker.TrackerIssue, error) {
	return nil, nil
}
func (*mockTracker) CreateIssue(context.Context, *types.Issue) (*tracker.TrackerIssue, error) {
	return &tracker.TrackerIssue{ID: "EXT-3", Identifier: "EXT-3", URL: "https://tracker.test/EXT-3"}, nil
}
func (*mockTracker) UpdateIssue(context.Context, string, *types.Issue) (*tracker.TrackerIssue, error) {
	return &tracker.TrackerIssue{ID: "EXT-1", Identifier: "EXT-1", URL: "https://tracker.test/EXT-1"}, nil
}
func (*mockTracker) FieldMapper() tracker.FieldMapper                    { return mockMapper{} }
func (*mockTracker) IsExternalRef(string) bool                           { return true }
func (*mockTracker) ExtractIdentifier(ref string) string                 { return ref[strings.LastIndex(ref, "/")+1:] }
func (*mockTracker) BuildExternalRef(issue *tracker.TrackerIssue) string { return issue.URL }

type mockMapper struct{}

func (mockMapper) PriorityToBeads(interface{}) int           { return 2 }
func (mockMapper) PriorityToTracker(int) interface{}         { return 2 }
func (mockMapper) StatusToBeads(interface{}) types.Status    { return types.StatusOpen }
func (mockMapper) StatusToTracker(types.Status) interface{}  { return "open" }
func (mockMapper) TypeToBeads(interface{}) types.IssueType   { return types.TypeTask }
func (mockMapper) TypeToTracker(types.IssueType) interface{} { return "task" }
func (mockMapper) IssueToBeads(issue *tracker.TrackerIssue) *tracker.IssueConversion {
	id := "bd-2"
	if issue.Identifier == "EXT-1" {
		id = "bd-1"
	}
	conversion := &tracker.IssueConversion{Issue: &types.Issue{ID: id, Title: issue.Title, Status: types.StatusClosed, IssueType: types.TypeTask, Priority: 2, Labels: issue.Labels}}
	if issue.Identifier == "EXT-2" {
		conversion.Dependencies = []tracker.DependencyInfo{{FromExternalID: "EXT-2", ToExternalID: "EXT-1", Type: "blocks", Source: tracker.DependencySourceRelation}}
	}
	return conversion
}
func (mockMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	return map[string]interface{}{"title": issue.Title}
}
