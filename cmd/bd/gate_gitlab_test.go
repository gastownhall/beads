package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/types"
)

func glFixtureRunner(t *testing.T, want []string, response string) ghCommandRunner {
	t.Helper()
	return func(args ...string) ([]byte, []byte, error) {
		t.Helper()
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("glab args = %q, want %q", args, want)
		}
		return []byte(response), nil, nil
	}
}

func TestGitLabPipelineStatuses(t *testing.T) {
	for _, status := range []string{"success", "failed", "canceled", "created", "waiting_for_resource", "preparing", "pending", "running", "manual", "scheduled", "skipped", "future_state", ""} {
		t.Run(status, func(t *testing.T) {
			gate := &types.Issue{AwaitType: "gl:pipeline", AwaitID: "456"}
			response, _ := json.Marshal(map[string]interface{}{"id": 456, "iid": 2, "status": status})
			run := glFixtureRunner(t, []string{"api", "projects/:fullpath/pipelines/456", "--method", "GET"}, string(response))
			resolved, failed, _, err := checkGLPipelineWithRunner(gate, run)
			if err != nil || resolved != (status == "success") || failed != (status == "failed" || status == "canceled") {
				t.Fatalf("status %q: resolved=%v failed=%v err=%v", status, resolved, failed, err)
			}
		})
	}
}

func TestGitLabMergeRequestStates(t *testing.T) {
	for _, state := range []string{"merged", "closed", "opened", "locked", "future_state", ""} {
		t.Run(state, func(t *testing.T) {
			gate := &types.Issue{AwaitType: "gl:mr", AwaitID: "7", Metadata: json.RawMessage(`{"repo":"team/subgroup/project"}`)}
			response, _ := json.Marshal(map[string]interface{}{"id": 900, "iid": 7, "state": state, "title": "Change"})
			run := glFixtureRunner(t, []string{"api", "projects/team%2Fsubgroup%2Fproject/merge_requests/7", "--method", "GET"}, string(response))
			resolved, failed, _, err := checkGLMRWithRunner(gate, run)
			if err != nil || resolved != (state == "merged") || failed != (state == "closed") {
				t.Fatalf("state %q: resolved=%v failed=%v err=%v", state, resolved, failed, err)
			}
		})
	}
}

func TestGitLabProjectSelectors(t *testing.T) {
	for _, tt := range []struct {
		repo, project, host string
	}{
		{"", ":fullpath", ""},
		{"team/project", "team%2Fproject", ""},
		{"team/sub/project", "team%2Fsub%2Fproject", ""},
		{"team.name/sub/project", "team.name%2Fsub%2Fproject", ""},
		{"https://forge/team/sub/project.git", "team%2Fsub%2Fproject", "forge"},
		{"https://code.example.org:8443/team/project", "team%2Fproject", "code.example.org:8443"},
	} {
		t.Run(tt.repo, func(t *testing.T) {
			project, host, err := gitlabProjectSelector(tt.repo)
			if err != nil || project != tt.project || host != tt.host {
				t.Fatalf("selector = %q %q %v, want %q %q", project, host, err, tt.project, tt.host)
			}
		})
	}
	for _, repo := range []string{"project", "team//project", "team/../project", "team/project?x=1", " team/project", "https://forge/team/project?x=1", "https://user@forge/team/project", "ftp://forge/team/project"} {
		if _, _, err := gitlabProjectSelector(repo); err == nil {
			t.Errorf("accepted invalid selector %q", repo)
		}
	}
}

