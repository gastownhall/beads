//go:build cgo && unix

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/tracker"
	trackerconformance "github.com/steveyegge/beads/internal/tracker/conformance"
	"github.com/steveyegge/beads/internal/types"
)

// TestLinearConflictResolutionSQLServerFrontdoor proves conflict policy through
// a child bd process and a real SQL-backed tracker store.
func TestLinearConflictResolutionSQLServerFrontdoor(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt SQL test server not available")
	}
	bd := buildBDForInitTests(t)
	runLinearConflictPolicies(t, func(t *testing.T) linearCommandFixture {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, ".beads", "issues.db")
		raw := newTestStoreIsolatedDB(t, dbPath, "linconflict")
		if err := os.WriteFile(filepath.Join(dir, ".beads", "config.yaml"), []byte("# Beads Config\n"), 0o600); err != nil {
			t.Fatalf("create direct SQL config.yaml: %v", err)
		}
		return linearCommandFixture{
			bd:    bd,
			dir:   dir,
			env:   append(bdEnv(dir), "BEADS_DIR="+filepath.Join(dir, ".beads"), "BEADS_TEST_SERVER=1", "LINEAR_API_KEY=test-api-key"),
			store: tracker.NewStore(raw),
			setUpdatedAt: func(id string, at time.Time) {
				linearSetIssueUpdatedAtTime(t, raw.DB(), id, at)
			},
		}
	})
}

// TestManagedLocalProxiedLinearConflictResolutionParity runs the same public
// command policy over the managed proxy's UOW-backed tracker store.
func TestManagedLocalProxiedLinearConflictResolutionParity(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildBDForInitTests(t)
	runLinearConflictPolicies(t, func(t *testing.T) linearCommandFixture {
		project := bdManagedLocalInit(t, bd, "linconflict", 5*time.Minute)
		provider, err := newProxiedServerUOWProvider(t.Context(), project.beadsDir, "")
		if err != nil {
			t.Fatalf("open managed-local proxy UOW provider: %v", err)
		}
		t.Cleanup(func() { _ = provider.Close(t.Context()) })
		proxyDB := openProxiedDB(t, project)
		return linearCommandFixture{
			bd:      bd,
			dir:     project.dir,
			env:     append(bdProxiedEnv(project.dir), "LINEAR_API_KEY=test-api-key"),
			store:   tracker.NewUOWStore(provider),
			proxied: true,
			setUpdatedAt: func(id string, at time.Time) {
				linearSetIssueUpdatedAtTime(t, proxyDB, id, at)
			},
		}
	})
}

// TestLinearDryRunSQLServerFrontdoor proves that a JSON dry-run remains
// side-effect free in both the local database and the remote GraphQL service.
func TestLinearDryRunSQLServerFrontdoor(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt SQL test server not available")
	}
	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".beads", "issues.db")
	raw := newTestStoreIsolatedDB(t, dbPath, "lindryrun")
	if err := os.WriteFile(filepath.Join(dir, ".beads", "config.yaml"), []byte("# Beads Config\n"), 0o600); err != nil {
		t.Fatalf("create direct SQL config.yaml: %v", err)
	}
	runLinearDryRunFixture(t, linearCommandFixture{
		bd:    bd,
		dir:   dir,
		env:   append(bdEnv(dir), "BEADS_DIR="+filepath.Join(dir, ".beads"), "BEADS_TEST_SERVER=1", "LINEAR_API_KEY=test-api-key"),
		store: tracker.NewStore(raw),
		setUpdatedAt: func(id string, at time.Time) {
			linearSetIssueUpdatedAtTime(t, raw.DB(), id, at)
		},
	})
}

