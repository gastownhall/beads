//go:build cgo && unix

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/tracker"
	trackerconformance "github.com/steveyegge/beads/internal/tracker/conformance"
	"github.com/steveyegge/beads/internal/types"
)

// TestDirectTrackerConformance is the direct-storage leg of the same shared
// scenario used by the managed-local proxy test below. Keeping both legs on
// Run prevents the proxy assertion from becoming a one-sided contract.
func TestDirectTrackerConformance(t *testing.T) {
	bd := buildBDForInitTests(t)
	trackerconformance.Run(t, func(t *testing.T, f *trackerconformance.Fixture) trackerconformance.Setup {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "trkpx", "--non-interactive", "--skip-hooks", "--skip-agents")
		cmd := exec.Command(bd, "config", "set", "test.project", "PROJ")
		cmd.Dir, cmd.Env = dir, bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("set tracker config through bd front door: %v\n%s", err, out)
		}
		raw, err := newDoltStoreFromConfig(context.Background(), beadsDir)
		if err != nil {
			t.Fatalf("open direct tracker store: %v", err)
		}
		t.Cleanup(func() { _ = raw.Close() })
		store := tracker.NewStore(raw)
		seedTrackerConformance(t, store)
		return trackerConformanceSetup(store, func(ctx context.Context, issue *types.Issue) error { return raw.CreateIssue(ctx, issue, "conformance") }, f)
	})
}

// TestManagedLocalProxiedTrackerUOWConformance runs the shared tracker scenario
// through a real managed-local proxy and UOW provider. The normal conformance
// fixture remains map-backed for adapter tests; this test owns the distinct
// persistence and proxy boundary risk.
func TestManagedLocalProxiedTrackerUOWConformance(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildBDForInitTests(t)

	trackerconformance.Run(t, func(t *testing.T, f *trackerconformance.Fixture) trackerconformance.Setup {
		p := bdManagedLocalInit(t, bd, "trkpx", 5*time.Minute)
		if out, err := bdProxiedRun(t, bd, p.dir, "config", "set", "test.project", "PROJ"); err != nil {
			t.Fatalf("set tracker config through bd front door: %v\n%s", err, out)
		}

		provider, err := newProxiedServerUOWProvider(context.Background(), p.beadsDir, "")
		if err != nil {
			t.Fatalf("open managed-local proxy UOW provider: %v", err)
		}
		t.Cleanup(func() { _ = provider.Close(context.Background()) })
		store := tracker.NewUOWStore(provider)
		seedTrackerConformance(t, store)
		return trackerConformanceSetup(store, func(ctx context.Context, wisp *types.Issue) error {
			if err := uow.RunTx(ctx, provider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
				_, err := uw.IssueUseCase().CreateWisp(ctx, domain.CreateIssueParams{Issue: wisp}, "conformance")
				return "bd: tracker conformance wisp", err
			}); err != nil {
				return err
			}
			return nil
		}, f)
	})
}

func seedTrackerConformance(t *testing.T, store tracker.Store) {
	t.Helper()
	ref := "https://tracker.test/EXT-1"
	seed := &types.Issue{ID: "trkpx-1", Title: "local", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2, Labels: []string{"old"}, ExternalRef: &ref, UpdatedAt: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)}
	if err := store.CreateIssue(context.Background(), seed, "conformance"); err != nil {
		t.Fatalf("seed tracker issue: %v", err)
	}
}

func trackerConformanceSetup(store tracker.Store, createWisp func(context.Context, *types.Issue) error, f *trackerconformance.Fixture) trackerconformance.Setup {
	ref := "https://tracker.test/EXT-1"
	f.StoreFactory = &proxyTrackerStoreFactory{store: store}
	f.HTTP.Enqueue(trackerconformance.Response{Body: `[{"id":"EXT-1","identifier":"EXT-1","url":"https://tracker.test/EXT-1","title":"remote","updated_at":"2026-09-03T01:00:00Z","labels":[" bug ","","bug"]},{"id":"EXT-2","identifier":"EXT-2","url":"https://tracker.test/EXT-2","title":"dependent","updated_at":"2026-09-03T01:00:00Z"}]`})
	return trackerconformance.Setup{Engine: tracker.NewEngine(&proxyConformanceTracker{client: f.HTTP.Client()}, store, "conformance"), Store: store,
		Snapshot: func(ctx context.Context) (trackerconformance.Snapshot, error) {
			return proxyTrackerSnapshot(ctx, store, "test.last_sync")
		},
		SeedExternalRefPlanes: func(ctx context.Context) (string, string, error) {
			wisp := &types.Issue{ID: "aaa-wisp-trkpx-1", Title: "pushed wisp", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2, Ephemeral: true, ExternalRef: &ref}
			if err := createWisp(ctx, wisp); err != nil {
				return "", "", err
			}
			otherRef := ref + "-wisp-only"
			only := &types.Issue{ID: "wisp-only", Title: "wisp only", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2, Ephemeral: true, ExternalRef: &otherRef}
			return "trkpx-1", "wisp-only", createWisp(ctx, only)
		},
		DependencyExists: func(ctx context.Context) (bool, error) {
			deps, err := store.GetDependenciesWithMetadata(ctx, "trkpx-2")
			if err != nil {
				return false, err
			}
			for _, dep := range deps {
				if dep.ID == "trkpx-1" {
					return true, nil
				}
			}
			return false, nil
		},
		Expected: trackerconformance.Expected{ExternalRef: ref, ConfigKey: "test.project", MetadataKey: "test.last_sync"},
		Refusal: func(context.Context) (*tracker.SyncResult, error) {
			return nil, &storage.ErrUnsupported{Op: "proxy-only operation", Backend: "conformance"}
		},
		APIOnly: func(context.Context, func() tracker.Store) error {
			f.HTTP.Enqueue(trackerconformance.Response{Status: http.StatusOK, Body: `{"teams":[]}`})
			_, err := f.HTTP.Client().Get("https://tracker.test/teams")
			return err
		},
	}
}