func TestGitLabHostArguments(t *testing.T) {
	for _, repo := range []string{"", "team/sub/project", "https://forge/team/sub/project"} {
		t.Run(repo, func(t *testing.T) {
			project, host, _ := gitlabProjectSelector(repo)
			want := []string{"api", "projects/" + project + "/pipelines/42", "--method", "GET"}
			if host != "" {
				want = append(want, "--hostname", host)
			}
			var response glPipeline
			if err := queryGitLabAPI(repo, "pipelines/42", glFixtureRunner(t, want, `{"id":42,"status":"running"}`), &response); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitLabMetadataAndCreation(t *testing.T) {
	for _, raw := range []string{`{"repo":null}`, `{"repo":12}`, `{"repo":[]}`, `[]`, `{`, `{"repo":"team//project"}`} {
		if _, err := gitlabRepoFromIssue(&types.Issue{Metadata: json.RawMessage(raw)}); err == nil {
			t.Errorf("accepted invalid metadata %s", raw)
		}
	}
	for _, gateType := range []string{"gl:pipeline", "gl:mr"} {
		target := &types.Issue{ID: "bd-target", Metadata: json.RawMessage(`{"repo":"team/sub/project","other":true}`)}
		metadata, err := gateMetadataForCreate(gateCreateInput{gateType: gateType}, target)
		if err != nil || string(metadata) != `{"repo":"team/sub/project"}` {
			t.Fatalf("inherited metadata = %s, err=%v", metadata, err)
		}
		metadata, err = gateMetadataForCreate(gateCreateInput{gateType: gateType, repo: "https://forge/other/project"}, target)
		if err != nil || string(metadata) != `{"repo":"https://forge/other/project"}` {
			t.Fatalf("explicit metadata = %s, err=%v", metadata, err)
		}
	}
}

func TestGitLabCheckErrors(t *testing.T) {
	for _, gateType := range []string{"gl:pipeline", "gl:mr"} {
		check := checkGLPipelineWithRunner
		if gateType == "gl:mr" {
			check = checkGLMRWithRunner
		}
		for _, response := range []string{`{`, `null`, `{}`, `{"id":900,"iid":900,"status":"success","state":"merged"}`, `{"id":"42","iid":"42"}`} {
			gate := &types.Issue{AwaitType: gateType, AwaitID: "42"}
			run := func(...string) ([]byte, []byte, error) { return []byte(response), nil, nil }
			resolved, failed, _, err := check(gate, run)
			if err == nil || resolved || failed {
				t.Errorf("%s accepted malformed response %s", gateType, response)
			}
		}
		for _, diagnostic := range []string{"not authenticated", "HTTP 404", "HTTP 429", "executable file not found"} {
			run := func(...string) ([]byte, []byte, error) { return nil, []byte(diagnostic), errors.New("CLI failed") }
			resolved, failed, _, err := check(&types.Issue{AwaitType: gateType, AwaitID: "42"}, run)
			if err == nil || !strings.Contains(err.Error(), diagnostic) || resolved || failed {
				t.Errorf("%s CLI failure: resolved=%v failed=%v err=%v", gateType, resolved, failed, err)
			}
		}
		run := func(...string) ([]byte, []byte, error) { t.Fatal("unexpected CLI invocation"); return nil, nil, nil }
		if resolved, failed, _, err := check(&types.Issue{AwaitID: ""}, run); err != nil || resolved || failed {
			t.Errorf("empty ID should wait: %v", err)
		}
		if _, _, _, err := check(&types.Issue{AwaitID: "--bad"}, run); err == nil {
			t.Error("accepted nonnumeric ID")
		}
	}
}

func TestGitLabDiscoveryAtBranchHEAD(t *testing.T) {
	gate := &types.Issue{AwaitType: "gl:pipeline", Metadata: json.RawMessage(`{"repo":"https://forge/team/sub/project"}`)}
	run := func(args ...string) ([]byte, []byte, error) {
		if len(args) != 6 || args[0] != "api" || args[2] != "--method" || args[3] != "GET" || args[4] != "--hostname" || args[5] != "forge" {
			t.Fatalf("args = %q", args)
		}
		u, err := url.Parse(args[1])
		if _, hasRef := u.Query()["ref"]; err != nil || hasRef || u.Query().Get("sha") != "abc123" || u.Query().Get("per_page") != "20" || u.Query().Get("order_by") != "id" || u.Query().Get("sort") != "desc" {
			t.Fatalf("invalid discovery query: %s", args[1])
		}
		return []byte(`[{"id":456,"iid":2,"sha":"abc123","ref":"feature/a","status":"running"}]`), nil, nil
	}
	if id, err := discoverGLPipelineWithRunner(gate, "feature/a", "abc123", run); err != nil || id != "456" {
		t.Fatalf("discovery = %q %v", id, err)
	}
	for _, response := range []string{`[]`, `[{"id":42,"sha":"old","ref":"feature/a"}]`, `[{"id":42,"sha":"abc123","ref":"other"}]`, `{`, `[{"iid":2,"sha":"abc123","ref":"feature/a"}]`} {
		run := func(...string) ([]byte, []byte, error) { return []byte(response), nil, nil }
		id, err := discoverGLPipelineWithRunner(gate, "feature/a", "abc123", run)
		if response == `[]` {
			if id != "" || err != nil {
				t.Fatalf("empty list = %q %v", id, err)
			}
		} else if err == nil {
			t.Errorf("accepted discovery response %s", response)
		}
	}
	if !needsDiscovery(&types.Issue{AwaitType: "gl:pipeline"}) || needsDiscovery(&types.Issue{AwaitType: "gl:mr"}) || needsDiscovery(&types.Issue{AwaitType: "gl:pipeline", AwaitID: "456"}) {
		t.Fatal("incorrect GitLab discovery selection")
	}
}

func TestGitLabDiscoveryMergeRequestPipelines(t *testing.T) {
	const head = "abc123"
	tests := []struct {
		name     string
		response string
		wantID   string
		wantErr  bool
	}{
		{"branch ref", `[{"id":10,"sha":"abc123","ref":"feature/a","source":"push"}]`, "10", false},
		{"branch ref without source", `[{"id":10,"sha":"abc123","ref":"feature/a"}]`, "10", false},
		{"merge request head ref", `[{"id":11,"sha":"abc123","ref":"refs/merge-requests/7/head","source":"merge_request_event"}]`, "11", false},
		{"valid branch pipeline after rejected tag pipeline", `[{"id":13,"sha":"abc123","ref":"v1.0","source":"push"},{"id":12,"sha":"abc123","ref":"feature/a","source":"push"}]`, "12", false},
		{"valid merge request pipeline after rejected foreign branch", `[{"id":15,"sha":"abc123","ref":"other","source":"push"},{"id":14,"sha":"abc123","ref":"refs/merge-requests/8/head","source":"merge_request_event"}]`, "14", false},
		{"newest accepted pipeline wins", `[{"id":17,"sha":"abc123","ref":"refs/merge-requests/8/head","source":"merge_request_event"},{"id":16,"sha":"abc123","ref":"feature/a","source":"push"}]`, "17", false},
		{"merge request ref with push source", `[{"id":18,"sha":"abc123","ref":"refs/merge-requests/7/head","source":"push"}]`, "", true},
		{"merge request ref without source", `[{"id":18,"sha":"abc123","ref":"refs/merge-requests/7/head"}]`, "", true},
		{"merge result ref", `[{"id":19,"sha":"abc123","ref":"refs/merge-requests/7/merge","source":"merge_request_event"}]`, "", true},
		{"nonnumeric merge request ref", `[{"id":20,"sha":"abc123","ref":"refs/merge-requests/x/head","source":"merge_request_event"}]`, "", true},
		{"empty merge request ref", `[{"id":20,"sha":"abc123","ref":"refs/merge-requests//head","source":"merge_request_event"}]`, "", true},
		{"zero merge request ref", `[{"id":20,"sha":"abc123","ref":"refs/merge-requests/0/head","source":"merge_request_event"}]`, "", true},
		{"nested merge request ref", `[{"id":20,"sha":"abc123","ref":"refs/merge-requests/7/8/head","source":"merge_request_event"}]`, "", true},
		{"foreign merge request namespace", `[{"id":21,"sha":"abc123","ref":"refs/heads/refs/merge-requests/7/head","source":"merge_request_event"}]`, "", true},
		{"foreign branch", `[{"id":22,"sha":"abc123","ref":"other","source":"merge_request_event"}]`, "", true},
		{"tag ref", `[{"id":23,"sha":"abc123","ref":"v1.0","source":"push"}]`, "", true},
		{"wrong sha on branch ref", `[{"id":24,"sha":"def456","ref":"feature/a","source":"push"}]`, "", true},
		{"wrong sha on merge request ref", `[{"id":25,"sha":"def456","ref":"refs/merge-requests/7/head","source":"merge_request_event"}]`, "", true},
		{"nonpositive ID", `[{"id":0,"sha":"abc123","ref":"feature/a"}]`, "", true},
		{"no pipelines yet", `[]`, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{AwaitType: "gl:pipeline"}
			run := glFixtureRunner(t, []string{"api", "projects/:fullpath/pipelines?order_by=id&per_page=20&sha=abc123&sort=desc", "--method", "GET"}, tt.response)
			id, err := discoverGLPipelineWithRunner(gate, "feature/a", head, run)
			if (err != nil) != tt.wantErr || id != tt.wantID {
				t.Fatalf("discovery = %q, %v; want %q, error=%v", id, err, tt.wantID, tt.wantErr)
			}
		})
	}
}

func TestGitLabManualCloseParity(t *testing.T) {
	previous := runGLCommandFunc
	t.Cleanup(func() { runGLCommandFunc = previous })
	for _, tt := range []struct {
		gateType, response string
		wantClose          bool
	}{
		{"gl:pipeline", `{"id":42,"status":"success"}`, true},
		{"gl:pipeline", `{"id":42,"status":"running"}`, false},
		{"gl:pipeline", `{"id":42,"status":"failed"}`, false},
		{"gl:pipeline", `{"id":42,"status":"canceled"}`, false},
		{"gl:mr", `{"iid":42,"state":"merged"}`, true},
		{"gl:mr", `{"iid":42,"state":"opened"}`, false},
		{"gl:mr", `{"iid":42,"state":"closed"}`, false},
	} {
		runGLCommandFunc = func(...string) ([]byte, []byte, error) { return []byte(tt.response), nil, nil }
		gate := &types.Issue{IssueType: "gate", AwaitType: tt.gateType, AwaitID: "42"}
		if !isMachineCheckableGate(gate) || !shouldCheckGate(gate, "gl") || shouldCheckGate(gate, "gh") {
			t.Fatalf("gate type not wired: %s", tt.gateType)
		}
		results := evaluateGates(context.Background(), []*types.Issue{gate}, time.Now(), nil, nil, nil)
		if len(results) != 1 || results[0].err != nil || results[0].resolved != tt.wantClose {
			t.Fatalf("gate check disagrees with manual close for %s", tt.response)
		}
		if err := checkGateSatisfaction(gate, nil); (err == nil) != tt.wantClose {
			t.Errorf("%s manual close: %v, wantClose=%v", tt.response, err, tt.wantClose)
		}
	}
	// Preserve the existing forge-gate manual-close policy on CLI errors.
	runGLCommandFunc = func(...string) ([]byte, []byte, error) {
		return nil, []byte("not authenticated"), errors.New("CLI failed")
	}
	for _, gateType := range []string{"gl:pipeline", "gl:mr"} {
		gate := &types.Issue{IssueType: "gate", AwaitType: gateType, AwaitID: "42"}
		if err := checkGateSatisfaction(gate, nil); err != nil {
			t.Errorf("manual close should retain forge error policy: %v", err)
		}
	}
}

func TestGitLabFormulaRepo(t *testing.T) {
	for _, gateType := range []string{"gl:pipeline", "gl:mr"} {
		step := &formula.Step{ID: "await-ci", Gate: &formula.Gate{Type: gateType, AwaitID: "42", Repo: "team/sub/project"}}
		gate := createGateIssue(step, "mol-release")
		if gate == nil || gate.AwaitID != "42" || string(gate.Metadata) != `{"repo":"team/sub/project"}` {
			t.Fatalf("GitLab formula gate = %+v", gate)
		}
	}
}

func TestGitLabDiscoveryErrorsAndIsolation(t *testing.T) {
	gate := &types.Issue{AwaitType: "gl:pipeline"}
	run := func(...string) ([]byte, []byte, error) {
		return nil, []byte("not authenticated"), errors.New("CLI failed")
	}
	if _, err := discoverGLPipelineWithRunner(gate, "main", "sha", run); err == nil {
		t.Fatal("discovery swallowed CLI error")
	}
	neverRun := func(...string) ([]byte, []byte, error) {
		t.Fatal("discovery queried without branch HEAD")
		return nil, nil, nil
	}
	for _, pair := range [][2]string{{"", "sha"}, {"HEAD", "sha"}, {"main", ""}} {
		if _, err := discoverGLPipelineWithRunner(gate, pair[0], pair[1], neverRun); err == nil {
			t.Error("discovery accepted missing branch HEAD")
		}
	}
	github := &types.Issue{AwaitType: "gh:run"}
	if selected := githubDiscoveryGates([]*types.Issue{gate, github}); len(selected) != 1 || selected[0] != github {
		t.Fatal("GitLab gate leaked into GitHub discovery")
	}
}