// TestManagedLocalProxiedLinearDryRunParity proves the same no-mutation
// contract under the managed proxy and strict readonly command posture.
func TestManagedLocalProxiedLinearDryRunParity(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildBDForInitTests(t)
	project := bdManagedLocalInit(t, bd, "lindryrun", 5*time.Minute)
	provider, err := newProxiedServerUOWProvider(t.Context(), project.beadsDir, "")
	if err != nil {
		t.Fatalf("open managed-local proxy UOW provider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close(t.Context()) })
	proxyDB := openProxiedDB(t, project)
	runLinearDryRunFixture(t, linearCommandFixture{
		bd:      bd,
		dir:     project.dir,
		env:     append(bdProxiedEnv(project.dir), "LINEAR_API_KEY=test-api-key"),
		store:   tracker.NewUOWStore(provider),
		proxied: true,
		setUpdatedAt: func(id string, at time.Time) {
			linearSetIssueUpdatedAtTime(t, proxyDB, id, at)
		},
	})
}

type linearCommandFixture struct {
	bd           string
	dir          string
	env          []string
	store        tracker.Store
	setUpdatedAt func(string, time.Time)
	proxied      bool
}

func (f linearCommandFixture) run(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(f.bd, args...)
	cmd.Dir = f.dir
	cmd.Env = append([]string(nil), f.env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (f linearCommandFixture) configure(t *testing.T, endpoint string) {
	t.Helper()
	for _, entry := range [][2]string{
		{"linear.team_id", linearProxyTestTeamID},
		{"linear.api_endpoint", endpoint},
		{"linear.state_map.unstarted", "open"},
		{"linear.state_map.started", "in_progress"},
		{"linear.state_map.completed", "closed"},
	} {
		f.run(t, "config", "set", entry[0], entry[1])
	}
}

func runLinearConflictPolicies(t *testing.T, newFixture func(*testing.T) linearCommandFixture) {
	t.Helper()
	const previousSync = "2020-01-01T00:00:00Z"
	newerTS := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	olderTS := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name          string
		identifier    string
		args          []string
		localUpdated  time.Time
		remoteUpdated time.Time
		wantLocal     bool
	}{
		{name: "timestamp default chooses newer local", identifier: "MOCK-10", localUpdated: newerTS, remoteUpdated: olderTS, wantLocal: true},
		// The timestamp default's other arm reaches the pull-overwrite path via
		// allowPullOverwriteIDs through `default:`+`else` rather than the
		// ConflictExternal case below, so its skip/overwrite bookkeeping can
		// regress independently.
		{name: "timestamp default imports newer Linear", identifier: "MOCK-13", localUpdated: olderTS, remoteUpdated: newerTS},
		{name: "prefer local overrides newer Linear", identifier: "MOCK-11", args: []string{"--prefer-local"}, localUpdated: olderTS, remoteUpdated: newerTS, wantLocal: true},
		{name: "prefer Linear overrides newer local", identifier: "MOCK-12", args: []string{"--prefer-linear"}, localUpdated: newerTS, remoteUpdated: olderTS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFixture(t)
			mock := newMockLinearServer(linearProxyTestTeamID, "MOCK")
			server := httptest.NewServer(mock)
			t.Cleanup(server.Close)
			fixture.configure(t, server.URL)

			remoteTitle := "remote " + tc.name
			seedLinearConflictIssue(mock, tc.identifier, remoteTitle, tc.remoteUpdated)
			localTitle := "local " + tc.name
			wantTitle := remoteTitle
			if tc.wantLocal {
				wantTitle = localTitle
			}
			local := parseIssueJSON(t, fixture.run(t, "create", "--json", localTitle, "--external-ref", "https://linear.app/mock/issue/"+tc.identifier))
			fixture.setUpdatedAt(local.ID, tc.localUpdated)
			if err := fixture.store.SetLocalMetadata(t.Context(), "linear.last_sync", previousSync); err != nil {
				t.Fatalf("seed linear.last_sync: %v", err)
			}

			cmd := exec.Command(fixture.bd, append([]string{"--json", "linear", "sync"}, tc.args...)...)
			cmd.Dir = fixture.dir
			cmd.Env = append([]string(nil), fixture.env...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("conflict sync: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr.String())
			}
			var result tracker.SyncResult
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("conflict stdout is not one JSON document: %v\n%s", err, out)
			}
			if result.Stats.Conflicts != 1 {
				t.Fatalf("conflicts = %d, want 1; result=%+v", result.Stats.Conflicts, result)
			}
			issues, err := fixture.store.SearchIssues(t.Context(), "", types.IssueFilter{})
			if err != nil {
				t.Fatalf("read local conflict result: %v", err)
			}
			gotLocal := findLinearIssueByID(t, issues, local.ID)
			if gotLocal.Title != wantTitle {
				t.Fatalf("local title = %q, want %q", gotLocal.Title, wantTitle)
			}
			if gotRemote := linearMockIssueTitle(t, mock, tc.identifier); gotRemote != wantTitle {
				t.Fatalf("remote title = %q, want %q", gotRemote, wantTitle)
			}
		})
	}
}

func runLinearDryRunFixture(t *testing.T, fixture linearCommandFixture) {
	t.Helper()
	if err := os.Chmod(filepath.Join(fixture.dir, ".beads"), 0o700); err != nil {
		t.Fatalf("restrict .beads permissions for stable dry-run warnings: %v", err)
	}
	mock := newMockLinearServer(linearProxyTestTeamID, "MOCK")
	server := httptest.NewServer(mock)
	t.Cleanup(server.Close)
	fixture.configure(t, server.URL)
	seedLinearConflictIssue(mock, "MOCK-1", "remote dry-run title", time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
	// An unlinked remote issue makes pull planning emit an OnMessage line too.
	seedLinearConflictIssue(mock, "MOCK-2", "remote issue that dry-run would import", time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))
	// Pin the premise the direction table below measures, so a seed-side loss
	// fails here rather than as a mystery stats mismatch sixty lines down.
	if got := mock.issueCount(); got != 2 {
		t.Fatalf("dry-run fixture seeded %d remote issues, want 2 (linked MOCK-1 + unlinked MOCK-2)", got)
	}

	linked := parseIssueJSON(t, fixture.run(t, "create", "--json", "local dry-run title", "--external-ref", "https://linear.app/mock/issue/MOCK-1"))
	other := parseIssueJSON(t, fixture.run(t, "create", "--json", "local issue that dry-run would create"))
	fixture.run(t, "label", "add", linked.ID, "local-label")
	fixture.run(t, "dep", "add", linked.ID, other.ID)
	fixture.setUpdatedAt(linked.ID, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err := fixture.store.SetLocalMetadata(t.Context(), "linear.last_sync", "2020-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed linear.last_sync: %v", err)
	}

	before, err := proxyTrackerSnapshot(t.Context(), fixture.store, "linear.last_sync")
	if err != nil {
		t.Fatalf("snapshot before dry-run: %v", err)
	}
	remoteBefore := mock.snapshotIssues(t)

	// Ordinary dry-runs are valid through both stores. Test each direction
	// separately: combined sync has different conflict and push planning paths
	// than either one-way invocation.
	//
	// wantConflicts/wantPulled pin what each direction is supposed to plan, so the
	// seeds above cannot stop meaning what they say without a failure here: the
	// linked MOCK-1 pair is what makes conflict messages reachable, and the
	// unlinked MOCK-2 is what makes an import line reachable. Conflict detection
	// runs only for bidirectional sync (internal/tracker/engine.go:178-187), so
	// the one-way directions legitimately report zero conflicts, and the
	// per-phase PullStats/PushStats are `json:"-"` so only the SyncStats
	// aggregate is observable from the command's output.
	for _, tc := range []struct {
		name          string
		args          []string
		wantConflicts int
		wantPulled    int
	}{
		{name: "pull", args: []string{"--json", "linear", "sync", "--dry-run", "--pull"}, wantPulled: 1},
		{name: "push", args: []string{"--json", "linear", "sync", "--dry-run", "--push"}},
		{name: "bidirectional", args: []string{"--json", "linear", "sync", "--dry-run"}, wantConflicts: 1, wantPulled: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(fixture.bd, tc.args...)
			cmd.Dir = fixture.dir
			cmd.Env = append([]string(nil), fixture.env...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("dry-run bd %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(tc.args, " "), err, out, stderr.String())
			}
			var result tracker.SyncResult
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("dry-run stdout is not one JSON document: %v\n%s", err, out)
			}
			if !result.Success || result.LastSync != "" || len(result.Warnings) != 0 {
				t.Fatalf("dry-run result = %+v, want success without warnings or last_sync", result)
			}
			if result.Stats.Conflicts != tc.wantConflicts {
				t.Fatalf("dry-run conflicts = %d, want %d; result=%+v\nstderr:\n%s", result.Stats.Conflicts, tc.wantConflicts, result, stderr.String())
			}
			if result.Stats.Pulled != tc.wantPulled {
				t.Fatalf("dry-run pulled = %d, want %d; result=%+v\nstderr:\n%s", result.Stats.Pulled, tc.wantPulled, result, stderr.String())
			}
			if strings.Contains(stderr.String(), "Warning:") {
				t.Fatalf("dry-run emitted an unexpected warning:\n%s", stderr.String())
			}
			assertLinearSnapshotsUnchanged(t, fixture, mock, before, remoteBefore, tc.name+" dry-run")
		})
	}

	t.Run("text plan", func(t *testing.T) {
		// The guard encodes stream placement, so read stdout alone: the text arm
		// prints the plan on stdout, and the JSON subtests above prove the same
		// lines never reach stdout with --json.
		cmd := exec.Command(fixture.bd, "linear", "sync", "--dry-run")
		cmd.Dir = fixture.dir
		cmd.Env = append([]string(nil), fixture.env...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("text dry-run: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr.String())
		}
		if !strings.Contains(string(out), "[dry-run] Would") {
			t.Fatalf("text dry-run is missing the human-readable plan on stdout:\nstdout:\n%s\nstderr:\n%s", out, stderr.String())
		}
		assertLinearSnapshotsUnchanged(t, fixture, mock, before, remoteBefore, "text dry-run")
	})

	assertLinearJSONRefusalDoesNotMutate(t, fixture, mock, before, remoteBefore,
		[]string{"--json", "linear", "sync", "--prefer-local", "--prefer-linear"},
		"cannot use both --prefer-local and --prefer-linear")
	assertLinearStrictReadonlyMatrix(t, fixture, mock, before, remoteBefore)
}

func assertLinearJSONRefusalDoesNotMutate(t *testing.T, fixture linearCommandFixture, mock *mockLinearServer, before trackerconformance.Snapshot, remoteBefore map[string]linear.Issue, args []string, want string) {
	t.Helper()
	cmd := exec.Command(fixture.bd, args...)
	cmd.Dir = fixture.dir
	cmd.Env = append([]string(nil), fixture.env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		t.Fatalf("bd %s unexpectedly succeeded", strings.Join(args, " "))
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("bd %s exit = %v, want exit 1", strings.Join(args, " "), err)
	}
	var response map[string]any
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatalf("refusal stdout is not JSON: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr.String())
	}
	if !strings.Contains(strings.TrimSpace(string(out)), want) {
		t.Fatalf("refusal JSON %s does not contain %q", out, want)
	}
	assertLinearSnapshotsUnchanged(t, fixture, mock, before, remoteBefore, "unsupported option refusal")
}

func assertLinearStrictReadonlyMatrix(t *testing.T, fixture linearCommandFixture, mock *mockLinearServer, before trackerconformance.Snapshot, remoteBefore map[string]linear.Issue) {
	t.Helper()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "pull", args: []string{"--readonly", "--json", "linear", "sync", "--dry-run", "--pull"}},
		{name: "push", args: []string{"--readonly", "--json", "linear", "sync", "--dry-run", "--push"}},
		{name: "bidirectional", args: []string{"--readonly", "--json", "linear", "sync", "--dry-run"}},
	} {
		t.Run("dry-run "+tc.name, func(t *testing.T) {
			if fixture.proxied {
				assertLinearProxyReadonlyRefusalDoesNotMutate(t, fixture, mock, before, remoteBefore, tc.args)
				return
			}
			assertLinearReadonlyDryRunSucceedsWithoutMutation(t, fixture, mock, before, remoteBefore, tc.args)
		})
	}

	mutatingArgs := []string{"--readonly", "--json", "linear", "sync", "--pull"}
	if fixture.proxied {
		assertLinearProxyReadonlyRefusalDoesNotMutate(t, fixture, mock, before, remoteBefore, mutatingArgs)
		return
	}
	assertLinearDirectReadonlyRefusalDoesNotMutate(t, fixture, mock, before, remoteBefore, mutatingArgs)
}