type proxyTrackerStoreFactory struct {
	store tracker.Store
	opens int
}

func (f *proxyTrackerStoreFactory) Open() tracker.Store { f.opens++; return f.store }
func (f *proxyTrackerStoreFactory) OpenCount() int      { return f.opens }

func proxyTrackerSnapshot(ctx context.Context, store tracker.Store, lastSyncKey string) (trackerconformance.Snapshot, error) {
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{SkipWisps: true})
	if err != nil {
		return trackerconformance.Snapshot{}, err
	}
	config, err := store.GetAllConfig(ctx)
	if err != nil {
		return trackerconformance.Snapshot{}, err
	}
	lastSync, err := store.GetLocalMetadata(ctx, lastSyncKey)
	if err != nil {
		return trackerconformance.Snapshot{}, err
	}
	snapshot := trackerconformance.Snapshot{Issues: make(map[string]types.Issue, len(issues)), Dependencies: map[string][]string{}, Config: config, Metadata: map[string]string{lastSyncKey: lastSync}, LastSync: lastSync}
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		copy := *issue
		copy.Labels = append([]string(nil), issue.Labels...)
		if issue.ExternalRef != nil {
			ref := *issue.ExternalRef
			copy.ExternalRef = &ref
		}
		snapshot.Issues[copy.ID] = copy
		deps, err := store.GetDependenciesWithMetadata(ctx, copy.ID)
		if err != nil {
			return trackerconformance.Snapshot{}, err
		}
		for _, dep := range deps {
			snapshot.Dependencies[copy.ID] = append(snapshot.Dependencies[copy.ID], dep.ID)
		}
		sort.Strings(snapshot.Dependencies[copy.ID])
	}
	return snapshot, nil
}

type proxyConformanceTracker struct{ client *http.Client }

func (*proxyConformanceTracker) Name() string                              { return "test" }
func (*proxyConformanceTracker) DisplayName() string                       { return "Test" }
func (*proxyConformanceTracker) ConfigPrefix() string                      { return "test" }
func (*proxyConformanceTracker) Init(context.Context, tracker.Store) error { return nil }
func (*proxyConformanceTracker) Validate() error                           { return nil }
func (*proxyConformanceTracker) Close() error                              { return nil }
func (t *proxyConformanceTracker) FetchIssues(ctx context.Context, _ tracker.FetchOptions) ([]tracker.TrackerIssue, error) {
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
func (*proxyConformanceTracker) FetchIssue(context.Context, string) (*tracker.TrackerIssue, error) {
	return nil, nil
}
func (*proxyConformanceTracker) CreateIssue(context.Context, *types.Issue) (*tracker.TrackerIssue, error) {
	return &tracker.TrackerIssue{ID: "EXT-3", Identifier: "EXT-3", URL: "https://tracker.test/EXT-3"}, nil
}
func (*proxyConformanceTracker) UpdateIssue(context.Context, string, *types.Issue) (*tracker.TrackerIssue, error) {
	return &tracker.TrackerIssue{ID: "EXT-1", Identifier: "EXT-1", URL: "https://tracker.test/EXT-1"}, nil
}
func (*proxyConformanceTracker) FieldMapper() tracker.FieldMapper { return proxyConformanceMapper{} }
func (*proxyConformanceTracker) IsExternalRef(string) bool        { return true }
func (*proxyConformanceTracker) ExtractIdentifier(ref string) string {
	return ref[strings.LastIndex(ref, "/")+1:]
}
func (*proxyConformanceTracker) BuildExternalRef(issue *tracker.TrackerIssue) string {
	return issue.URL
}

type proxyConformanceMapper struct{}

func (proxyConformanceMapper) PriorityToBeads(interface{}) int           { return 2 }
func (proxyConformanceMapper) PriorityToTracker(int) interface{}         { return 2 }
func (proxyConformanceMapper) StatusToBeads(interface{}) types.Status    { return types.StatusOpen }
func (proxyConformanceMapper) StatusToTracker(types.Status) interface{}  { return "open" }
func (proxyConformanceMapper) TypeToBeads(interface{}) types.IssueType   { return types.TypeTask }
func (proxyConformanceMapper) TypeToTracker(types.IssueType) interface{} { return "task" }
func (proxyConformanceMapper) IssueToBeads(issue *tracker.TrackerIssue) *tracker.IssueConversion {
	id := "trkpx-2"
	if issue.Identifier == "EXT-1" {
		id = "trkpx-1"
	}
	conversion := &tracker.IssueConversion{Issue: &types.Issue{ID: id, Title: issue.Title, Status: types.StatusClosed, IssueType: types.TypeTask, Priority: 2, Labels: issue.Labels}}
	if issue.Identifier == "EXT-2" {
		conversion.Dependencies = []tracker.DependencyInfo{{FromExternalID: "EXT-2", ToExternalID: "EXT-1", Type: "blocks", Source: tracker.DependencySourceRelation}}
	}
	return conversion
}
func (proxyConformanceMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	return map[string]interface{}{"title": issue.Title}
}
