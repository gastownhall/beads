package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// GitLab API contracts: pipelines use id (not iid); merge requests use the
// project-local iid. Keep the response fields explicit rather than decoding
// presentation output from glab ci or glab mr.
type glPipeline struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
}

type glMergeRequest struct {
	IID   int64  `json:"iid"`
	State string `json:"state"`
	Title string `json:"title"`
}

var runGLCommandFunc ghCommandRunner = runGLCommand

func runGLCommand(args ...string) (stdout, stderr []byte, err error) {
	cmd := exec.Command("glab", args...) // #nosec G204 -- argument vector, no shell
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err = cmd.Run()
	return out.Bytes(), diagnostic.Bytes(), err
}

func isGitLabGateType(gateType string) bool {
	return gateType == "gl:pipeline" || gateType == "gl:mr"
}

func gitlabRepoFromIssue(issue *types.Issue) (string, error) {
	if issue == nil || len(issue.Metadata) == 0 || string(issue.Metadata) == "null" {
		return "", nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
		return "", fmt.Errorf("metadata must be a JSON object: %w", err)
	}
	raw, exists := metadata["repo"]
	if !exists {
		return "", nil
	}
	var repo *string
	if err := json.Unmarshal(raw, &repo); err != nil || repo == nil {
		return "", fmt.Errorf("metadata.repo must be a string")
	}
	if *repo != "" {
		if _, _, err := gitlabProjectSelector(*repo); err != nil {
			return "", err
		}
	}
	return *repo, nil
}

// An empty selector delegates remote/project and authenticated-host selection
// to glab. This supports configured self-managed hosts regardless of their
// name. Bare paths allow nested namespaces; URLs disambiguate arbitrary hosts.
func gitlabProjectSelector(repo string) (project, host string, err error) {
	if repo == "" {
		return ":fullpath", "", nil
	}
	if strings.Contains(repo, "://") {
		u, parseErr := url.Parse(repo)
		if parseErr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", "", fmt.Errorf("invalid GitLab project URL %q", repo)
		}
		host, repo = u.Host, strings.TrimPrefix(u.Path, "/")
	}
	repo = strings.TrimSuffix(repo, ".git")
	parts := strings.Split(repo, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("GitLab repo must use NAMESPACE/PROJECT or https://HOST/NAMESPACE/PROJECT")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", fmt.Errorf("invalid GitLab project path %q", repo)
		}
		for _, c := range part {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
				continue
			}
			return "", "", fmt.Errorf("invalid GitLab project path %q", repo)
		}
	}
	return url.PathEscape(repo), host, nil
}

func queryGitLabAPI(repo, resource string, runGL ghCommandRunner, result interface{}) error {
	project, host, err := gitlabProjectSelector(repo)
	if err != nil {
		return err
	}
	args := []string{"api", "projects/" + project + "/" + resource, "--method", "GET"}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	stdout, stderr, err := runGL(args...)
	if err != nil {
		return fmt.Errorf("glab api failed: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	if err := json.Unmarshal(stdout, result); err != nil {
		return fmt.Errorf("parse glab API response: %w", err)
	}
	return nil
}

func checkGLPipeline(gate *types.Issue) (resolved, escalated bool, reason string, err error) {
	return checkGLPipelineWithRunner(gate, runGLCommandFunc)
}

func checkGLPipelineWithRunner(gate *types.Issue, runGL ghCommandRunner) (resolved, escalated bool, reason string, err error) {
	if gate.AwaitID == "" {
		return false, false, "no pipeline ID specified - run bd gate discover", nil
	}
	if !isNumericID(gate.AwaitID) {
		return false, false, "", fmt.Errorf("GitLab pipeline await_id must be a numeric pipeline ID")
	}
	repo, err := gitlabRepoFromIssue(gate)
	if err != nil {
		return false, false, "", err
	}
	var pipeline glPipeline
	if err := queryGitLabAPI(repo, "pipelines/"+gate.AwaitID, runGL, &pipeline); err != nil {
		return false, false, "", err
	}
	if strconv.FormatInt(pipeline.ID, 10) != gate.AwaitID {
		return false, false, "", fmt.Errorf("GitLab response pipeline ID does not match await_id")
	}
	switch pipeline.Status {
	case "success":
		return true, false, "GitLab pipeline succeeded", nil
	case "failed", "canceled":
		return false, true, "GitLab pipeline " + pipeline.Status, nil
	default:
		return false, false, "GitLab pipeline status: " + pipeline.Status, nil
	}
}

func checkGLMR(gate *types.Issue) (resolved, escalated bool, reason string, err error) {
	return checkGLMRWithRunner(gate, runGLCommandFunc)
}

func checkGLMRWithRunner(gate *types.Issue, runGL ghCommandRunner) (resolved, escalated bool, reason string, err error) {
	if gate.AwaitID == "" {
		return false, false, "no merge request IID specified", nil
	}
	if !isNumericID(gate.AwaitID) {
		return false, false, "", fmt.Errorf("GitLab merge request await_id must be a numeric project-local IID")
	}
	repo, err := gitlabRepoFromIssue(gate)
	if err != nil {
		return false, false, "", err
	}
	var mr glMergeRequest
	if err := queryGitLabAPI(repo, "merge_requests/"+gate.AwaitID, runGL, &mr); err != nil {
		return false, false, "", err
	}
	if strconv.FormatInt(mr.IID, 10) != gate.AwaitID {
		return false, false, "", fmt.Errorf("GitLab response merge request IID does not match await_id")
	}
	switch mr.State {
	case "merged":
		return true, false, fmt.Sprintf("merge request '%s' merged", mr.Title), nil
	case "closed":
		return false, true, fmt.Sprintf("merge request '%s' closed without merging", mr.Title), nil
	default:
		return false, false, "GitLab merge request state: " + mr.State, nil
	}
}

func discoverGLPipelineWithRunner(gate *types.Issue, branch, sha string, runGL ghCommandRunner) (string, error) {
	if branch == "" || branch == "HEAD" || sha == "" {
		return "", fmt.Errorf("GitLab discovery requires the current branch and HEAD commit")
	}
	repo, err := gitlabRepoFromIssue(gate)
	if err != nil {
		return "", err
	}
	query := url.Values{"ref": {branch}, "sha": {sha}, "order_by": {"id"}, "sort": {"desc"}, "per_page": {"1"}}
	var pipelines []glPipeline
	if err := queryGitLabAPI(repo, "pipelines?"+query.Encode(), runGL, &pipelines); err != nil {
		return "", err
	}
	if len(pipelines) == 0 {
		return "", nil
	}
	pipeline := pipelines[0]
	if pipeline.ID <= 0 || pipeline.SHA != sha || pipeline.Ref != branch {
		return "", fmt.Errorf("GitLab pipeline response does not match the requested branch HEAD")
	}
	return strconv.FormatInt(pipeline.ID, 10), nil
}