func assertLinearReadonlyDryRunSucceedsWithoutMutation(t *testing.T, fixture linearCommandFixture, mock *mockLinearServer, before trackerconformance.Snapshot, remoteBefore map[string]linear.Issue, args []string) {
	t.Helper()
	cmd := exec.Command(fixture.bd, args...)
	cmd.Dir = fixture.dir
	cmd.Env = append([]string(nil), fixture.env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("readonly bd %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, out, stderr.String())
	}
	var result tracker.SyncResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("readonly dry-run stdout is not one JSON document: %v\n%s", err, out)
	}
	if !result.Success || result.LastSync != "" || len(result.Warnings) != 0 {
		t.Fatalf("readonly dry-run result = %+v, want success without warnings or last_sync", result)
	}
	if strings.Contains(stderr.String(), "Warning:") {
		t.Fatalf("readonly dry-run emitted an unexpected warning:\n%s", stderr.String())
	}
	assertLinearSnapshotsUnchanged(t, fixture, mock, before, remoteBefore, "readonly dry-run")
}

func assertLinearProxyReadonlyRefusalDoesNotMutate(t *testing.T, fixture linearCommandFixture, mock *mockLinearServer, before trackerconformance.Snapshot, remoteBefore map[string]linear.Issue, args []string) {
	t.Helper()
	cmd := exec.Command(fixture.bd, args...)
	cmd.Dir = fixture.dir
	cmd.Env = append([]string(nil), fixture.env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		t.Fatalf("proxied readonly bd %s unexpectedly succeeded:\n%s", strings.Join(args, " "), out)
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("proxied readonly bd %s exit = %v, want exit 1", strings.Join(args, " "), err)
	}
	var response map[string]any
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatalf("proxied readonly refusal is not JSON: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr.String())
	}
	if response["schema_version"] != float64(JSONSchemaVersion) || response["code"] != "proxy.readonly.unsupported" || response["mutates"] != false {
		t.Fatalf("proxied readonly refusal = %#v, want schema_version=%d code=proxy.readonly.unsupported mutates=false", response, JSONSchemaVersion)
	}
	assertLinearSnapshotsUnchanged(t, fixture, mock, before, remoteBefore, "proxied readonly refusal")
}

func assertLinearDirectReadonlyRefusalDoesNotMutate(t *testing.T, fixture linearCommandFixture, mock *mockLinearServer, before trackerconformance.Snapshot, remoteBefore map[string]linear.Issue, args []string) {
	t.Helper()
	cmd := exec.Command(fixture.bd, args...)
	cmd.Dir = fixture.dir
	cmd.Env = append([]string(nil), fixture.env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if out, err := cmd.Output(); err == nil {
		t.Fatalf("direct readonly bd %s unexpectedly succeeded:\n%s", strings.Join(args, " "), out)
	} else if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("direct readonly bd %s exit = %v, want exit 1", strings.Join(args, " "), err)
	}
	// CheckReadonly's legacy refusal writes plain stderr even with --json.
	if !strings.Contains(stderr.String(), "operation 'linear sync' is not allowed in read-only mode") {
		t.Fatalf("direct readonly refusal missing stable error:\n%s", stderr.String())
	}
	assertLinearSnapshotsUnchanged(t, fixture, mock, before, remoteBefore, "direct readonly refusal")
}

func assertLinearSnapshotsUnchanged(t *testing.T, fixture linearCommandFixture, mock *mockLinearServer, before trackerconformance.Snapshot, remoteBefore map[string]linear.Issue, operation string) {
	t.Helper()
	after, err := proxyTrackerSnapshot(t.Context(), fixture.store, "linear.last_sync")
	if err != nil {
		t.Fatalf("snapshot after %s: %v", operation, err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%s changed local state:\nbefore=%#v\nafter=%#v", operation, before, after)
	}
	if remoteAfter := mock.snapshotIssues(t); !reflect.DeepEqual(remoteBefore, remoteAfter) {
		t.Fatalf("%s changed remote state:\nbefore=%#v\nafter=%#v", operation, remoteBefore, remoteAfter)
	}
}

// seedLinearConflictIssue adds one remote issue to the fixture. It is additive:
// fixtures that need several remote issues call it once per issue. The map key
// is the issue's GraphQL id (handleUpdate keys on it, and this helper keeps the
// key equal to Issue.ID); linearMockIssueTitle and handleFetchIssues scan by
// Identifier. The sequence counter only moves forward, past the highest seeded
// number, so a later create cannot mint a duplicate Identifier.
func seedLinearConflictIssue(mock *mockLinearServer, identifier, title string, updated time.Time) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.issues == nil {
		mock.issues = map[string]*linear.Issue{}
	}
	id := "remote-" + identifier
	mock.issues[id] = &linear.Issue{
		ID:          id,
		Identifier:  identifier,
		Title:       title,
		Description: "remote conflict fixture",
		URL:         "https://linear.app/mock/issue/" + identifier,
		Priority:    2,
		State:       &linear.State{ID: "state-unstarted", Name: "Todo", Type: "unstarted"},
		CreatedAt:   "2019-01-01T00:00:00Z",
		UpdatedAt:   updated.UTC().Format(time.RFC3339),
	}
	if seq, err := strconv.Atoi(identifier[strings.LastIndex(identifier, "-")+1:]); err == nil {
		mock.nextSeq = max(mock.nextSeq, seq)
	}
}

func linearSetIssueUpdatedAtTime(t *testing.T, db *sql.DB, id string, at time.Time) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), "UPDATE issues SET updated_at = ? WHERE id = ?", at, id); err != nil {
		t.Fatalf("seed local issue %s updated_at: %v", id, err)
	}
}

func findLinearIssueByID(t *testing.T, issues []*types.Issue, id string) *types.Issue {
	t.Helper()
	for _, issue := range issues {
		if issue.ID == id {
			return issue
		}
	}
	t.Fatalf("no local issue %q", id)
	return nil
}
